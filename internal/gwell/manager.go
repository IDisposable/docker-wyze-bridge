package gwell

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// processRunner abstracts exec.Cmd so tests can inject a fake process.
type processRunner interface {
	Start() error
	Wait() error
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Pid() int
}

// execRunner is the production adapter over *exec.Cmd.
type execRunner struct {
	cmd *exec.Cmd
}

func (r *execRunner) Start() error              { return r.cmd.Start() }
func (r *execRunner) Wait() error               { return r.cmd.Wait() }
func (r *execRunner) StdoutPipe() (io.ReadCloser, error) { return r.cmd.StdoutPipe() }
func (r *execRunner) StderrPipe() (io.ReadCloser, error) { return r.cmd.StderrPipe() }
func (r *execRunner) Pid() int {
	if r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

// runnerFactory builds a processRunner for the given context + binary.
// Overridden in tests; the default wraps exec.CommandContext.
type runnerFactory func(ctx context.Context, bin string, args ...string) processRunner

// Manager manages the gwell-proxy subprocess lifecycle.
//
// The manager is lazily started — if no Gwell cameras are discovered,
// no subprocess is ever spawned. Call EnsureStarted() before using the
// control API (Producer does this transparently).
//
// Thread-safe.
type Manager struct {
	log zerolog.Logger
	cfg Config

	// newRunner is overridden in tests.
	newRunner runnerFactory

	mu      sync.Mutex
	runner  processRunner
	cancel  context.CancelFunc
	started bool
}

// NewManager constructs a manager but does not spawn anything yet.
func NewManager(cfg Config, log zerolog.Logger) *Manager {
	return &Manager{
		log:       log,
		cfg:       cfg,
		newRunner: defaultRunnerFactory,
	}
}

func defaultRunnerFactory(ctx context.Context, bin string, args ...string) processRunner {
	return &execRunner{cmd: exec.CommandContext(ctx, bin, args...)}
}

// Config returns a copy of the config the manager was built with.
func (m *Manager) Config() Config { return m.cfg }

// Started reports whether the subprocess has been launched in this
// manager's lifetime.
func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// EnsureStarted launches the proxy if it hasn't been started yet, and
// waits for its control API to respond. Safe to call concurrently.
func (m *Manager) EnsureStarted(ctx context.Context) error {
	if !m.cfg.Enabled {
		return ErrDisabled
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	if err := m.cfg.Validate(); err != nil {
		return err
	}

	binary, err := m.cfg.ResolveBinary()
	if err != nil {
		return fmt.Errorf("gwell: resolve binary: %w", err)
	}

	args := []string{
		"--listen", fmt.Sprintf("127.0.0.1:%d", m.cfg.ControlPort),
		"--rtsp", fmt.Sprintf("127.0.0.1:%d", m.cfg.RTSPPort),
	}
	if m.cfg.StateDir != "" {
		args = append(args, "--state", m.cfg.StateDir)
	}
	if m.cfg.LogLevel != "" {
		args = append(args, "--log", m.cfg.LogLevel)
	}

	procCtx, cancel := context.WithCancel(context.Background())
	runner := m.newRunner(procCtx, binary, args...)

	stdout, err := runner.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("gwell: stdout pipe: %w", err)
	}
	stderr, err := runner.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("gwell: stderr pipe: %w", err)
	}

	if err := runner.Start(); err != nil {
		cancel()
		// Give a gentle hint if the binary was never built.
		if os.IsNotExist(err) || strings.Contains(err.Error(), "executable file not found") {
			return fmt.Errorf(
				"gwell: binary %q not found; build gwell-proxy from "+
					"github.com/wlatic/hacky-wyze-gwell or set GWELL_BINARY: %w",
				binary, err,
			)
		}
		return fmt.Errorf("gwell: start proxy: %w", err)
	}

	m.mu.Lock()
	m.runner = runner
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	m.log.Info().
		Str("binary", binary).
		Int("rtsp_port", m.cfg.RTSPPort).
		Int("control_port", m.cfg.ControlPort).
		Int("pid", runner.Pid()).
		Msg("gwell-proxy started")

	go m.relayOutput(stdout)
	go m.relayOutput(stderr)

	// Watch for exit.
	go func() {
		werr := runner.Wait()
		m.mu.Lock()
		m.runner = nil
		m.started = false
		m.mu.Unlock()
		if werr != nil && procCtx.Err() == nil {
			m.log.Error().Err(werr).Msg("gwell-proxy exited unexpectedly")
		} else {
			m.log.Info().Msg("gwell-proxy stopped")
		}
	}()

	// Wait for the control API to answer.
	if err := m.waitReady(ctx, 10*time.Second); err != nil {
		_ = m.Stop()
		return err
	}
	return nil
}

// Stop terminates the subprocess if it is running.
// Idempotent and safe to call concurrently.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.started = false
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// IsHealthy returns true when the proxy's control API responds OK.
func (m *Manager) IsHealthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.BaseURL()+"/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// waitReady polls the control API until it answers or the deadline hits.
func (m *Manager) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("gwell: proxy not ready after %v", timeout)
		case <-ticker.C:
			if m.IsHealthy(ctx) {
				m.log.Info().Msg("gwell-proxy control API ready")
				return nil
			}
		}
	}
}

// relayOutput reads from a process pipe and re-emits each line through
// zerolog at a level parsed from the upstream line prefix.
func (m *Manager) relayOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		m.emitLogLine(scanner.Text())
	}
}

// emitLogLine maps a proxy log line to the right zerolog level. The
// upstream proxy is a Go program using the stdlib logger, so lines
// look like "2026/04/16 12:34:56 INFO ..." or "... [ERROR] ...".
func (m *Manager) emitLogLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, " ERROR ") || strings.Contains(upper, "[ERROR]"):
		m.log.Error().Msg(line)
	case strings.Contains(upper, " WARN ") || strings.Contains(upper, "[WARN]"):
		m.log.Warn().Msg(line)
	case strings.Contains(upper, " INFO ") || strings.Contains(upper, "[INFO]"):
		m.log.Debug().Msg(line) // downgrade INFO to our debug (less noise)
	case strings.Contains(upper, " DEBUG ") || strings.Contains(upper, "[DEBUG]"):
		m.log.Trace().Msg(line)
	default:
		m.log.Debug().Msg(line)
	}
}
