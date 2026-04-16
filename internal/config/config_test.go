package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear any env that might interfere (devcontainer sets STATE_DIR, IMG_DIR, LOG_LEVEL)
	t.Setenv("WYZE_EMAIL", "")
	t.Setenv("WYZE_PASSWORD", "")
	t.Setenv("WYZE_API_ID", "")
	t.Setenv("WYZE_API_KEY", "")
	t.Setenv("STATE_DIR", "")
	t.Setenv("IMG_DIR", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("WB_PORT", "")
	t.Setenv("QUALITY", "")
	t.Setenv("AUDIO", "")
	t.Setenv("MQTT_TOPIC", "")
	t.Setenv("MQTT_DTOPIC", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.WBPort != 5080 {
		t.Errorf("WBPort = %d, want 5080", cfg.WBPort)
	}
	if cfg.Quality != "hd" {
		t.Errorf("Quality = %q, want hd", cfg.Quality)
	}
	if !cfg.Audio {
		t.Error("Audio should default to true")
	}
	if cfg.MQTTTopic != "wyzebridge" {
		t.Errorf("MQTTTopic = %q, want wyzebridge", cfg.MQTTTopic)
	}
	if cfg.MQTTDTopic != "homeassistant" {
		t.Errorf("MQTTDTopic = %q, want homeassistant", cfg.MQTTDTopic)
	}
	if cfg.StateDir != "/config" {
		t.Errorf("StateDir = %q, want /config", cfg.StateDir)
	}
}

func TestLoadPasswordDerivation(t *testing.T) {
	t.Setenv("WYZE_EMAIL", "user@example.com")
	t.Setenv("WB_PASSWORD", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.WBPassword != "user" {
		t.Errorf("WBPassword = %q, want 'user' derived from email", cfg.WBPassword)
	}
}

func TestLoadMQTTHostEnablesMQTT(t *testing.T) {
	t.Setenv("MQTT_HOST", "mosquitto")
	t.Setenv("MQTT_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.MQTTEnabled {
		t.Error("MQTT_HOST should implicitly enable MQTT")
	}
}

func TestCamOverrides(t *testing.T) {
	t.Setenv("QUALITY_FRONT_DOOR", "sd")
	t.Setenv("AUDIO_BACKYARD", "false")
	t.Setenv("RECORD_GARAGE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if q := cfg.CamQuality("front_door"); q != "sd" {
		t.Errorf("CamQuality(front_door) = %q, want sd", q)
	}
	if q := cfg.CamQuality("unknown_cam"); q != cfg.Quality {
		t.Errorf("CamQuality(unknown) = %q, want default %q", q, cfg.Quality)
	}
	if a := cfg.CamAudio("backyard"); a {
		t.Error("CamAudio(backyard) should be false")
	}
	if r := cfg.CamRecord("garage"); !r {
		t.Error("CamRecord(garage) should be true")
	}
}

func TestNormalizeCamName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"front door", "FRONT_DOOR"},
		{"  Backyard  ", "BACKYARD"},
		{"GARAGE", "GARAGE"},
		{"living room cam", "LIVING_ROOM_CAM"},
	}
	for _, tt := range tests {
		if got := normalizeCamName(tt.in); got != tt.want {
			t.Errorf("normalizeCamName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"trace", "trace"},
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"warning", "warn"},
		{"error", "error"},
		{"garbage", "info"},
	}
	for _, tt := range tests {
		got := parseLogLevel(tt.in)
		if got.String() != tt.want {
			t.Errorf("parseLogLevel(%q) = %q, want %q", tt.in, got.String(), tt.want)
		}
	}
}
