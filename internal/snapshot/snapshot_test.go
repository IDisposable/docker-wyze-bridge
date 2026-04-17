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
		Quality:         "hd",
		Audio:           true,
		CamOverrides:    make(map[string]config.CamOverride),
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
		SnapshotInterval: 60,
		SnapshotPath:     imgDir,
		CamOverrides:     make(map[string]config.CamOverride),
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

func TestManager_CaptureOne_WithFileName(t *testing.T) {
	_, api := mockGo2RTC(t)
	camMgr := makeCamMgr(t, api)

	imgDir := t.TempDir()
	cfg := &config.Config{
		SnapshotInterval: 60,
		SnapshotFileName: "{cam_name}_%Y%m%d",
		SnapshotPath:     imgDir,
		CamOverrides:     make(map[string]config.CamOverride),
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
	cfg := &config.Config{SnapshotPath: imgDir, CamOverrides: make(map[string]config.CamOverride)}
	m := &Manager{cfg: cfg, log: zerolog.Nop()}

	jpeg := []byte{0xFF, 0xD8, 0xFF}
	gotPath, err := m.saveSnapshot("test_cam", jpeg)
	if err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}

	path := filepath.Join(imgDir, "test_cam.jpg")
	if gotPath != path {
		t.Errorf("saveSnapshot returned path %q, want %q", gotPath, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if len(data) != 3 {
		t.Errorf("file size = %d, want 3", len(data))
	}
}

// TestManager_SaveSnapshot_SplitTemplates exercises the SNAPSHOT_PATH +
// SNAPSHOT_FILE_NAME split API: both fields are templates (with
// {cam_name} and strftime tokens), and MkdirAll has to create the
// chain of strftime subdirs before the write lands. This is what HA
// users get by default via home_assistant/run.sh.
func TestManager_SaveSnapshot_SplitTemplates(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		SnapshotPath:     root + "/{cam_name}/%Y/%m/%d",
		SnapshotFileName: "%H-%M-%S",
		CamOverrides:     make(map[string]config.CamOverride),
	}
	m := &Manager{cfg: cfg, log: zerolog.Nop()}

	jpeg := []byte{0xFF, 0xD8, 0xFF}
	if _, err := m.saveSnapshot("front_door", jpeg); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}

	// Walk the cam subtree — no asserting exact filename since time-of-day
	// is non-deterministic, but we want exactly one .jpg under a
	// YYYY/MM/DD chain.
	camDir := filepath.Join(root, "front_door")
	matches, err := filepath.Glob(filepath.Join(camDir, "*/*/*/*.jpg"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 snapshot under %s/YYYY/MM/DD/*.jpg, got %d: %v",
			camDir, len(matches), matches)
	}
}

func TestManager_CaptureAll_FiltersCameras(t *testing.T) {
	_, api := mockGo2RTC(t)
	camMgr := makeCamMgr(t, api)

	cfg := &config.Config{
		SnapshotInterval: 60,
		SnapshotCameras:  []string{"FRONT_DOOR"},
		SnapshotPath:     t.TempDir(),
		CamOverrides:     make(map[string]config.CamOverride),
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
