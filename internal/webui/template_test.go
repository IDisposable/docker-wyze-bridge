package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
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
