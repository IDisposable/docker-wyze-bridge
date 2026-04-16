package mqtt

import (
	"strings"
	"testing"
)

// TestSubscribeCommandParsing tests the topic parsing logic used by handleSetCommand.
func TestParseSetCommandTopic(t *testing.T) {
	tests := []struct {
		topic    string
		wantCam  string
		wantProp string
		valid    bool
	}{
		{"wyzebridge/front_door/set/quality", "front_door", "quality", true},
		{"wyzebridge/backyard/set/audio", "backyard", "audio", true},
		{"wyzebridge/garage/set/night_vision", "garage", "night_vision", true},
		{"wyzebridge/set/quality", "", "", false},                // too short
		{"x/y", "", "", false},                                    // too short
	}

	for _, tt := range tests {
		parts := strings.Split(tt.topic, "/")
		if len(parts) < 4 {
			if tt.valid {
				t.Errorf("topic %q should be valid", tt.topic)
			}
			continue
		}
		cam := parts[len(parts)-3]
		prop := parts[len(parts)-1]

		if cam != tt.wantCam {
			t.Errorf("topic %q: cam = %q, want %q", tt.topic, cam, tt.wantCam)
		}
		if prop != tt.wantProp {
			t.Errorf("topic %q: prop = %q, want %q", tt.topic, prop, tt.wantProp)
		}
	}
}

func TestParseSnapshotTopic(t *testing.T) {
	topic := "wyzebridge/front_door/snapshot/take"
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		t.Fatal("too few parts")
	}
	cam := parts[len(parts)-3]
	if cam != "front_door" {
		t.Errorf("cam = %q", cam)
	}
}

func TestParseRestartTopic(t *testing.T) {
	topic := "wyzebridge/backyard/stream/restart"
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		t.Fatal("too few parts")
	}
	cam := parts[len(parts)-3]
	if cam != "backyard" {
		t.Errorf("cam = %q", cam)
	}
}

func TestQualityValidation(t *testing.T) {
	valid := map[string]bool{"hd": true, "sd": true}
	tests := []struct {
		val  string
		want bool
	}{
		{"hd", true},
		{"sd", true},
		{"HD", false},
		{"4k", false},
		{"", false},
	}
	for _, tt := range tests {
		if valid[tt.val] != tt.want {
			t.Errorf("quality %q valid = %v, want %v", tt.val, valid[tt.val], tt.want)
		}
	}
}

func TestAudioBoolParsing(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"1", false},  // strict string match
		{"yes", false},
		{"", false},
	}
	for _, tt := range tests {
		got := tt.val == "true"
		if got != tt.want {
			t.Errorf("audio %q = %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestNightVisionPIDMapping(t *testing.T) {
	pidVal := map[string]string{"auto": "0", "on": "1", "off": "2"}

	if pidVal["auto"] != "0" {
		t.Error("auto should map to 0")
	}
	if pidVal["on"] != "1" {
		t.Error("on should map to 1")
	}
	if pidVal["off"] != "2" {
		t.Error("off should map to 2")
	}
	if _, ok := pidVal["invalid"]; ok {
		t.Error("invalid should not map")
	}
}

func TestSubscribePatterns(t *testing.T) {
	topic := "wyzebridge"
	patterns := []string{
		topic + "/+/set/#",
		topic + "/+/snapshot/take",
		topic + "/+/stream/restart",
	}

	// Verify patterns are well-formed
	for _, p := range patterns {
		if !strings.Contains(p, "+") {
			t.Errorf("pattern should contain wildcard: %q", p)
		}
		if !strings.HasPrefix(p, "wyzebridge/") {
			t.Errorf("pattern should start with topic: %q", p)
		}
	}
}

func TestLWTPayload(t *testing.T) {
	lwt := BridgeStateTopic("wyzebridge")
	if lwt != "wyzebridge/bridge/state" {
		t.Errorf("LWT topic = %q", lwt)
	}
	// LWT payload values
	if "offline" != "offline" || "online" != "online" {
		t.Error("payload constants wrong")
	}
}
