package mqtt

import (
	"encoding/json"
	"fmt"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// FormatCameraState returns the MQTT state string for a camera.
func FormatCameraState(cam *camera.Camera) string {
	if cam.GetState() == camera.StateStreaming {
		return "connected"
	}
	return "disconnected"
}

// FormatCameraInfoJSON returns the camera_info JSON payload.
func FormatCameraInfoJSON(info wyzeapi.CameraInfo) string {
	data := map[string]string{
		"ip":         info.LanIP,
		"model":      info.Model,
		"fw_version": info.FWVersion,
		"mac":        info.MAC,
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// FormatStreamInfoJSON returns the stream_info JSON payload.
func FormatStreamInfoJSON(bridgeIP, name string) string {
	data := map[string]string{
		"rtsp_url":   fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name),
		"webrtc_url": fmt.Sprintf("http://%s:8889/%s", bridgeIP, name),
		"hls_url":    fmt.Sprintf("http://%s:1984/api/stream.m3u8?src=%s", bridgeIP, name),
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// CameraStateTopic returns the full topic for a camera's state.
func CameraStateTopic(baseTopic, camName string) string {
	return fmt.Sprintf("%s/%s/state", baseTopic, camName)
}

// CameraPropertyTopic returns the topic for a camera property.
func CameraPropertyTopic(baseTopic, camName, property string) string {
	return fmt.Sprintf("%s/%s/%s", baseTopic, camName, property)
}

// BridgeStateTopic returns the bridge LWT topic.
func BridgeStateTopic(baseTopic string) string {
	return fmt.Sprintf("%s/bridge/state", baseTopic)
}

// DiscoveryTopic returns the HA discovery config topic.
func DiscoveryTopic(dtopic, component, id, suffix string) string {
	if suffix != "" {
		return fmt.Sprintf("%s/%s/%s%s/config", dtopic, component, id, suffix)
	}
	return fmt.Sprintf("%s/%s/%s/config", dtopic, component, id)
}

// FormatDiscoveryCamera returns the HA discovery JSON for a camera entity.
func FormatDiscoveryCamera(baseTopic, camName, nickname, mac, fwVer string) map[string]interface{} {
	return map[string]interface{}{
		"name":                  nickname,
		"unique_id":             "wyze_" + mac,
		"topic":                 fmt.Sprintf("%s/%s/", baseTopic, camName),
		"availability_topic":    fmt.Sprintf("%s/%s/state", baseTopic, camName),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device": map[string]interface{}{
			"identifiers":  []string{"wyze_" + mac},
			"name":         nickname,
			"manufacturer": "Wyze",
			"sw_version":   fwVer,
		},
	}
}
