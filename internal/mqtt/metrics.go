package mqtt

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
)

// MetricsSource is the subset of the WebUI metrics snapshot that the
// MQTT publisher needs. Subsumes what we'd otherwise recompute — we
// let the webui do the work once per tick and forward the result.
type MetricsSource interface {
	CameraCount() int
	StreamingCount() int
	IsRecording(camName string) bool
}

// PublishCameraRecording publishes a single camera's recording state.
// Called synchronously from recording.Manager's OnChange so the topic
// flips in real time.
func (c *Client) PublishCameraRecording(camName string, recording bool) {
	if !c.IsConnected() {
		return
	}
	val := "OFF"
	if recording {
		val = "ON"
	}
	c.publish(fmt.Sprintf("%s/%s/recording", c.topic, camName), val, true)
}

// PublishBridgeMetrics publishes the bridge-wide gauge set. Called on
// the 30s metrics tick and whenever a state change touches the
// streaming count.
func (c *Client) PublishBridgeMetrics(uptimeSec, cameraCount, streamingCount, errorCount, issueCount int, recordingsBytes int64) {
	if !c.IsConnected() {
		return
	}
	prefix := c.topic + "/bridge"
	c.publish(prefix+"/uptime_s", strconv.Itoa(uptimeSec), true)
	c.publish(prefix+"/camera_count", strconv.Itoa(cameraCount), true)
	c.publish(prefix+"/streaming_count", strconv.Itoa(streamingCount), true)
	c.publish(prefix+"/error_count", strconv.Itoa(errorCount), true)
	c.publish(prefix+"/config_errors", strconv.Itoa(issueCount), true)
	c.publish(prefix+"/recordings_bytes_total", strconv.FormatInt(recordingsBytes, 10), true)
}

// RunMetricsPublisher ticks every 30s, emits the gauge set and a
// recording state for each camera. Blocks; call from a goroutine.
// Stops when ctx ends.
//
// The IsRecording / uptime / counts arguments come as closures so the
// mqtt package doesn't have to import webui or recording. main.go
// binds them at wire-up time.
func (c *Client) RunMetricsPublisher(
	ctx context.Context,
	interval time.Duration,
	uptimeFn func() int,
	streamingCountFn func() int,
	errorCountFn func() int,
	issueCountFn func() int,
	recordingsBytesFn func() int64,
	isRecordingFn func(camName string) bool,
) {
	if interval < time.Second {
		interval = 30 * time.Second
	}
	tick := func() {
		cams := c.camMgr.Cameras()
		c.PublishBridgeMetrics(uptimeFn(), len(cams), streamingCountFn(), errorCountFn(), issueCountFn(), recordingsBytesFn())
		for _, cam := range cams {
			c.PublishCameraRecording(cam.Name(), isRecordingFn(cam.Name()))
		}
	}
	tick() // fire once immediately so retained topics show current state on broker join
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// publishMetricsDiscovery registers HA discovery entities for the
// bridge-wide sensors + per-camera recording state. Appends to the
// set of entities PublishDiscovery already creates for cameras.
func (c *Client) publishMetricsDiscovery(cam *camera.Camera) {
	name := cam.Name()
	info := cam.GetInfo()
	mac := info.MAC
	device := haDeviceFromInfo(info)

	// Per-camera recording switch — HA toggles routed through
	// <topic>/<cam>/record/set (see handleRecordCommand in
	// subscribe.go). State feedback comes back on
	// <topic>/<cam>/recording as "ON"/"OFF", published by
	// recording.Manager's OnChange.
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_recording/config", c.dtopic, mac), map[string]interface{}{
		"name":          info.Nickname + " Recording",
		"unique_id":     "wyze_" + mac + "_recording",
		"state_topic":   fmt.Sprintf("%s/%s/recording", c.topic, name),
		"command_topic": fmt.Sprintf("%s/%s/record/set", c.topic, name),
		"payload_on":    "start",
		"payload_off":   "stop",
		"state_on":      "ON",
		"state_off":     "OFF",
		"icon":          "mdi:record-rec",
		"device":        device,
	})
}

// PublishBridgeDiscovery registers HA discovery for the bridge-wide
// gauges and control buttons. Called once, from PublishAllDiscovery.
func (c *Client) PublishBridgeDiscovery() {
	device := map[string]interface{}{
		"identifiers":  []string{"wyze_bridge"},
		"name":         "Wyze Bridge",
		"manufacturer": "IDisposable",
		"model":        "wyze-bridge-go",
	}

	// Rediscover button — sends any payload to the MQTT command
	// topic; the bridge treats any non-empty payload as "run it".
	c.publishDiscoveryConfig(fmt.Sprintf("%s/button/wyze_bridge_discover/config", c.dtopic), map[string]interface{}{
		"name":          "Wyze Bridge Rediscover",
		"unique_id":     "wyze_bridge_discover",
		"command_topic": fmt.Sprintf("%s/bridge/discover/set", c.topic),
		"payload_press": "PRESS",
		"icon":          "mdi:refresh",
		"device":        device,
	})
	sensors := []struct {
		Suffix, Name, UniqueSuffix, Unit, DeviceClass, StateClass, Icon string
	}{
		{"uptime_s", "Bridge Uptime", "uptime", "s", "duration", "total_increasing", "mdi:timer-outline"},
		{"camera_count", "Bridge Cameras", "cam_count", "", "", "measurement", "mdi:camera"},
		{"streaming_count", "Bridge Streaming", "streaming", "", "", "measurement", "mdi:video"},
		{"error_count", "Bridge Errored", "err_count", "", "", "measurement", "mdi:alert-circle"},
		{"config_errors", "Bridge Config Errors", "cfg_err", "", "", "measurement", "mdi:alert-octagram"},
		{"recordings_bytes_total", "Bridge Recordings Size", "rec_bytes", "B", "data_size", "measurement", "mdi:harddisk"},
	}
	for _, s := range sensors {
		cfg := map[string]interface{}{
			"name":        s.Name,
			"unique_id":   "wyze_bridge_" + s.UniqueSuffix,
			"state_topic": fmt.Sprintf("%s/bridge/%s", c.topic, s.Suffix),
			"icon":        s.Icon,
			"device":      device,
		}
		if s.Unit != "" {
			cfg["unit_of_measurement"] = s.Unit
		}
		if s.DeviceClass != "" {
			cfg["device_class"] = s.DeviceClass
		}
		if s.StateClass != "" {
			cfg["state_class"] = s.StateClass
		}
		c.publishDiscoveryConfig(fmt.Sprintf("%s/sensor/wyze_bridge_%s/config", c.dtopic, s.UniqueSuffix), cfg)
	}
}
