package wyzeapi

import (
	"os"
	"path/filepath"
	"strings"
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

func TestStateFile_IgnoredRoundTrip(t *testing.T) {
	// The WebUI ignore list survives a save and load, empty fields and all.
	// SetIgnored keeps its own copy of the list, and nil clears it.
	dir := t.TempDir()
	since := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	sf := &StateFile{Cameras: map[string]CameraInfo{}}
	sf.SetIgnored([]IgnoredCamera{
		{MAC: "D03F2700CA01", Nickname: "Front Porch Cam", Model: "WYZE_CAKP2JFUS", Since: since},
		{MAC: "80482C0D6B02", Since: since},
	})
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Ignored) != 2 || loaded.Ignored[0].MAC != "D03F2700CA01" || loaded.Ignored[0].Nickname != "Front Porch Cam" || !loaded.Ignored[0].Since.Equal(since) {
		t.Errorf("ignored = %+v", loaded.Ignored)
	}
	if loaded.Ignored[1].Nickname != "" || loaded.Ignored[1].Model != "" {
		t.Errorf("empty nickname/model should round-trip empty: %+v", loaded.Ignored[1])
	}

	// SetIgnored copies: mutating the caller's slice afterwards must
	// not reach the state file.
	list := []IgnoredCamera{{MAC: "AA"}}
	sf.SetIgnored(list)
	list[0].MAC = "BB"
	if sf.Ignored[0].MAC != "AA" {
		t.Error("SetIgnored aliased the caller's slice")
	}
	sf.SetIgnored(nil)
	if len(sf.Ignored) != 0 {
		t.Error("SetIgnored(nil) should clear the list")
	}
}

func TestStateFile_LoadWithoutIgnoredField(t *testing.T) {
	// Older state files have no "ignored" key: load must leave the
	// list empty rather than fail.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wyze-bridge.state.json"), []byte(`{"cameras":{},"updated":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Ignored) != 0 {
		t.Errorf("ignored = %+v, want empty", loaded.Ignored)
	}
}

func TestStateFile_LayoutRoundTrip(t *testing.T) {
	// A state file with no layout loads with a nil Layout. A saved tile
	// order and lock come back as set, even if the caller changes its
	// slice after SetLayout.
	dir := t.TempDir()
	sf := &StateFile{Cameras: map[string]CameraInfo{}}
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadState(dir, zerolog.Nop())
	if loaded.Layout != nil {
		t.Errorf("no layout saved should load as nil, got %+v", loaded.Layout)
	}

	order := []string{"driveway", "side_garage"}
	sf.SetLayout(Layout{Order: order, Locked: true})
	order[0] = "mutated" // SetLayout must have copied
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ = LoadState(dir, zerolog.Nop())
	if loaded.Layout == nil || !loaded.Layout.Locked || len(loaded.Layout.Order) != 2 || loaded.Layout.Order[0] != "driveway" {
		t.Errorf("layout = %+v", loaded.Layout)
	}
}

func TestStateFile_LayoutRotationRoundTrip(t *testing.T) {
	// Per-tile rotation survives a save and load, and SetLayout copies the
	// map so later edits by the caller don't leak in. No rotations at all
	// is stored as nil.
	dir := t.TempDir()
	sf := &StateFile{Cameras: map[string]CameraInfo{}}
	rot := map[string]int{"garage": 90}
	sf.SetLayout(Layout{Order: []string{"garage"}, Rotation: rot})
	rot["garage"] = 270 // SetLayout must have copied the map
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadState(dir, zerolog.Nop())
	if loaded.Layout == nil || loaded.Layout.Rotation["garage"] != 90 {
		t.Errorf("layout = %+v", loaded.Layout)
	}
	sf.SetLayout(Layout{Order: []string{"garage"}})
	if sf.Layout.Rotation != nil {
		t.Error("empty rotation should be stored as nil")
	}
}

func TestStateFile_PinnedIPsRoundTrip(t *testing.T) {
	// Pinned LAN IPs are saved under pinned_ips and load back as set, even
	// if the caller edits its map afterwards. Clearing the last pin drops
	// the field from the file.
	dir := t.TempDir()
	sf := &StateFile{Cameras: map[string]CameraInfo{}}
	pins := map[string]string{"D03F27B081EF": "192.168.4.200"}
	sf.SetPinnedIPs(pins)
	pins["D03F27B081EF"] = "mutated" // SetPinnedIPs must have copied
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadState(dir, zerolog.Nop())
	if loaded.PinnedIPs["D03F27B081EF"] != "192.168.4.200" {
		t.Errorf("pinned_ips = %v", loaded.PinnedIPs)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "wyze-bridge.state.json"))
	if !strings.Contains(string(data), `"pinned_ips"`) {
		t.Error("pins should be in the file under pinned_ips")
	}

	// Clearing the last pin drops the field from the file entirely.
	sf.SetPinnedIPs(nil)
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "wyze-bridge.state.json"))
	if strings.Contains(string(data), `"pinned_ips"`) {
		t.Error("an empty pin map should be omitted from the file")
	}
	loaded, _ = LoadState(dir, zerolog.Nop())
	if len(loaded.PinnedIPs) != 0 {
		t.Errorf("after clear: %v", loaded.PinnedIPs)
	}
}

func TestStateFile_RulesRoundTrip(t *testing.T) {
	// Camera rules survive a save and load, copied away from the caller.
	// Rules that do nothing clear the field, but extra subnets alone are
	// kept for when the scope goes back to local.
	dir := t.TempDir()
	sf := &StateFile{Cameras: map[string]CameraInfo{}}
	names := []string{"Pet Cam*"}
	sf.SetRules(CameraRules{Scope: "local", Subnets: []string{"192.168.4.0/22"}, Names: names, Block: true})
	names[0] = "mutated" // SetRules must have copied
	if err := sf.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadState(dir, zerolog.Nop())
	if loaded.Rules == nil || loaded.Rules.Scope != "local" || loaded.Rules.Names[0] != "Pet Cam*" || !loaded.Rules.Block || loaded.Rules.Subnets[0] != "192.168.4.0/22" {
		t.Errorf("rules = %+v", loaded.Rules)
	}
	// Empty rules drop the field; extra subnets alone keep it (they're
	// worth remembering for when scope goes back to local).
	sf.SetRules(CameraRules{})
	if sf.Rules != nil {
		t.Error("empty rules should clear the field")
	}
	sf.SetRules(CameraRules{Subnets: []string{"10.0.0.0/8"}})
	if sf.Rules == nil {
		t.Error("subnets alone should be kept")
	}
	if !(CameraRules{}).Empty() || (CameraRules{Scope: "local"}).Empty() || (CameraRules{MACs: []string{"A"}}).Empty() || !(CameraRules{Scope: "any"}).Empty() {
		t.Error("Empty() wrong")
	}
}
