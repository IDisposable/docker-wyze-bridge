// Package mqtt handles MQTT publishing, subscription, and Home Assistant discovery.
package mqtt

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// maxInflightPublishes caps concurrent publish-token waiter goroutines.
// publish() drops the message when saturated.
const maxInflightPublishes = 1024

// Client manages the MQTT broker connection and message handling.
// rootCtx (set via SetRootContext) ties subscribe-handler goroutines
// to the bridge's shutdown so fire-and-forget work cancels cleanly.
// publishSem bounds concurrent publish-waiter goroutines.
type Client struct {
	log           zerolog.Logger
	paho          paho.Client
	topic         string // MQTT_TOPIC, default "wyzebridge"
	dtopic        string // MQTT_DISCOVERY_TOPIC, default "homeassistant"
	camMgr        *camera.Manager
	wyzeAPI       *wyzeapi.Client
	bridgeIP      string
	rootCtx       context.Context
	publishSem    chan struct{}
	droppedPubs   atomic.Uint64
	onSnapshot    func(ctx context.Context, camName string)         // snapshot trigger callback
	onRecord      func(ctx context.Context, camName, action string) // record start/stop callback (action = "start"|"stop")
	onDiscover    func(ctx context.Context)                         // bridge-wide rediscovery trigger
	mu            sync.Mutex
	connected     bool
}

// Config holds MQTT connection settings.
type Config struct {
	Host           string
	Port           int
	Username       string
	Password       string
	Topic          string
	DiscoveryTopic string
}

// NewClient creates a new MQTT client. ctx is the bridge's
// signal-cancellable root context; subscribe-handler fire-and-forget
// goroutines derive from it so they cancel on shutdown.
func NewClient(ctx context.Context, cfg Config, camMgr *camera.Manager, api *wyzeapi.Client, bridgeIP string, log zerolog.Logger) *Client {
	c := &Client{
		log:        log,
		topic:      cfg.Topic,
		dtopic:     cfg.DiscoveryTopic,
		camMgr:     camMgr,
		wyzeAPI:    api,
		bridgeIP:   bridgeIP,
		rootCtx:    ctx,
		publishSem: make(chan struct{}, maxInflightPublishes),
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

// OnRecordRequest registers a callback for MQTT-triggered record
// start/stop. action is "start" or "stop".
func (c *Client) OnRecordRequest(fn func(ctx context.Context, camName, action string)) {
	c.onRecord = fn
}

// OnDiscoverRequest registers a callback for MQTT-triggered bridge
// rediscovery (re-poll Wyze API for added/removed cameras).
func (c *Client) OnDiscoverRequest(fn func(ctx context.Context)) {
	c.onDiscover = fn
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
	c.publishGuarded(topic, payload, retained)
}

func (c *Client) publishBytes(topic string, payload []byte, retained bool) {
	c.publishGuarded(topic, payload, retained)
}

// publishGuarded acquires a publishSem slot or drops the message.
// Drops are counted; logs fire on count == 1 and at every power of
// two thereafter (loud-then-rate-limited).
func (c *Client) publishGuarded(topic string, payload any, retained bool) {
	select {
	case c.publishSem <- struct{}{}:
	default:
		n := c.droppedPubs.Add(1)
		if n == 1 || (n&(n-1)) == 0 {
			c.log.Warn().Uint64("dropped_total", n).Str("topic", topic).Msg("MQTT publish saturated; dropping message")
		}
		return
	}
	token := c.paho.Publish(topic, 1, retained, payload)
	go func() {
		defer func() { <-c.publishSem }()
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
