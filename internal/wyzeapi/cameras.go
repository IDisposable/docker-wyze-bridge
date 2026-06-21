package wyzeapi

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// FixKVSSignalingURL works around Wyze's get_streams double-encoding the
// AWS SigV4 query parameters in the KVS WebRTC signaling URL for some
// cameras (observed on the LD_CFP Floodlight Pro): the slashes/colons in
// X-Amz-Credential, X-Amz-ChannelARN, X-Amz-Security-Token come back as
// %252F/%253A/%252B instead of %2F/%3A/%2B. Sent verbatim, AWS rejects
// the websocket handshake with 403 "Credential must have exactly 5
// slash-delimited elements".
//
// The tell-tale is a literal %25 (an encoded percent) — correctly
// single-encoded presigned URLs never contain one. When present, decode
// exactly one layer with PathUnescape (which, unlike QueryUnescape,
// leaves '+' alone so base64 tokens survive). Otherwise return the URL
// untouched so non-double-encoded cameras (e.g. Doorbell Pro) are
// unaffected.
func FixKVSSignalingURL(u string) string {
	if !strings.Contains(u, "%25") {
		return u
	}
	if dec, err := url.PathUnescape(u); err == nil {
		return dec
	}
	return u
}

// GetCameraList fetches the list of cameras from the Wyze API.
func (c *Client) GetCameraList() ([]CameraInfo, error) {
	if err := c.EnsureAuth(); err != nil {
		return nil, err
	}

	c.log.Info().Msg("fetching camera list from Wyze API")

	resp, err := c.postJSON(
		c.WyzeURL+"/v2/home_page/get_object_list",
		c.defaultHeaders(),
		c.authenticatedPayload("default"),
	)
	if err != nil {
		return nil, fmt.Errorf("get_camera_list: %w", err)
	}

	deviceList, ok := resp["device_list"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("get_camera_list: missing device_list in response")
	}

	var cameras []CameraInfo
	for _, item := range deviceList {
		dev, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		productType, _ := dev["product_type"].(string)
		productModel, _ := dev["product_model"].(string)
		nickname, _ := dev["nickname"].(string)

		// Accept Camera and Doorbell product types (TUTK-based devices).
		// Log all skipped devices so users can report missing device types.
		if !isSupportedProductType(productType) {
			c.log.Debug().
				Str("nickname", nickname).
				Str("product_type", productType).
				Str("product_model", productModel).
				Msg("skipping unsupported device type")
			continue
		}

		params, _ := dev["device_params"].(map[string]interface{})
		if params == nil {
			c.log.Debug().
				Str("nickname", nickname).
				Str("product_type", productType).
				Msg("skipping device with no device_params")
			continue
		}

		cam := CameraInfo{
			Nickname:    nickname,
			Model:       productModel,
			MAC:         strings.ToUpper(getString(dev, "mac")),
			ENR:         getString(dev, "enr"),
			FWVersion:   getString(dev, "firmware_ver"),
			ProductType: productType,
			ParentENR:   getString(dev, "parent_device_enr"),
			ParentMAC:   strings.ToUpper(getString(dev, "parent_device_mac")),
			LanIP:       getString(params, "ip"),
			P2PID:       getString(params, "p2p_id"),
			DTLS:        getInt(params, "dtls") != 0,
			ParentDTLS:  getInt(params, "main_device_dtls") != 0,
			Online:      true,
		}

		// Extract thumbnail
		if thumbs, ok := params["camera_thumbnails"].(map[string]interface{}); ok {
			cam.Thumbnail = getString(thumbs, "thumbnails_url")
		}

		// Generate normalized name
		cam.Name = cam.NormalizedName()

		// Log all discovered devices at debug level for diagnostics
		c.log.Debug().
			Str("nickname", cam.Nickname).
			Str("model", cam.Model).
			Str("mac", cam.MAC).
			Str("ip", cam.LanIP).
			Str("p2p_id", cam.P2PID).
			Str("product_type", productType).
			Bool("dtls", cam.DTLS).
			Str("fw", cam.FWVersion).
			Msg("discovered device")

		// Skip devices missing required P2P fields. The field set is
		// protocol-specific:
		//  - TUTK cameras: need P2PID + LanIP + ENR + MAC + Model.
		//    LanIP comes straight from the Wyze cloud response and is
		//    non-empty for online cameras.
		//  - Gwell cameras (GW_BE1/GC1/GC2/DBD): Wyze returns an empty
		//    LanIP for these — the actual IP is recovered by the proxy
		//    during P2P discovery. P2PID is just the device MAC echoed
		//    back. Require MAC + ENR + Model only.
		//  - WebRTC/KVS cameras (LD_CFP Floodlight Pro): Wyze serves them
		//    over AWS KVS, signaled per-session by get_streams. No LAN IP
		//    and no useful P2PID, so require MAC + Model only.
		var missing bool
		switch {
		case cam.IsGwell():
			missing = cam.MAC == "" || cam.ENR == "" || cam.Model == ""
		case cam.IsWebRTCStreamer():
			missing = cam.MAC == "" || cam.Model == ""
		default:
			missing = cam.P2PID == "" || cam.LanIP == "" || cam.ENR == "" || cam.MAC == "" || cam.Model == ""
		}
		if missing {
			c.log.Warn().
				Str("nickname", cam.Nickname).
				Str("model", cam.Model).
				Str("product_type", productType).
				Str("ip", cam.LanIP).
				Str("p2p_id", cam.P2PID).
				Bool("has_enr", cam.ENR != "").
				Bool("gwell", cam.IsGwell()).
				Msg("skipping device with missing P2P params")
			continue
		}

		cameras = append(cameras, cam)
	}

	c.log.Info().Int("count", len(cameras)).Msg("cameras discovered")
	return cameras, nil
}

