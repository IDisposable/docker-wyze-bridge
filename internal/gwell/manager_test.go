package gwell

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fakeRunner is a processRunner that never actually execs anything; it
// lets us assert lifecycle behavior deterministically.
type fakeRunner struct {
	mu         sync.Mutex
	started    bool
	waitCh     chan error
	stdoutPipe io.ReadCloser
	stderrPipe io.ReadCloser
	pid        int
	startErr   error
}

func newFakeRunner() *fakeRunner {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	// Close the write ends on Wait so scanners finish.
	f := &fakeRunner{
		waitCh:     make(chan error, 1),
		stdoutPipe: outR,
		stderrPipe: errR,
		pid:        4242,
	}
	_ = outW
	_ = errW
	return f
}

func (f *fakeRunner) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return nil
}

func (f *fakeRunner) Wait() error {
	return <-f.waitCh
}

func (f *fakeRunner) StdoutPipe() (io.ReadCloser, error) { return f.stdoutPipe, nil }
func (f *fakeRunner) StderrPipe() (io.ReadCloser, error) { return f.stderrPipe, nil }
func (f *fakeRunner) Pid() int                           { return f.pid }

func (f *fakeRunner) kill(err error) {
	f.stdoutPipe.Close()
	f.stderrPipe.Close()
	select {
	case f.waitCh <- err:
	default:
	}
}

func TestManager_LazyStart_NotYetRunning(t *testing.T) {
	m := NewManager(DefaultConfig(), zerolog.Nop())
	if m.Started() {
		t.Error("expected Started()=false before EnsureStarted")
	}
}

func TestManager_DisabledReturnsErrDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	m := NewManager(cfg, zerolog.Nop())

	err := m.EnsureStarted(context.Background())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
	if m.Started() {
		t.Error("disabled manager should not be Started()")
	}
}

func TestManager_InvalidConfigFailsValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RTSPPort = 0
	m := NewManager(cfg, zerolog.Nop())

	err := m.EnsureStarted(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "RTSPPort") {
		t.Errorf("error should mention RTSPPort, got %v", err)
	}
}

// TestManager_EnsureStarted_WaitsForReady spawns a fake runner and a
// fake HTTP healthz endpoint, verifying the manager waits for ready.
func TestManager_EnsureStarted_WaitsForReady(t *testing.T) {
	// Healthy control API: serves /healthz with 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Extract the test server's port and point our config at it.
	// httptest always binds 127.0.0.1, so the manager's health check
	// against 127.0.0.1:<port> hits our server.
	var port int
	if _, err := stringsScanPort(srv.URL, &port); err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	cfg := DefaultConfig()
	cfg.ControlPort = port
	cfg.RTSPPort = port + 1

	fake := newFakeRunner()

	m := NewManager(cfg, zerolog.Nop())
	m.newRunner = func(ctx context.Context, bin string, args ...string) processRunner {
		return fake
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := m.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	if !m.Started() {
		t.Error("expected Started()=true after EnsureStarted")
	}

	// Calling again is a no-op (lazy idempotent).
	if err := m.EnsureStarted(ctx); err != nil {
		t.Fatalf("second EnsureStarted: %v", err)
	}

	// Cleanup: simulate process exit.
	_ = m.Stop()
	fake.kill(nil)
}

func TestManager_EnsureStarted_TimesOutWithoutReady(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ControlPort = 1 // unlikely to be listening
	cfg.RTSPPort = 2

	fake := newFakeRunner()

	m := NewManager(cfg, zerolog.Nop())
	m.newRunner = func(ctx context.Context, bin string, args ...string) processRunner {
		return fake
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// waitReady has its own 10s timeout; we don't wait for it in tests.
	// Instead, cancel early and assert we get a context error.
	done := make(chan error, 1)
	go func() { done <- m.EnsureStarted(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from EnsureStarted after context cancel")
		}
	case <-time.After(12 * time.Second):
		t.Fatal("EnsureStarted did not return in time")
	}

	fake.kill(nil)
}

func TestEmitLogLine_NoPanic(t *testing.T) {
	m := NewManager(DefaultConfig(), zerolog.Nop())
	lines := []string{
		"",
		"   ",
		"2026/04/16 12:34:56 INFO p2p connected",
		"2026/04/16 12:34:56 WARN kcp retransmit",
		"2026/04/16 12:34:56 ERROR decrypt failed",
		"[DEBUG] rc5 expand",
		"random noise line",
	}
	for _, l := range lines {
		m.emitLogLine(l)
	}
}

// stringsScanPort extracts the port from "http://host:port..." without
// pulling in net/url just for a test helper.
func stringsScanPort(u string, out *int) (int, error) {
	i := strings.LastIndex(u, ":")
	if i == -1 {
		return 0, errors.New("no port")
	}
	rest := u[i+1:]
	end := strings.IndexAny(rest, "/")
	if end >= 0 {
		rest = rest[:end]
	}
	var p int
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		p = p*10 + int(r-'0')
	}
	*out = p
	return p, nil
}
