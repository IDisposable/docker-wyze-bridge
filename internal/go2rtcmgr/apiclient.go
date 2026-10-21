package go2rtcmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// APIClient communicates with the go2rtc HTTP API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
	log        zerolog.Logger

	// mu lets one stream-table call at a time reach go2rtc. go2rtc
	// 1.9.14 guards its stream table on create and lookup but not when
	// it lists the table or deletes from it, and the Go runtime kills
	// the process when it catches a map being read and written at the
	// same moment. The bridge used to fire a camera's delete and add
	// alongside every other camera's at startup and on reconnects; one
	// at a time costs a few milliseconds per camera.
	mu sync.Mutex
}

// Unreachable reports an API call that failed because go2rtc itself
// wasn't there to answer (not running, or restarting), as opposed to
// go2rtc answering with an error about the stream.
func Unreachable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// StreamInfo contains details about a go2rtc stream.
type StreamInfo struct {
	Name      string         `json:"name"`
	Producers []ProducerInfo `json:"producers,omitempty"`
	Consumers []ConsumerInfo `json:"consumers,omitempty"`
}

// ProducerInfo describes a stream producer (source). go2rtc reports a
// bare {"url": ...} for a producer it hasn't dialed; once connected
// the object grows remote_addr, bytes_recv and per-track receivers
// with their own byte counters. The wyze:// source feeds its tracks
// through the receivers, so Receivers[].Bytes is where TUTK video
// shows up (bytes_recv stays 0 for it).
type ProducerInfo struct {
	URL        string         `json:"url,omitempty"`
	RemoteAddr string         `json:"remote_addr,omitempty"`
	BytesRecv  int            `json:"bytes_recv,omitempty"`
	Medias     []interface{}  `json:"medias,omitempty"`
	Receivers  []ReceiverInfo `json:"receivers,omitempty"`
}

// ReceiverInfo is one producer track's counters.
type ReceiverInfo struct {
	Bytes   int `json:"bytes,omitempty"`
	Packets int `json:"packets,omitempty"`
}

// Connected reports whether go2rtc currently holds a connection to
// this producer, as opposed to just knowing its URL.
func (p ProducerInfo) Connected() bool {
	return p.RemoteAddr != "" || len(p.Receivers) > 0 || p.BytesRecv > 0
}

// Bytes is the total the producer has delivered on this connection.
func (p ProducerInfo) Bytes() int {
	n := p.BytesRecv
	for _, r := range p.Receivers {
		n += r.Bytes
	}
	return n
}

// ProducerConnected reports whether any producer of the stream is
// connected.
func (s *StreamInfo) ProducerConnected() bool {
	for _, p := range s.Producers {
		if p.Connected() {
			return true
		}
	}
	return false
}

// ProducerBytes sums every producer's delivered bytes. go2rtc resets
// the counters when a producer reconnects, so callers comparing polls
// must treat a decrease as a fresh connection, not as negative flow.
func (s *StreamInfo) ProducerBytes() int {
	n := 0
	for _, p := range s.Producers {
		n += p.Bytes()
	}
	return n
}

// ConsumerInfo describes a stream consumer.
type ConsumerInfo struct {
	URL    string        `json:"url,omitempty"`
	Medias []interface{} `json:"medias,omitempty"`
}

// NewAPIClient creates a new go2rtc API client.
func NewAPIClient(baseURL string, log zerolog.Logger) *APIClient {
	return &APIClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}
}

// BaseURL returns the go2rtc base URL (e.g. "http://127.0.0.1:1984").
// Exposed so callers can proxy additional endpoints not covered by
// this client's method surface (HLS segments, etc).
func (c *APIClient) BaseURL() string {
	return c.baseURL
}

// ListStreams returns all configured streams.
func (c *APIClient) ListStreams(ctx context.Context) (map[string]*StreamInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/streams", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list streams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list streams: %d %s", resp.StatusCode, string(body))
	}

	var result map[string]*StreamInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode streams: %w", err)
	}
	return result, nil
}

// AddStream adds or updates a stream in go2rtc.
//
// An empty streamURL is valid — it registers the stream name without
// a source, which is what we want for streams fed by external RTSP
// PUSH (e.g. the gwell-proxy sidecar pushing Gwell H.264 via ffmpeg).
// Without the pre-registration go2rtc's RTSP server accepts the
// ANNOUNCE but closes the producer after a short grace period, which
// manifests as `av_interleaved_write_frame(): Broken pipe` on the
// ffmpeg side.
func (c *APIClient) AddStream(ctx context.Context, name, streamURL string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.Debug().Str("stream", name).Bool("placeholder", streamURL == "").Msg("adding stream to go2rtc")

	u := fmt.Sprintf("%s/api/streams?name=%s", c.baseURL, url.QueryEscape(name))
	if streamURL != "" {
		u += "&src=" + url.QueryEscape(streamURL)
	}

	// go2rtc uses PUT to create streams, POST is for redirecting
	req, err := http.NewRequestWithContext(ctx, "PUT", u, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("add stream %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add stream %q: %d %s", name, resp.StatusCode, string(body))
	}

	c.log.Debug().Str("name", name).Msg("stream added to go2rtc")
	return nil
}

// DeleteStream removes a stream from go2rtc.
//
// go2rtc reads the stream to delete from src=, not name=. Asked with
// name= alone it took the request for "list everything" and deleted
// nothing, which is how this call behaved until 4.7.
func (c *APIClient) DeleteStream(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := fmt.Sprintf("%s/api/streams?src=%s", c.baseURL, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete stream %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete stream %q: %d %s", name, resp.StatusCode, string(body))
	}

	c.log.Debug().Str("name", name).Msg("stream deleted from go2rtc")
	return nil
}

// GetStreamInfo returns detailed info for a specific stream.
func (c *APIClient) GetStreamInfo(ctx context.Context, name string) (*StreamInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := fmt.Sprintf("%s/api/streams?src=%s", c.baseURL, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get stream info %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get stream info %q: %d %s", name, resp.StatusCode, string(body))
	}

	var info StreamInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode stream info: %w", err)
	}
	info.Name = name
	return &info, nil
}

// HasActiveProducer checks if a stream has an active producer (camera is connected).
func (c *APIClient) HasActiveProducer(ctx context.Context, name string) (bool, error) {
	streams, err := c.ListStreams(ctx)
	if err != nil {
		return false, err
	}

	info, ok := streams[name]
	if !ok {
		return false, nil
	}

	return len(info.Producers) > 0, nil
}

// GetSnapshot retrieves a JPEG snapshot from a stream.
func (c *APIClient) GetSnapshot(ctx context.Context, name string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/frame.jpeg?src=%s", c.baseURL, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get snapshot %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get snapshot %q: %d %s", name, resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
