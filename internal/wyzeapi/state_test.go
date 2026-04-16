package wyzeapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestStateFile_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	log := zerolog.Nop()

	// Create and save state
	sf := &StateFile{
		Auth: &AuthState{
			AccessToken:  "test_token",
			RefreshToken: "test_refresh",
			UserID:       "user123",
			PhoneID:      "phone456",
			ExpiresAt:    time.Now().Add(24 * time.Hour),
		},
		Cameras: map[string]CameraInfo{
			"AABBCCDDEEFF": {
				Name:  "front_door",
				Model: "HL_CAM4",
				MAC:   "AABBCCDDEEFF",
				LanIP: "192.168.1.10",
			},
		},
	}

	if err := sf.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "wyze-bridge.state.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not created: %v", err)
	}

	// Load it back
	loaded, err := LoadState(dir, log)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if loaded.Auth == nil {
		t.Fatal("loaded auth is nil")
	}
	if loaded.Auth.AccessToken != "test_token" {
		t.Errorf("access_token = %q", loaded.Auth.AccessToken)
	}
	if loaded.Auth.UserID != "user123" {
		t.Errorf("user_id = %q", loaded.Auth.UserID)
	}
	if len(loaded.Cameras) != 1 {
		t.Errorf("cameras count = %d, want 1", len(loaded.Cameras))
	}
	if cam, ok := loaded.Cameras["AABBCCDDEEFF"]; !ok || cam.Name != "front_door" {
		t.Errorf("camera not loaded correctly")
	}
}

func TestStateFile_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	log := zerolog.Nop()

	sf, err := LoadState(dir, log)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if sf.Auth != nil {
		t.Error("missing state should have nil auth")
	}
	if len(sf.Cameras) != 0 {
		t.Error("missing state should have empty cameras")
	}
}

func TestStateFile_LoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	log := zerolog.Nop()

	path := filepath.Join(dir, "wyze-bridge.state.json")
	os.WriteFile(path, []byte("not valid json{{{"), 0600)

	sf, err := LoadState(dir, log)
	if err != nil {
		t.Fatalf("corrupt state should not error: %v", err)
	}
	if sf.Auth != nil {
		t.Error("corrupt state should have nil auth")
	}
}

func TestStateFile_UpdateCameras(t *testing.T) {
	sf := &StateFile{Cameras: make(map[string]CameraInfo)}

	cameras := []CameraInfo{
		{MAC: "AA", Name: "cam1"},
		{MAC: "BB", Name: "cam2"},
	}
	sf.UpdateCameras(cameras)

	if len(sf.Cameras) != 2 {
		t.Errorf("cameras = %d, want 2", len(sf.Cameras))
	}
	if sf.Cameras["AA"].Name != "cam1" {
		t.Errorf("cam1 not found by MAC")
	}

	// Update replaces
	sf.UpdateCameras([]CameraInfo{{MAC: "CC", Name: "cam3"}})
	if len(sf.Cameras) != 1 {
		t.Errorf("cameras after update = %d, want 1", len(sf.Cameras))
	}
}

func TestStateFile_Permissions(t *testing.T) {
	if os.Getenv("GOOS") == "windows" || filepath.Separator == '\\' {
		t.Skip("skipping permission test on Windows")
	}

	dir := t.TempDir()

	sf := &StateFile{Cameras: make(map[string]CameraInfo)}
	sf.Save(dir)

	path := filepath.Join(dir, "wyze-bridge.state.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// File should be 0600 (owner read/write only)
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		t.Errorf("state file permissions = %o, should not be world-readable", perm)
	}
}
