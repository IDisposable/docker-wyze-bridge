package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAML_FilePresent(t *testing.T) {
	dir := t.TempDir()
	yaml := `
WYZE_EMAIL: yaml@example.com
WYZE_API_ID: yaml-id
MQTT_HOST: mosquitto
MQTT_ENABLED: true
MQTT_TOPIC: custom-topic
LATITUDE: 38.627
LONGITUDE: -90.199
RECORD_ALL: true
SNAPSHOT_INTERVAL: 300
CAM_OPTIONS:
  - CAM_NAME: garage
    QUALITY: sd
    AUDIO: false
    RECORD: true
`
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte(yaml), 0644)

	cfg := &Config{
		StateDir:     dir,
		CamOverrides: make(map[string]CamOverride),
		MQTTTopic:    "", // empty so YAML can fill it
		MQTTPort:     1883,
	}

	err := cfg.loadYAML()
	if err != nil {
		t.Fatalf("loadYAML: %v", err)
	}

	if cfg.WyzeEmail != "yaml@example.com" {
		t.Errorf("WyzeEmail = %q", cfg.WyzeEmail)
	}
	if cfg.WyzeAPIID != "yaml-id" {
		t.Errorf("WyzeAPIID = %q", cfg.WyzeAPIID)
	}
	if cfg.MQTTHost != "mosquitto" {
		t.Errorf("MQTTHost = %q", cfg.MQTTHost)
	}
	if !cfg.MQTTEnabled {
		t.Error("MQTT should be enabled from YAML")
	}
	if cfg.MQTTTopic != "custom-topic" {
		t.Errorf("MQTTTopic = %q", cfg.MQTTTopic)
	}
	if cfg.Latitude != 38.627 {
		t.Errorf("Latitude = %f", cfg.Latitude)
	}
	if !cfg.RecordAll {
		t.Error("RecordAll should be true from YAML")
	}
	if cfg.SnapshotInterval != 300 {
		t.Errorf("SnapshotInterval = %d", cfg.SnapshotInterval)
	}

	ov, ok := cfg.CamOverrides["GARAGE"]
	if !ok {
		t.Fatal("GARAGE override not found")
	}
	if ov.Quality == nil || *ov.Quality != "sd" {
		t.Errorf("GARAGE quality = %v", ov.Quality)
	}
	if ov.Audio == nil || *ov.Audio != false {
		t.Errorf("GARAGE audio = %v", ov.Audio)
	}
	if ov.Record == nil || *ov.Record != true {
		t.Errorf("GARAGE record = %v", ov.Record)
	}
}

func TestLoadYAML_EnvTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	yaml := `WYZE_EMAIL: yaml@example.com`
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte(yaml), 0644)

	cfg := &Config{
		WyzeEmail:    "env@example.com", // already set from env
		StateDir:     dir,
		CamOverrides: make(map[string]CamOverride),
	}

	cfg.loadYAML()

	if cfg.WyzeEmail != "env@example.com" {
		t.Errorf("env should take precedence, got %q", cfg.WyzeEmail)
	}
}

func TestLoadYAML_Missing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		StateDir:     dir,
		CamOverrides: make(map[string]CamOverride),
	}

	err := cfg.loadYAML()
	if err != nil {
		t.Errorf("missing YAML should not error: %v", err)
	}
}

func TestLoadYAML_Invalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte("{{invalid yaml"), 0644)

	cfg := &Config{
		StateDir:     dir,
		CamOverrides: make(map[string]CamOverride),
	}

	err := cfg.loadYAML()
	if err == nil {
		t.Error("invalid YAML should return error")
	}
}
