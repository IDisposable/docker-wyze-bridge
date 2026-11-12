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
		"topic":                 fmt.Sprintf("%s/%s/thumbnail", c.topic, name),
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Stream switch entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_stream/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Stream",
		"unique_id":             "wyze_" + mac + "_stream",
		"state_topic":           fmt.Sprintf("%s/%s/state", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/state/set", c.topic, name),
		"payload_on":            "start",
		"payload_off":           "stop",
		"state_on":              "connected",
		"state_off":             "disconnected",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Power switch entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_power/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Power",
		"unique_id":             "wyze_" + mac + "_power",
		"state_topic":           fmt.Sprintf("%s/%s/power", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/power/set", c.topic, name),
		"payload_on":            "on",
		"payload_off":           "off",
		"state_on":              "on",
		"state_off":             "off",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// Reboot button entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/button/%s_reboot/config", c.dtopic, mac), map[string]interface{}{
		"name":          info.Nickname + " Reboot",
		"unique_id":     "wyze_" + mac + "_reboot",
		"command_topic": fmt.Sprintf("%s/%s/power/set", c.topic, name),
		"payload_press": "restart",
		"device":        device,
	})

	// Snapshot button entity
	c.publishDiscoveryConfig(fmt.Sprintf("%s/button/%s_snapshot/config", c.dtopic, mac), map[string]interface{}{
		"name":          info.Nickname + " Snapshot",
		"unique_id":     "wyze_" + mac + "_snapshot",
		"command_topic": fmt.Sprintf("%s/%s/snapshot/take", c.topic, name),
		"payload_press": "take",
		"device":        device,
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

	// Generic cloud-backed property controls (Phase 1 write-only).
	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_irled/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " IR",
		"unique_id":             "wyze_" + mac + "_irled",
		"state_topic":           fmt.Sprintf("%s/%s/irled", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/irled", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_status_light/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Status Light",
		"unique_id":             "wyze_" + mac + "_status_light",
		"state_topic":           fmt.Sprintf("%s/%s/status_light", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/status_light", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_motion_detection/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Motion Detection",
		"unique_id":             "wyze_" + mac + "_motion_detection",
		"state_topic":           fmt.Sprintf("%s/%s/motion_detection", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/motion_detection", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_motion_tagging/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Motion Tagging",
		"unique_id":             "wyze_" + mac + "_motion_tagging",
		"state_topic":           fmt.Sprintf("%s/%s/motion_tagging", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/motion_tagging", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/number/%s_bitrate/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Bitrate",
		"unique_id":             "wyze_" + mac + "_bitrate",
		"state_topic":           fmt.Sprintf("%s/%s/bitrate", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/bitrate", c.topic, name),
		"min":                   1,
		"max":                   1000,
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/number/%s_fps/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " FPS",
		"unique_id":             "wyze_" + mac + "_fps",
		"state_topic":           fmt.Sprintf("%s/%s/fps", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/fps", c.topic, name),
		"min":                   1,
		"max":                   30,
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_hor_flip/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Flip Horizontal",
		"unique_id":             "wyze_" + mac + "_hor_flip",
		"state_topic":           fmt.Sprintf("%s/%s/hor_flip", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/hor_flip", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	c.publishDiscoveryConfig(fmt.Sprintf("%s/switch/%s_ver_flip/config", c.dtopic, mac), map[string]interface{}{
		"name":                  info.Nickname + " Flip Vertical",
		"unique_id":             "wyze_" + mac + "_ver_flip",
		"state_topic":           fmt.Sprintf("%s/%s/ver_flip", c.topic, name),
		"command_topic":         fmt.Sprintf("%s/%s/set/ver_flip", c.topic, name),
		"payload_on":            "1",
		"payload_off":           "2",
		"availability_topic":    fmt.Sprintf("%s/%s/state", c.topic, name),
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	})

	// PTZ + cruise-point entities, gated to the same cameras the MQTT
	// PTZ commands accept (see subscribe.supportedPTZInfo).
	c.publishPTZDiscovery(cam, info, device)
}

// haSupportsPTZ mirrors the PTZ command gate in subscribe.supportedPTZInfo:
// native TUTK Pan models only, and never while the camera is pinned to the
// WebRTC path (model-promoted or runtime-promoted).
func haSupportsPTZ(info wyzeapi.CameraInfo, forceWebRTC bool) bool {
	return info.SupportsPTZ() && !forceWebRTC
}

// haEntityConfig is one announced entity: where it lives on the discovery
// prefix plus the payload to announce.
type haEntityConfig struct {
	component string
	suffix    string
	config    map[string]interface{}
}

// ptzEntitySpec describes one PTZ/cruise-point entity. The table is the
// single source of truth for both the announce (ptzDiscoveryConfigs) and
// unannounce (RemoveDiscovery / retractPTZDiscovery) paths, so a stale
// entity can never linger because a topic pair drifted.
type ptzEntitySpec struct {
	component string
	suffix    string
	label     string // appended to the camera nickname
	icon      string
	command   string // camera-relative topic the press goes to
	payload   string // payload_press; empty marks a state-only sensor
}

// ptzEntitySpecs is the PTZ/cruise entity set: five movement buttons, a
// cruise-point refresh, per-slot save/recall buttons (slots 1–4, matching
// wyzeapi.MaxCruisePoints), and the retained cruise_points sensor.
var ptzEntitySpecs = buildPTZEntitySpecs()

func buildPTZEntitySpecs() []ptzEntitySpec {
	specs := []ptzEntitySpec{
		{component: "button", suffix: "_ptz_up", label: "PTZ Up", icon: "mdi:arrow-up", command: "set/rotary_action", payload: "up"},
		{component: "button", suffix: "_ptz_down", label: "PTZ Down", icon: "mdi:arrow-down", command: "set/rotary_action", payload: "down"},
		{component: "button", suffix: "_ptz_left", label: "PTZ Left", icon: "mdi:arrow-left", command: "set/rotary_action", payload: "left"},
		{component: "button", suffix: "_ptz_right", label: "PTZ Right", icon: "mdi:arrow-right", command: "set/rotary_action", payload: "right"},
		{component: "button", suffix: "_ptz_stop", label: "PTZ STOP", icon: "mdi:stop", command: "set/rotary_action", payload: "stop"},
		{component: "button", suffix: "_cruise_refresh", label: "Cruise Points Refresh", icon: "mdi:refresh", command: "cruise_points/get", payload: "get"},
	}
	for slot := 1; slot <= wyzeapi.MaxCruisePoints; slot++ {
		specs = append(specs,
			ptzEntitySpec{
				component: "button",
				suffix:    fmt.Sprintf("_cruise_save_%d", slot),
				label:     fmt.Sprintf("Cruise Save %d", slot),
				icon:      "mdi:map-marker-plus",
				command:   "set/cruise_point",
				payload:   fmt.Sprintf(`{"action":"save","slot":%d}`, slot),
			},
			ptzEntitySpec{
				component: "button",
				suffix:    fmt.Sprintf("_cruise_goto_%d", slot),
				label:     fmt.Sprintf("Cruise Go %d", slot),
				icon:      "mdi:map-marker",
				command:   "set/cruise_point",
				payload:   fmt.Sprintf(`{"action":"goto","slot":%d}`, slot),
			},
		)
	}
	specs = append(specs, ptzEntitySpec{
		component: "sensor",
		suffix:    "_cruise_points",
		label:     "Cruise Points",
		icon:      "mdi:map-marker-path",
		command:   "cruise_points",
	})
	return specs
}

// ptzDiscoveryConfigs builds the announcement payloads for the PTZ set.
// Split out from the publish path so the entity surface is testable and
// so the configs all share one consistent device + availability block.
func ptzDiscoveryConfigs(name, topic string, info wyzeapi.CameraInfo, device map[string]interface{}) []haEntityConfig {
	entities := make([]haEntityConfig, 0, len(ptzEntitySpecs))
	for _, spec := range ptzEntitySpecs {
		cfg := map[string]interface{}{
			"name":                  info.Nickname + " " + spec.label,
			"unique_id":             "wyze_" + info.MAC + spec.suffix,
			"icon":                  spec.icon,
			"qos":                   1,
			"availability_topic":    fmt.Sprintf("%s/%s/state", topic, name),
			"payload_available":     "connected",
			"payload_not_available": "disconnected",
			"device":                device,
		}
		target := fmt.Sprintf("%s/%s/%s", topic, name, spec.command)
		if spec.payload == "" {
			cfg["state_topic"] = target
		} else {
			cfg["command_topic"] = target
			cfg["payload_press"] = spec.payload
		}
		entities = append(entities, haEntityConfig{component: spec.component, suffix: spec.suffix, config: cfg})
	}
	return entities
}

// publishPTZDiscovery announces the PTZ/cruise entities for an eligible
// camera. For a Pan camera that left the TUTK path at runtime the same
// topics are cleared instead, so HA doesn't keep movement buttons that
// can only fail. Non-Pan cameras never had these entities, so they are
// left alone rather than writing empty retained configs for each one.
func (c *Client) publishPTZDiscovery(cam *camera.Camera, info wyzeapi.CameraInfo, device map[string]interface{}) {
	if !haSupportsPTZ(info, cam.ForceWebRTC()) {
		if info.IsPanCam() {
			c.retractPTZDiscovery(info.MAC)
		}
		return
	}
	for _, entity := range ptzDiscoveryConfigs(cam.Name(), c.topic, info, device) {
		c.publishDiscoveryConfig(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, entity.component, info.MAC, entity.suffix), entity.config)
	}
}

// retractPTZDiscovery clears the PTZ/cruise entity configs for a camera.
func (c *Client) retractPTZDiscovery(mac string) {
	for _, spec := range ptzEntitySpecs {
		c.publish(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, spec.component, mac, spec.suffix), "", true)
	}
}

// RetractPTZDiscovery drops a camera's PTZ/cruise entities over MQTT, used
// when a TUTK camera is promoted to the WebRTC path while running.
func (c *Client) RetractPTZDiscovery(mac string) {
	if mac == "" || !c.IsConnected() {
		return
	}
	c.retractPTZDiscovery(mac)
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
		{"switch", "_stream"},
		{"switch", "_power"},
		{"button", "_reboot"},
		{"button", "_snapshot"},
		{"select", "_quality"},
		{"select", "_night_vision"},
		{"switch", "_audio"},
		{"switch", "_irled"},
		{"switch", "_status_light"},
		{"switch", "_motion_detection"},
		{"switch", "_motion_tagging"},
		{"number", "_bitrate"},
		{"number", "_fps"},
		{"switch", "_hor_flip"},
		{"switch", "_ver_flip"},
		{"switch", "_recording"},
	}
	for _, e := range entities {
		c.publish(fmt.Sprintf("%s/%s/%s%s/config", c.dtopic, e.component, mac, e.suffix), "", true)
	}
	// PTZ/cruise entities are gated on announce, but unannouncing them for
	// every removed camera is harmless and keeps stale movement buttons
	// out of HA if a camera's model override changed mid-process.
	c.retractPTZDiscovery(mac)
}

func (c *Client) publishDiscoveryConfig(topic string, config map[string]interface{}) {
	data, err := json.Marshal(config)
	if err != nil {
		c.log.Error().Err(err).Str("topic", topic).Msg("failed to marshal discovery config")
		return
	}
	c.publish(topic, string(data), true)
}
