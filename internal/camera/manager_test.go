package camera

import (
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestManager_GetCamera(t *testing.T) {
	// Build a manager with manually injected cameras (no API needed)
	m := &Manager{
		cameras: map[string]*Camera{
			"front_door": NewCamera(wyzeapi.CameraInfo{Name: "front_door", Nickname: "Front Door"}, "hd", true, false),
			"backyard":   NewCamera(wyzeapi.CameraInfo{Name: "backyard", Nickname: "Backyard"}, "sd", false, true),
		},
	}

	cam := m.GetCamera("front_door")
	if cam == nil {
		t.Fatal("front_door should exist")
	}
	if cam.Quality != "hd" {
		t.Errorf("quality = %q", cam.Quality)
	}

	cam = m.GetCamera("nonexistent")
	if cam != nil {
		t.Error("nonexistent should be nil")
	}
}

func TestManager_Cameras(t *testing.T) {
	m := &Manager{
		cameras: map[string]*Camera{
			"a": NewCamera(wyzeapi.CameraInfo{Name: "a"}, "hd", true, false),
			"b": NewCamera(wyzeapi.CameraInfo{Name: "b"}, "sd", true, false),
		},
	}

	cams := m.Cameras()
	if len(cams) != 2 {
		t.Errorf("cameras = %d, want 2", len(cams))
	}
}

// TestManager_Cameras_StableOrder pins the contract callers rely on
// for predictable UI rendering: Cameras() is sorted by name.
// Go map iteration is randomized, so without the sort the grid
// shuffled on every refresh.
func TestManager_Cameras_StableOrder(t *testing.T) {
	// Insert in random-looking order so a stable result implies
	// the sort actually happened (not just matching insertion order).
	m := &Manager{
		cameras: map[string]*Camera{
			"zebra":  NewCamera(wyzeapi.CameraInfo{Name: "zebra"}, "hd", true, false),
			"apple":  NewCamera(wyzeapi.CameraInfo{Name: "apple"}, "hd", true, false),
			"mango":  NewCamera(wyzeapi.CameraInfo{Name: "mango"}, "hd", true, false),
			"banana": NewCamera(wyzeapi.CameraInfo{Name: "banana"}, "hd", true, false),
		},
	}
	want := []string{"apple", "banana", "mango", "zebra"}

	// Run several times because map iteration randomness means a
	// broken implementation could coincidentally produce the sorted
	// result on a single iteration.
	for i := 0; i < 20; i++ {
		cams := m.Cameras()
		for idx, name := range want {
			if cams[idx].Name() != name {
				t.Fatalf("run %d: cams[%d]=%q, want %q (full order: %v)", i, idx, cams[idx].Name(), name, cameraNames(cams))
			}
		}
	}
}

func cameraNames(cams []*Camera) []string {
	out := make([]string, len(cams))
	for i, c := range cams {
		out[i] = c.Name()
	}
	return out
}

func TestManager_OnStateChange(t *testing.T) {
	m := &Manager{
		cameras: map[string]*Camera{
			"test": NewCamera(wyzeapi.CameraInfo{Name: "test"}, "hd", true, false),
		},
	}

	var called bool
	var gotOld, gotNew State
	m.OnStateChange(func(cam *Camera, oldState, newState State) {
		called = true
		gotOld = oldState
		gotNew = newState
	})

	cam := m.cameras["test"]
	m.changeState(cam, StateConnecting)

	if !called {
		t.Error("OnStateChange callback not called")
	}
	if gotOld != StateOffline {
		t.Errorf("old state = %v, want Offline", gotOld)
	}
	if gotNew != StateConnecting {
		t.Errorf("new state = %v, want Connecting", gotNew)
	}
}

func TestManager_ChangeState_SameState(t *testing.T) {
	m := &Manager{
		cameras: map[string]*Camera{
			"test": NewCamera(wyzeapi.CameraInfo{Name: "test"}, "hd", true, false),
		},
	}

	var callCount int
	m.OnStateChange(func(cam *Camera, oldState, newState State) {
		callCount++
	})

	cam := m.cameras["test"]
	m.changeState(cam, StateOffline) // same as initial

	if callCount != 0 {
		t.Error("should not fire callback for same state")
	}
}

func TestCamera_StreamURL(t *testing.T) {
	cam := NewCamera(wyzeapi.CameraInfo{
		LanIP: "10.0.0.5",
		P2PID: "UID123",
		ENR:   "abc",
		MAC:   "AABB",
		Model: "HL_CAM4",
		DTLS:  true,
	}, "hd", true, false)

	url := cam.StreamURL()
	if url == "" {
		t.Error("StreamURL should not be empty")
	}
}

func TestCamera_UpdateInfo(t *testing.T) {
	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "test",
		LanIP: "10.0.0.1",
	}, "hd", true, false)

	cam.UpdateInfo(wyzeapi.CameraInfo{
		Name:  "test",
		LanIP: "10.0.0.99",
	})

	if cam.Info.LanIP != "10.0.0.99" {
		t.Errorf("IP not updated: %q", cam.Info.LanIP)
	}
}
