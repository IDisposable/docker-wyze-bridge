package go2rtcmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigBuilder_Build(t *testing.T) {
	b := NewConfigBuilder("warn", "stun:stun.l.google.com:19302", "192.168.1.50")
	b.AddStream(StreamEntry{
		Name: "front_door",
		URL:  "wyze://192.168.1.10?uid=XXX&enr=YYY&mac=AABBCCDDEEFF&model=WYZEDB3&subtype=hd&dtls=true",
	})
	b.AddStream(StreamEntry{
		Name: "backyard",
		URL:  "wyze://192.168.1.11?uid=ZZZ&enr=WWW&mac=001122334455&model=WYZE_CAKP2JFUS&subtype=hd&dtls=true",
	})

	cfg := b.Build()

	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want warn", cfg.Log.Level)
	}
	if cfg.API.Listen != ":1984" {
		t.Errorf("API.Listen = %q, want :1984", cfg.API.Listen)
	}
	if cfg.API.Origin != "*" {
		t.Errorf("API.Origin = %q, want *", cfg.API.Origin)
	}
	if cfg.RTSP.Listen != ":8554" {
		t.Errorf("RTSP.Listen = %q, want :8554", cfg.RTSP.Listen)
	}
	if cfg.WebRTC.Listen != ":8889" {
		t.Errorf("WebRTC.Listen = %q, want :8889", cfg.WebRTC.Listen)
	}
	if len(cfg.WebRTC.Candidates) != 1 || cfg.WebRTC.Candidates[0] != "192.168.1.50:8889" {
		t.Errorf("WebRTC.Candidates = %v, want [192.168.1.50:8889]", cfg.WebRTC.Candidates)
	}
	if len(cfg.WebRTC.ICEServers) != 1 {
		t.Errorf("ICEServers length = %d, want 1", len(cfg.WebRTC.ICEServers))
	}
	if len(cfg.Streams) != 2 {
		t.Errorf("Streams length = %d, want 2", len(cfg.Streams))
	}
	if _, ok := cfg.Streams["front_door"]; !ok {
		t.Error("missing front_door stream")
	}
}

func TestConfigBuilder_NoWBIP(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	cfg := b.Build()

	if len(cfg.WebRTC.Candidates) != 0 {
		t.Errorf("Candidates should be empty when WB_IP is not set, got %v", cfg.WebRTC.Candidates)
	}
	if len(cfg.WebRTC.ICEServers) != 0 {
		t.Errorf("ICEServers should be empty when no STUN server, got %v", cfg.WebRTC.ICEServers)
	}
}

func TestConfigBuilder_WriteConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go2rtc.yaml")

	b := NewConfigBuilder("info", "stun:stun.l.google.com:19302", "")
	b.AddStream(StreamEntry{
		Name: "test_cam",
		URL:  "wyze://10.0.0.1?uid=ABC&enr=DEF&mac=AABB&model=HL_CAM4&subtype=hd&dtls=true",
	})

	if err := b.WriteConfig(path); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "test_cam") {
		t.Error("config file should contain test_cam stream")
	}

	// Verify it's valid YAML
	var parsed Go2RTCConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal written config: %v", err)
	}
	if parsed.API.Listen != ":1984" {
		t.Errorf("parsed API.Listen = %q, want :1984", parsed.API.Listen)
	}
}

func TestConfigBuilder_RecordingStream(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	b.AddStream(StreamEntry{
		Name:           "front_door",
		URL:            "wyze://10.0.0.1?uid=X",
		Record:         true,
		RecordPath:     "/media/recordings/front_door/%Y/%m/%d/%H-%M-%S",
		RecordDuration: "60s",
	})
	b.AddStream(StreamEntry{
		Name: "backyard",
		URL:  "wyze://10.0.0.2?uid=Y",
	})

	cfg := b.Build()

	// front_door should be a map (recording enabled)
	fd, ok := cfg.Streams["front_door"].(map[string]interface{})
	if !ok {
		t.Fatalf("front_door should be a map, got %T", cfg.Streams["front_door"])
	}
	if fd["record"] != true {
		t.Errorf("record = %v", fd["record"])
	}
	if fd["record_path"] != "/media/recordings/front_door/%Y/%m/%d/%H-%M-%S" {
		t.Errorf("record_path = %v", fd["record_path"])
	}

	// backyard should be a simple list (no recording)
	by, ok := cfg.Streams["backyard"].([]string)
	if !ok {
		t.Fatalf("backyard should be []string, got %T", cfg.Streams["backyard"])
	}
	if len(by) != 1 {
		t.Errorf("backyard URLs = %d", len(by))
	}
}

func TestConfigBuilder_ClearStreams(t *testing.T) {
	b := NewConfigBuilder("info", "", "")
	b.AddStream(StreamEntry{Name: "a", URL: "test://a"})
	b.AddStream(StreamEntry{Name: "b", URL: "test://b"})

	cfg := b.Build()
	if len(cfg.Streams) != 2 {
		t.Fatal("expected 2 streams before clear")
	}

	b.ClearStreams()
	cfg = b.Build()
	if len(cfg.Streams) != 0 {
		t.Errorf("expected 0 streams after clear, got %d", len(cfg.Streams))
	}
}
