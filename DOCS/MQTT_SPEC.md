# MQTT Specification

This document defines the full MQTT topic surface for the Wyze Bridge, covering both
published state (reporting) and subscribed commands (control). It is the reference
for the Go implementation and for Home Assistant MQTT discovery.

## Phasing and architecture constraints

This spec includes the long-term target MQTT surface from the Python bridge and the
current Go rewrite implementation plan.

- Phase 1 goal: ship a stable MQTT baseline using bridge-owned controls and Wyze cloud
	API calls only.
- Constraint: go2rtc owns the live TUTK session for most cameras, and go2rtc does not
	expose a property-control API for Wyze (K10xxx/K11xxx IoT commands).
- Consequence: Phase 1 can support stream controls and cloud-backed property writes, but
	cannot provide full live property readback parity with Python's direct TUTK control loop.

Legend used in this document:

- `Phase 1` = planned and supported in first implementation pass.
- `Deferred` = intentionally out of scope for Phase 1.
- `Write-only` = accepted command and mirrored state publication, but not authoritative
	live GET from camera.

---

## Topic conventions

| Placeholder | Value |
|---|---|
| `{topic}` | `MQTT_TOPIC` env var, default `wyzebridge` |
| `{dtopic}` | `MQTT_DISCOVERY` env var, default `homeassistant` |
| `{cam}` | Camera URI name (lowercase, spaces → underscores) |

---

## Bridge-level topics

| Topic | Direction | Payload | Notes |
|---|---|---|---|
| `{topic}/bridge/state` | Publish | `online` / `offline` | LWT=`offline`, retained |
| `{topic}/bridge/uptime_s` | Publish | integer seconds | Metrics tick (30s) |
| `{topic}/bridge/camera_count` | Publish | integer | Metrics tick |
| `{topic}/bridge/streaming_count` | Publish | integer | Metrics tick |
| `{topic}/bridge/error_count` | Publish | integer | Metrics tick |
| `{topic}/bridge/issue_count` | Publish | integer | Metrics tick |
| `{topic}/bridge/recordings_bytes` | Publish | integer bytes | Metrics tick |
| `{topic}/bridge/discover/set` | Subscribe | any | Trigger bridge-wide rediscovery |

---

## Per-camera state topics (Publish)

All retained unless noted.

| Topic | Payload | HA entity type | Notes |
|---|---|---|---|
| `{topic}/{cam}/state` | `connected` / `disconnected` | availability | Core streaming state |
| `{topic}/{cam}/power` | `on` / `off` | switch | Published with state; `connected`→`on` |
| `{topic}/{cam}/quality` | `hd` / `sd` | select | Configurable via `set` |
| `{topic}/{cam}/audio` | `true` / `false` | switch | |
| `{topic}/{cam}/night_vision` | `auto` / `on` / `off` | select | Mapped from PID P3: `3`/`1`/`2` |
| `{topic}/{cam}/net_mode` | `lan` / `p2p` | sensor | |
| `{topic}/{cam}/irled` | `on` / `off` | switch | PID P50; `1`=on, `2`=off |
| `{topic}/{cam}/status_light` | `on` / `off` | switch | PID P1; `1`=on, `2`=off |
| `{topic}/{cam}/motion` | `1` / `2` | binary_sensor | `1`=motion, `2`=clear |
| `{topic}/{cam}/motion_detection` | `1` / `2` | switch | PID P13; `1`=on, `2`=off |
| `{topic}/{cam}/notifications` | `1` / `2` | switch | `1`=on, `2`=off |
| `{topic}/{cam}/motion_tagging` | `1` / `2` | switch | PID P21; `1`=on, `2`=off |
| `{topic}/{cam}/bitrate` | integer (kbps) | number | PID P3; range 1–1000 |
| `{topic}/{cam}/fps` | integer | number | PID P5; range 1–30 |
| `{topic}/{cam}/res` | string (e.g. `1080p`) | sensor | PID P4 |
| `{topic}/{cam}/wifi` | integer (dBm or %) | sensor | PID P50 (wifi signal) |
| `{topic}/{cam}/hor_flip` | `1` / `2` | switch | PID P6; `1`=on, `2`=off |
| `{topic}/{cam}/ver_flip` | `1` / `2` | switch | PID P7; `1`=on, `2`=off |
| `{topic}/{cam}/alarm` | `1` / `2` | siren | `1`=on, `2`=off |
| `{topic}/{cam}/recording` | `ON` / `OFF` | switch | Bridge-managed recording |
| `{topic}/{cam}/camera_info` | JSON object | — | `{ip, model, fw_version, mac}` |
| `{topic}/{cam}/stream_info` | JSON object | — | `{rtsp_url, webrtc_url, hls_url}` |
| `{topic}/{cam}/thumbnail` | binary JPEG | camera | From snapshot manager |

