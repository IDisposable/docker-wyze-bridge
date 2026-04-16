package camera

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// mockGo2RTCServer creates a fake go2rtc API for testing.
func mockGo2RTCServer(t *testing.T) (*httptest.Server, *go2rtcmgr.APIClient) {
	t.Helper()
	streams := make(map[string]bool) // track added streams

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == "GET":
			result := make(map[string]*go2rtcmgr.StreamInfo)
			for name := range streams {
				result[name] = &go2rtcmgr.StreamInfo{
					Producers: []go2rtcmgr.ProducerInfo{{URL: "wyze://test"}},
				}
			}
			json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/api/streams" && r.Method == "PUT":
			name := r.URL.Query().Get("name")
			if name != "" {
				streams[name] = true
			}
			w.WriteHeader(200)
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			name := r.URL.Query().Get("name")
			delete(streams, name)
			w.WriteHeader(200)
		case r.URL.Path == "/api/frame.jpeg":
			w.Write([]byte{0xFF, 0xD8})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	return srv, api
}

func mockWyzeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"device_list": []interface{}{
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "HL_CAM4",
						"mac":           "AABB01",
						"nickname":      "Front Door",
						"enr":           "enr1",
						"firmware_ver":  "4.52.9",
						"device_params": map[string]interface{}{
							"p2p_id":   "UID01234567890123456",
							"p2p_type": float64(4),
							"ip":       "10.0.0.1",
							"dtls":     float64(1),
							"camera_thumbnails": map[string]interface{}{},
						},
					},
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "WYZE_CAKP2JFUS",
						"mac":           "AABB02",
						"nickname":      "Backyard",
						"enr":           "enr2",
						"firmware_ver":  "4.36.14",
						"device_params": map[string]interface{}{
							"p2p_id":   "UID01234567890123457",
							"p2p_type": float64(4),
							"ip":       "10.0.0.2",
							"dtls":     float64(1),
							"camera_thumbnails": map[string]interface{}{},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestManager(t *testing.T) (*Manager, *go2rtcmgr.APIClient) {
	t.Helper()

	_, go2rtcAPI := mockGo2RTCServer(t)

	cfg := &config.Config{
		Quality:         "hd",
		Audio:           true,
		CamOverrides:    make(map[string]config.CamOverride),
		RefreshInterval: 30 * time.Minute,
	}

	wapi := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	mgr := NewManager(cfg, wapi, go2rtcAPI, zerolog.Nop())
	return mgr, go2rtcAPI
}

func TestManager_ConnectAll_Empty(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	// No cameras → nothing happens, no panic
	mgr.ConnectAll(ctx)
}

func TestManager_ConnectCamera(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	ctx := context.Background()

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "test_cam",
		LanIP: "10.0.0.1",
		P2PID: "UID12345678901234567",
		ENR:   "enr123",
		MAC:   "AABB01",
		Model: "HL_CAM4",
		DTLS:  true,
	}, "hd", true, false)

	mgr.cameras["test_cam"] = cam

	mgr.connectCamera(ctx, cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}

	// Verify stream was added to go2rtc
	active, err := go2rtcAPI.HasActiveProducer(ctx, "test_cam")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("test_cam should have an active producer")
	}
}

func TestManager_HealthCheck(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	// Add a camera that thinks it's streaming but isn't in go2rtc
	cam := NewCamera(wyzeapi.CameraInfo{Name: "ghost_cam"}, "hd", true, false)
	cam.SetState(StateStreaming)
	mgr.cameras["ghost_cam"] = cam

	mgr.HealthCheck(ctx)

	// Should have been marked offline
	if cam.GetState() != StateOffline {
		t.Errorf("ghost cam should be offline, got %v", cam.GetState())
	}
}

func TestManager_SetQuality(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "qual_cam",
		LanIP: "10.0.0.1",
		P2PID: "UID12345",
		ENR:   "enr",
		MAC:   "AABB",
		Model: "HL_CAM4",
		DTLS:  true,
	}, "hd", true, false)
	mgr.cameras["qual_cam"] = cam

	err := mgr.SetQuality(ctx, "qual_cam", "sd")
	if err != nil {
		t.Fatalf("SetQuality: %v", err)
	}

	if cam.Quality != "sd" {
		t.Errorf("quality = %q, want sd", cam.Quality)
	}
}

func TestManager_RestartStream(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "restart_cam",
		LanIP: "10.0.0.1",
		P2PID: "UID12345",
		ENR:   "enr",
		MAC:   "AABB",
		Model: "HL_CAM4",
		DTLS:  true,
	}, "hd", true, false)
	cam.SetState(StateStreaming)
	mgr.cameras["restart_cam"] = cam

	var stateChanges []State
	mgr.OnStateChange(func(c *Camera, old, new State) {
		stateChanges = append(stateChanges, new)
	})

	mgr.RestartStream(ctx, "restart_cam")

	// Should have gone offline then reconnected
	if len(stateChanges) < 2 {
		t.Fatalf("expected at least 2 state changes, got %d: %v", len(stateChanges), stateChanges)
	}
	if stateChanges[0] != StateOffline {
		t.Errorf("first transition should be Offline, got %v", stateChanges[0])
	}
}

func TestManager_RestartStream_NonExistent(t *testing.T) {
	mgr, _ := newTestManager(t)
	// Should not panic
	mgr.RestartStream(context.Background(), "nonexistent")
}

func TestManager_SetQuality_NonExistent(t *testing.T) {
	mgr, _ := newTestManager(t)
	err := mgr.SetQuality(context.Background(), "nonexistent", "sd")
	if err != nil {
		t.Errorf("SetQuality on nonexistent should return nil, got %v", err)
	}
}

func TestManager_ReconnectErrored(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "err_cam",
		LanIP: "10.0.0.1",
		P2PID: "UID12345",
		ENR:   "enr",
		MAC:   "AA",
		Model: "HL_CAM4",
		DTLS:  true,
	}, "hd", true, false)
	cam.SetState(StateError)
	cam.ErrorCount = 1
	cam.LastSeen = time.Now().Add(-1 * time.Hour) // long past backoff
	mgr.cameras["err_cam"] = cam

	mgr.reconnectErrored(ctx)

	// Should have attempted reconnect (state changed from Error)
	if cam.GetState() == StateError {
		t.Error("camera should have been reconnected from error state")
	}
}
