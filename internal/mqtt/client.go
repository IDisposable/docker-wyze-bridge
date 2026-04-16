// Package mqtt handles MQTT publishing, subscription, and Home Assistant discovery.
package mqtt

import (
	"context"
	"fmt"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Client manages the MQTT broker connection and message handling.
type Client struct {
	log         zerolog.Logger
	paho        paho.Client
	topic       string // MQTT_TOPIC, default "wyzebridge"
	dtopic      string // MQTT_DTOPIC, default "homeassistant"
	camMgr      *camera.Manager
	wyzeAPI     *wyzeapi.Client
	bridgeIP    string
	onSnapshot  func(ctx context.Context, camName string) // snapshot trigger callback
	mu          sync.Mutex
	connected   bool
}

// Config holds MQTT connection settings.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	Topic    string
	DTopic   string
}

// NewClient creates a new MQTT client.
func NewClient(cfg Config, camMgr *camera.Manager, api *wyzeapi.Client, bridgeIP string, log zerolog.Logger) *Client {
	c := &Client{
		log:      log,
		topic:    cfg.Topic,
		dtopic:   cfg.DTopic,
		camMgr:   camMgr,
		wyzeAPI:  api,
		bridgeIP: bridgeIP,
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.Host, cfg.Port))
	opts.SetClientID("wyze-bridge")
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(30 * time.Second)
	opts.SetKeepAlive(60 * time.Second)
	opts.SetCleanSession(false)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}

	// LWT: bridge offline on disconnect
	opts.SetWill(
		fmt.Sprintf("%s/bridge/state", c.topic),
		"offline",
		1,    // QoS 1
		true, // retain
	)

	opts.SetOnConnectHandler(func(_ paho.Client) {
		c.mu.Lock()
		c.connected = true
		c.mu.Unlock()
		c.log.Info().Msg("MQTT connected")
		c.onConnect()
	})

	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.log.Warn().Err(err).Msg("MQTT connection lost")
	})

	c.paho = paho.NewClient(opts)
	return c
}

// Connect initiates the MQTT broker connection.
func (c *Client) Connect() error {
	token := c.paho.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}
	return nil
}

// Disconnect gracefully disconnects from the MQTT broker.
func (c *Client) Disconnect() {
	c.publish(fmt.Sprintf("%s/bridge/state", c.topic), "offline", true)
	c.paho.Disconnect(1000)
}

// OnSnapshotRequest registers a callback for MQTT-triggered snapshots.
func (c *Client) OnSnapshotRequest(fn func(ctx context.Context, camName string)) {
	c.onSnapshot = fn
}

// IsConnected returns whether the client is currently connected.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// onConnect is called on initial connect and reconnect.
func (c *Client) onConnect() {
	// Publish bridge online
	c.publish(fmt.Sprintf("%s/bridge/state", c.topic), "online", true)

	// Re-subscribe to command topics
	c.subscribeCommands()

	// Re-publish all camera states
	for _, cam := range c.camMgr.Cameras() {
		c.PublishCameraState(cam)
		c.PublishCameraInfo(cam)
	}

	// Publish HA discovery
	c.PublishAllDiscovery()
}

func (c *Client) publish(topic, payload string, retained bool) {
	token := c.paho.Publish(topic, 1, retained, payload)
	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			c.log.Warn().Err(err).Str("topic", topic).Msg("MQTT publish failed")
		}
	}()
}

func (c *Client) publishBytes(topic string, payload []byte, retained bool) {
	token := c.paho.Publish(topic, 1, retained, payload)
	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			c.log.Warn().Err(err).Str("topic", topic).Msg("MQTT publish failed")
		}
	}()
}

func (c *Client) subscribe(topic string, handler paho.MessageHandler) {
	token := c.paho.Subscribe(topic, 1, handler)
	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			c.log.Warn().Err(err).Str("topic", topic).Msg("MQTT subscribe failed")
		}
	}()
}
