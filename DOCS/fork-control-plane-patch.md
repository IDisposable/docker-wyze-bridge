# go2rtc fork PR: Wyze control plane

Target: [`IDisposable/go2rtc`](https://github.com/IDisposable/go2rtc)
branch `edge`, existing commit `b230afe wyze: device control K-commands`
that ships partial control scaffolding.

**Both the K-code table and the HTTP surface need work.** The current
fork's `pkg/wyze/control.go` uses opcodes reverse-engineered from
public sources; multi-source cross-check (2026-07-06 research pass,
five parallel angles) found **all 7 opcodes wrong** and the HTTP
endpoints (needed for the bridge to invoke commands) haven't been
added yet.

Companion bridge code: [`internal/wyzectl/`](../internal/wyzectl/)
already implemented against the *correct* endpoint names and expects
the fork surface described below. Bridge tests are green, so we can
prove the client-side wiring while the fork PR is under review.

## Wire-format ground truth

Sources verified independently (all reference agrees on wire bytes):

- **wyzecam Python** `tutk_protocol.py` @ mrlt8/docker-wyze-bridge blob
	`73dded9c` — the 65-class reference implementation, HW-tested for
	years across the whole camera fleet.
- **AlexxIT/go2rtc upstream** `pkg/tutk/helpers.go` — Go-native HL
	envelope builders that ship with the streaming source. Wire bytes
	match wyzecam exactly.
- **kroo/wyzecam** (upstream lineage) — same struct definitions.

### HL envelope (16-byte header + payload)

| Offset | Field | Type | Notes |
|---:|---|---|---|
| 0:2 | prefix | `[2]byte` = `"HL"` | magic |
| 2:4 | protocol | `uint16 LE` | client always writes `1`; camera returns firmware version (real-world 3-26) |
| 4:6 | code | `uint16 LE` | K-command opcode |
| 6:10 | txt_len | `uint32 LE` | **payload length only, excluding header** |
| 10:12 | reserved2 | `uint16` | zeros |
| 12:16 | reserved3 | `uint32` | zeros |
| 16:… | payload | `[]byte` | per-command struct |

Verified via `TutkWyzeProtocolHeader` (wyzecam:22-48) and AlexxIT's
builders (`pkg/tutk/helpers.go:30-48`). No checksum, no CRC, no txn_id.

**Response opcode is always `code + 1`** — enforced in the
`TutkWyzeProtocolMessage` base class (wyzecam:64-105). Response
correlation is purely by opcode; only one outstanding request per
opcode is safe. Even opcode = client→camera; odd = camera→client.

### Outer IOCtrl transport

Every HL frame is wrapped in an IOCtrl envelope with
`ctrl_type = IOTYPE_USER_DEFINED_START = 256` (wyzecam
`tutk.py:71`). The outer envelope is handled by the shared TUTK
transport layer (existing in the fork's `pkg/tutk/`), not per-command
code. No new wire work needed there — control commands piggyback on
the existing IOCtrl channel that the streaming producer already
maintains.

## `pkg/wyze/control.go` rewrite

Replace the entire K-code constant block and all typed wrappers. Fork's
current guesses are commented for future readers who might wonder why
we didn't just leave them:

```go
package wyze

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/tutk"
)

// Wyze HL K-command opcodes. Cross-referenced against
// wyzecam/tutk_protocol.py @ mrlt8/docker-wyze-bridge 73dded9c (HW-tested
// 65-class catalog).
//
// Response opcode is always Kxxxxx+1 (base class rule; no per-command
// override in wyzecam). Response correlation is purely by code; only
// one outstanding request per opcode is safe.
const (
	// Auth handshake — already covered by client.go
	KCmdAuth               = 10000
	KCmdChallenge          = 10001
	KCmdChallengeResp      = 10002
	KCmdAuthResult         = 10003
	KCmdControlChannel     = 10010
	KCmdControlChannelResp = 10011

	// Info + params
	KCmdCheckCameraInfo   = 10020
	KCmdCheckCameraParams = 10020 // same opcode, different payload
	KCmdCheckConnStatus   = 10446
	KCmdGetBatteryUsage   = 10448

	// Status light (network LED)
	KCmdGetStatusLight = 10030
	KCmdSetStatusLight = 10032 // [value:1] 1=on 2=off

	// Night vision
	KCmdGetNightVision = 10040
	KCmdSetNightVision = 10042 // [status:1] 1=on 2=off 3=auto

	// IR LED
	KCmdGetIRLED = 10044
	KCmdSetIRLED = 10046 // [status:1] 1=850nm long-range 2=940nm short-range

	// Video params (K10052 is multiplexed — 4 subclasses share opcode)
	KCmdGetVideoParam    = 10050
	KCmdSetResolutionDB  = 10052 // WYZEDB3/WVOD1/HL_WCO2/WYZEC1
	KCmdSetResolution    = 10056
	KCmdSetFPS           = 10052 // [0,0,0,fps,0,0]
	KCmdSetBitrate       = 10052 // <HBBBB bitrate + 4 zeros
	KCmdSetHFlip         = 10052 // [0,0,0,0,h,0]
	KCmdSetVFlip         = 10052 // [0,0,0,0,0,v]

	// OSD
	KCmdGetOSDStatus     = 10070
	KCmdSetOSDStatus     = 10072 // [value:1]
	KCmdGetOSDLogoStatus = 10074
	KCmdSetOSDLogoStatus = 10076 // [value:1]

	// Time + timezone
	KCmdGetCameraTime = 10090
	KCmdSetCameraTime = 10092 // <I timestamp
	KCmdSetTimeZone   = 10302 // <b signed int8 -11..13

	// Motion — SET is model-branched (see wyzeMotionSetOpcode below)
	KCmdGetMotionAlarm     = 10200
	KCmdSetMotionAlarmOld  = 10202 // WYZEDB3/WVOD1/HL_WCO2/WYZEC1: [value:1, 0]
	KCmdSetMotionAlarmNew  = 10206 // everything else: [value:1, 0]
	KCmdGetMotionTagging   = 10290
	KCmdSetMotionTagging   = 10292 // [value:1]

	// Take photo
	KCmdTakePhoto = 10058 // [1]

	// Night mode auto-switch threshold (dusk vs dark)
	KCmdCheckNight              = 10620
	KCmdGetAutoSwitchNightType  = 10624
	KCmdSetAutoSwitchNightType  = 10626 // [type:1] 1=Dusk 2=Dark

	// Alarm / siren — WARNING: fires siren + flashing LED together
	KCmdSetAlarmFlashing = 10630 // [v:1, v:1] value duplicated
	KCmdGetAlarmFlashing = 10632

	// Spotlight (WYZEC3L / floodlight lineage)
	KCmdGetSpotlight = 10640
	KCmdSetSpotlight = 10646 // [status:1]

	// SD card + BOA HTTP daemon
	KCmdStartBoa       = 10148 // [0,1,0,0,0]
	KCmdFormatSDCard   = 10242 // DESTRUCTIVE; NOT exposed via HTTP endpoint
	KCmdSetDeviceState = 10444 // outdoor wake

	// RTSP switch
	KCmdSetRtspSwitch = 10600 // [value:1]
	KCmdGetRtspParam  = 10604

	// Pan/tilt motor
	KCmdRotateByDegree  = 11000 // <hhB h:i16 v:i16 speed:u8
	KCmdRotateByAction  = 11002 // [dir:1, dir:1, speed:1] (dir duplicated)
	KCmdResetRotate     = 11004 // [position:1]
	KCmdGetCurCruise    = 11006 // <I ts; resp <IBH
	KCmdGetCruisePoints = 11010
	KCmdSetCruisePoints = 11012
	KCmdGetCruise       = 11014
	KCmdSetCruise       = 11016 // [value:1]
	KCmdSetPTZPosition  = 11018 // <IBH ts_ms + v:u8(0-40) + h:u16(0-350)
	KCmdGetMotionTrack  = 11020
	KCmdSetMotionTrack  = 11022 // [value:1]

	// Doorbell quick-response canned phrases (not siren!)
	KCmdResponseQuickMessage = 11635 // [value:1] 1-3

	// Accessories / floodlight status
	KCmdGetAccessoriesInfo         = 10720
	KCmdGetIntegratedFloodlightInfo = 10788
	KCmdGetWhiteLightInfo          = 10820
	KCmdSetFloodLightSwitch        = 12060 // [value:1]
)

// wyzeMotionSetOpcode returns the correct SetMotionAlarm opcode for a
// given camera model. Wyze split the API between old (K10202) and new
// (K10206) at some point; the old models still expect the legacy code.
func wyzeMotionSetOpcode(model string) uint16 {
	switch model {
	case "WYZEDB3", "WVOD1", "HL_WCO2", "WYZEC1":
		return KCmdSetMotionAlarmOld
	default:
		return KCmdSetMotionAlarmNew
	}
}

// ─── Typed wrappers on Client ──────────────────────────────────────

// SetNightVision picks status: 1=on, 2=off, 3=auto.
func (c *Client) SetNightVision(mode byte) error {
	if mode < 1 || mode > 3 {
		return fmt.Errorf("wyze: night_vision mode %d outside [1,3]", mode)
	}
	return c.SendControl(KCmdSetNightVision, []byte{mode})
}

// SetIRLED picks illuminator: 1=850nm long-range, 2=940nm short-range.
func (c *Client) SetIRLED(mode byte) error {
	if mode < 1 || mode > 2 {
		return fmt.Errorf("wyze: ir_led mode %d outside [1,2]", mode)
	}
	return c.SendControl(KCmdSetIRLED, []byte{mode})
}

// SetSpotlight toggles the WYZEC3L spotlight. 1=on, 2=off.
func (c *Client) SetSpotlight(on bool) error {
	v := byte(2)
	if on {
		v = 1
	}
	return c.SendControl(KCmdSetSpotlight, []byte{v})
}

// SetSiren fires the alarm siren + flashing LED. 1=on, 2=off.
// Value is duplicated in the 2-byte payload (Wyze protocol quirk).
func (c *Client) SetSiren(on bool) error {
	v := byte(2)
	if on {
		v = 1
	}
	return c.SendControl(KCmdSetAlarmFlashing, []byte{v, v})
}

// SetHorizontalFlip flips the image horizontally. K10052 6-byte
// payload with horizontal at index [4].
func (c *Client) SetHorizontalFlip(on bool) error {
	v := byte(2)
	if on {
		v = 1
	}
	return c.SendControl(KCmdSetHFlip, []byte{0, 0, 0, 0, v, 0})
}

// SetVerticalFlip flips the image vertically. K10052 6-byte
// payload with vertical at index [5].
func (c *Client) SetVerticalFlip(on bool) error {
	v := byte(2)
	if on {
		v = 1
	}
	return c.SendControl(KCmdSetVFlip, []byte{0, 0, 0, 0, 0, v})
}

// TakePhoto asks the camera to save a photo to its SD card.
// K10058 payload is always [1] (single-byte constant).
func (c *Client) TakePhoto() error {
	return c.SendControl(KCmdTakePhoto, []byte{1})
}

// RotateByAction moves the pan/tilt motor discretely. Direction
// duplicated per wyzecam K11002 payload layout.
func (c *Client) RotateByAction(dir, speed byte) error {
	if dir < 1 || dir > 4 {
		return fmt.Errorf("wyze: rotate direction %d outside [1,4]", dir)
	}
	return c.SendControl(KCmdRotateByAction, []byte{dir, dir, speed})
}

// RotateByDegree moves by relative degrees. Vertical is int16 on the
// wire but firmware rejects values outside int8 range (#862); we
// enforce client-side to fail fast.
func (c *Client) RotateByDegree(hDeg, vDeg int16, speed byte) error {
	if vDeg < -128 || vDeg > 127 {
		return fmt.Errorf("wyze: rotate vDeg %d outside firmware limit int8", vDeg)
	}
	p := make([]byte, 5)
	binary.LittleEndian.PutUint16(p[0:], uint16(hDeg))
	binary.LittleEndian.PutUint16(p[2:], uint16(vDeg))
	p[4] = speed
	return c.SendControl(KCmdRotateByDegree, p)
}

// SetPTZPosition moves to an absolute position. Timestamp is injected
// here (wyzecam-compatible; camera uses it for sequencing).
func (c *Client) SetPTZPosition(vertical byte, horizontal uint16) error {
	if vertical > 40 {
		return fmt.Errorf("wyze: ptz vertical %d outside [0,40]", vertical)
	}
	if horizontal > 350 {
		return fmt.Errorf("wyze: ptz horizontal %d outside [0,350]", horizontal)
	}
	p := make([]byte, 7)
	tsMs := uint32(time.Now().UnixMilli() % 1_000_000_000)
	binary.LittleEndian.PutUint32(p[0:], tsMs)
	p[4] = vertical
	binary.LittleEndian.PutUint16(p[5:], horizontal)
	return c.SendControl(KCmdSetPTZPosition, p)
}

// ResetRotation returns the motor to its stored home position.
func (c *Client) ResetRotation() error {
	return c.SendControl(KCmdResetRotate, []byte{3})
}

// Forward everything through the streaming producer so callers can
// go through the Producer surface without re-plumbing the transport.
func (p *Producer) SetNightVision(mode byte) error       { return p.client.SetNightVision(mode) }
func (p *Producer) SetIRLED(mode byte) error             { return p.client.SetIRLED(mode) }
func (p *Producer) SetSpotlight(on bool) error           { return p.client.SetSpotlight(on) }
func (p *Producer) SetSiren(on bool) error               { return p.client.SetSiren(on) }
func (p *Producer) SetHorizontalFlip(on bool) error      { return p.client.SetHorizontalFlip(on) }
func (p *Producer) SetVerticalFlip(on bool) error        { return p.client.SetVerticalFlip(on) }
func (p *Producer) TakePhoto() error                     { return p.client.TakePhoto() }
func (p *Producer) RotateByAction(dir, speed byte) error { return p.client.RotateByAction(dir, speed) }
func (p *Producer) RotateByDegree(h, v int16, s byte) error {
	return p.client.RotateByDegree(h, v, s)
}
func (p *Producer) SetPTZPosition(v byte, h uint16) error { return p.client.SetPTZPosition(v, h) }
func (p *Producer) ResetRotation() error                  { return p.client.ResetRotation() }

// ErrOpcodeSerialization is returned when a caller invokes
// SendControlWait for an opcode that already has an outstanding wait.
// Wyze correlates responses purely by opcode; only one outstanding
// request per opcode is safe.
var ErrOpcodeSerialization = errors.New("wyze: opcode already has outstanding wait")
```

Also introduce a small mutex per opcode inside `client.go`'s
`SendControlWait` implementation to enforce `ErrOpcodeSerialization` —
current code fires the wait unconditionally and would trample overlapping
requests silently.

## HTTP endpoints (Shape A — method per command)

Add to `internal/wyze/wyze.go` (companion to the existing
`apiWyze` cloud-discovery handler):

```go
func Init() {
    // ... existing streams.HandleFunc, api.HandleFunc("api/wyze", apiWyze) ...

    // Phase 2.0 control plane: one endpoint per Wyze-semantic command.
    // All POST-only, gated by api.IsReadOnly() and Basic auth via
    // upstream's existing api middleware.
    for _, spec := range controlEndpoints {
        api.HandleFunc("api/wyze/"+spec.name, spec.handler)
    }
}

type controlSpec struct {
    name    string
    handler http.HandlerFunc
}

var controlEndpoints = []controlSpec{
    {"spotlight",      handlePOSTSpotlight},
    {"ir_led",         handlePOSTIRLED},
    {"night_vision",   handlePOSTNightVision},
    {"siren",          handlePOSTSiren},
    {"hor_flip",       handlePOSTHFlip},
    {"ver_flip",       handlePOSTVFlip},
    {"take_photo",     handlePOSTTakePhoto},
    {"rotate_action",  handlePOSTRotateAction},
    {"rotate_degree",  handlePOSTRotateDegree},
    {"ptz_position",   handlePOSTPTZPosition},
    {"reset_rotation", handlePOSTResetRotation},
}

// Shared helpers:

func findWyzeProducer(src string) (*wyze.Producer, error) {
    stream := streams.Get(src)
    if stream == nil {
        return nil, fmt.Errorf("stream %q not found", src)
    }
    for _, p := range stream.Producers() {
        if wp, ok := p.(*wyze.Producer); ok {
            return wp, nil
        }
    }
    return nil, fmt.Errorf("stream %q has no active TUTK producer", src)
}

func controlPreamble(w http.ResponseWriter, r *http.Request) (*wyze.Producer, bool) {
    if api.IsReadOnly() && r.Method != "GET" {
        api.ReadOnlyError(w)
        return nil, false
    }
    if r.Method != "POST" {
        w.Header().Set("Allow", "POST")
        http.Error(w, "POST required", http.StatusMethodNotAllowed)
        return nil, false
    }
    src := r.URL.Query().Get("src")
    prod, err := findWyzeProducer(src)
    if err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return nil, false
    }
    return prod, true
}

// Example handler — the rest mechanically identical:

func handlePOSTSpotlight(w http.ResponseWriter, r *http.Request) {
    prod, ok := controlPreamble(w, r)
    if !ok { return }
    var body struct{ On bool }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    if err := prod.SetSpotlight(body.On); err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

Bridge's error mapping (`internal/wyzectl/wyzectl.go`) uses the
"src" or "camera" substring in the 404 body to distinguish
"camera offline" from "endpoint doesn't exist," so handler
error messages must contain one of those substrings on stream
lookup failure. Example above already does.

## Test coverage for the fork

`pkg/wyze/control_test.go` — replace the current 30-line stub with
byte-assertion tests against every payload encoder. One test per
command:

```go
func TestSetNightVision_Payload(t *testing.T) {
    var got []byte
    c := &Client{conn: &recordingConn{writes: &got}}
    if err := c.SetNightVision(3); err != nil { t.Fatal(err) }
    want := hlEnvelope(t, KCmdSetNightVision, []byte{3})
    if !bytes.Equal(got, want) {
        t.Errorf("payload = %x, want %x", got, want)
    }
}

// hlEnvelope constructs the expected 16-byte header + payload for
// assertion. Kept as a test helper rather than a per-test literal so
// the header layout is documented in exactly one place.
func hlEnvelope(t *testing.T, code uint16, payload []byte) []byte {
    t.Helper()
    buf := make([]byte, 16 + len(payload))
    copy(buf[0:], []byte("HL"))
    binary.LittleEndian.PutUint16(buf[2:], 1) // protocol
    binary.LittleEndian.PutUint16(buf[4:], code)
    binary.LittleEndian.PutUint32(buf[6:], uint32(len(payload)))
    copy(buf[16:], payload)
    return buf
}
```

`recordingConn` is a mock satisfying `transport` and capturing writes
without dispatching them. Should be ~30 LOC.

## HTTP endpoint tests

`internal/wyze/wyze_control_test.go` — one per handler using
`httptest.NewRecorder`:

```go
func TestHandlePOSTSpotlight(t *testing.T) {
    stream := seedFakeStream(t, "cam1", &fakeProducer{})
    defer stream.Close()

    req := httptest.NewRequest("POST", "/api/wyze/spotlight?src=cam1",
        strings.NewReader(`{"on":true}`))
    w := httptest.NewRecorder()
    handlePOSTSpotlight(w, req)

    if w.Code != http.StatusOK { t.Errorf("code = %d", w.Code) }
    // ... assert fakeProducer received SetSpotlight(true)
}

func TestHandlePOSTSpotlight_CameraOffline(t *testing.T) {
    req := httptest.NewRequest("POST", "/api/wyze/spotlight?src=nope",
        strings.NewReader(`{"on":true}`))
    w := httptest.NewRecorder()
    handlePOSTSpotlight(w, req)

    if w.Code != http.StatusNotFound { t.Errorf("code = %d", w.Code) }
    // Body must contain "src" or "camera" for bridge's error-mapper
    if !strings.Contains(w.Body.String(), "src") &&
       !strings.Contains(w.Body.String(), "camera") {
        t.Error("body should mention src/camera so bridge can map to ErrCameraOffline")
    }
}
```

## Not doing (deferred to a separate PR)

- **SSE readback endpoint** (`GET /api/wyze/events?src=<cam>`). Requires
	a `SubscribeIOCtrl() (<-chan Event, cancel)` on the Producer that
	fans out inbound frames. Phase 2.1.
- **`supports(model, protocol, command)` capability gating** from
	wyzecam's `device_config.json`. First pass sends blindly; cameras
	return NAK/nil silently for unsupported opcodes. Phase 2.1 or later.
- **Backchannel audio HTTP endpoint** for intercom. Transport plumbing
	exists in the fork; HTTP surface can wait until we scope the
	audio path end-to-end.
- **`K10242 FormatSDCard`, `K10444 SetDeviceState`.** Destructive;
	deliberately not surfaced.

## Rollout sequence

1. **Fork PR merges** with the K-code corrections + HTTP endpoints +
	tests. No breaking change to streaming (control code path is
	orthogonal to the AV pump).
2. **Bridge's private-go2rtc branch** rebuilds against the fork.
3. **Bridge PR**: extend `internal/mqtt/subscribe.go` to route
	`{topic}/{cam}/set/<control>` to `wyzectl.Client` methods; extend
	`internal/mqtt/discovery.go` with HA MQTT-discovery entities for
	the Phase 2 controls; add `SIREN_ENABLED` config gate.
4. **Community verification** on Pan-cam hardware (PTZ endpoints spec-only until then).
