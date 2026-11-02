package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestRenderIndex_WithCameras(t *testing.T) {
	srv, _ := testServer(t)

	// Inject a camera into the manager
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:      "front_door",
		Nickname:  "Front Door",
		Model:     "HL_CAM4",
		MAC:       "AABB",
		LanIP:     "10.0.0.1",
		FWVersion: "4.52.9",
	}, "hd", true, false)
	cam.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("front_door", cam)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Front Door") {
		t.Error("page should contain camera nickname")
	}
	if !strings.Contains(body, "streaming") {
		t.Error("page should contain streaming state")
	}
	if !strings.Contains(body, "/camera/front_door") {
		t.Error("page should contain camera link")
	}
}

func TestRenderCamera_Detail(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:      "test_cam",
		Nickname:  "Test Cam",
		Model:     "WYZE_CAKP2JFUS",
		MAC:       "112233",
		LanIP:     "10.0.0.2",
		FWVersion: "4.36.14",
	}, "sd", false, false)
	srv.camMgr.InjectCamera("test_cam", cam)

	req := httptest.NewRequest("GET", "/camera/test_cam", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Test Cam") {
		t.Error("page should contain camera nickname")
	}
	if !strings.Contains(body, "WYZE_CAKP2JFUS") {
		t.Error("page should contain model")
	}
	if !strings.Contains(body, "video-rtc") {
		t.Error("page should reference video-rtc.js")
	}
	if !strings.Contains(body, "rtsp://") {
		t.Error("page should contain RTSP URL")
	}
}

func TestRenderStreamsM3U8_WithCameras(t *testing.T) {
	srv, _ := testServer(t)

	cam1 := camera.NewCamera(wyzeapi.CameraInfo{Name: "cam1", Nickname: "Cam One"}, "hd", true, false)
	cam2 := camera.NewCamera(wyzeapi.CameraInfo{Name: "cam2", Nickname: "Cam Two"}, "sd", true, false)
	srv.camMgr.InjectCamera("cam1", cam1)
	srv.camMgr.InjectCamera("cam2", cam2)

	req := httptest.NewRequest("GET", "/api/streams", nil)
	req.Host = "myhost:5080"
	w := httptest.NewRecorder()
	srv.handleStreamsM3U8(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "#EXTM3U") {
		t.Error("missing M3U header")
	}
	if !strings.Contains(body, "Cam One") {
		t.Error("missing Cam One")
	}
	if !strings.Contains(body, "rtsp://myhost:8554/cam") {
		t.Error("missing RTSP URL with host")
	}
}

func TestRenderStreamM3U8_Single(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "solo", Nickname: "Solo Cam"}, "hd", true, false)
	srv.camMgr.InjectCamera("solo", cam)

	req := httptest.NewRequest("GET", "/api/streams/solo.m3u8", nil)
	req.Host = "host:5080"
	w := httptest.NewRecorder()
	srv.handleStreamM3U8(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Solo Cam") {
		t.Error("missing camera name")
	}
}

func TestRenderStreamM3U8_BackwardCompat(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "compat", Nickname: "Compat"}, "hd", true, false)
	srv.camMgr.InjectCamera("compat", cam)

	// Test /stream/ backward compat path
	req := httptest.NewRequest("GET", "/stream/compat.m3u8", nil)
	req.Host = "host:5080"
	w := httptest.NewRecorder()
	srv.handleStreamM3U8(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("backward compat status = %d", w.Code)
	}
}

func TestHandleSnapshot_Success(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "snap_cam"}, "hd", true, false)
	srv.camMgr.InjectCamera("snap_cam", cam)

	req := httptest.NewRequest("GET", "/api/snapshot/snap_cam", nil)
	w := httptest.NewRecorder()
	srv.handleSnapshot(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("snapshot should have data")
	}
}

func TestResponseWriter_StatusTracking(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w, status: 200}

	rw.WriteHeader(404)
	if rw.status != 404 {
		t.Errorf("status = %d, want 404", rw.status)
	}
}

