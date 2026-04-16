package gwell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"
)

// APIClient talks to the gwell-proxy's loopback control HTTP API.
//
// Wire format (JSON) — mirrors the routes exposed by the upstream
// cmd/gwell-proxy. Fields not understood by a given proxy version are
// ignored server-side, so adding new ones is backward-compatible.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
	log        zerolog.Logger
}

// NewAPIClient builds a client against the given base URL
// (e.g. "http://127.0.0.1:18564").
func NewAPIClient(baseURL string, log zerolog.Logger) *APIClient {
	return &APIClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}
}

// RegisterRequest is the POST /cameras body. It carries everything
// the proxy needs to set up the Gwell P2P session:
//   - MAC and ENR identify the camera in the Gwell cloud.
//   - LanIP is a hint; the proxy will discover it if empty.
//   - AccessToken is the current Wyze cloud access_token (bridge-minted).
//   - PhoneID/UserID/AppVersion replicate the account-scoped headers
//     Wyze's official app sends.
//   - Quality ("hd"/"sd") maps to the AVSTREAMCTL quality request.
//   - Audio toggles the A-channel advertisement.
type RegisterRequest struct {
	Name        string `json:"name"`
	MAC         string `json:"mac"`
	ENR         string `json:"enr"`
	Model       string `json:"model"`
	LanIP       string `json:"lan_ip,omitempty"`
	AccessToken string `json:"access_token"`
	PhoneID     string `json:"phone_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	AppVersion  string `json:"app_version,omitempty"`
	Quality     string `json:"quality,omitempty"`
	Audio       bool   `json:"audio,omitempty"`
}

// RegisterResponse echoes the RTSP path chosen by the proxy. If the
// upstream proxy version doesn't return this, clients should fall back
// to Config.RTSPURL(name).
type RegisterResponse struct {
	Name    string `json:"name"`
	RTSPURL string `json:"rtsp_url,omitempty"`
}

// CameraStatus is one entry in the GET /cameras response.
type CameraStatus struct {
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	State    string `json:"state"`
	RTSPURL  string `json:"rtsp_url,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

// Register tells the proxy to start / refresh a camera session.
// Returns the RTSP URL the proxy will publish on.
func (c *APIClient) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if req.AccessToken == "" {
		return nil, ErrNoAuth
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("gwell: marshal register: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cameras", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gwell: register %q: %w", req.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gwell: register %q: %d %s", req.Name, resp.StatusCode, string(b))
	}

	var out RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && err != io.EOF {
		return nil, fmt.Errorf("gwell: decode register response: %w", err)
	}
	if out.Name == "" {
		out.Name = req.Name
	}
	return &out, nil
}

// Unregister removes a camera from the proxy. Unknown cameras return
// a 404 — treated as a no-op success.
func (c *APIClient) Unregister(ctx context.Context, name string) error {
	u := fmt.Sprintf("%s/cameras/%s", c.baseURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gwell: unregister %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gwell: unregister %q: %d %s", name, resp.StatusCode, string(b))
	}
	return nil
}

// ListCameras returns the proxy's view of all registered cameras.
func (c *APIClient) ListCameras(ctx context.Context) ([]CameraStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/cameras", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gwell: list cameras: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gwell: list cameras: %d %s", resp.StatusCode, string(b))
	}

	var out []CameraStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gwell: decode list cameras: %w", err)
	}
	return out, nil
}
