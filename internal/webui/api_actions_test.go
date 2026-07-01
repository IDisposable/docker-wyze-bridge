package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/issues"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// injectStreamingCamera registers a TUTK camera in StateStreaming so
// /api/cameras/<name>/* action handlers find a real target.
func injectStreamingCamera(srv *Server, name string) *camera.Camera {
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		Model: "WYZE_CAKP2JFUS",
		MAC:   "AABBCC" + strings.ToUpper(name[:1]) + "11223344",
		LanIP: "10.0.0.1",
	}, "hd", true, false)
	cam.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera(name, cam)
	return cam
}

func TestHandleAPICameraAction_AudioToggle(t *testing.T) {
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "kitchen")
	cam.SetAudioOn(true)

	body := bytes.NewBufferString(`{"enabled":false}`)
	req := httptest.NewRequest("POST", "/api/cameras/kitchen/audio", body)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if cam.GetAudioOn() {
		t.Error("AudioOn not flipped to false")
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["audio"] != false {
		t.Errorf("response audio = %v, want false", resp["audio"])
	}
}

func TestHandleAPICameraAction_QualityValidation(t *testing.T) {
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "garage")

	cases := []struct {
		body     string
		wantCode int
	}{
		{`{"quality":"4k"}`, http.StatusBadRequest}, // invalid value
		{`not json`, http.StatusBadRequest},         // malformed
		{`{"quality":"hd"}`, http.StatusOK},         // valid; testServer's mock go2rtc accepts the AddStream
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/api/cameras/garage/quality", bytes.NewBufferString(c.body))
		w := httptest.NewRecorder()
		srv.handleAPICameraAction(w, req)
		if w.Code != c.wantCode {
			t.Errorf("body %q: status = %d, want %d (body=%s)", c.body, w.Code, c.wantCode, w.Body.String())
		}
		// All error responses should be JSON, not bare text.
		if c.wantCode >= 400 {
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("body %q: error content-type = %q, want json", c.body, ct)
			}
		}
	}
}

// fakeRecordingController satisfies recordingController without spawning ffmpeg.
type fakeRecordingController struct {
	started  atomic.Int32
	stopped  atomic.Int32
	startErr error
}

func (f *fakeRecordingController) Start(_ context.Context, _ string) error {
	f.started.Add(1)
	return f.startErr
}
func (f *fakeRecordingController) Stop(_ string) {
	f.stopped.Add(1)
}

// fakeRecObserver satisfies the RecordingObserver interface; needed
// because recMgr is type-asserted to recordingController inside the
// handler and the underlying value must also be the controller.
type fakeRecMgr struct {
	*fakeRecordingController
	recording bool
}

func (f *fakeRecMgr) IsRecording(_ string) bool        { return f.recording }
func (f *fakeRecMgr) ActiveRecorders() []string         { return nil }
func (f *fakeRecMgr) SessionBytes(_ string) int64       { return 0 }

func TestHandleAPICameraAction_RecordStartStop(t *testing.T) {
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "deck")
	ctrl := &fakeRecordingController{}
	srv.recMgr = &fakeRecMgr{fakeRecordingController: ctrl}

	// Start
	req := httptest.NewRequest("POST", "/api/cameras/deck/record", bytes.NewBufferString(`{"action":"start"}`))
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start status %d: %s", w.Code, w.Body.String())
	}
	if ctrl.started.Load() != 1 {
		t.Errorf("Start not invoked, count = %d", ctrl.started.Load())
	}

	// Stop
	req = httptest.NewRequest("POST", "/api/cameras/deck/record", bytes.NewBufferString(`{"action":"stop"}`))
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stop status %d", w.Code)
	}
	if ctrl.stopped.Load() != 1 {
		t.Errorf("Stop not invoked, count = %d", ctrl.stopped.Load())
	}
}

