// Package go2rtcmgr manages the go2rtc sidecar subprocess.
package go2rtcmgr

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Manager manages the go2rtc subprocess lifecycle.
type Manager struct {
	log        zerolog.Logger
	binaryPath string
	configPath string
	apiURL     string
	cmd        *exec.Cmd
	ready      chan struct{}
	mu         sync.Mutex
	cancel     context.CancelFunc
}

// NewManager creates a new go2rtc process manager.
func NewManager(binaryPath, configPath string, log zerolog.Logger) *Manager {
	return &Manager{
		log:        log,
		binaryPath: binaryPath,
		configPath: configPath,
		apiURL:     "http://127.0.0.1:1984",
		ready:      make(chan struct{}),
	}
}

// APIURL returns the base URL for the go2rtc API.
func (m *Manager) APIURL() string {
	return m.apiURL
}

// Start launches the go2rtc subprocess.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		return fmt.Errorf("go2rtc already running")
	}

	// Pre-flight: if something is already listening on :1984 it's
	// almost certainly an orphaned go2rtc from a previous run. Fail
	// fast with a clear error instead of silently talking to the
	// stale instance.
	if err := checkPortFree(":1984"); err != nil {
		return fmt.Errorf("go2rtc port pre-flight: %w "+
			"(hint: 'pkill go2rtc' to clear an orphan)", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.cmd = exec.CommandContext(ctx, m.binaryPath, "-config", m.configPath)
	configureProcess(m.cmd)

	stdout, err := m.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := m.cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := m.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start go2rtc: %w", err)
	}

	m.log.Info().
		Str("binary", m.binaryPath).
		Str("config", m.configPath).
		Int("pid", m.cmd.Process.Pid).
		Msg("go2rtc started")

	// Relay stdout/stderr through zerolog
	go m.relayOutput(bufio.NewScanner(stdout))
	go m.relayOutput(bufio.NewScanner(stderr))

	// Monitor process exit
	go func() {
		err := m.cmd.Wait()
		m.mu.Lock()
		m.cmd = nil
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			m.log.Error().Err(err).Msg("go2rtc exited unexpectedly")
		} else {
			m.log.Info().Msg("go2rtc stopped")
		}
	}()

	return nil
}

// Stop gracefully stops the go2rtc subprocess.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// Reset ready channel for potential restart
	m.ready = make(chan struct{})
	return nil
}

// WaitReady polls the go2rtc API until it responds or the timeout expires.
func (m *Manager) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("go2rtc not ready after %v", timeout)
		case <-ticker.C:
			if m.IsHealthy(ctx) {
				close(m.ready)
				m.log.Info().Msg("go2rtc is ready")
				return nil
			}
		}
	}
}

// IsHealthy checks if go2rtc is responding to API requests.
func (m *Manager) IsHealthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", m.apiURL+"/api/streams", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// checkPortFree verifies that nothing is already listening on the given
// address (e.g. ":1984"). Returns an error if the port is occupied.
func checkPortFree(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %s in use: %w", addr, err)
	}
	ln.Close()
	return nil
}

// relayOutput reads scanner lines and re-emits them through zerolog.
func (m *Manager) relayOutput(scanner *bufio.Scanner) {
	for scanner.Scan() {
		line := scanner.Text()
		m.emitLogLine(line)
	}
}

// emitLogLine parses go2rtc log lines and re-emits at appropriate levels.
//
// go2rtc uses zerolog's console writer, so its output is:
//
//	"HH:MM:SS.mmm LVL [module] message"
//
// where LVL is one of TRC DBG INF WRN ERR FTL PNC. Note the zerolog
// *abbreviation* — not "[DEBUG]" style.
//
// Some stdout lines lack the prefix entirely — most visibly the "[OOO]"
// out-of-order packet reports from go2rtc's Wyze TUTK reassembler, which
// are raw fmt.Print output. We treat any unparseable line as trace-level
// noise so it only shows at LOG_LEVEL=trace.
//
// We also drop the redundant timestamp/level prefix from parsed lines —
// our own zerolog already renders a timestamp and level.
func (m *Manager) emitLogLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if shouldSuppressGo2RTCLogLine(line) {
		return
	}

	parts := strings.SplitN(line, " ", 3)
	if len(parts) == 3 {
		switch parts[1] {
		case "TRC", "DBG":
			m.log.Trace().Msg(parts[2])
			return
		case "INF":
			m.log.Debug().Msg(parts[2]) // go2rtc INF → our debug to keep noise down
			return
		case "WRN":
			m.log.Warn().Msg(parts[2])
			return
		case "ERR", "FTL", "PNC":
			m.log.Error().Msg(parts[2])
			return
		}
	}
	m.log.Trace().Msg(line)
}

func shouldSuppressGo2RTCLogLine(line string) bool {
	return strings.HasPrefix(line, "[OOO]") && strings.Contains(line, " - reset")
}