// SetProperty sets a device property via the Wyze cloud API.
func (c *Client) SetProperty(cam CameraInfo, pid, pvalue string) error {
	if err := c.EnsureAuth(); err != nil {
		return err
	}

	payload := c.authenticatedPayload("set_device_Info")
	payload["device_mac"] = cam.MAC
	payload["device_model"] = cam.Model
	payload["pid"] = strings.ToUpper(pid)
	payload["pvalue"] = pvalue

	_, err := c.postJSON(c.WyzeURL+"/v2/device/set_property", c.defaultHeaders(), payload)
	if err != nil {
		return fmt.Errorf("set_property: %w", err)
	}
	return nil
}

// GetDeviceInfo fetches device properties from the Wyze cloud API.
func (c *Client) GetDeviceInfo(cam CameraInfo) ([]map[string]interface{}, error) {
	if err := c.EnsureAuth(); err != nil {
		return nil, err
	}

	payload := c.authenticatedPayload("get_device_Info")
	payload["device_mac"] = cam.MAC
	payload["device_model"] = cam.Model

	resp, err := c.postJSON(c.WyzeURL+"/v2/device/get_device_Info", c.defaultHeaders(), payload)
	if err != nil {
		return nil, fmt.Errorf("get_device_info: %w", err)
	}

	propList, ok := resp["property_list"].([]interface{})
	if !ok {
		return nil, nil
	}

	var result []map[string]interface{}
	for _, p := range propList {
		if m, ok := p.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// isSupportedProductType returns true for device types that use TUTK P2P
// and can be streamed via go2rtc. We accept "Camera" and "Doorbell" —
// doorbells like WYZEDB3 are TUTK-based and go2rtc already handles them.
func isSupportedProductType(pt string) bool {
	switch pt {
	case "Camera", "Doorbell":
		return true
	}
	return false
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// GetCameraStream calls /v4/camera/get_streams to mint a Wyze KVS WebRTC
// signaling URL + ICE servers for a camera. Consumed by the shim handler
// at /internal/wyze/webrtc/<cam> which go2rtc's native #format=wyze
// source fetches when it needs to start a stream. Returns the raw
// response map; shape:
//
//	{"code":"1","data":[{"params":{"signaling_url":"wss://...",
//	                              "ice_servers":[{"url":"...","username":"...","credential":"..."}],
//	                              "auth_token":""}}]}
func (c *Client) GetCameraStream(cam CameraInfo) (map[string]interface{}, error) {
	if err := c.EnsureAuth(); err != nil {
		return nil, err
	}
	payload := map[string]interface{}{
		"device_list": []interface{}{
			map[string]interface{}{
				"device_id":    cam.MAC,
				"device_model": cam.Model,
				"provider":     "webrtc",
				"parameters":   map[string]interface{}{"use_trickle": true},
			},
		},
		"nonce": time.Now().UnixMilli(),
	}
	sorted := sortDict(payload)
	headers := c.signPayloadHeaders("9319141212m2ik", sorted)
	// Wyze's v4 front door also checks lowercase `authorization`; the
	// HMAC-signed `access_token` header alone isn't enough.
	headers["authorization"] = c.auth.AccessToken

	url := c.NewWyzeURL + "/v4/camera/get_streams"
	resp, err := c.postRaw(url, headers, sorted)
	if err != nil {
		return nil, fmt.Errorf("get_streams: %w", err)
	}
	return resp, nil
}
