package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// testServer creates a Server with mock dependencies for testing.
func testServer(t *testing.T) (*Server, *go2rtcmgr.APIClient) {
	t.Helper()

	// Mock go2rtc API server
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{})
		case r.URL.Path == "/api/streams" && r.Method == "PUT":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/frame.jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(go2rtcSrv.Close)

	cfg := &config.Config{
		WBPort:       5080,
		Quality:      "hd",
		Audio:        true,
		CamOverrides: make(map[string]config.CamOverride),
	}

	go2rtcAPI := go2rtcmgr.NewAPIClient(go2rtcSrv.URL, zerolog.Nop())
	apiClient := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	camMgr := camera.NewManager(cfg, apiClient, go2rtcAPI, zerolog.Nop())

	srv := NewServer(cfg, camMgr, go2rtcAPI, "test-version", zerolog.Nop())
	srv.startTime = time.Now()

	return srv, go2rtcAPI
}

func TestHandleHealth(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["status"] != "ok" {
		t.Errorf("status = %v", resp["status"])
	}
	if resp["version"] != "test-version" {
		t.Errorf("version = %v", resp["version"])
	}
	if _, ok := resp["uptime"]; !ok {
		t.Error("missing uptime")
	}
}

func TestHandleVersion(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/version", nil)
	w := httptest.NewRecorder()
	srv.handleVersion(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["version"] != "test-version" {
		t.Errorf("version = %v", resp["version"])
	}
}

func TestHandleAPICameras_Empty(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameras(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp []interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

func TestHandleStreamsM3U8(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/streams", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleStreamsM3U8(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("content-type = %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "#EXTM3U") {
		t.Error("missing #EXTM3U header")
	}
}

func TestHandleSnapshot_Missing(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/snapshot/", nil)
	w := httptest.NewRecorder()
	srv.handleSnapshot(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing cam should be 400, got %d", w.Code)
	}
}

func TestHandleIndex_Root(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), "Wyze Bridge") {
		t.Error("page should contain 'Wyze Bridge'")
	}
}

func TestHandleIndex_NotRoot(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("non-root should be 404, got %d", w.Code)
	}
}

func TestHandleCameraPage_Missing(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/camera/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("missing cam should be 404, got %d", w.Code)
	}
}

func TestHandleCameraPage_Empty(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/camera/", nil)
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("empty cam name should redirect, got %d", w.Code)
	}
}

func TestHandleAPICameraAction_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("missing cam should be 404, got %d", w.Code)
	}
}

func TestHandleAPICameraAction_NoName(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras/", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("no name should be 400, got %d", w.Code)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, map[string]string{"hello": "world"})

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["hello"] != "world" {
		t.Errorf("response = %v", resp)
	}
}