func TestHandleAPIDiscover(t *testing.T) {
	srv, _ := testServer(t)

	// No hook wired -> 503
	req := httptest.NewRequest("POST", "/api/discover", nil)
	w := httptest.NewRecorder()
	srv.handleAPIDiscover(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no-hook status = %d, want 503", w.Code)
	}

	// GET -> 405
	req = httptest.NewRequest("GET", "/api/discover", nil)
	w = httptest.NewRecorder()
	srv.handleAPIDiscover(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", w.Code)
	}

	// Hook wired -> 200, hook fires
	var hit atomic.Int32
	srv.OnDiscoverRequest(func(context.Context) { hit.Add(1) })
	req = httptest.NewRequest("POST", "/api/discover", nil)
	w = httptest.NewRecorder()
	srv.handleAPIDiscover(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("with-hook status = %d", w.Code)
	}
	// The hook fires in a goroutine; poll briefly so the assert isn't flaky.
	for i := 0; i < 100; i++ {
		if hit.Load() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if hit.Load() == 0 {
		t.Error("discover callback didn't fire within 100ms")
	}
}

func TestHandleHealth_DegradedWhenIssuesOpen(t *testing.T) {
	srv, _ := testServer(t)
	srv.issues = issues.New()
	srv.issues.Report(issues.Issue{
		ID:       "test/issue",
		Severity: issues.SeverityError,
		Scope:    "config",
		Message:  "synthetic problem",
	})

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	var body map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
	if body["config_errors"] != float64(1) { // JSON numbers decode as float64
		t.Errorf("config_errors = %v, want 1", body["config_errors"])
	}
	list, ok := body["issues"].([]interface{})
	if !ok || len(list) != 1 {
		t.Errorf("issues list shape wrong: %v", body["issues"])
	}
}

// fakeKVSProvider satisfies KVSStreamProvider with a canned response
// matching what wyzeapi.GetCameraKVSConfig produces.
type fakeKVSProvider struct {
	signalingURL string
	ice          []KVSIceServer
	authToken    string
	err          error
}

func (f *fakeKVSProvider) GetCameraStream(_ context.Context, mac, model string) (string, []KVSIceServer, string, error) {
	return f.signalingURL, f.ice, f.authToken, f.err
}

func TestHandleShimKVSSignaling_HappyPath(t *testing.T) {
	srv, _ := testServer(t)
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:  "front_door",
		Model: "GW_BE1", // WebRTC streamer
		MAC:   "BEBEBEBEBEBE",
	}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)
	srv.kvs = &fakeKVSProvider{
		signalingURL: "wss://wyze-mars-webcsrv.wyzecam.com?token=xyz",
		ice: []KVSIceServer{
			{URL: "stun:stun.kinesisvideo.us-west-2.amazonaws.com:443"},
			{URL: "turn:turn.example.com:443", Username: "u", Credential: "p"},
		},
		authToken: "auth-blob",
	}

	req := httptest.NewRequest("GET", "/internal/wyze/webrtc/front_door", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.handleShimKVSSignaling(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["signalingUrl"] != "wss://wyze-mars-webcsrv.wyzecam.com?token=xyz" {
		t.Errorf("signalingUrl = %v", got["signalingUrl"])
	}
	if got["result"] != "ok" {
		t.Errorf("result = %v", got["result"])
	}
	servers, _ := got["servers"].([]interface{})
	if len(servers) != 2 {
		t.Errorf("servers count = %d, want 2", len(servers))
	}
}

func TestHandleShimKVSSignaling_NonWebRTCCameraRejected(t *testing.T) {
	srv, _ := testServer(t)
	// TUTK camera — shouldn't route through KVS at all
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name: "front_door", Model: "HL_CAM4", MAC: "AABB",
	}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)
	srv.kvs = &fakeKVSProvider{signalingURL: "irrelevant"}

	req := httptest.NewRequest("GET", "/internal/wyze/webrtc/front_door", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.handleShimKVSSignaling(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-WebRTC camera", w.Code)
	}
}

func TestHandleShimKVSSignaling_ForceWebRTCPassesGate(t *testing.T) {
	// The runtime TUTK→WebRTC auto-fallback flips a per-Camera flag
	// without changing the model registry. The shim must honor it so
	// a promoted HL_CAM4 (or any other TUTK-default model) can
	// actually mint a KVS signaling URL.
	srv, _ := testServer(t)
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name: "front_door", Model: "HL_CAM4", MAC: "AABB",
	}, "hd", true, false)
	cam.SetForceWebRTC(true)
	srv.camMgr.InjectCamera("front_door", cam)
	srv.kvs = &fakeKVSProvider{
		signalingURL: "wss://wyze-mars-webcsrv.wyzecam.com?token=forced",
		ice:          []KVSIceServer{{URL: "stun:stun.example.com:443"}},
	}

	req := httptest.NewRequest("GET", "/internal/wyze/webrtc/front_door", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.handleShimKVSSignaling(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; force-promoted camera should reach KVS", w.Code, w.Body.String())
	}
}
