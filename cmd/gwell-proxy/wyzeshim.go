// wyzeshim.go — local replacement for upstream's pkg/wyze.Client.
//
// Upstream's proxy depended on a Python wyze-api companion service
// reachable at CRYZE_API_URL serving /Camera/CameraList,
// /Camera/DeviceInfo, and /Camera/CameraToken. We don't want a Python
// sidecar; instead our Go bridge exposes the same three endpoints on
// its own internal-only HTTP listener (see TODO: internal/webui/shim.go)
// and this client talks to them.
//
// Wire format matches upstream byte-for-byte so the proxy main.go
// above ports verbatim with just an import swap.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type wyzeShimClient struct {
	baseURL    string
	httpClient *http.Client
}

type shimDeviceInfo struct {
	CameraID   string `json:"cameraId"`
	StreamName string `json:"streamName"`
	LanIP      string `json:"lanIp"`
}

type shimAccessCredential struct {
	AccessID    string `json:"accessId"`
	AccessToken string `json:"accessToken"`
}

func newWyzeShimClient(baseURL string) *wyzeShimClient {
	return &wyzeShimClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetCameraList returns the list of Gwell camera IDs the bridge knows
// about. The shim only lists GW_* model cameras (our bridge filters
// TUTK cameras out of this endpoint so the proxy doesn't try to run
// the Gwell handshake against a TUTK camera that speaks a different
// protocol).
func (c *wyzeShimClient) GetCameraList() ([]string, error) {
	var out []string
	if err := c.getJSON("/Camera/CameraList", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetDeviceInfo returns the per-camera metadata the Gwell handshake
// needs. StreamName is the go2rtc stream path the ffmpeg publisher
// writes to — typically matches the camera's normalized name.
func (c *wyzeShimClient) GetDeviceInfo(deviceID string) (*shimDeviceInfo, error) {
	var out shimDeviceInfo
	q := "?deviceId=" + url.QueryEscape(deviceID)
	if err := c.getJSON("/Camera/DeviceInfo"+q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCameraToken returns the Gwell-specific accessID + accessToken
// minted by the Wyze Mars service. The bridge does the signed call to
// wyze-mars-service.wyzecam.com/plugin/mars/v2/regist_gw_user/<id> on
// our behalf so this proxy doesn't need to know about Wpk HMAC signing.
func (c *wyzeShimClient) GetCameraToken(deviceID string) (*shimAccessCredential, error) {
	var out shimAccessCredential
	q := "?deviceId=" + url.QueryEscape(deviceID)
	if err := c.getJSON("/Camera/CameraToken"+q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *wyzeShimClient) getJSON(path string, out interface{}) error {
	resp, err := c.httpClient.Get(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("shim GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shim GET %s: %d %s", path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
