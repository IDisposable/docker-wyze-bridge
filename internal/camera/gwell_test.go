package camera

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// fakeGwellProducer records calls so we can assert the camera manager
// routes Gwell cameras through it (and only those).
type fakeGwellProducer struct {
	mu        sync.Mutex
	enabled   bool
	connects  []fakeGwellCall
	disconns  []string
	connectFn func(info wyzeapi.CameraInfo, name, quality string, audio bool) (string, error)
}

type fakeGwellCall struct {
	Name    string
	Model   string
	Quality string
	Audio   bool
}

func (f *fakeGwellProducer) Enabled() bool { return f.enabled }
func (f *fakeGwellProducer) Connect(ctx context.Context, info wyzeapi.CameraInfo, name, quality string, audio bool) (string, error) {
	f.mu.Lock()
	f.connects = append(f.connects, fakeGwellCall{Name: name, Model: info.Model, Quality: quality, Audio: audio})
	f.mu.Unlock()
	if f.connectFn != nil {
		return f.connectFn(info, name, quality, audio)
	}
	return "rtsp://127.0.0.1:8564/" + name, nil
}
func (f *fakeGwellProducer) Disconnect(ctx context.Context, name string) error {
	f.mu.Lock()
	f.disconns = append(f.disconns, name)
	f.mu.Unlock()
	return nil
}

func (f *fakeGwellProducer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.connects)
}

func TestManager_RoutesGwellCameraThroughProducer(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)

	fake := &fakeGwellProducer{enabled: true}
	mgr.SetGwellProducer(fake)

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_cam",
		MAC:   "AA:BB:CC:DD:EE:01",
		ENR:   "enr-og",
		Model: "GW_GC1",
	}, "hd", true, false)
	mgr.cameras["og_cam"] = cam

	mgr.connectCamera(context.Background(), cam)

	if fake.callCount() != 1 {
		t.Fatalf("gwell producer Connect should have been called once, got %d", fake.callCount())
	}
	got := fake.connects[0]
	if got.Model != "GW_GC1" || got.Quality != "hd" || !got.Audio {
		t.Errorf("wrong Connect args: %+v", got)
	}

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}

	// go2rtc should have received the rtsp loopback URL, not a wyze:// URL.
	has, err := go2rtcAPI.HasActiveProducer(context.Background(), "og_cam")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("og_cam should have been added to go2rtc")
	}
}

func TestManager_TUTKCameraSkipsGwellProducer(t *testing.T) {
	mgr, _ := newTestManager(t)

	fake := &fakeGwellProducer{enabled: true}
	mgr.SetGwellProducer(fake)

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "v3_cam",
		LanIP: "10.0.0.5",
		P2PID: "UID",
		ENR:   "enr",
		MAC:   "AA",
		Model: "WYZE_CAKP2JFUS", // V3, TUTK
		DTLS:  true,
	}, "hd", true, false)
	mgr.cameras["v3_cam"] = cam

	mgr.connectCamera(context.Background(), cam)

	if fake.callCount() != 0 {
		t.Errorf("TUTK camera should not go through Gwell producer, got %d calls", fake.callCount())
	}
	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}
}

func TestManager_GwellCameraWithoutProducer_GoesToError(t *testing.T) {
	mgr, _ := newTestManager(t)
	// No SetGwellProducer call → m.gwell == nil

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_noprod",
		MAC:   "AA",
		Model: "GW_GC1",
	}, "hd", true, false)
	mgr.cameras["og_noprod"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateError {
		t.Errorf("state = %v, want Error (no producer attached)", cam.GetState())
	}
}

func TestManager_GwellCameraWithDisabledProducer_GoesToError(t *testing.T) {
	mgr, _ := newTestManager(t)

	mgr.SetGwellProducer(&fakeGwellProducer{enabled: false})

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_dis",
		MAC:   "AA",
		Model: "GW_GC1",
	}, "hd", true, false)
	mgr.cameras["og_dis"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateError {
		t.Errorf("state = %v, want Error (producer disabled)", cam.GetState())
	}
}

func TestManager_GwellProducerConnectFailure_Backoff(t *testing.T) {
	mgr, _ := newTestManager(t)

	fake := &fakeGwellProducer{
		enabled: true,
		connectFn: func(info wyzeapi.CameraInfo, name, quality string, audio bool) (string, error) {
			return "", errors.New("p2p handshake failed")
		},
	}
	mgr.SetGwellProducer(fake)

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_fail",
		MAC:   "AA",
		Model: "GW_GC2",
	}, "hd", true, false)
	mgr.cameras["og_fail"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateError {
		t.Errorf("state = %v, want Error", cam.GetState())
	}
	if cam.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", cam.ErrorCount)
	}
}

func TestManager_Discover_IncludesGwellWhenProducerActive(t *testing.T) {
	// We're not wiring a full HTTP Wyze API mock here — we just verify
	// the filter-loop semantics directly.
	cameras := []wyzeapi.CameraInfo{
		{Nickname: "V3", Model: "WYZE_CAKP2JFUS", MAC: "A1"},
		{Nickname: "OG", Model: "GW_GC1", MAC: "A2"},
		{Nickname: "Door", Model: "GW_BE1", MAC: "A3"},
	}

	t.Run("no producer: Gwell filtered", func(t *testing.T) {
		mgr, _ := newTestManager(t)
		supported := applyGwellFilter(mgr, cameras)
		if len(supported) != 1 || supported[0].Model != "WYZE_CAKP2JFUS" {
			t.Errorf("supported = %+v", supported)
		}
	})

	t.Run("enabled producer: Gwell included", func(t *testing.T) {
		mgr, _ := newTestManager(t)
		mgr.SetGwellProducer(&fakeGwellProducer{enabled: true})
		supported := applyGwellFilter(mgr, cameras)
		if len(supported) != 3 {
			t.Errorf("want all 3 supported, got %d: %+v", len(supported), supported)
		}
	})

	t.Run("disabled producer: Gwell filtered", func(t *testing.T) {
		mgr, _ := newTestManager(t)
		mgr.SetGwellProducer(&fakeGwellProducer{enabled: false})
		supported := applyGwellFilter(mgr, cameras)
		if len(supported) != 1 {
			t.Errorf("want 1 supported, got %d", len(supported))
		}
	})
}

// applyGwellFilter mirrors the Discover() filter loop so we can unit
// test the routing decision without a live HTTP Wyze API.
func applyGwellFilter(m *Manager, cameras []wyzeapi.CameraInfo) []wyzeapi.CameraInfo {
	gwellActive := m.gwell != nil && m.gwell.Enabled()
	var supported []wyzeapi.CameraInfo
	for _, cam := range cameras {
		if cam.IsGwell() && !gwellActive {
			continue
		}
		supported = append(supported, cam)
	}
	return supported
}
