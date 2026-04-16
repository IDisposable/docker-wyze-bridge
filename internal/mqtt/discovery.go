package mqtt

import (
	"encoding/json"
	"fmt"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
)

// haDevice returns the HA device block for a camera.
func haDevice(cam *camera.Camera) map[string]interface{} {
	return map[string]interface{}{
		"identifiers":  []string{"wyze_" + cam.Info.MAC},
		"name":         cam.Info.Nickname,
		"model":        cam.Info.Model,
		"manufacturer": "Wyze",
		"sw_version":   cam.Info.FWVersion,
	}
}

// PublishAllDiscovery publishes HA MQTT discovery messages for all cameras.
func (c *Client) PublishAllDiscovery() {
	for _, cam := range c.camMgr.Cameras() {
		c.PublishDiscovery(cam)
	}
}

// PublishDiscovery publishes HA MQTT discovery messages for a single camera.
func (c *Client) PublishDiscovery(cam *camera.Camera) {
	name := cam.Name()
	mac := cam.Info.MAC
	device := haDevice(cam)

	// Camera entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/camera/%s/config", c.dtopic, mac), map[string]interface{}{
		"name":                 cam.Info.Nickname,
		"unique_id":            "wyze_" + mac,
		"topic":                fmt.Sprintf("%s/%s/", c.topic, name),
		"availability_topic":   fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":    "connected",
		"payload_not_available": "disconnected",
		"device":               device,
	})

	// Quality select entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/select/%s_quality/config", c.dtopic, mac), map[string]interface{}{
		"name":             cam.Info.Nickname + " Quality",
		"unique_id":        "wyze_" + mac + "_quality",
		"state_topic":      fmt.Sprintf("%s/%s/quality", c.topic, name),
		"command_topic":    fmt.Sprintf("%s/%s/set/quality", c.topic, name),
		"options":          []string{"hd", "sd"},
		"availability_topic":   fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":    "connected",
		"payload_not_available": "disconnected",
		"device":           device,
	})

	// Audio switch entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_audio/config", c.dtopic, mac), map[string]interface{}{
		"name":             cam.Info.Nickname + " Audio",
		"unique_id":        "wyze_" + mac + "_audio",
		"state_topic":      fmt.Sprintf("%s/%s/audio", c.topic, name),
		"command_topic":    fmt.Sprintf("%s/%s/set/audio", c.topic, name),
		"payload_on":       "true",
		"payload_off":      "false",
		"state_on":         "true",
		"state_off":        "false",
		"availability_topic":   fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":    "connected",
		"payload_not_available": "disconnected",
		"device":           device,
	})

	// Night vision select entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/select/%s_night_vision/config", c.dtopic, mac), map[string]interface{}{
		"name":             cam.Info.Nickname + " Night Vision",
		"unique_id":        "wyze_" + mac + "_night_vision",
		"state_topic":      fmt.Sprintf("%s/%s/night_vision", c.topic, name),
		"command_topic":    fmt.Sprintf("%s/%s/set/night_vision", c.topic, name),
		"options":          []string{"auto", "on", "off"},
		"availability_topic":   fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":    "connected",
		"payload_not_available": "disconnected",
		"device":           device,
	})
}

// RemoveDiscovery publishes empty discovery configs to remove a camera from HA.
func (c *Client) RemoveDiscovery(mac string) {
	for _, component := range []string{"camera", "select", "switch"} {
		var suffix string
		switch component {
		case "select":
			for _, s := range []string{"_quality", "_night_vision"} {
				c.publish(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, component, mac, s), "", true)
			}
			continue
		case "switch":
			suffix = "_audio"
		}
		c.publish(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, component, mac, suffix), "", true)
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