### Pan-cam additional topics (IsPanCam)

| Topic | Payload | HA entity type | Notes |
|---|---|---|---|
| `{topic}/{cam}/pan_cruise` | `1` / `2` | switch | `1`=on, `2`=off |
| `{topic}/{cam}/motion_tracking` | `1` / `2` | switch | `1`=on, `2`=off |
| `{topic}/{cam}/cruise_point` | `1`–`4` or `-` | select | Current cruise point |

---

## Per-camera command topics (Subscribe)

### SET commands — `{topic}/{cam}/set/{property}`

| Property | Accepted values | Action |
|---|---|---|
| `quality` | `hd` / `sd` | `camera.Manager.SetQuality` + reconnect |
| `audio` | `true` / `false` | `camera.SetAudioOn` |
| `night_vision` | `auto` / `on` / `off` | `wyzeapi.SetProperty(P3, 3/1/2)` |
| `irled` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P50, 1/2)` via K10046 |
| `status_light` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P1, 1/2)` via K10032 |
| `motion_detection` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P13, 1/2)` |
| `notifications` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(…)` |
| `motion_tagging` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P21, 1/2)` via K10292 |
| `alarm` | `on` / `off` / `1` / `2` | K10630SetAlarmFlashing |
| `bitrate` | integer string | `wyzeapi.SetProperty(P3, value)` |
| `fps` | integer string | `wyzeapi.SetProperty(P5, value)` |
| `hor_flip` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P6, 1/2)` via K10052 |
| `ver_flip` | `on` / `off` / `1` / `2` | `wyzeapi.SetProperty(P7, 1/2)` via K10052 |

### Dedicated command topics

| Topic | Payload | Action |
|---|---|---|
| `{topic}/{cam}/state/set` | `start` / `stop` | Start or disable camera stream |
| `{topic}/{cam}/power/set` | `on` / `off` / `restart` | Power on/off or reboot via `wyzeapi.RunAction` |
| `{topic}/{cam}/snapshot/take` | any | Trigger snapshot |
| `{topic}/{cam}/stream/restart` | any | `camera.Manager.RestartStream` |
| `{topic}/{cam}/record/set` | `start`/`ON`/`1`/`true` → start, else stop | Toggle bridge recording |

### GET commands — `{topic}/{cam}/{property}/get`

Sends a GET query to the camera and publishes the result to the corresponding state topic.

| Property | Wyze command | Notes |
|---|---|---|
| `state` | — | Re-publish current state |
| `power` | — | Re-publish current power state |
| `irled` | K10044GetIRLEDStatus | |
| `night_vision` | K10040GetNightVisionStatus | |
| `status_light` | K10030GetNetworkLightStatus | |
| `motion_detection` | K10200GetMotionAlarm | |
| `motion_tagging` | K10290GetMotionTagging | |
| `notifications` | — | |
| `alarm` | K10632GetAlarmFlashing | |
| `camera_info` | K10020CheckCameraInfo | Re-publishes camera_info JSON |
| `update_snapshot` | — | Trigger snapshot refresh |
| `param_info` | K10020CheckCameraParams | Bulk param fetch; payload = param list |

### Pan-cam command topics

