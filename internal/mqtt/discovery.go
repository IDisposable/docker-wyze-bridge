package mqtt

import (
	"encoding/json"
	"fmt"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// haDevice returns the HA device block for a camera. Captures a
// single snapshot so the multi-field read can't tear against a
// concurrent UpdateInfo.
func haDevice(cam *camera.Camera) map[string]interface{} {
	info := cam.GetInfo()
	return map[string]interface{}{
		"identifiers":  []string{"wyze_" + info.MAC},
		"name":         info.Nickname,
		"model":        info.Model,
		"manufacturer": "Wyze",
		"sw_version":   info.FWVersion,
	}
}

// PublishAllDiscovery publishes HA MQTT discovery messages for all cameras
// and the bridge-wide sensors.
func (c *Client) PublishAllDiscovery() {
	c.PublishBridgeDiscovery()
	for _, cam := range c.camMgr.Cameras() {
		c.PublishDiscovery(cam)
		c.publishMetricsDiscovery(cam)
	}
}

// PublishDiscovery publishes HA MQTT discovery messages for a single camera.
func (c *Client) PublishDiscovery(cam *camera.Camera) {
	info := cam.GetInfo()
	name := cam.Name()
	mac := info.MAC
	device := haDeviceFromInfo(info)

	// Camera entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/camera/%s/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname,
		"unique_id":             "wyze_" + mac,
		"topic":                 fmt.Sprintf("%s/%s/", c.topic, name),
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Quality select entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/select/%s_quality/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Quality",
		"unique_id":             "wyze_" + mac + "_quality",
		"state_topic":           fmt.Sprintf("%s/%s/quality", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/quality", c.topic, name),
		"options":               []string{"hd", "sd"},
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Audio switch entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_audio/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Audio",
		"unique_id":             "wyze_" + mac + "_audio",
		"state_topic":           fmt.Sprintf("%s/%s/audio", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/audio", c.topic, name),
		"payload_on":            "true",
		"payload_off":           "false",
		"state_on":              "true",
		"state_off":             "false",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Night vision select entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/select/%s_night_vision/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Night Vision",
		"unique_id":             "wyze_" + mac + "_night_vision",
		"state_topic":           fmt.Sprintf("%s/%s/night_vision", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/night_vision", c.topic, name),
		"options":               []string{"auto", "on", "off"},
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})
}

// haDeviceFromInfo builds the HA device block from a pre-captured
// CameraInfo snapshot. Used when the caller already has a consistent
// Info (from GetInfo) and wants to avoid a second lock acquisition.
func haDeviceFromInfo(info wyzeapi.CameraInfo) map[string]interface{} {
	return map[string]interface{}{
		"identifiers":  []string{"wyze_" + info.MAC},
		"name":         info.Nickname,
		"model":        info.Model,
		"manufacturer": "Wyze",
		"sw_version":   info.FWVersion,
	}
}

// RemoveDiscovery publishes empty discovery configs to remove a
// camera's HA entities. Components + per-component suffixes must
// match what PublishDiscovery / publishMetricsDiscovery emit or
// entities will linger.
func (c *Client) RemoveDiscovery(mac string) {
	entities := []struct {
		component string
		suffix    string
	}{
		{"camera", ""},
		{"select", "_quality"},
		{"select", "_night_vision"},
		{"switch", "_audio"},
		{"switch", "_recording"},
	}
	for _, e := range entities {
		c.publish(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, e.component, mac, e.suffix), "", true)
	}
}

func (c *Client) publishDiscoveryConfig(topic string, config map[string]interface{}) {
	data, err := json.Marshal(config)
	if err != nil {
		c.log.Error().Err(err).Str("topic", topic).Msg("failed to marshal discovery config")
		return
	}
	c.publish(topic, string(data), true)
}
