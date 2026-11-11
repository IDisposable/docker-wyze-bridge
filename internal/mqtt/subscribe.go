package mqtt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	paho "github.com/eclipse/paho.mqtt.golang"
)

const ptzCommandQueueSize = 8

type mqttCommandResult struct {
	Command   string `json:"command"`
	Status    string `json:"status"`
	Direction string `json:"direction,omitempty"`
	Action    string `json:"action,omitempty"`
	Slot      int    `json:"slot,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ptzCommand struct {
	result mqttCommandResult
	run    func() error
}

type ptzCommandQueue struct {
	jobs    chan ptzCommand
	mu      sync.Mutex
	pending int
}

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

	cruisePointsPattern := fmt.Sprintf("%s/+/cruise_points/get", c.topic)
	c.subscribe(cruisePointsPattern, c.handleCruisePointsGet)

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
		if property == "rotary_action" || property == "cruise_point" {
			c.publishCommandResult(camName, mqttCommandResult{
				Command: property, Status: "error", Error: "unknown camera",
			})
		}
		return
	}

	// Handle commands in goroutines so the MQTT message handler doesn't block
	switch property {
	case "quality":
		if value == "hd" || value == "sd" {
			go func() {
				if _, err := c.camMgr.SetQuality(c.rootCtx, camName, value); err != nil {
					c.log.Error().Err(err).Str("cam", camName).Msg("quality change failed")
				} else {
					c.publish(fmt.Sprintf("%s/%s/quality", c.topic, camName), value, true)
				}
			}()
		}
	case "audio":
		cam.SetAudioOn(value == "true")
		c.publish(fmt.Sprintf("%s/%s/audio", c.topic, camName), value, true)
	case "rotary_action":
		c.applyPTZAction(camName, cam, value)
	case "cruise_point":
		c.applyCruisePoint(camName, cam, value)
	case "night_vision":
		fallthrough
	case "irled", "status_light", "motion_detection", "motion_tagging", "hor_flip", "ver_flip", "bitrate", "fps":
		c.applyCloudSetProperty(camName, cam, property, value)
	}
}

func (c *Client) applyPTZAction(camName string, cam *camera.Camera, direction string) {
	direction = strings.ToLower(strings.TrimSpace(direction))
	result := mqttCommandResult{Command: "rotary_action", Direction: direction}
	info, err := c.supportedPTZInfo(cam)
	if err != nil {
		result.Status, result.Error = "error", err.Error()
		c.publishCommandResult(camName, result)
		return
	}
	if _, _, err := wyzeapi.ParsePTZDirection(direction); err != nil {
		result.Status, result.Error = "error", "direction must be up, down, left, right, or stop"
		c.publishCommandResult(camName, result)
		return
	}

	task := ptzCommand{
		result: result,
		run:    func() error { return c.wyzeAPI.MovePTZ(info, direction) },
	}
	busy, accepted := c.enqueuePTZCommand(camName, task)
	if direction == "stop" && busy {
		// Preserve an immediate stop attempt while ensuring a final stop runs
		// after any already-queued movement command.
		go c.executePTZCommand(camName, task)
	}
	if !accepted {
		return
	}
}

type cruisePointCommand struct {
	action string
	slot   int
}

func parseCruisePointCommand(payload string) (cruisePointCommand, error) {
	payload = strings.TrimSpace(payload)
	if slot, err := strconv.Atoi(payload); err == nil {
		if slot < 1 || slot > 4 {
			return cruisePointCommand{}, fmt.Errorf("cruise-point slot must be between 1 and 4")
		}
		return cruisePointCommand{action: "goto", slot: slot}, nil
	}

	var request struct {
		Action string `json:"action"`
		Slot   int    `json:"slot"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return cruisePointCommand{}, fmt.Errorf("payload must be a slot number or JSON action")
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if request.Action != "save" && request.Action != "goto" {
		return cruisePointCommand{}, fmt.Errorf("action must be save or goto")
	}
	if request.Slot < 1 || request.Slot > 4 {
		return cruisePointCommand{}, fmt.Errorf("cruise-point slot must be between 1 and 4")
	}
	return cruisePointCommand{action: request.Action, slot: request.Slot}, nil
}

func (c *Client) applyCruisePoint(camName string, cam *camera.Camera, payload string) {
	request, err := parseCruisePointCommand(payload)
	if err != nil {
		c.publishCommandResult(camName, mqttCommandResult{
			Command: "cruise_point", Status: "error", Error: err.Error(),
		})
		return
	}
	result := mqttCommandResult{Command: "cruise_point", Action: request.action, Slot: request.slot}
	info, err := c.supportedPTZInfo(cam)
	if err != nil {
		result.Status, result.Error = "error", err.Error()
		c.publishCommandResult(camName, result)
		return
	}

	task := ptzCommand{result: result}
	switch request.action {
	case "save":
		task.run = func() error { return c.wyzeAPI.SaveCruisePoint(info, request.slot) }
	case "goto":
		task.run = func() error { return c.wyzeAPI.GoToCruisePoint(info, request.slot) }
	}
	c.enqueuePTZCommand(camName, task)
}

