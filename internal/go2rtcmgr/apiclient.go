package go2rtcmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// APIClient communicates with the go2rtc HTTP API.
type APIClient struct {
	baseURL      string
	httpClient   *http.Client
	log          zerolog.Logger
	authUsername string
	authPassword string
}

// StreamInfo contains details about a go2rtc stream.
type StreamInfo struct {
	Name      string         `json:"name"`
	Producers []ProducerInfo `json:"producers,omitempty"`
	Consumers []ConsumerInfo `json:"consumers,omitempty"`
}

// ProducerInfo describes a stream producer (source).
type ProducerInfo struct {
	ID        int64         `json:"id,omitempty"`
	BytesRecv int64         `json:"bytes_recv,omitempty"`
	URL       string        `json:"url,omitempty"`
	Medias    []interface{} `json:"medias,omitempty"`
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

// SetBasicAuth attaches HTTP Basic auth credentials to every request
// issued by this client. Empty username or password disables auth
// (both fields must be set for auth to be sent).
func (c *APIClient) SetBasicAuth(username, password string) {
	c.authUsername = username
	c.authPassword = password
}

// hasAuth reports whether Basic auth is configured.
func (c *APIClient) hasAuth() bool {
	return c.authUsername != "" && c.authPassword != ""
}

// applyAuth attaches Basic auth to a request when configured. Called
// from every method that constructs a request against the go2rtc API.
func (c *APIClient) applyAuth(req *http.Request) {
	if c.hasAuth() {
		req.SetBasicAuth(c.authUsername, c.authPassword)
	}
}

// BasicAuthCreds returns the configured username/password so the WebUI
// proxy layer (HLS reverse proxy + WS byte-copy) can forward the same
// credentials to go2rtc. Empty strings mean auth is off.
func (c *APIClient) BasicAuthCreds() (username, password string) {
	return c.authUsername, c.authPassword
}

// BaseURL returns the go2rtc base URL (e.g. "http://127.0.0.1:1984").
// Exposed so callers can proxy additional endpoints not covered by
// this client's method surface (HLS segments, etc).
func (c *APIClient) BaseURL() string {
	return c.baseURL
}

// ListStreams returns all configured streams.
func (c *APIClient) ListStreams(ctx context.Context) (map[string]*StreamInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/streams", nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)

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
	c.applyAuth(req)

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
func (c *APIClient) DeleteStream(ctx context.Context, name string) error {
	return c.deleteStream(ctx, name, false)
}

// DeleteStreamAndStop closes producers and consumers when removing an owned
// stream. When allowedSources is supplied, a source mismatch returns conflict
// without stopping it. This opt-in behavior requires the embedded engine patch.
func (c *APIClient) DeleteStreamAndStop(ctx context.Context, name string, allowedSources ...string) error {
	return c.deleteStream(ctx, name, true, allowedSources...)
}

func (c *APIClient) deleteStream(ctx context.Context, name string, stop bool, allowedSources ...string) error {
	u := fmt.Sprintf("%s/api/streams?src=%s", c.baseURL, url.QueryEscape(name))
	if stop {
		u += "&stop=1"
		for _, source := range allowedSources {
			u += "&if_src=" + url.QueryEscape(source)
		}
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)

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
	u := fmt.Sprintf("%s/api/streams?src=%s", c.baseURL, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)

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
	c.applyAuth(req)

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

// PlayToStream redirects src into the dst stream's consumer side
// (POST /api/streams?dst=&src=). For Wyze cameras this plays audio out of
// the camera speaker via go2rtc's backchannel.
func (c *APIClient) PlayToStream(ctx context.Context, dst, src string) error {
	u := fmt.Sprintf("%s/api/streams?dst=%s&src=%s", c.baseURL, url.QueryEscape(dst), url.QueryEscape(src))
	req, err := http.NewRequestWithContext(ctx, "POST", u, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("play to %q: %w", dst, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("play to %q: %d %s", dst, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// BackchannelCodec returns the go2rtc codec string of the stream's
// sendonly audio track (e.g. "PCML/16000/1"), or "" when the active
// producer has no speaker channel.
func (c *APIClient) BackchannelCodec(ctx context.Context, name string) (string, error) {
	info, err := c.GetStreamInfo(ctx, name)
	if err != nil {
		return "", err
	}
	for _, p := range info.Producers {
		for _, m := range p.Medias {
			s, ok := m.(string)
			if !ok {
				continue
			}
			// "audio, sendonly, PCML/16000/1"
			parts := strings.Split(s, ",")
			if len(parts) == 3 && strings.TrimSpace(parts[0]) == "audio" && strings.TrimSpace(parts[1]) == "sendonly" {
				return strings.TrimSpace(parts[2]), nil
			}
		}
	}
	return "", nil
}
