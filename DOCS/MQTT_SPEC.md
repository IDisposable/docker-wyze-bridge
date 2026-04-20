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

| Property | PID | GET command | SET command |
|---|---|---|---|
| status_light | P1 | K10030GetNetworkLightStatus | K10032SetNetworkLightStatus |
| night_vision | P2 (mode) / P3 (status) | K10040GetNightVisionStatus | K10042SetNightVisionStatus |
| bitrate | P3 | K10050GetVideoParam | — |
| res | P4 | — | — |
| fps | P5 | — | — |
| hor_flip | P6 | — | K10052HorizontalFlip |
| ver_flip | P7 | — | K10052VerticalFlip |
| motion_detection | P13 | K10200GetMotionAlarm | — |
| motion_tagging | P21 | K10290GetMotionTagging | K10292SetMotionTagging |
| irled | P50 | K10044GetIRLEDStatus | K10046SetIRLEDStatus |
| alarm | — | K10632GetAlarmFlashing | K10630SetAlarmFlashing |
| pan_cruise | — | K11014GetCruise | K11016SetCruise |
| motion_tracking (pan) | — | K11020GetMotionTracking | K11022SetMotionTracking |
| rotary_degree | — | — | K11000SetRotaryByDegree |
| reset_rotation | — | — | K11004ResetRotatePosition |

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
| GET command subscriptions (`{prop}/get`) with live TUTK readback | Deferred |
| Property state from live TUTK polling (`param_info`, `K10050`) | Deferred |
| Motion event parity (`{cam}/motion`) from camera alarm stream | Deferred |
| Alarm (`K10630/K10632`) parity | Deferred |
| Pan-cam state + K110xx commands | Deferred |
| Sensors requiring live readback (`res`, `wifi`) | Deferred |
