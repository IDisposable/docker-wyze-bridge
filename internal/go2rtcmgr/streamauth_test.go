package go2rtcmgr

import (
	"testing"
)

func TestParseStreamAuth_Simple(t *testing.T) {
	entries := ParseStreamAuth("admin:pass123")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Username != "admin" {
		t.Errorf("username = %q", entries[0].Username)
	}
	if entries[0].Password != "pass123" {
		t.Errorf("password = %q", entries[0].Password)
	}
	if len(entries[0].Cameras) != 0 {
		t.Errorf("cameras should be empty, got %v", entries[0].Cameras)
	}
}

func TestParseStreamAuth_WithCameras(t *testing.T) {
	entries := ParseStreamAuth("user:pass@cam1,cam2")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Username != "user" {
		t.Errorf("username = %q", entries[0].Username)
	}
	if len(entries[0].Cameras) != 2 {
		t.Fatalf("cameras = %v", entries[0].Cameras)
	}
	if entries[0].Cameras[0] != "cam1" || entries[0].Cameras[1] != "cam2" {
		t.Errorf("cameras = %v", entries[0].Cameras)
	}
}

func TestParseStreamAuth_MultiUser(t *testing.T) {
	entries := ParseStreamAuth("user1:pass1@cam1,cam2|user2:pass2")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Username != "user1" || len(entries[0].Cameras) != 2 {
		t.Errorf("entry[0] = %+v", entries[0])
	}
	if entries[1].Username != "user2" || len(entries[1].Cameras) != 0 {
		t.Errorf("entry[1] = %+v", entries[1])
	}
}

func TestParseStreamAuth_Empty(t *testing.T) {
	entries := ParseStreamAuth("")
	if entries != nil {
		t.Errorf("empty should return nil, got %v", entries)
	}
}

func TestParseStreamAuth_Invalid(t *testing.T) {
	// No colon separator — should skip
	entries := ParseStreamAuth("justausername")
	if len(entries) != 0 {
		t.Errorf("invalid format should return empty, got %v", entries)
	}
}

func TestStreamAuth_GlobalRTSP(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	b.SetStreamAuth([]StreamAuthEntry{
		{Username: "admin", Password: "secret"},
	})

	cfg := b.Build()
	if cfg.RTSP.Username != "admin" {
		t.Errorf("RTSP username = %q, want admin", cfg.RTSP.Username)
	}
	if cfg.RTSP.Password != "secret" {
		t.Errorf("RTSP password = %q, want secret", cfg.RTSP.Password)
	}
}

func TestStreamAuth_PerCameraNotGlobal(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	b.SetStreamAuth([]StreamAuthEntry{
		{Username: "user1", Password: "pass1", Cameras: []string{"cam1"}},
	})

	cfg := b.Build()
	// Per-camera auth should NOT set global RTSP credentials
	if cfg.RTSP.Username != "" {
		t.Errorf("per-camera auth should not set global RTSP username, got %q", cfg.RTSP.Username)
	}
}

func TestStreamAuth_MultiUser(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	b.SetStreamAuth([]StreamAuthEntry{
		{Username: "u1", Password: "p1", Cameras: []string{"cam1"}},
		{Username: "u2", Password: "p2"},
	})

	cfg := b.Build()
	// Multiple users → don't set global auth (ambiguous)
	if cfg.RTSP.Username != "" {
		t.Error("multi-user should not set global RTSP auth")
	}
}

func TestParseStreamAuth_Whitespace(t *testing.T) {
	entries := ParseStreamAuth(" user:pass@cam1 , cam2 | admin:secret ")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Cameras[0] != "cam1" || entries[0].Cameras[1] != "cam2" {
		t.Errorf("cameras should be trimmed: %v", entries[0].Cameras)
	}
}
