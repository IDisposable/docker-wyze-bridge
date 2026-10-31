// Package wyzectl talks to go2rtc's Wyze device-control HTTP endpoints
// (part of our private go2rtc fork's `pkg/wyze/control.go` surface).
//
// The endpoints are send-only: every call returns error only on transport
// or 4xx/5xx response; the camera's actual state change is not
// synchronously confirmed. Live property readback is a separate SSE
// subscription (planned Phase 2.1, not yet exposed).
//
// All calls require the camera to have an active streaming session (a
// Wyze TUTK producer inside go2rtc). Absent that, endpoints 404; the
// caller should treat 404 as "camera offline" and either wait for
// reconnect or surface the failure to MQTT.
//
// Scoping: TUTK cameras only. Doorbell / OG / floodlight-pro cameras
// route through mars-webcsrv WebRTC and don't share this control
// plane. For those, use the Wyze cloud API instead (`internal/wyzeapi`).
//
// Wire ground truth: opcodes and payload layouts come from
// wyzecam/tutk_protocol.py @ mrlt8/docker-wyze-bridge blob 73dded9c
// (65-class catalog, verified against real hardware for years). See
// DOCS/fork-control-plane-patch.md for the full mapping.
package wyzectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Client sends Wyze device-control commands to go2rtc.
//
// Method surface mirrors the fork's `pkg/wyze/control.go` — one Go
// method per HTTP endpoint per Wyze-semantic command. Endpoint names
// are camera-user-visible ("spotlight", "ir_led") not K-code-visible,
// so the wire opcode can change on the fork side without breaking us.
// The client is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client
	log        zerolog.Logger

	// Basic-auth credentials matching what go2rtc's api.username /
	// api.password expect (see GO2RTC_API_USERNAME / GO2RTC_API_PASSWORD
	// from #123). Both empty = no auth header sent.
	authUsername string
	authPassword string
}

// NewClient constructs a wyzectl Client. baseURL is the go2rtc HTTP
// root — same value passed to go2rtcmgr.NewAPIClient (typically
// "http://127.0.0.1:<GO2RTC_API_PORT>"). The client uses a short
// timeout because these are fire-and-forget IOCtrl frames that should
// complete in a single roundtrip.
func NewClient(baseURL string, log zerolog.Logger) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		log:        log,
	}
}

// SetBasicAuth attaches credentials to every request. Empty username
// or password disables auth (both fields must be set for auth to fire).
// Idempotent; call from main.go with the same GO2RTC_API_USERNAME /
// GO2RTC_API_PASSWORD used by go2rtcmgr.APIClient.
func (c *Client) SetBasicAuth(username, password string) {
	c.authUsername = username
	c.authPassword = password
}

// ErrCameraOffline is returned when go2rtc has no active TUTK producer
// for the requested camera. Distinguishable from other 4xx errors so
// the MQTT subscriber can silently drop the command instead of logging
// it as a bug.
var ErrCameraOffline = errors.New("wyzectl: camera has no active TUTK session")

// ErrControlNotSupported is returned when the go2rtc build doesn't
// expose the requested endpoint — either the fork isn't in use, or the
// endpoint was removed. Different from ErrCameraOffline because the
// operator can fix this one (rebuild against the fork).
var ErrControlNotSupported = errors.New("wyzectl: go2rtc build lacks this control endpoint")

// NightVisionMode is the tri-state accepted by SetNightVision.
// Byte values match K10042SetNightVisionStatus (wyzecam
// tutk_protocol.py:413).
type NightVisionMode byte

const (
	NightVisionOn   NightVisionMode = 1
	NightVisionOff  NightVisionMode = 2
	NightVisionAuto NightVisionMode = 3
)

