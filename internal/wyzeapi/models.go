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
	// IsGwellP2P: OG-family Gwell models that always stream via
	// gwell-proxy on the LAN, even when the Wyze cloud reports an
	// empty LAN IP. Pinned so they don't fall into the WebRTC
	// fallback path that's meant for doorbell-lineage models.
	IsGwellP2P bool
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
	// OG cameras stream via Wyze's mars-webcsrv WebRTC backend
	// (same path as the Doorbell Pro). The gwell-proxy LAN-direct
	// path is no longer reliable for OG; users who still want it can
	// flip via MODEL_OVERRIDES (HA: Camera Model Registry → Model
	// Overrides) with `is_gwell=true,is_gwell_p2p=true,is_webrtc=false`.
	"GW_GC1": {Name: "OG", IsWebRTCStreamer: true},
	"GW_GC2": {Name: "OG 3X", IsWebRTCStreamer: true},
	"GW_WC":  {Name: "Window Cam", IsGwell: true, IsGwellP2P: true},
	// GW_DUO: PR #118 hardware-verified WebRTC via mars-webcsrv (same as
	// GW_BE1 Doorbell Pro). PR #116 had it as Gwell P2P; PR #118 wins
	// because get_streams returns a wyze-mars-webcsrv signaling URL.
	"GW_DUO":  {Name: "Cam Pan Duo", IsWebRTCStreamer: true, IsPan: true},
	"WVOD1":   {Name: "Outdoor"},
	"HL_WCO2": {Name: "Outdoor V2"},
	"AN_RSCW": {Name: "Battery Cam Pro"},
	"LD_CFP":  {Name: "Floodlight Pro", IsWebRTCStreamer: true},
}

// ModelSpecFor returns the registry entry for a model code, or the
// zero spec if the model isn't registered.
func ModelSpecFor(model string) ModelSpec {
	return modelRegistry[model]
}

// RegisterModel adds or replaces a row in modelRegistry. Used by main
// at startup to apply MODEL_OVERRIDES so operators can add a brand-new
// Wyze model code or flip flags on an existing one without recompiling.
// The supplied spec replaces any prior entry wholesale; partial updates
// require reading the existing spec first.
func RegisterModel(model string, spec ModelSpec) {
	modelRegistry[model] = spec
}

// ApplyModelOverrides parses and applies the MODEL_OVERRIDES env-var
// format. Each entry overrides (or adds) one model. Existing flags are
// preserved unless mentioned in the override — set `flag=false` to
// clear, omit to keep the registry's value.
//
// Format: `MODEL[:flag=value]*` entries joined by `;`. Whitespace OK.
//
//	GW_DUO:name=Cam Pan Duo,is_webrtc=true,is_pan=true
//	GW_NEW:name=Made-up Cam,is_gwell=true,is_gwell_p2p=true
//
// Flag names (case-insensitive): name, is_gwell, is_gwell_p2p,
// is_webrtc, is_pan, is_doorbell. Returns a slice of (model, err)
// for entries that failed to parse so the caller can log them; the
// successful entries are applied regardless.
func ApplyModelOverrides(raw string) []ModelOverrideError {
	var errs []ModelOverrideError
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		head, tail, _ := strings.Cut(entry, ":")
		model := strings.TrimSpace(head)
		if model == "" {
			errs = append(errs, ModelOverrideError{Entry: entry, Err: fmt.Errorf("empty model code")})
			continue
		}
		spec := modelRegistry[model] // start from existing or zero
		entryErr := false
		for _, kv := range strings.Split(tail, ",") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				errs = append(errs, ModelOverrideError{Entry: entry, Err: fmt.Errorf("flag %q missing '='", kv)})
				entryErr = true
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			v = strings.TrimSpace(v)
			switch k {
			case "name":
				spec.Name = v
			case "is_gwell":
				spec.IsGwell = parseBoolFlag(v)
			case "is_gwell_p2p":
				spec.IsGwellP2P = parseBoolFlag(v)
			case "is_webrtc", "is_webrtc_streamer":
				spec.IsWebRTCStreamer = parseBoolFlag(v)
			case "is_pan":
				spec.IsPan = parseBoolFlag(v)
			case "is_doorbell":
				spec.IsDoorbell = parseBoolFlag(v)
			default:
				errs = append(errs, ModelOverrideError{Entry: entry, Err: fmt.Errorf("unknown flag %q", k)})
				entryErr = true
			}
		}
		if !entryErr {
			RegisterModel(model, spec)
		}
	}
	return errs
}

// ModelOverrideError pairs the offending raw entry with its parse error
// so callers can log them user-actionably.
type ModelOverrideError struct {
	Entry string
	Err   error
}

func parseBoolFlag(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
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

// IsGwellP2P returns true for OG-family cameras that always stream
// via gwell-proxy, even when the cloud reports an empty LAN IP.
func (c CameraInfo) IsGwellP2P() bool {
	return modelRegistry[c.Model].IsGwellP2P
}

// IsWebRTCStreamer returns true when this camera streams via Wyze's
// WebRTC path (served by go2rtc's native #format=wyze source). True
// either because the model is explicitly flagged in the registry,
// or because it's a non-OG Gwell model the cloud reports without a
// LAN IP (a reliable runtime signal for the doorbell lineage). OG
// models are excluded — gwell-proxy discovers their LAN IP over P2P.
func (c CameraInfo) IsWebRTCStreamer() bool {
	spec := modelRegistry[c.Model]
	if spec.IsWebRTCStreamer {
		return true
	}
	if spec.IsGwell && !spec.IsGwellP2P && (c.LanIP == "" || c.LanIP == "0.0.0.0") {
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
