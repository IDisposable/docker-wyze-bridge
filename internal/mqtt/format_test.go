package mqtt

import (
	"encoding/json"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestFormatCameraState(t *testing.T) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "t"}, "hd", true, false)

	if got := FormatCameraState(cam); got != "disconnected" {
		t.Errorf("offline → %q, want disconnected", got)
	}

	cam.SetState(camera.StateConnecting)
	if got := FormatCameraState(cam); got != "disconnected" {
		t.Errorf("connecting → %q, want disconnected", got)
	}

	cam.SetState(camera.StateStreaming)
	if got := FormatCameraState(cam); got != "connected" {
		t.Errorf("streaming → %q, want connected", got)
	}

	cam.SetState(camera.StateError)
	if got := FormatCameraState(cam); got != "disconnected" {
		t.Errorf("error → %q, want disconnected", got)
	}
}

func TestFormatCameraInfoJSON(t *testing.T) {
	info := wyzeapi.CameraInfo{
		LanIP:     "192.168.1.10",
		Model:     "HL_CAM4",
		FWVersion: "4.52.9.4188",
		MAC:       "AABBCCDDEEFF",
	}

	got := FormatCameraInfoJSON(info)

	var parsed map[string]string
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["ip"] != "192.168.1.10" {
		t.Errorf("ip = %q", parsed["ip"])
	}
	if parsed["model"] != "HL_CAM4" {
		t.Errorf("model = %q", parsed["model"])
	}
	if parsed["fw_version"] != "4.52.9.4188" {
		t.Errorf("fw_version = %q", parsed["fw_version"])
	}
	if parsed["mac"] != "AABBCCDDEEFF" {
		t.Errorf("mac = %q", parsed["mac"])
	}
}

func TestFormatStreamInfoJSON(t *testing.T) {
	got := FormatStreamInfoJSON("192.168.1.50", "front_door")

	var parsed map[string]string
	json.Unmarshal([]byte(got), &parsed)

	if parsed["rtsp_url"] != "rtsp://192.168.1.50:8554/front_door" {
		t.Errorf("rtsp = %q", parsed["rtsp_url"])
	}
	if parsed["webrtc_url"] != "http://192.168.1.50:8889/front_door" {
		t.Errorf("webrtc = %q", parsed["webrtc_url"])
	}
	if parsed["hls_url"] != "http://192.168.1.50:1984/api/stream.m3u8?src=front_door" {
		t.Errorf("hls = %q", parsed["hls_url"])
	}
}

func TestCameraStateTopic(t *testing.T) {
	got := CameraStateTopic("wyzebridge", "front_door")
	if got != "wyzebridge/front_door/state" {
		t.Errorf("got %q", got)
	}
}

func TestCameraPropertyTopic(t *testing.T) {
	tests := []struct {
		prop, want string
	}{
		{"quality", "wyzebridge/cam/quality"},
		{"audio", "wyzebridge/cam/audio"},
		{"net_mode", "wyzebridge/cam/net_mode"},
		{"camera_info", "wyzebridge/cam/camera_info"},
		{"stream_info", "wyzebridge/cam/stream_info"},
		{"thumbnail", "wyzebridge/cam/thumbnail"},
	}
	for _, tt := range tests {
		got := CameraPropertyTopic("wyzebridge", "cam", tt.prop)
		if got != tt.want {
			t.Errorf("property %q: got %q, want %q", tt.prop, got, tt.want)
		}
	}
}

func TestBridgeStateTopic(t *testing.T) {
	got := BridgeStateTopic("wyzebridge")
	if got != "wyzebridge/bridge/state" {
		t.Errorf("got %q", got)
	}
}

func TestDiscoveryTopic(t *testing.T) {
	tests := []struct {
		component, id, suffix, want string
	}{
		{"camera", "AABB", "", "homeassistant/camera/AABB/config"},
		{"select", "AABB", "_quality", "homeassistant/select/AABB_quality/config"},
		{"switch", "AABB", "_audio", "homeassistant/switch/AABB_audio/config"},
		{"select", "AABB", "_night_vision", "homeassistant/select/AABB_night_vision/config"},
	}
	for _, tt := range tests {
		got := DiscoveryTopic("homeassistant", tt.component, tt.id, tt.suffix)
		if got != tt.want {
			t.Errorf("DiscoveryTopic(%q, %q, %q) = %q, want %q", tt.component, tt.id, tt.suffix, got, tt.want)
		}
	}
}

func TestFormatDiscoveryCamera(t *testing.T) {
	disc := FormatDiscoveryCamera("wyzebridge", "front_door", "Front Door", "AABBCCDDEEFF", "4.52.9")

	if disc["unique_id"] != "wyze_AABBCCDDEEFF" {
		t.Errorf("unique_id = %v", disc["unique_id"])
	}
	if disc["name"] != "Front Door" {
		t.Errorf("name = %v", disc["name"])
	}
	if disc["availability_topic"] != "wyzebridge/front_door/state" {
		t.Errorf("availability = %v", disc["availability_topic"])
	}
	if disc["payload_available"] != "connected" {
		t.Errorf("available = %v", disc["payload_available"])
	}

	dev, ok := disc["device"].(map[string]interface{})
	if !ok {
		t.Fatal("device should be a map")
	}
	if dev["manufacturer"] != "Wyze" {
		t.Errorf("manufacturer = %v", dev["manufacturer"])
	}

	// Verify JSON round-trip
	data, err := json.Marshal(disc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	if parsed["unique_id"] != "wyze_AABBCCDDEEFF" {
		t.Error("JSON round-trip failed")
	}
}

func TestFormatStreamInfoJSON_EmptyIP(t *testing.T) {
	got := FormatStreamInfoJSON("", "cam")
	var parsed map[string]string
	json.Unmarshal([]byte(got), &parsed)
	if parsed["rtsp_url"] != "rtsp://:8554/cam" {
		t.Errorf("empty IP should still format: %q", parsed["rtsp_url"])
	}
}
