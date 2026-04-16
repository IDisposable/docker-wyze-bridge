// Package webhooks sends HTTP POST notifications on camera state changes.
package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Event types sent to webhook endpoints.
const (
	EventCameraOnline  = "camera_online"
	EventCameraOffline = "camera_offline"
	EventCameraError   = "camera_error"
	EventSnapshotReady = "snapshot_ready"
)

// Payload is the JSON body sent to webhook URLs.
type Payload struct {
	Event     string                 `json:"event"`
	Camera    string                 `json:"camera,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// Config holds webhook configuration.
type Config struct {
	URLs    []string      // Webhook endpoint URLs
	Timeout time.Duration // HTTP request timeout
}

// ParseURLs parses a comma-separated list of webhook URLs.
func ParseURLs(raw string) []string {
	if raw == "" {
		return nil
	}
	var urls []string
	for _, u := range strings.Split(raw, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// Client sends webhook notifications.
type Client struct {
	log        zerolog.Logger
	httpClient *http.Client
	urls       []string
}

// NewClient creates a new webhook client.
func NewClient(cfg Config, log zerolog.Logger) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		log:        log,
		httpClient: &http.Client{Timeout: timeout},
		urls:       cfg.URLs,
	}
}

// Enabled returns true if any webhook URLs are configured.
func (c *Client) Enabled() bool {
	return len(c.urls) > 0
}

// URLs returns the configured webhook URLs.
func (c *Client) URLs() []string {
	return c.urls
}

// Send sends a webhook event to all configured URLs.
func (c *Client) Send(ctx context.Context, event, camera string, data map[string]interface{}) {
	if !c.Enabled() {
		return
	}

	payload := Payload{
		Event:     event,
		Camera:    camera,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		c.log.Error().Err(err).Str("event", event).Msg("webhook marshal failed")
		return
	}

	for _, url := range c.urls {
		go c.post(ctx, url, body, event, camera)
	}
}

// SendCameraOnline sends a camera_online event.
func (c *Client) SendCameraOnline(ctx context.Context, camera string, data map[string]interface{}) {
	c.Send(ctx, EventCameraOnline, camera, data)
}

// SendCameraOffline sends a camera_offline event.
func (c *Client) SendCameraOffline(ctx context.Context, camera string, data map[string]interface{}) {
	c.Send(ctx, EventCameraOffline, camera, data)
}

// SendCameraError sends a camera_error event.
func (c *Client) SendCameraError(ctx context.Context, camera string, data map[string]interface{}) {
	c.Send(ctx, EventCameraError, camera, data)
}

// SendSnapshotReady sends a snapshot_ready event.
func (c *Client) SendSnapshotReady(ctx context.Context, camera string) {
	c.Send(ctx, EventSnapshotReady, camera, nil)
}

func (c *Client) post(ctx context.Context, url string, body []byte, event, camera string) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		c.log.Error().Err(err).Str("url", url).Msg("webhook request creation failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Wyze-Bridge-Event", event)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Warn().Err(err).
			Str("url", url).
			Str("event", event).
			Str("cam", camera).
			Msg("webhook delivery failed")
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		c.log.Warn().
			Int("status", resp.StatusCode).
			Str("url", url).
			Str("event", event).
			Msg("webhook returned error status")
	} else {
		c.log.Debug().
			Str("url", url).
			Str("event", event).
			Str("cam", camera).
			Int("status", resp.StatusCode).
			Msg("webhook delivered")
	}
}

// FormatCameraData creates common camera data for webhook payloads.
func FormatCameraData(ip, model, fwVersion, mac, quality string) map[string]interface{} {
	return map[string]interface{}{
		"ip":         ip,
		"model":      model,
		"fw_version": fwVersion,
		"mac":        mac,
		"quality":    quality,
	}
}
