package mqtt

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// TestPublishTopics verifies topic string construction for camera state publishing.
func TestPublishTopics(t *testing.T) {
	topic := "wyzebridge"
	camName := "front_door"

	tests := []struct {
		format string
		want   string
	}{
		{"%s/%s/state", "wyzebridge/front_door/state"},
		{"%s/%s/quality", "wyzebridge/front_door/quality"},
		{"%s/%s/audio", "wyzebridge/front_door/audio"},
		{"%s/%s/net_mode", "wyzebridge/front_door/net_mode"},
		{"%s/%s/camera_info", "wyzebridge/front_door/camera_info"},
		{"%s/%s/stream_info", "wyzebridge/front_door/stream_info"},
		{"%s/%s/thumbnail", "wyzebridge/front_door/thumbnail"},
		{"%s/bridge/state", "wyzebridge/bridge/state"},
	}

	for _, tt := range tests {
		var got string
		if tt.format == "%s/bridge/state" {
			got = fmt.Sprintf(tt.format, topic)
		} else {
			got = fmt.Sprintf(tt.format, topic, camName)
		}
		if got != tt.want {
			t.Errorf("topic = %q, want %q", got, tt.want)
		}
	}
}

func TestCameraInfoJSON(t *testing.T) {
	info := wyzeapi.CameraInfo{
		LanIP:     "192.168.1.10",
		Model:     "HL_CAM4",
		FWVersion: "4.52.9.4188",
		MAC:       "AABBCCDDEEFF",
	}

	cameraInfo := map[string]string{
		"ip":         info.LanIP,
		"model":      info.Model,
		"fw_version": info.FWVersion,
		"mac":        info.MAC,
	}
	data, err := json.Marshal(cameraInfo)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]string
	json.Unmarshal(data, &parsed)

	if parsed["ip"] != "192.168.1.10" {
		t.Errorf("ip = %q", parsed["ip"])
	}
	if parsed["mac"] != "AABBCCDDEEFF" {
		t.Errorf("mac = %q", parsed["mac"])
	}
}

func TestStreamInfoJSON(t *testing.T) {
	bridgeIP := "192.168.1.50"
	name := "front_door"

	streamInfo := map[string]string{
		"rtsp_url":   fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name),
		"webrtc_url": fmt.Sprintf("http://%s:8889/%s", bridgeIP, name),
		"hls_url":    fmt.Sprintf("http://%s:1984/api/stream.m3u8?src=%s", bridgeIP, name),
	}
	data, err := json.Marshal(streamInfo)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]string
	json.Unmarshal(data, &parsed)

	if parsed["rtsp_url"] != "rtsp://192.168.1.50:8554/front_door" {
		t.Errorf("rtsp_url = %q", parsed["rtsp_url"])
	}
}

func TestCameraStateStrings(t *testing.T) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "test"}, "hd", true, false)

	// Offline → "disconnected"
	state := cam.GetState()
	stateStr := "disconnected"
	if state == camera.StateStreaming {
		stateStr = "connected"
	}
	if stateStr != "disconnected" {
		t.Errorf("offline should be disconnected, got %q", stateStr)
	}

	// Streaming → "connected"
	cam.SetState(camera.StateStreaming)
	state = cam.GetState()
	stateStr = "disconnected"
	if state == camera.StateStreaming {
		stateStr = "connected"
	}
	if stateStr != "connected" {
		t.Errorf("streaming should be connected, got %q", stateStr)
	}
}
