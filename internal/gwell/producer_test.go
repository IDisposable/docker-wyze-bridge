package gwell

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// fakeAuth is an in-memory AuthProvider for unit tests.
type fakeAuth struct {
	state      *wyzeapi.AuthState
	ensureErr  error
	ensureCall int
}

func (f *fakeAuth) EnsureAuth() error { f.ensureCall++; return f.ensureErr }
func (f *fakeAuth) Auth() *wyzeapi.AuthState {
	return f.state
}

func newHealthyProxy(t *testing.T, onRegister func(RegisterRequest) RegisterResponse) (*httptest.Server, int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/cameras", func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := onRegister(req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	var port int
	if _, err := stringsScanPort(srv.URL, &port); err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return srv, port
}

func TestProducer_Connect_HappyPath(t *testing.T) {
	srv, port := newHealthyProxy(t, func(req RegisterRequest) RegisterResponse {
		return RegisterResponse{Name: req.Name, RTSPURL: "rtsp://127.0.0.1:8564/" + req.Name}
	})
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.ControlPort = port
	mgr := NewManager(cfg, zerolog.Nop())
	mgr.newRunner = func(ctx context.Context, bin string, args ...string) processRunner {
		return newFakeRunner()
	}

	auth := &fakeAuth{state: &wyzeapi.AuthState{AccessToken: "tok", PhoneID: "phone", UserID: "u"}}
	p := NewProducer(mgr, auth, "test-1.0", zerolog.Nop())

	info := wyzeapi.CameraInfo{
		Nickname: "Front Porch",
		Model:    "GW_GC1",
		MAC:      "AA:BB:CC:DD:EE:FF",
		ENR:      "enr-value",
		LanIP:    "192.168.1.42",
	}

	url, err := p.Connect(context.Background(), info, "front_porch", "hd", true)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !strings.HasPrefix(url, "rtsp://127.0.0.1:") || !strings.HasSuffix(url, "/front_porch") {
		t.Errorf("url = %q, expected rtsp loopback URL", url)
	}
	if auth.ensureCall == 0 {
		t.Error("expected EnsureAuth to be called")
	}
}

func TestProducer_Connect_NonGwellModelRejected(t *testing.T) {
	mgr := NewManager(DefaultConfig(), zerolog.Nop())
	auth := &fakeAuth{state: &wyzeapi.AuthState{AccessToken: "tok"}}
	p := NewProducer(mgr, auth, "test", zerolog.Nop())

	info := wyzeapi.CameraInfo{Model: "WYZE_CAKP2JFUS"} // V3, TUTK
	_, err := p.Connect(context.Background(), info, "cam", "hd", true)
	if err == nil {
		t.Fatal("expected error for non-Gwell model")
	}
	if !strings.Contains(err.Error(), "not a Gwell") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProducer_Connect_DisabledIntegration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	mgr := NewManager(cfg, zerolog.Nop())
	auth := &fakeAuth{}
	p := NewProducer(mgr, auth, "test", zerolog.Nop())

	if p.Enabled() {
		t.Error("producer should be disabled")
	}

	_, err := p.Connect(context.Background(), wyzeapi.CameraInfo{Model: "GW_GC1"}, "og", "hd", true)
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

func TestProducer_Connect_NoAuthToken(t *testing.T) {
	mgr := NewManager(DefaultConfig(), zerolog.Nop())
	auth := &fakeAuth{state: &wyzeapi.AuthState{AccessToken: ""}}
	p := NewProducer(mgr, auth, "test", zerolog.Nop())

	_, err := p.Connect(context.Background(), wyzeapi.CameraInfo{Model: "GW_GC1"}, "og", "hd", true)
	if !errors.Is(err, ErrNoAuth) {
		t.Errorf("err = %v, want ErrNoAuth", err)
	}
}

func TestProducer_Connect_AuthRefreshError(t *testing.T) {
	mgr := NewManager(DefaultConfig(), zerolog.Nop())
	auth := &fakeAuth{ensureErr: errors.New("boom")}
	p := NewProducer(mgr, auth, "test", zerolog.Nop())

	_, err := p.Connect(context.Background(), wyzeapi.CameraInfo{Model: "GW_GC1"}, "og", "hd", true)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}

func TestProducer_Disconnect_NotStartedIsNoop(t *testing.T) {
	mgr := NewManager(DefaultConfig(), zerolog.Nop())
	auth := &fakeAuth{}
	p := NewProducer(mgr, auth, "test", zerolog.Nop())

	// Manager never started → Disconnect must not fail.
	if err := p.Disconnect(context.Background(), "absent"); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
}
