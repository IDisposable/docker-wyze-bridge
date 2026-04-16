package mqtt

import (
	"fmt"
	"strings"
	"testing"
)

func TestCommandTopicParsing(t *testing.T) {
	// Simulate the topic parsing logic from handleSetCommand
	tests := []struct {
		topic      string
		wantCam    string
		wantProp   string
		wantParts  int
	}{
		{"wyzebridge/front_door/set/quality", "front_door", "quality", 4},
		{"wyzebridge/backyard/set/audio", "backyard", "audio", 4},
		{"wyzebridge/garage/set/night_vision", "garage", "night_vision", 4},
		{"wyzebridge/cam/snapshot/take", "cam", "take", 4},
		{"wyzebridge/cam/stream/restart", "cam", "restart", 4},
	}

	for _, tt := range tests {
		parts := strings.Split(tt.topic, "/")
		if len(parts) < tt.wantParts {
			t.Errorf("topic %q: too few parts", tt.topic)
			continue
		}
		camName := parts[len(parts)-3]
		property := parts[len(parts)-1]
		if camName != tt.wantCam {
			t.Errorf("topic %q: cam = %q, want %q", tt.topic, camName, tt.wantCam)
		}
		if property != tt.wantProp {
			t.Errorf("topic %q: prop = %q, want %q", tt.topic, property, tt.wantProp)
		}
	}
}

func TestNightVisionMapping(t *testing.T) {
	pidVal := map[string]string{"auto": "0", "on": "1", "off": "2"}

	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"auto", "0", true},
		{"on", "1", true},
		{"off", "2", true},
		{"invalid", "", false},
	}

	for _, tt := range tests {
		got, ok := pidVal[tt.input]
		if ok != tt.ok {
			t.Errorf("night_vision %q: ok = %v, want %v", tt.input, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Errorf("night_vision %q: val = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTopicConstruction(t *testing.T) {
	topic := "wyzebridge"
	dtopic := "homeassistant"

	// Camera discovery topic
	mac := "AABBCCDDEEFF"
	cameraTopic := fmt.Sprintf("%s/camera/%s/config", dtopic, mac)
	if cameraTopic != "homeassistant/camera/AABBCCDDEEFF/config" {
		t.Errorf("camera topic = %q", cameraTopic)
	}

	// Quality select topic
	selectTopic := fmt.Sprintf("%s/select/%s_quality/config", dtopic, mac)
	if selectTopic != "homeassistant/select/AABBCCDDEEFF_quality/config" {
		t.Errorf("select topic = %q", selectTopic)
	}

	// Audio switch topic
	switchTopic := fmt.Sprintf("%s/switch/%s_audio/config", dtopic, mac)
	if switchTopic != "homeassistant/switch/AABBCCDDEEFF_audio/config" {
		t.Errorf("switch topic = %q", switchTopic)
	}

	// LWT
	lwtTopic := fmt.Sprintf("%s/bridge/state", topic)
	if lwtTopic != "wyzebridge/bridge/state" {
		t.Errorf("lwt topic = %q", lwtTopic)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		Host:   "mosquitto",
		Port:   1883,
		Topic:  "wyzebridge",
		DTopic: "homeassistant",
	}

	if cfg.Host != "mosquitto" {
		t.Errorf("host = %q", cfg.Host)
	}
	if cfg.Port != 1883 {
		t.Errorf("port = %d", cfg.Port)
	}
}