| Topic | Payload | Action |
|---|---|---|
| `{topic}/{cam}/set/pan_cruise` | `on`/`off`/`1`/`2` | K11016SetCruise |
| `{topic}/{cam}/set/motion_tracking` | `on`/`off`/`1`/`2` | K11022SetMotionTracking |
| `{topic}/{cam}/set/rotary_degree` | `up`/`down`/`left`/`right` or `(x,y)` | K11000SetRotaryByDegree |
| `{topic}/{cam}/set/rotary_action` | action string | K11002SetRotaryByAction |
| `{topic}/{cam}/set/reset_rotation` | any | K11004ResetRotatePosition |
| `{topic}/{cam}/set/cruise_point` | `1`–`4` | K11012SetCruisePoints or K11018SetPTZPosition |
| `{topic}/{cam}/set/ptz_position` | position | K11018SetPTZPosition |

---

## Home Assistant MQTT Discovery entities

All entities use availability from `{topic}/{cam}/state` (connected/disconnected) unless noted.

### All cameras

| Entity key | Component | State topic | Command topic | Notes |
|---|---|---|---|---|
| `snapshot` | `camera` | `{base}image` (thumbnail) | — | availability via `{base}state`, payloads `connected`/`stopped` |
| `stream` | `switch` | `{base}state` | `{base}state/set` | `payload_on=start`, `state_on=connected`, `payload_off=stop`, `state_off=disconnected` |
| `power` | `switch` | `{base}power` | `{base}power/set` | `payload_on=on`, `payload_off=off` |
| `reboot` | `button` | — | `{base}power/set` | `payload_press=restart` |
| `update_snapshot` | `button` | — | `{base}update_snapshot/get` | |
| `quality` | `select` | `{base}quality` | `{base}set/quality` | options: `hd`, `sd` |
| `audio` | `switch` | `{base}audio` | `{base}set/audio` | `payload_on=true`, `payload_off=false` |
| `night_vision` | `select` | `{base}night_vision` | `{base}set/night_vision` | options: `auto`, `on`, `off` |
| `ir` | `switch` | `{base}irled` | `{base}set/irled` | `payload_on=1`, `payload_off=2` |
| `status_light` | `switch` | `{base}status_light` | `{base}set/status_light` | `payload_on=1`, `payload_off=2` |
| `alarm` | `siren` | `{base}alarm` | `{base}set/alarm` | `payload_on=1`, `payload_off=2` |
| `motion` | `binary_sensor` | `{base}motion` | — | `payload_on=1`, `payload_off=2` |
| `motion_detection` | `switch` | `{base}motion_detection` | `{base}set/motion_detection` | `payload_on=1`, `payload_off=2` |
| `notifications` | `switch` | `{base}notifications` | `{base}set/notifications` | `payload_on=1`, `payload_off=2` |
| `motion_tagging` | `switch` | `{base}motion_tagging` | `{base}set/motion_tagging` | `payload_on=1`, `payload_off=2` |
| `bitrate` | `number` | `{base}bitrate` | `{base}set/bitrate` | min=1, max=1000, device_class=data_rate |
| `fps` | `number` | `{base}fps` | `{base}set/fps` | min=1, max=30 |
| `flip_horizontal` | `switch` | `{base}hor_flip` | `{base}set/hor_flip` | `payload_on=1`, `payload_off=2` |
| `flip_vertical` | `switch` | `{base}ver_flip` | `{base}set/ver_flip` | `payload_on=1`, `payload_off=2` |
| `res` | `sensor` | `{base}res` | — | diagnostic |
| `signal` | `sensor` | `{base}wifi` | — | diagnostic |
| `recording` | `switch` | `{base}recording` | `{base}record/set` | Bridge-managed; `payload_on=start`, `state_on=ON` |

### Pan-cam only (`IsPanCam`)

| Entity key | Component | State topic | Command topic |
|---|---|---|---|
| `pan_cruise` | `switch` | `{base}pan_cruise` | `{base}set/pan_cruise` |
| `motion_tracking` | `switch` | `{base}motion_tracking` | `{base}set/motion_tracking` |
| `reset_rotation` | `button` | — | `{base}set/reset_rotation` |
| `cruise_point` | `select` | `{base}cruise_point` | `{base}set/cruise_point` |
| `pan_tilt` | `cover` | — | `{base}set/rotary_degree` + tilt |