func (c *Client) handleCruisePointsGet(_ paho.Client, msg paho.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 4 {
		return
	}
	camName := parts[len(parts)-3]
	cam := c.camMgr.GetCamera(camName)
	if cam == nil {
		c.publishCommandResult(camName, mqttCommandResult{
			Command: "cruise_points/get", Status: "error", Error: "unknown camera",
		})
		return
	}
	info, err := c.supportedPTZInfo(cam)
	if err != nil {
		c.publishCommandResult(camName, mqttCommandResult{
			Command: "cruise_points/get", Status: "error", Error: err.Error(),
		})
		return
	}

	task := ptzCommand{
		result: mqttCommandResult{Command: "cruise_points/get"},
		run: func() error {
			points, err := c.wyzeAPI.GetCruisePoints(info)
			if err != nil {
				return err
			}
			data, err := json.Marshal(points)
			if err != nil {
				return fmt.Errorf("encode cruise points: %w", err)
			}
			c.publish(fmt.Sprintf("%s/%s/cruise_points", c.topic, camName), string(data), true)
			return nil
		},
	}
	c.enqueuePTZCommand(camName, task)
}

func (c *Client) supportedPTZInfo(cam *camera.Camera) (wyzeapi.CameraInfo, error) {
	if cam == nil {
		return wyzeapi.CameraInfo{}, fmt.Errorf("camera unavailable")
	}
	info := cam.GetInfo()
	if !info.SupportsPTZ() || cam.ForceWebRTC() {
		return wyzeapi.CameraInfo{}, fmt.Errorf("PTZ is supported only for TUTK Pan cameras")
	}
	if c.wyzeAPI == nil {
		return wyzeapi.CameraInfo{}, fmt.Errorf("Wyze API unavailable")
	}
	return info, nil
}

func (c *Client) enqueuePTZCommand(camName string, task ptzCommand) (busy, accepted bool) {
	c.ptzQueueMu.Lock()
	if c.ptzQueues == nil {
		c.ptzQueues = make(map[string]*ptzCommandQueue)
	}
	queue := c.ptzQueues[camName]
	if queue == nil {
		queue = &ptzCommandQueue{jobs: make(chan ptzCommand, ptzCommandQueueSize)}
		c.ptzQueues[camName] = queue
		go c.runPTZCommandQueue(camName, queue)
	}
	c.ptzQueueMu.Unlock()

	var cancelled []mqttCommandResult
	queue.mu.Lock()
	busy = queue.pending > 0
	if isStopCommand(task) && len(queue.jobs) == cap(queue.jobs) {
		for {
			select {
			case queued := <-queue.jobs:
				queue.pending--
				queued.result.Status = "error"
				queued.result.Error = "cancelled to prioritize STOP"
				cancelled = append(cancelled, queued.result)
			default:
				goto drained
			}
		}
	}
drained:
	if len(queue.jobs) == cap(queue.jobs) {
		queue.mu.Unlock()
		task.result.Status, task.result.Error = "error", "PTZ command queue is full"
		c.publishCommandResult(camName, task.result)
		for _, result := range cancelled {
			c.publishCommandResult(camName, result)
		}
		return busy, false
	}
	queue.pending++
	queue.jobs <- task
	queue.mu.Unlock()
	for _, result := range cancelled {
		c.publishCommandResult(camName, result)
	}
	return busy, true
}

func isStopCommand(task ptzCommand) bool {
	return task.result.Command == "rotary_action" && task.result.Direction == "stop"
}

func (c *Client) runPTZCommandQueue(camName string, queue *ptzCommandQueue) {
	for {
		if c.rootCtx.Err() != nil {
			return
		}
		select {
		case <-c.rootCtx.Done():
			return
		case task := <-queue.jobs:
			c.executePTZCommand(camName, task)
			queue.mu.Lock()
			queue.pending--
			queue.mu.Unlock()
		}
	}
}

func (c *Client) executePTZCommand(camName string, task ptzCommand) {
	if err := task.run(); err != nil {
		task.result.Status, task.result.Error = "error", err.Error()
		c.log.Error().Err(err).Str("cam", camName).Str("command", task.result.Command).Msg("MQTT PTZ command failed")
	} else {
		task.result.Status = "ok"
	}
	c.publishCommandResult(camName, task.result)
}

func (c *Client) publishCommandResult(camName string, result mqttCommandResult) {
	data, err := marshalMQTTCommandResult(result)
	if err != nil {
		c.log.Error().Err(err).Str("cam", camName).Msg("failed to encode MQTT command result")
		return
	}
	c.publish(fmt.Sprintf("%s/%s/command/status", c.topic, camName), string(data), false)
}

func marshalMQTTCommandResult(result mqttCommandResult) ([]byte, error) {
	return json.Marshal(result)
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
		go c.camMgr.StartStream(c.rootCtx, camName)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "connected", true)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "on", true)
	case "stop", "off", "0", "false":
		go c.camMgr.StopStream(c.rootCtx, camName)
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
		go c.camMgr.StartStream(c.rootCtx, camName)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "on", true)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "connected", true)
	case "off", "stop", "0", "false":
		go c.camMgr.StopStream(c.rootCtx, camName)
		c.publish(fmt.Sprintf("%s/%s/power", c.topic, camName), "off", true)
		c.publish(fmt.Sprintf("%s/%s/state", c.topic, camName), "disconnected", true)
	case "restart":
		go c.camMgr.RestartStream(c.rootCtx, camName)
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
		go c.onSnapshot(c.rootCtx, camName)
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
	go c.camMgr.RestartStream(c.rootCtx, camName)
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
		go c.onRecord(c.rootCtx, camName, action)
	}
}

// handleDiscoverCommand handles the bridge-wide rediscovery trigger.
// Any non-empty payload kicks off a discovery + reconnect pass (HA
// button entities fire "PRESS" which we treat as "run it").
func (c *Client) handleDiscoverCommand(_ paho.Client, _ paho.Message) {
	c.log.Info().Msg("bridge rediscovery requested via MQTT")
	if c.onDiscover != nil {
		go c.onDiscover(c.rootCtx)
	}
}
