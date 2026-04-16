package wyzeapi

import (
	"fmt"
	"strings"
)

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

		// Skip devices missing required P2P fields
		if cam.P2PID == "" || cam.LanIP == "" || cam.ENR == "" || cam.MAC == "" || cam.Model == "" {
			c.log.Warn().
				Str("nickname", cam.Nickname).
				Str("model", cam.Model).
				Str("product_type", productType).
				Str("ip", cam.LanIP).
				Str("p2p_id", cam.P2PID).
				Bool("has_enr", cam.ENR != "").
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