---

## PID / command reference

All opcodes cross-referenced against wyzecam Python
`tutk_protocol.py` @ mrlt8/docker-wyze-bridge blob 73dded9c
(65-class catalog, HW-tested against production hardware for years).

Response opcode is **always request + 1** — encoded in the `TutkWyzeProtocolMessage`
base class. Verified across 9 request/response pairs in the source.

| Property | PID | GET command | SET command | SET payload | Notes |
|---|---|---|---|---|---|
| status_light | P1 | K10030 GetNetworkLightStatus | K10032 SetNetworkLightStatus | `[value:1]` (1=on, 2=off) | |
| night_vision | P2 / P3 | K10040 GetNightVisionStatus | K10042 SetNightVisionStatus | `[status:1]` (1=on, 2=off, **3=auto**) | |
| bitrate | P3 | K10050 GetVideoParam | K10052 SetBitrate | `<HBBBB` = `bitrate:u16 LE + 4 zero bytes` | K10050 for GET only on FW 4.51+ |
| res | P4 | — | K10056 SetResolvingBit (WYZEDB3 uses K10052 variant) | multi-field | |
| fps | P5 | — | K10052 SetFPS | `[0,0,0,fps,0,0]` (6 bytes; fps at [3]) | |
| hor_flip | P6 | — | K10052 HorizontalFlip | `[0,0,0,0,h,0]` (6 bytes; horizontal at [4]) | Shares opcode 10052 with FPS/bitrate/ver_flip |
| ver_flip | P7 | — | K10052 VerticalFlip | `[0,0,0,0,0,v]` (6 bytes; vertical at [5]) | Shares opcode 10052 |
| motion_detection | P13 | K10200 GetMotionAlarm | K10202 (WYZEDB3/WVOD1/HL_WCO2/WYZEC1) or K10206 SetMotionAlarm | `[value:1, 0]` | Model-branched SET |
| motion_tagging | P21 | K10290 GetMotionTagging | K10292 SetMotionTagging | `[value:1]` | |
| irled | P50 | K10044 GetIRLEDStatus | K10046 SetIRLEDStatus | `[status:1]` (1=850nm long-range/on, 2=940nm short-range/off) | |
| alarm (siren) | — | K10632 GetAlarmFlashing | K10630 SetAlarmFlashing | `[value:1, value:1]` (**value duplicated, 2 bytes**) | Fires both siren AND flashing LED — no separate opcode |
| spotlight | — | K10640 GetSpotlightStatus | K10646 SetSpotlightStatus | `[status:1]` (1=on, 2=off) | WYZEC3L / floodlight lineage only |
| take_photo | — | — | K10058 TakePhoto | `[1]` (always) | To SD card; separate BOA retrieval |
| pan_cruise | — | K11014 GetCruise | K11016 SetCruise | `[value:1]` | |
| motion_tracking (pan) | — | K11020 GetMotionTracking | K11022 SetMotionTracking | `[value:1]` | |
| rotate_degree | — | — | K11000 SetRotaryByDegree | `<hhB` = `h:i16 LE, v:i16 LE, speed:u8` | Vertical clamped to int8 range by firmware (#862) |
| rotate_action | — | — | K11002 SetRotaryByAction | `[direction:1, direction:1, speed:1]` (3 bytes, direction duplicated) | 1=L, 2=R, 3=U, 4=D |
| reset_rotation | — | — | K11004 ResetRotatePosition | `[position:1]` (default 3) | |
| ptz_position | — | — | K11018 SetPTZPosition | `<IBH` = `timestamp_ms:u32 LE, v:u8 (0-40), h:u16 LE (0-350)` | **Absolute** position; timestamp injected by handler |
| get_cruise_point | — | K11006 GetCurCruisePoint | — | resp: `<IBH` | |
| cruise_points | — | K11010 GetCruisePoints | K11012 SetCruisePoints | multi-field | |
| osd_text | — | K10070 GetOSDStatus | K10072 SetOSDStatus | `[value:1]` | Camera name overlay |
| osd_logo | — | K10074 GetOSDLogoStatus | K10076 SetOSDLogoStatus | `[value:1]` | Wyze logo overlay |
| camera_time | — | K10090 GetCameraTime | K10092 SetCameraTime | `<I` = `timestamp:u32 LE` | |
| timezone | — | — | K10302 SetTimeZone | `<b` = `tz:i8 (-11..13)` | |
| rtsp_switch | — | K10604 GetRtspParam | K10600 SetRtspSwitch | `[value:1]` | Turn cam into RTSP-firmware mode |
| device_state | — | — | K10444 SetDeviceState | `[value:1]` | Outdoor Cam wake |
| battery | — | K10448 GetBatteryUsage | — | resp: JSON | Battery-powered models |
| format_sd | — | — | K10242 FormatSDCard | asserts `value==1` | **DESTRUCTIVE — not exposed via MQTT** |

### Not implemented via TUTK

- **`restart` / `reboot`** — there is no K-code for camera reboot in wyzecam.
	Bridge uses Wyze cloud API `wyzeapi.RunAction(cam, "restart")` instead.

### Fork-guess corrections (2026-07-06 research)

Our private go2rtc fork's `pkg/wyze/control.go` initially shipped with 7
opcodes reverse-engineered from public sources. Cross-referenced against
wyzecam Python (5 parallel research angles), **all 7 were wrong**:

| Fork guess (wrong) | What that opcode actually does | Correct opcode |
|---|---|---|
| SetSpotlight = 11000 | K11000 = PTZ rotate-by-degree | **K10646** |
| SetNightVision = 10626 | K10626 = dusk/dark auto-switch threshold | **K10042** |
| SetIRLED = 10646 | K10646 = spotlight | **K10046** |
| SetSiren = 11635 | K11635 = doorbell quick-response canned phrases | **K10630** |
| SetFlip = 10058 (1-byte per axis) | K10058 = TakePhoto | **K10052** (6-byte, index 4/5) |
| PTZ = 11018 (2×i16) | K11018 payload is `<IBH` not `<hh` | **K11018** (`<IBH`) |
| Restart = 10004 (empty) | No such opcode exists | none (use cloud API) |

Fork PR to correct all 7 tracked in `DOCS/fork-control-plane-patch.md`.

---

## Implementation status

| Area | Status |
|---|---|
| Bridge state LWT | Implemented |
| Bridge metrics | Implemented |
| Camera state (connected/disconnected) | Implemented |
| `quality`, `audio`, `net_mode` publish | Implemented |
| `camera_info`, `stream_info` JSON | Implemented |
| `thumbnail` binary | Implemented |
| `recording` state | Implemented |
| HA discovery: camera, quality, audio, night_vision | Implemented |
| `quality/set`, `audio/set`, `night_vision/set` | Implemented |
| Snapshot trigger | Implemented |
| Stream restart | Implemented |
| Record start/stop | Implemented |
| Bridge rediscovery | Implemented |
| `power` state + `power/set` (on/off/restart) | Phase 1 |
| `state/set` start/stop stream | Phase 1 |
| SET: irled, status_light, motion_detection, motion_tagging, bitrate, fps, hor_flip, ver_flip | Phase 1 (Write-only) |
| Property publish mirror for the above SET commands | Phase 1 (Write-only) |
| HA discovery: stream, power, reboot, update_snapshot, ir, status_light, motion_detection, motion_tagging, bitrate, fps, flip_h/v, recording | Phase 1 |
| Notifications (`set/notifications`, discovery entity) | Deferred |

## Phase 2: TUTK control-plane (in progress, targets 4.7)

The private go2rtc fork's `pkg/wyze/` exposes IOCtrl transport +
HL K-command encoders. Fork PR (see `DOCS/fork-control-plane-patch.md`)
adds HTTP endpoints so the bridge can send commands via
`internal/wyzectl/` — one endpoint per Wyze-semantic command
(Shape A). Send-only for Phase 2.0; property readback (SSE
subscription) deferred to Phase 2.1.

Scoping: TUTK cameras only. Doorbell / OG / floodlight-pro
route through mars-webcsrv WebRTC and use the Wyze cloud API
for control (unchanged from Phase 1).

Confidence rating per command family:

- `[HW-verified: V3]` — testable on the maintainer's V3 fleet
- `[Spec-only]` — opcode/payload verified against wyzecam, but
	no hardware in the test fleet. First community reporter to
	confirm gets a mention.

| Command | Wyze opcode | Confidence | Notes |
|---|---|---|---|
| SET `night_vision` (on/off/auto) | K10042 | HW-verified: V3 | 1/2/3 |
| SET `ir_led` (on/off) | K10046 | HW-verified: V3 | 1/2 (850nm / 940nm) |
| SET `hor_flip` (on/off) | K10052 (h at [4]) | HW-verified: V3 | |
| SET `ver_flip` (on/off) | K10052 (v at [5]) | HW-verified: V3 | |
| SET `spotlight` (on/off) | K10646 | HW-verified: WYZEC3L | Silent no-op on plain V3 |
| SET `siren` (on/off) | K10630 | HW-verified: V3 | See safety note below |
| CMD `take_photo` | K10058 | HW-verified: V3 | |
| SET `pan_cruise` (on/off) | K11016 | Spec-only | Pan-cam only |
| SET `motion_tracking` (on/off) | K11022 | Spec-only | Pan-cam only |
| CMD `rotate_action` (L/R/U/D + speed) | K11002 | Spec-only | Pan-cam only |
| CMD `rotate_degree` (h/v deg + speed) | K11000 | Spec-only | vDeg clamped to int8 range (#862) |
| CMD `ptz_position` (v 0-40, h 0-350) | K11018 | Spec-only | |
| CMD `reset_rotation` | K11004 | Spec-only | |

### Safety notes

**Siren** (K10630) fires the hardware siren (~90-100dB on V3-family)
AND the flashing status LED simultaneously — there is no separate
opcode for either. New env var `SIREN_ENABLED=false` (default) gates
the entire `set/alarm` subscription so a stray MQTT publish can't
trigger a 3-AM incident. Operators must explicitly opt in.

**Format SD** (K10242) is deliberately not exposed via MQTT. Wiring
an "erase disk" button through a subscribable topic is asking for
trouble; if operators want this they can call the cloud API
directly. No bridge surface will invoke K10242.

**PTZ** commands have no camera-side rate limit but the motor can't
absorb rapid direction changes without visible jitter (mrlt8 #728).
The bridge does not debounce; upstream automations are responsible
for reasonable command cadence.

### Firmware quirks noted during research

- **HL_CAM4 4.52.7.0367+** — WAN P2P blocked, some LAN works.
	Bridge auto-fallback to WebRTC (4.5.0) is the recovery path.
- **HL_PAN4 4.70.1.3311+** — `IOTC_ER_UNLICENSE (-10)` on session
	setup, distinct from CAM4's issue. Wyze-side SDK-version bump.
	No workaround known; can't be reached via TUTK at all.
- **Cam v3 4.36.14.2589 / Pan v3 4.50.15.4900** — auth-drift Feb
	2025. `network_mode: host` recovers LAN-mode for most.
- **Motion alarm SET is model-branched**: K10202 on
	WYZEDB3/WVOD1/HL_WCO2/WYZEC1; K10206 on everything else.
	Fork PR handles the branch server-side.
- **Bitrate GET** moved to K10050 on FW 4.51+ (was K10052).

| Area | Status |
|---|---|
| Phase 2: fork PR (opcodes + HTTP endpoints) | In progress |
| Phase 2: `internal/wyzectl/` HTTP client | Implemented |
| Phase 2: MQTT `set/<control>` subscribers | Not started |
| Phase 2: HA discovery for Phase 2 controls | Not started |
| Phase 2: `SIREN_ENABLED` safety env | Not started |
| Phase 2.1: SSE readback subscription | Deferred |
| Property state from live TUTK polling (`param_info`, `K10050`) | Deferred |
| Motion event parity (`{cam}/motion`) from camera alarm stream | Deferred |
| Sensors requiring live readback (`res`, `wifi`) | Deferred |
