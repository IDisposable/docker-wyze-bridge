package snapshot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func mockGo2RTC(t *testing.T) (*httptest.Server, *go2rtcmgr.APIClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/frame.jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}) // JPEG header
		case r.URL.Path == "/api/streams" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{})
		case r.URL.Path == "/api/streams" && r.Method == "PUT":
			w.WriteHeader(200)
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	return srv, api
}

func makeCamMgr(t *testing.T, api *go2rtcmgr.APIClient) *camera.Manager {
	t.Helper()
	cfg := &config.Config{
		Quality:      "hd",
		Audio:        true,
		CamOverrides: make(map[string]config.CamOverride),
		RefreshInterval: 30 * time.Minute,
	}
	wapi := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	mgr := camera.NewManager(cfg, wapi, api, zerolog.Nop())
	return mgr
}

func TestManager_CaptureOne(t *testing.T) {
	_, api := mockGo2RTC(t)
	camMgr := makeCamMgr(t, api)

	imgDir := t.TempDir()
	cfg := &config.Config{
		SnapshotInt: 60,
		ImgDir:      imgDir,
		CamOverrides: make(map[string]config.CamOverride),
	}

	m := NewManager(cfg, camMgr, api, zerolog.Nop())

	var captured []byte
	m.OnCapture(func(name string, jpeg []byte) {
		captured = jpeg
	})

	m.CaptureOne(context.Background(), "test_cam")

	if len(captured) == 0 {
		t.Error("capture callback should have been called with data")
	}

	// Check file was saved
	files, _ := filepath.Glob(filepath.Join(imgDir, "*.jpg"))
	if len(files) != 1 {
		t.Errorf("expected 1 snapshot file, got %d", len(files))
	}
}

func TestManager_CaptureOne_WithFormat(t *testing.T) {
	_, api := mockGo2RTC(t)
	camMgr := makeCamMgr(t, api)

	imgDir := t.TempDir()
	cfg := &config.Config{
		SnapshotInt:    60,
		SnapshotFormat: "{cam_name}_%Y%m%d",
		ImgDir:         imgDir,
		CamOverrides:   make(map[string]config.CamOverride),
	}

	m := NewManager(cfg, camMgr, api, zerolog.Nop())
	m.CaptureOne(context.Background(), "front_door")

	files, _ := filepath.Glob(filepath.Join(imgDir, "front_door_*.jpg"))
	if len(files) != 1 {
		t.Errorf("expected 1 formatted snapshot file, got %d", len(files))
	}
}

func TestManager_SaveSnapshot(t *testing.T) {
	imgDir := t.TempDir()
	cfg := &config.Config{ImgDir: imgDir, CamOverrides: make(map[string]config.CamOverride)}
	m := &Manager{cfg: cfg, log: zerolog.Nop()}

	jpeg := []byte{0xFF, 0xD8, 0xFF}
	err := m.saveSnapshot("test_cam", jpeg)
	if err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}

	path := filepath.Join(imgDir, "test_cam.jpg")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if len(data) != 3 {
		t.Errorf("file size = %d, want 3", len(data))
	}
}

func TestManager_CaptureAll_FiltersCameras(t *testing.T) {
	_, api := mockGo2RTC(t)
	camMgr := makeCamMgr(t, api)

	cfg := &config.Config{
		SnapshotInt:     60,
		SnapshotCameras: []string{"FRONT_DOOR"},
		ImgDir:          t.TempDir(),
		CamOverrides:    make(map[string]config.CamOverride),
	}

	m := NewManager(cfg, camMgr, api, zerolog.Nop())

	captured := 0
	m.OnCapture(func(name string, jpeg []byte) {
		captured++
	})

	// No cameras in manager → nothing captured
	m.captureAll(context.Background())
	if captured != 0 {
		t.Errorf("should capture 0 with no cameras, got %d", captured)
	}
}

func TestManager_OnCapture(t *testing.T) {
	m := &Manager{log: zerolog.Nop()}

	var callCount int
	m.OnCapture(func(name string, jpeg []byte) {
		callCount++
	})

	if m.onCapture == nil {
		t.Error("onCapture should be set")
	}
}
