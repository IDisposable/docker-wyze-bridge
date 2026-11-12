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
- Exception: PTZ and cruise-point MQTT commands use a separate short-lived TUTK/DTLS
	control connection and are gated to supported Pan cameras.

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

All retained unless noted. The property topics (`night_vision`,
`hor_flip`, `ver_flip`, ...) are also republished when the same
property is set from the WebUI's camera controls
(`POST /api/cameras/{name}/property`), so HA follows either route.

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
| `{topic}/{cam}/cruise_points` | JSON array | — | Retained response to the cruise-point GET command |

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
| `{topic}/{cam}/set/rotary_action` | `up` / `down` / `left` / `right` / `stop` | K11002SetRotaryByAction; DTLS TUTK Pan cameras only |
| `{topic}/{cam}/set/reset_rotation` | any | K11004ResetRotatePosition |
| `{topic}/{cam}/set/cruise_point` | `1`–`4` or JSON | Save current position or go to a configured cruise point |
| `{topic}/{cam}/cruise_points/get` | any | Read and publish the configured cruise points |
| `{topic}/{cam}/set/ptz_position` | position | K11018SetPTZPosition |
| `{topic}/{cam}/command/status` | JSON | Publish | Non-retained success/error result for PTZ and cruise-point commands |

PTZ and cruise-point commands are implemented only for cameras whose model
supports TUTK PTZ and which are not forced to WebRTC; Gwell and WebRTC paths
are not used. Publish one of `up`, `down`, `left`, `right`, or `stop` to
`{topic}/{cam}/set/rotary_action`. Movement has no automatic timeout, so send
an explicit `stop`. Commands for the same camera are serialized. If a STOP
arrives while an earlier PTZ command is in flight, it is attempted immediately
and queued once more after earlier commands to reduce the chance of a late
movement command leaving the camera moving. If the queue is full, not-yet-started
commands are cancelled so the final STOP can be queued. A successful response
means the camera acknowledged the protocol command; it does not confirm
mechanical movement. A STOP whose initial TUTK discovery times out before the
action is sent is retried once, 200 ms apart; other connection or authentication
failures are not retried. See *TUTK Pan PTZ + cruise-point entities* below for
what Home Assistant's tap-only buttons change about this.

For example, with the default `{topic}`:

```sh
# Terminal 1: watch non-retained results
mosquitto_sub -t wyzebridge/living_room/command/status

# Terminal 2: issue movement, then explicitly stop
mosquitto_pub -t wyzebridge/living_room/set/rotary_action -m left
mosquitto_pub -t wyzebridge/living_room/set/rotary_action -m stop
```

The status topic receives non-retained JSON such as
`{"command":"rotary_action","direction":"stop","status":"ok","delivery":"acknowledged"}`.
For STOP, `delivery` is `acknowledged` only after a protocol response, `not_sent`
when the bridge knows the action was not sent, or `unknown` when the action may
have reached the camera but no acknowledgement was received. When STOP overlaps
an in-flight command, two status events may be published: one for the immediate
attempt and one for the queued final attempt.

Publish any payload to `{topic}/{cam}/cruise_points/get` to read the current
list. The retained `{topic}/{cam}/cruise_points` state is a JSON array with
`vertical`, `horizontal`, and `wait_seconds` for each configured slot. To save
the current position into a slot, publish `{"action":"save","slot":1}` to
`{topic}/{cam}/set/cruise_point`; slots are numbered 1–4 and new slots must be
saved sequentially. To recall a slot, publish either its number (for example
`2`) or `{"action":"goto","slot":2}`. Cruise point reads, saves, and recalls
also report completion or errors to `{topic}/{cam}/command/status`.

```sh
mosquitto_pub -t wyzebridge/living_room/cruise_points/get -n
mosquitto_sub -t wyzebridge/living_room/cruise_points
mosquitto_pub -t wyzebridge/living_room/set/cruise_point -m '{"action":"save","slot":1}'
mosquitto_pub -t wyzebridge/living_room/set/cruise_point -m 1
```

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
| `pan_tilt` | `cover` | — | `{base}set/rotary_degree` + tilt |

### TUTK Pan PTZ + cruise-point entities (`supports_ptz`)