func TestHandleSnapshot_FailureLandsOnTheCamera(t *testing.T) {
	// go2rtc dials the camera for a dashboard tile's snapshot. When
	// that fails it answers 500 with the dial error, and the camera
	// has to hear about it: the tile is the only thing pulling a
	// camera nobody is watching, and the failure used to stop here.
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/frame.jpeg" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("streams: wyze: connect failed: discovery timeout"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(go2rtcSrv.Close)
	srv, _ := testServer(t)
	srv.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(go2rtcSrv.URL, zerolog.Nop()))

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "snap_cam", Model: "WYZE_CAKP2JFUS", LanIP: "192.168.4.141"}, "hd", true, false)
	cam.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("snap_cam", cam)

	req := httptest.NewRequest("GET", "/api/snapshot/snap_cam", nil)
	w := httptest.NewRecorder()
	srv.handleSnapshot(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	snap := cam.Snapshot()
	if snap.ErrorCount != 1 || snap.LastError != "snapshot: wyze: connect failed: discovery timeout" {
		t.Errorf("camera after a failed snapshot: errors=%d err=%q", snap.ErrorCount, snap.LastError)
	}
}

func TestRenderIndex_DegradedBadge(t *testing.T) {
	// A camera that is "streaming" with a failed dial behind it gets
	// the degraded class on its state badge; a healthy one doesn't.
	srv, _ := testServer(t)
	good := camera.NewCamera(wyzeapi.CameraInfo{Name: "good_cam", Model: "WYZE_CAKP2JFUS", LanIP: "192.168.4.1"}, "hd", true, false)
	good.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("good_cam", good)
	bad := camera.NewCamera(wyzeapi.CameraInfo{Name: "bad_cam", Model: "WYZE_CAKP2JFUS", LanIP: "192.168.4.2"}, "hd", true, false)
	bad.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("bad_cam", bad)
	srv.camMgr.RecordStreamError(t.Context(), "bad_cam", "wyze: connect failed: discovery timeout")

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.renderIndex(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `class="state-badge streaming degraded" data-cam="bad_cam"`) {
		t.Error("failed camera's state badge is not marked degraded")
	}
	if !strings.Contains(body, `class="state-badge streaming" data-cam="good_cam"`) {
		t.Error("healthy camera's state badge should be plain streaming")
	}
	if !strings.Contains(body, `title="wyze: connect failed: discovery timeout`) {
		t.Error("the reason should be on the badge's tooltip")
	}
}

func TestHandleSnapshot_SlowVerdictStillReachesTheCamera(t *testing.T) {
	// go2rtc can take most of a minute to give up on a camera that
	// doesn't answer. The tile gets its 503 at snapshotWait, but the
	// request to go2rtc runs on, and the verdict lands on the camera.
	// Tiles asking in the meantime share the one request.
	var mu sync.Mutex
	hits := 0
	release := make(chan struct{})
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/frame.jpeg" {
			mu.Lock()
			hits++
			mu.Unlock()
			<-release
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("streams: wyze: connect failed: discovery timeout"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(go2rtcSrv.Close)
	srv, _ := testServer(t)
	srv.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(go2rtcSrv.URL, zerolog.Nop()))
	old := snapshotWait
	snapshotWait = 50 * time.Millisecond
	t.Cleanup(func() { snapshotWait = old })

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "slow_cam", Model: "WYZE_CAKP2JFUS", LanIP: "192.168.4.141"}, "hd", true, false)
	cam.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("slow_cam", cam)

	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			w := httptest.NewRecorder()
			srv.handleSnapshot(w, httptest.NewRequest("GET", "/api/snapshot/slow_cam", nil))
			codes <- w.Code
		}()
	}
	for i := 0; i < 2; i++ {
		if code := <-codes; code != http.StatusServiceUnavailable {
			t.Errorf("tile got %d while go2rtc was still dialing, want 503", code)
		}
	}
	if cam.Snapshot().ErrorCount != 0 {
		t.Fatal("nothing should land before go2rtc answers")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for cam.Snapshot().ErrorCount == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snap := cam.Snapshot(); snap.ErrorCount != 1 || snap.LastError != "snapshot: wyze: connect failed: discovery timeout" {
		t.Errorf("after go2rtc's answer: errors=%d err=%q", snap.ErrorCount, snap.LastError)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("go2rtc was asked %d times, want once for both tiles", hits)
	}
}
