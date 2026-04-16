package wyzeapi

import (
	"testing"
)

func TestIsSupportedProductType(t *testing.T) {
	tests := []struct {
		pt   string
		want bool
	}{
		{"Camera", true},
		{"Doorbell", true},
		{"Light", false},
		{"Plug", false},
		{"Lock", false},
		{"Sensor", false},
		{"", false},
		{"camera", false}, // case-sensitive match against Wyze API values
	}

	for _, tt := range tests {
		if got := isSupportedProductType(tt.pt); got != tt.want {
			t.Errorf("isSupportedProductType(%q) = %v, want %v", tt.pt, got, tt.want)
		}
	}
}

func TestGetCameraList_IncludesDoorbells(t *testing.T) {
	// Simulates the parsing logic with a doorbell device
	devices := []map[string]interface{}{
		{
			"product_type":  "Camera",
			"product_model": "HL_CAM4",
			"mac":           "AABB01",
			"nickname":      "Front Door",
			"enr":           "enr1",
			"device_params": map[string]interface{}{
				"p2p_id": "UID01234567890123456",
				"ip":     "10.0.0.1",
				"dtls":   float64(1),
				"camera_thumbnails": map[string]interface{}{},
			},
		},
		{
			"product_type":  "Doorbell",
			"product_model": "WYZEDB3",
			"mac":           "AABB02",
			"nickname":      "Front Doorbell",
			"enr":           "enr2",
			"device_params": map[string]interface{}{
				"p2p_id": "UID01234567890123457",
				"ip":     "10.0.0.2",
				"dtls":   float64(1),
				"camera_thumbnails": map[string]interface{}{},
			},
		},
		{
			"product_type":  "Light",
			"product_model": "WLPA19",
			"mac":           "AABB03",
			"nickname":      "Porch Light",
		},
	}

	var cameras []CameraInfo
	for _, dev := range devices {
		pt, _ := dev["product_type"].(string)
		if !isSupportedProductType(pt) {
			continue
		}
		params, _ := dev["device_params"].(map[string]interface{})
		if params == nil {
			continue
		}
		cam := CameraInfo{
			Nickname: getString(dev, "nickname"),
			Model:    getString(dev, "product_model"),
			MAC:      getString(dev, "mac"),
			ENR:      getString(dev, "enr"),
			LanIP:    getString(params, "ip"),
			P2PID:    getString(params, "p2p_id"),
			DTLS:     getInt(params, "dtls") != 0,
		}
		if cam.P2PID == "" || cam.LanIP == "" || cam.ENR == "" || cam.MAC == "" {
			continue
		}
		cameras = append(cameras, cam)
	}

	if len(cameras) != 2 {
		t.Fatalf("expected 2 devices (camera + doorbell), got %d", len(cameras))
	}
	if cameras[0].Model != "HL_CAM4" {
		t.Errorf("cameras[0] model = %q", cameras[0].Model)
	}
	if cameras[1].Model != "WYZEDB3" {
		t.Errorf("cameras[1] model = %q, want WYZEDB3", cameras[1].Model)
	}
}

func TestGetCameraList_DoorbellMissingIP(t *testing.T) {
	// Doorbell with missing IP should be skipped (not crash)
	devices := []map[string]interface{}{
		{
			"product_type":  "Doorbell",
			"product_model": "WYZEDB3",
			"mac":           "AABB02",
			"nickname":      "Doorbell No IP",
			"enr":           "enr2",
			"device_params": map[string]interface{}{
				"p2p_id": "UID01234567890123457",
				"ip":     "", // empty IP
				"camera_thumbnails": map[string]interface{}{},
			},
		},
	}

	var cameras []CameraInfo
	for _, dev := range devices {
		pt, _ := dev["product_type"].(string)
		if !isSupportedProductType(pt) {
			continue
		}
		params, _ := dev["device_params"].(map[string]interface{})
		if params == nil {
			continue
		}
		cam := CameraInfo{
			LanIP: getString(params, "ip"),
			P2PID: getString(params, "p2p_id"),
			ENR:   getString(dev, "enr"),
			MAC:   getString(dev, "mac"),
		}
		if cam.P2PID == "" || cam.LanIP == "" || cam.ENR == "" || cam.MAC == "" {
			continue
		}
		cameras = append(cameras, cam)
	}

	if len(cameras) != 0 {
		t.Errorf("doorbell with empty IP should be skipped, got %d", len(cameras))
	}
}
