package wyzeapi

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ModelSpec is the routing + UI metadata for a single Wyze product
// code. New camera = one row in modelRegistry + a README table row.
type ModelSpec struct {
	Name string
	// IsGwell: uses Wyze's Gwell/IoTVideo control plane. Doorbell-
	// lineage Gwell models also set IsWebRTCStreamer; OG models don't.
	IsGwell bool
	// IsWebRTCStreamer: streams via Wyze's mars-webcsrv KVS signaling
	// (go2rtc's native #format=wyze source).
	IsWebRTCStreamer bool
	IsPan            bool
	IsDoorbell       bool
}

// modelRegistry is the single source of truth for per-model routing.
var modelRegistry = map[string]ModelSpec{
	"WYZEC1":         {Name: "V1"},
	"WYZEC1-JZ":      {Name: "V2"},
	"WYZE_CAKP2JFUS": {Name: "V3"},
	"HL_CAM4":        {Name: "V4"},
	"HL_CAM3P":       {Name: "V3 Pro"},
	"WYZECP1_JEF":    {Name: "Pan", IsPan: true},
	"HL_PAN2":        {Name: "Pan V2", IsPan: true},
	"HL_PAN3":        {Name: "Pan V3", IsPan: true},
	"HL_PANP":        {Name: "Pan Pro", IsPan: true},
	"HL_CFL2":        {Name: "Floodlight V2"},
	"WYZEDB3":        {Name: "Doorbell", IsDoorbell: true},
	"HL_DB2":         {Name: "Doorbell V2", IsDoorbell: true},
	"GW_BE1":         {Name: "Doorbell Pro", IsGwell: true, IsWebRTCStreamer: true, IsDoorbell: true},
	"AN_RDB1":        {Name: "Doorbell Pro 2", IsGwell: true, IsWebRTCStreamer: true, IsDoorbell: true},
	"GW_DBD":         {Name: "Doorbell Duo", IsGwell: true, IsWebRTCStreamer: true, IsDoorbell: true},
	"GW_GC1":         {Name: "OG", IsGwell: true},
	"GW_GC2":         {Name: "OG 3X", IsGwell: true},
	"WVOD1":          {Name: "Outdoor"},
	"HL_WCO2":        {Name: "Outdoor V2"},
	"AN_RSCW":        {Name: "Battery Cam Pro"},
	"LD_CFP":         {Name: "Floodlight Pro"},
}

// ModelSpecFor returns the registry entry for a model code, or the
// zero spec if the model isn't registered.
func ModelSpecFor(model string) ModelSpec {
	return modelRegistry[model]
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

// ModelName returns the human-friendly model name, falling back to
// the raw model code when the model isn't in the registry yet.
func (c CameraInfo) ModelName() string {
	if spec, ok := modelRegistry[c.Model]; ok {
		return spec.Name
	}
	return c.Model
}

// IsGwell returns true if this camera uses the Gwell control plane
// (gwell-proxy LAN-direct for OG models; WebRTC for doorbell lineage).
func (c CameraInfo) IsGwell() bool {
	return modelRegistry[c.Model].IsGwell
}

// IsWebRTCStreamer returns true when this camera streams via Wyze's
// WebRTC path (served by go2rtc's native #format=wyze source). True
// either because the model is explicitly flagged in the registry,
// or because it's a Gwell model the cloud reports without a LAN IP
// (a reliable runtime signal for the doorbell lineage).
func (c CameraInfo) IsWebRTCStreamer() bool {
	spec := modelRegistry[c.Model]
	if spec.IsWebRTCStreamer {
		return true
	}
	if spec.IsGwell && (c.LanIP == "" || c.LanIP == "0.0.0.0") {
		return true
	}
	return false
}

// IsPanCam returns true if this is a pan/tilt camera.
func (c CameraInfo) IsPanCam() bool {
	return modelRegistry[c.Model].IsPan
}

// IsDoorbell returns true if this is a doorbell camera.
func (c CameraInfo) IsDoorbell() bool {
	return modelRegistry[c.Model].IsDoorbell
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
