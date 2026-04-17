package wyzeapi

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ModelNames maps product_model codes to human-readable names.
var ModelNames = map[string]string{
	"WYZEC1":        "V1",
	"WYZEC1-JZ":     "V2",
	"WYZE_CAKP2JFUS": "V3",
	"HL_CAM4":       "V4",
	"HL_CAM3P":      "V3 Pro",
	"WYZECP1_JEF":   "Pan",
	"HL_PAN2":       "Pan V2",
	"HL_PAN3":       "Pan V3",
	"HL_PANP":       "Pan Pro",
	"HL_CFL2":       "Floodlight V2",
	"WYZEDB3":       "Doorbell",
	"HL_DB2":        "Doorbell V2",
	"GW_BE1":        "Doorbell Pro",
	"AN_RDB1":       "Doorbell Pro 2",
	"GW_GC1":        "OG",
	"GW_GC2":        "OG 3X",
	"WVOD1":         "Outdoor",
	"HL_WCO2":       "Outdoor V2",
	"AN_RSCW":       "Battery Cam Pro",
	"LD_CFP":        "Floodlight Pro",
	"GW_DBD":        "Doorbell Duo",
}

// Gwell-protocol cameras (not supported by go2rtc TUTK).
var gwellModels = map[string]bool{
	"GW_BE1": true, "GW_GC1": true, "GW_GC2": true, "GW_DBD": true,
}

// webRTCStreamerModels: Wyze cameras that stream live media over WebRTC
// (Wyze's mars-webcsrv signaling + AWS TURN), handled by go2rtc's native
// #format=wyze source. Doorbell lineage. OG cameras (GW_GC1/GC2) use
// Gwell P2P on the LAN and don't belong here.
var webRTCStreamerModels = map[string]bool{
	"GW_BE1": true, "GW_DBD": true,
}

// PanCams is the set of pan/tilt camera models.
var PanCams = map[string]bool{
	"WYZECP1_JEF": true, "HL_PAN2": true, "HL_PAN3": true, "HL_PANP": true,
}

// Doorbell models.
var Doorbells = map[string]bool{
	"WYZEDB3": true, "HL_DB2": true, "GW_DBD": true,
}

// CameraInfo holds discovered camera information from the Wyze API.
type CameraInfo struct {
	Name        string `json:"name"`
	Nickname    string `json:"nickname"`
	Model       string `json:"model"`
	MAC         string `json:"mac"`
	LanIP       string `json:"lan_ip"`
	P2PID       string `json:"p2p_id"`
	ENR         string `json:"enr"`
	ParentENR   string `json:"parent_enr,omitempty"`
	ParentMAC   string `json:"parent_mac,omitempty"`
	DTLS        bool   `json:"dtls"`
	ParentDTLS  bool   `json:"parent_dtls"`
	FWVersion   string `json:"fw_version"`
	Online      bool   `json:"online"`
	ProductType string `json:"product_type"`
	Thumbnail   string `json:"thumbnail,omitempty"`
}

// ModelName returns the human-friendly model name.
func (c CameraInfo) ModelName() string {
	if name, ok := ModelNames[c.Model]; ok {
		return name
	}
	return c.Model
}

// IsGwell returns true if this camera uses the Gwell protocol (unsupported).
func (c CameraInfo) IsGwell() bool {
	return gwellModels[c.Model]
}

// IsWebRTCStreamer returns true when this camera streams via Wyze's
// WebRTC path (served by go2rtc's native #format=wyze source). True if
// either the model is in the allow-list OR the camera is Gwell-protocol
// but has no LAN IP — the cloud not reporting a LAN IP is a reliable
// signal for the doorbell lineage. OG cameras (LAN-direct Gwell P2P)
// always have a LAN IP and stay on gwell-proxy.
func (c CameraInfo) IsWebRTCStreamer() bool {
	if webRTCStreamerModels[c.Model] {
		return true
	}
	if c.IsGwell() && (c.LanIP == "" || c.LanIP == "0.0.0.0") {
		return true
	}
	return false
}

// IsPanCam returns true if this is a pan/tilt camera.
func (c CameraInfo) IsPanCam() bool {
	return PanCams[c.Model]
}

// IsDoorbell returns true if this is a doorbell camera.
func (c CameraInfo) IsDoorbell() bool {
	return Doorbells[c.Model]
}

var nameCleanRE = regexp.MustCompile(`[^\w\-]+`)

// NormalizedName returns a URL-safe lowercase name with spaces replaced.
func (c CameraInfo) NormalizedName() string {
	name := c.Nickname
	if name == "" {
		name = c.MAC
	}
	name = strings.ReplaceAll(strings.TrimSpace(name), " ", "_")
	name = nameCleanRE.ReplaceAllString(name, "")
	return strings.ToLower(name)
}

// StreamURL generates a go2rtc wyze:// stream URL for this camera.
func (c CameraInfo) StreamURL(quality string) string {
	return fmt.Sprintf(
		"wyze://%s?uid=%s&enr=%s&mac=%s&model=%s&subtype=%s&dtls=%v",
		c.LanIP,
		c.P2PID,
		url.QueryEscape(c.ENR),
		c.MAC,
		c.Model,
		quality,
		c.DTLS,
	)
}

// Property IDs for Wyze cloud API commands.
const (
	PIDResolution  = "P2"
	PIDAudio       = "P1"
	PIDNightVision = "P3"
	PIDMotionAlert = "P1047"
)
