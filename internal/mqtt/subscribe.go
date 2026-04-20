package mqtt

import (
	"context"
	"fmt"
	"strings"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	paho "github.com/eclipse/paho.mqtt.golang"
)

// subscribeCommands subscribes to all camera command topics.
func (c *Client) subscribeCommands() {
	// Subscribe to wildcard for all cameras
	pattern := fmt.Sprintf("%s/+/set/#", c.topic)
	c.subscribe(pattern, c.handleSetCommand)

	statePattern := fmt.Sprintf("%s/+/state/set", c.topic)
	c.subscribe(statePattern, c.handleStateCommand)

	powerPattern := fmt.Sprintf("%s/+/power/set", c.topic)
	c.subscribe(powerPattern, c.handlePowerCommand)

	snapPattern := fmt.Sprintf("%s/+/snapshot/take", c.topic)
	c.subscribe(snapPattern, c.handleSnapshotCommand)

	restartPattern := fmt.Sprintf("%s/+/stream/restart", c.topic)
	c.subscribe(restartPattern, c.handleRestartCommand)

	recordPattern := fmt.Sprintf("%s/+/record/set", c.topic)
	c.subscribe(recordPattern, c.handleRecordCommand)

	discoverTopic := fmt.Sprintf("%s/bridge/discover/set", c.topic)
	c.subscribe(discoverTopic, c.handleDiscoverCommand)

	c.log.Debug().Str("pattern", pattern).Msg("subscribed to command topics")
}

// handleSetCommand handles set/{property} commands.
func (c *Client) handleSetCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	// Expected: {topic}/{cam}/set/{property}
	if len(parts) < 4 {
		return
	}

	camName := parts[len(parts)-3]
	property := parts[len(parts)-1]
	value := string(msg.Payload())

	c.log.Debug().
		Str("cam", camName).
		Str("property", property).
		Str("value", value).
		Msg("MQTT command received")

	cam := c.camMgr.GetCamera(camName)
	if cam == nil {
		c.log.Warn().Str("cam", camName).Msg("unknown camera in MQTT command")
		return
	}

	// Handle commands in goroutines so the MQTT message handler doesn't block
	switch property {
	case "quality":
		if value == "hd" || value == "sd" {
			go func() {
				ctx := context.Background()
				if err := c.camMgr.SetQuality(ctx, camName, value); err != nil {
					c.log.Error().Err(err).Str("cam", camName).Msg("quality change failed")
				} else {
					c.publish(fmt.Sprintf("%s/%s/quality", c.topic, camName), value, true)
				}
			}()
		}
	case "audio":
		cam.SetAudioOn(value == "true")
		c.publish(fmt.Sprintf("%s/%s/audio", c.topic, camName), value, true)
	case "night_vision":
		fallthrough
	case "irled", "status_light", "motion_detection", "motion_tagging", "hor_flip", "ver_flip", "bitrate", "fps":
		c.applyCloudSetProperty(camName, cam, property, value)
	}
}

func (c *Client) applyCloudSetProperty(camName string, cam *camera.Camera, property, rawValue string) {
	pid, hasPID := cloudSetProperty[property]
	if !hasPID {
		return
	}

	pvalue, publishValue, ok := parseSetPropertyValue(property, rawValue)
	if !ok {
		c.log.Warn().Str("cam", camName).Str("property", property).Str("value", rawValue).Msg("invalid MQTT property payload")
		return
	}

	if c.wyzeAPI == nil {
		c.log.Warn().Str("cam", camName).Str("property", property).Msg("Wyze API unavailable for MQTT property command")
		return
	}

	info := cam.GetInfo()
	go func() {
		if err := c.wyzeAPI.SetProperty(info, pid, pvalue); err != nil {
			c.log.Error().Err(err).Str("cam", camName).Str("property", property).Msg("MQTT property command failed")
			return
		}
		c.publish(fmt.Sprintf("%s/%s/%s", c.topic, camName, property), publishValue, true)
	}()
}