Announced only for cameras that pass the same capability gate as the PTZ
command topics: registry Pan models (Pan V1/V2/V3/Pro) reachable over DTLS
TUTK. Never for Gwell or WebRTC cameras (including Cam Pan Duo), and never
while the camera is on the WebRTC path — whether the WebRTC route came from a
model override or from auto-promotion after repeated TUTK failures. This is the
same value the camera status JSON reports as `supports_ptz`. When a camera
moves to the WebRTC path mid-run, or is removed/ignored, these configs are
retracted with empty retained payloads so Home Assistant drops the controls
instead of leaving buttons that can only fail.

`{base}` = `{topic}/{cam}/`. Every entity uses `unique_id = wyze_{mac}{suffix}`
and joins the camera's existing device group (`identifiers: ["wyze_{mac}"]`), so
these show up beside the camera's switches and selects. Availability comes from
`{base}state` (`connected`/`disconnected`).

| Suffix | Component | Topic | Payload |
|---|---|---|---|
| `_ptz_up` | `button` | `{base}set/rotary_action` | `payload_press=up` |
| `_ptz_down` | `button` | `{base}set/rotary_action` | `payload_press=down` |
| `_ptz_left` | `button` | `{base}set/rotary_action` | `payload_press=left` |
| `_ptz_right` | `button` | `{base}set/rotary_action` | `payload_press=right` |
| `_ptz_stop` | `button` | `{base}set/rotary_action` | `payload_press=stop` |
| `_cruise_refresh` | `button` | `{base}cruise_points/get` | `payload_press=get` |
| `_cruise_save_1` … `_cruise_save_4` | `button` | `{base}set/cruise_point` | `payload_press={"action":"save","slot":N}` |
| `_cruise_goto_1` … `_cruise_goto_4` | `button` | `{base}set/cruise_point` | `payload_press={"action":"goto","slot":N}` |
| `_cruise_points` | `sensor` | state: `{base}cruise_points` | retained JSON array (`vertical`, `horizontal`, `wait_seconds`); no command topic |

Entity names are the camera nickname plus a suffix (`Pan V3 PTZ Up`, `Pan V3 PTZ
STOP`, `Pan V3 Cruise Save 1`, …), and each button carries an icon
(`mdi:arrow-up/down/left/right`, `mdi:stop`, `mdi:refresh`,
`mdi:map-marker-plus`, `mdi:map-marker`, `mdi:map-marker-path`).

**Home Assistant buttons are tap commands, not hold-to-move.** HA's `button`
entity fires once per press and has no release event, so there is no "press and
hold" like the WebUI's PTZ pad. A direction tap starts a movement that keeps
going until a `stop` arrives — the bridge adds no automatic movement timeout.
Keep `_ptz_stop` reachable (its own row or a large card next to the direction
buttons), and prefer one tap per nudge over holding/repeating a direction.

**STOP is best-effort; a physical stop is not guaranteed.** A `_ptz_stop` tap
publishes exactly the `stop` payload any other client would, and the outcome
lands on `{topic}/{cam}/command/status` with `delivery`:

- `acknowledged` — the camera answered the protocol command; it is still not
  proof the camera physically moved.
- `not_sent` — the bridge knows the stop was never sent.
- `unknown` — the stop may have reached the camera, but no acknowledgement came
  back.

A STOP whose initial TUTK discovery times out before any action is sent is
retried once, 200 ms apart; other connect or authentication failures are not
retried. If a direction command is still queued when STOP arrives, STOP is
attempted immediately and re-queued last so a final stop runs after the earlier
commands; if the per-camera queue (8 commands) is full, not-yet-started
movements are cancelled to make room for it. None of this can guarantee the
camera stops: if it keeps moving, disconnect its power rather than trusting
another software command.

The cruise-point entities mirror the command topics documented above
(`set/cruise_point`, `cruise_points/get`, `cruise_points`): `_cruise_refresh`
asks the bridge to re-read the camera's slots into the retained sensor, and
slots are 1–4 with new slots saved sequentially.

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
| MQTT PTZ action + cruise-point read/save/recall | Implemented for supported TUTK Pan cameras; command results on `{cam}/command/status` |
| HA discovery: PTZ up/down/left/right + STOP, cruise-point save/goto/refresh, cruise-point sensor | Implemented; announced only for cameras passing the TUTK Pan `supports_ptz` gate |
| Sensors requiring live readback (`res`, `wifi`) | Deferred |
