package mqtt

import (
	"context"
	"fmt"
	"strings"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// subscribeCommands subscribes to all camera command topics.
func (c *Client) subscribeCommands() {
	// Subscribe to wildcard for all cameras
	pattern := fmt.Sprintf("%s/+/set/#", c.topic)
	c.subscribe(pattern, c.handleSetCommand)

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
		pidVal := map[string]string{"auto": "0", "on": "1", "off": "2"}
		if pv, ok := pidVal[value]; ok {
			info := cam.GetInfo()
			go func() {
				c.log.Info().Str("cam", camName).Str("value", value).Msg("night vision command via Wyze API")
				if c.wyzeAPI != nil {
					if err := c.wyzeAPI.SetProperty(info, "P3", pv); err != nil {
						c.log.Error().Err(err).Str("cam", camName).Msg("night vision command failed")
					} else {
						c.publish(fmt.Sprintf("%s/%s/night_vision", c.topic, camName), value, true)
					}
				}
			}()
		}
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
