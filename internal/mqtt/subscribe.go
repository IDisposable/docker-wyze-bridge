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
		cam.AudioOn = value == "true"
		c.publish(fmt.Sprintf("%s/%s/audio", c.topic, camName), value, true)
	case "night_vision":
		pidVal := map[string]string{"auto": "0", "on": "1", "off": "2"}
		if pv, ok := pidVal[value]; ok {
			go func() {
				c.log.Info().Str("cam", camName).Str("value", value).Msg("night vision command via Wyze API")
				if c.wyzeAPI != nil {
					if err := c.wyzeAPI.SetProperty(cam.Info, "P3", pv); err != nil {
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