// handleStateCommand handles <topic>/<cam>/state/set commands.
func (c *Client) handleStateCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 4 {
		return
	}
	camName := parts[len(parts)-3]
	payload := strings.ToLower(strings.TrimSpace(string(msg.Payload())))

	if c.camMgr.GetCamera(camName) == nil {
		c.log.Warn().Str("cam", camName).Msg("unknown camera in MQTT state command")
		return
	}

	switch payload {
	case "start", "on", "1", "true":
		go c.camMgr.StartStream(context.Background(), camName)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "connected", true)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "on", true)
	case "stop", "off", "0", "false":
		go c.camMgr.StopStream(context.Background(), camName)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "disconnected", true)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "off", true)
	default:
		c.log.Warn().Str("cam", camName).Str("payload", payload).Msg("invalid MQTT state command payload")
	}
}

// handlePowerCommand handles <topic>/<cam>/power/set commands.
func (c *Client) handlePowerCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 4 {
		return
	}
	camName := parts[len(parts)-3]
	payload := strings.ToLower(strings.TrimSpace(string(msg.Payload())))

	cam := c.camMgr.GetCamera(camName)
	if cam == nil {
		c.log.Warn().Str("cam", camName).Msg("unknown camera in MQTT power command")
		return
	}

	switch payload {
	case "on", "start", "1", "true":
		go c.camMgr.StartStream(context.Background(), camName)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "on", true)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "connected", true)
	case "off", "stop", "0", "false":
		go c.camMgr.StopStream(context.Background(), camName)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "off", true)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "disconnected", true)
	case "restart":
		go c.camMgr.RestartStream(context.Background(), camName)
		if c.wyzeAPI != nil {
			info := cam.GetInfo()
			go func() {
				if err := c.wyzeAPI.RunAction(info, "restart"); err != nil {
					c.log.Error().Err(err).Str("cam", camName).Msg("power restart command failed")
				}
			}()
		}
	default:
		c.log.Warn().Str("cam", camName).Str("payload", payload).Msg("invalid MQTT power command payload")
	}
}

// handleSnapshotCommand handles snapshot/take commands.
func (c *Client) handleSnapshotCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 3 {
		return
	}
	camName := parts[len(parts)-3]
	c.log.Info().Str("cam", camName).Msg("snapshot requested via MQTT")
	if c.onSnapshot != nil {
		go c.onSnapshot(context.Background(), camName)
	}
}

// handleRestartCommand handles stream/restart commands.
func (c *Client) handleRestartCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 3 {
		return
	}
	camName := parts[len(parts)-3]
	c.log.Info().Str("cam", camName).Msg("stream restart requested via MQTT")
	go c.camMgr.RestartStream(context.Background(), camName)
}

// handleRecordCommand handles <topic>/<cam>/record/set commands.
// Payload semantics match HA's switch component: "start" / "ON" / "1" /
// "true" starts recording, anything else stops it. Both REST and
// MQTT paths end up in recording.Manager.Start/Stop — idempotent.
func (c *Client) handleRecordCommand(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	// Expected: {topic}/{cam}/record/set
	if len(parts) < 4 {
		return
	}
	camName := parts[len(parts)-3]
	payload := strings.ToLower(strings.TrimSpace(string(msg.Payload())))

	var action string
	switch payload {
	case "start", "on", "1", "true":
		action = "start"
	default:
		action = "stop"
	}

	c.log.Info().Str("cam", camName).Str("action", action).Msg("record command received via MQTT")
	if c.camMgr.GetCamera(camName) == nil {
		c.log.Warn().Str("cam", camName).Msg("unknown camera in MQTT record command")
		return
	}
	if c.onRecord != nil {
		go c.onRecord(context.Background(), camName, action)
	}
}

// handleDiscoverCommand handles the bridge-wide rediscovery trigger.
// Any non-empty payload kicks off a discovery + reconnect pass (HA
// button entities fire "PRESS" which we treat as "run it").
func (c *Client) handleDiscoverCommand(_ paho.Client, _ paho.Message) {
	c.log.Info().Msg("bridge rediscovery requested via MQTT")
	if c.onDiscover != nil {
		go c.onDiscover(context.Background())
	}
}