// String returns the MQTT-friendly form ("off"/"on"/"auto"/"unknown").
func (m NightVisionMode) String() string {
	switch m {
	case NightVisionOff:
		return "off"
	case NightVisionOn:
		return "on"
	case NightVisionAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// ParseNightVisionMode accepts the MQTT input shapes for night-vision:
// "off"/"on"/"auto" case-insensitive, or the raw byte-string ("1"/"2"/"3").
// Returns ok=false on unrecognised input so the caller can reject
// without publishing spurious state.
func ParseNightVisionMode(raw string) (NightVisionMode, bool) {
	switch raw {
	case "1", "on", "ON", "On":
		return NightVisionOn, true
	case "2", "off", "OFF", "Off":
		return NightVisionOff, true
	case "3", "auto", "AUTO", "Auto":
		return NightVisionAuto, true
	}
	return 0, false
}

// IRLEDMode toggles the camera IR LED wavelength / power.
// Byte values match K10046SetIRLEDStatus (wyzecam
// tutk_protocol.py:444).
//
// The naming reflects the actual optics: value 1 enables the 850nm
// long-range IR LEDs; value 2 selects the 940nm short-range mode
// (also colloquially "off" because it's used for close-up illumination
// only). We surface as on/off in MQTT for user simplicity but expose
// the raw modes here so callers can offer both if desired.
type IRLEDMode byte

const (
	IRLEDLongRange  IRLEDMode = 1 // 850nm; MQTT "on"
	IRLEDShortRange IRLEDMode = 2 // 940nm; MQTT "off"
)

// SetSpotlight toggles the visible-light spotlight on hardware that has
// one (V3 Spotlight edition, Floodlight lineage). Silent no-op on
// non-spotlight hardware — the camera drops the K10646 frame.
// K10646SetSpotlightStatus, payload `[on:1]` where 1=on, 2=off.
func (c *Client) SetSpotlight(ctx context.Context, cam string, on bool) error {
	return c.postJSON(ctx, cam, "spotlight", map[string]bool{"on": on})
}

// SetIRLED toggles the IR illuminator. Silent no-op on cameras without
// IR LEDs. See IRLEDMode for the semantic mapping.
// K10046SetIRLEDStatus, payload `[mode:1]` where 1=on (850nm), 2=off (940nm).
func (c *Client) SetIRLED(ctx context.Context, cam string, on bool) error {
	mode := IRLEDShortRange
	if on {
		mode = IRLEDLongRange
	}
	return c.postJSON(ctx, cam, "ir_led", map[string]byte{"mode": byte(mode)})
}

// SetNightVision picks on/off/auto.
// K10042SetNightVisionStatus, payload `[status:1]` where
// 1=on, 2=off, 3=auto.
func (c *Client) SetNightVision(ctx context.Context, cam string, mode NightVisionMode) error {
	return c.postJSON(ctx, cam, "night_vision", map[string]byte{"mode": byte(mode)})
}

// SetSiren toggles the built-in alarm siren.
// K10630SetAlarmFlashing, payload `[v, v]` (value duplicated).
// 1=on, 2=off. On models with both a siren and a flashing status LED,
// this fires both simultaneously — there is no separate opcode for
// flash-only vs siren-only.
//
// Hardware siren on V3-family cameras is LOUD (~90-100dB). Wire this
// to reflexive automation only with a safety guard — the bridge's
// SIREN_ENABLED env var defaults to false to require explicit opt-in.
func (c *Client) SetSiren(ctx context.Context, cam string, on bool) error {
	return c.postJSON(ctx, cam, "siren", map[string]bool{"on": on})
}

// SetHorizontalFlip flips the image horizontally.
// K10052 variant, 6-byte payload with horizontal at index [4].
// 1=on, 2=off.
func (c *Client) SetHorizontalFlip(ctx context.Context, cam string, on bool) error {
	return c.postJSON(ctx, cam, "hor_flip", map[string]bool{"on": on})
}

// SetVerticalFlip flips the image vertically.
// K10052 variant, 6-byte payload with vertical at index [5].
// 1=on, 2=off. Combine both for 180° rotation.
func (c *Client) SetVerticalFlip(ctx context.Context, cam string, on bool) error {
	return c.postJSON(ctx, cam, "ver_flip", map[string]bool{"on": on})
}

// TakePhoto asks the camera to capture a photo to its local SD card.
// K10058TakePhoto, payload `[1]` (always). Silent no-op on cameras
// without an SD card. Doesn't return the photo — retrieval is a
// separate BOA-server flow.
func (c *Client) TakePhoto(ctx context.Context, cam string) error {
	return c.postJSON(ctx, cam, "take_photo", nil)
}

// RotationDirection is the discrete direction accepted by
// RotateByAction. The fork maps it onto K11002SetRotaryByAction's
// separate horizontal/vertical bytes (wyzecam tutk_protocol.py:995).
type RotationDirection byte

const (
	RotationStop  RotationDirection = 0
	RotationLeft  RotationDirection = 1
	RotationRight RotationDirection = 2
	RotationUp    RotationDirection = 3
	RotationDown  RotationDirection = 4
)

// ParseRotationDirection accepts "left"/"right"/"up"/"down"/"stop"
// (case-insensitive). ok=false on anything else.
func ParseRotationDirection(raw string) (RotationDirection, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stop":
		return RotationStop, true
	case "left":
		return RotationLeft, true
	case "right":
		return RotationRight, true
	case "up":
		return RotationUp, true
	case "down":
		return RotationDown, true
	}
	return 0, false
}

// RotateByAction starts the pan/tilt motor moving in a direction at
// speed 1-9 (0 = firmware default 5). The motor keeps going until it
// hits its travel limit or receives RotationStop, so press-and-hold
// UIs send the direction on press and RotationStop on release.
//
// Verified on HL_PAN3 firmware 4.50.17.25 via K11006 position readback.
func (c *Client) RotateByAction(ctx context.Context, cam string, dir RotationDirection, speed uint8) error {
	if dir > RotationDown {
		return fmt.Errorf("wyzectl: rotate_action direction %d outside [0, 4]", dir)
	}
	return c.postJSON(ctx, cam, "rotate_action", map[string]byte{
		"direction": byte(dir),
		"speed":     speed,
	})
}

// RotateByDegree moves the pan/tilt motor by relative degrees.
// K11000SetRotaryByDegree, payload `<hhB` = int16 h, int16 v, uint8 speed.
//
// Firmware quirk (#862): `v` values outside int8 range (-128..127) are
// rejected by the camera even though the wire format allocates int16.
// We enforce the tighter range client-side to fail fast.
//
// Note: HL_PAN3 firmware 4.50 accepts the command but barely moves
// (a few degrees regardless of h/v); prefer SetPTZPosition for
// precise moves on that model.
func (c *Client) RotateByDegree(ctx context.Context, cam string, hDeg, vDeg int16, speed uint8) error {
	if vDeg < -128 || vDeg > 127 {
		return fmt.Errorf("wyzectl: rotate_degree vDeg %d outside [-128, 127] (firmware limit, #862)", vDeg)
	}
	return c.postJSON(ctx, cam, "rotate_degree", map[string]interface{}{
		"h":     hDeg,
		"v":     vDeg,
		"speed": speed,
	})
}

// PTZPosition is the motor's absolute position as reported by
// K11006/K11007. On HL_PAN3, Horizontal is 0-359 (increasing = camera
// turns left) and Vertical is 0-180 (0 = straight up, increasing =
// tilting down). Older Pan models use a 0-40 vertical range.
type PTZPosition struct {
	Vertical   uint8  `json:"vertical"`
	Horizontal uint16 `json:"horizontal"`
}

// SetPTZPosition moves the pan/tilt motor to an absolute position.
// K11018SetPTZPosition, payload `<IBH` = uint32 timestamp-ms, uint8
// vertical, uint16 horizontal. The fork's HTTP handler injects the
// timestamp; callers supply vertical and horizontal.
//
// Bounds are the widest seen across Pan models (vertical 0-180 on
// HL_PAN3, 0-40 on earlier Pans; horizontal 0-359). The firmware
// clamps anything beyond its own mechanical range.
//
// Verified on HL_PAN3 firmware 4.50.17.25.
func (c *Client) SetPTZPosition(ctx context.Context, cam string, vertical uint8, horizontal uint16) error {
	if vertical > 180 {
		return fmt.Errorf("wyzectl: ptz_position vertical %d outside [0, 180]", vertical)
	}
	if horizontal > 359 {
		return fmt.Errorf("wyzectl: ptz_position horizontal %d outside [0, 359]", horizontal)
	}
	return c.postJSON(ctx, cam, "ptz_position", map[string]interface{}{
		"v": vertical,
		"h": horizontal,
	})
}

// GetPTZPosition reads the motor's current absolute position
// (GET /api/wyze/ptz_state, backed by K11006GetCurCruisePoint).
func (c *Client) GetPTZPosition(ctx context.Context, cam string) (PTZPosition, error) {
	var pos PTZPosition
	if cam == "" {
		return pos, errors.New("wyzectl: camera name required")
	}
	u := fmt.Sprintf("%s/api/wyze/ptz_state?src=%s", c.baseURL, url.QueryEscape(cam))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return pos, fmt.Errorf("wyzectl: build ptz_state: %w", err)
	}
	if c.authUsername != "" && c.authPassword != "" {
		req.SetBasicAuth(c.authUsername, c.authPassword)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return pos, fmt.Errorf("wyzectl: ptz_state %s: %w", cam, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if err := mapStatus(resp.StatusCode, body, "ptz_state", cam); err != nil {
		return pos, err
	}
	if err := json.Unmarshal(body, &pos); err != nil {
		return pos, fmt.Errorf("wyzectl: ptz_state %s: decode: %w", cam, err)
	}
	return pos, nil
}

// ResetRotation returns the motor to its calibrated home position.
// K11004ResetRotatePosition, payload `[position:1]` (3 in the wyzecam
// reference implementation). On HL_PAN3 this runs a full calibration
// sweep before settling, which takes ~20s.
func (c *Client) ResetRotation(ctx context.Context, cam string) error {
	return c.postJSON(ctx, cam, "reset_rotation", nil)
}

// postJSON marshals body (or sends empty), POSTs to
// {baseURL}/api/wyze/{command}?src={cam}, and maps the go2rtc response
// into the typed errors above. Callers should not use this directly;
// it's the common transport for every method above.
func (c *Client) postJSON(ctx context.Context, cam, command string, body any) error {
	if cam == "" {
		return errors.New("wyzectl: camera name required")
	}
	if command == "" {
		return errors.New("wyzectl: command required")
	}

	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("wyzectl: marshal %s: %w", command, err)
		}
		payload = b
	}

	u := fmt.Sprintf("%s/api/wyze/%s?src=%s",
		c.baseURL, command, url.QueryEscape(cam))

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("wyzectl: build %s: %w", command, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authUsername != "" && c.authPassword != "" {
		req.SetBasicAuth(c.authUsername, c.authPassword)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("wyzectl: %s %s: %w", command, cam, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if err := mapStatus(resp.StatusCode, respBody, command, cam); err != nil {
		return err
	}
	c.log.Debug().Str("cam", cam).Str("cmd", command).Msg("wyze control sent")
	return nil
}

// mapStatus turns a go2rtc control response into the typed errors above.
func mapStatus(status int, body []byte, command, cam string) error {
	switch status {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		// Two distinguishable causes for 404: unknown camera vs.
		// unknown endpoint. We look for "src" or "camera" in the
		// body — the fork's handler emits either when the stream
		// lookup fails; a bare "404 page not found" from net/http's
		// default mux (unknown endpoint) contains neither.
		if bytes.Contains(body, []byte("src")) || bytes.Contains(body, []byte("camera")) {
			return ErrCameraOffline
		}
		return ErrControlNotSupported
	default:
		return fmt.Errorf("wyzectl: %s %s: %d %s", command, cam, status, strings.TrimSpace(string(body)))
	}
}
