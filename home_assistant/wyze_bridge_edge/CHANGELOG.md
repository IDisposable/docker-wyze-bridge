# Changelog

## 4.4.1-edge

Hotfix for [#119](https://github.com/IDisposable/docker-wyze-bridge/issues/119):
OG cameras (`GW_GC1` / `GW_GC2`) worked on 4.3.0 but broke on 4.4.0
for many users. Field reports confirm Wyze's mars-webcsrv WebRTC
backend serves OG hardware reliably, so the default has flipped to
WebRTC. Users on 4.4.0 already applying the `is_webrtc=true` override
can drop it.

- **OG default is now WebRTC** (`GW_GC1`, `GW_GC2`) — no more
	gwell-proxy sidecar spawn for OG-only fleets.

## 4.4.0-edge

Code-review pass + community PRs: hardening, parity, observability,
new camera models, and tests across the bridge. CI appends the
timestamp suffix on each dev push (e.g. `4.4.0-edge.20260629.0400`).

### Camera support

- **AN_RDB1 (Doorbell Pro 2)** routed to the WebRTC path (was silently
	falling through to TUTK).
- **GW_DUO (Cam Pan Duo)** routed to the WebRTC path (mars-webcsrv
	signaling, same backend as the Doorbell Pro) with `is_pan` set.
- **GW_WC (Window Cam)** routed to Gwell P2P (LAN-direct, same path
	as OG cameras). Needs a manual LAN IP — see new HA UI below.
- **LD_CFP (Floodlight Pro)** routed to WebRTC via AWS KVS.
- **OG cameras (GW_GC1 / GC2)** now correctly classify as Gwell P2P
	even when the Wyze cloud reports an empty LAN IP.
- New `IsGwellP2P` registry flag distinguishes LAN-direct Gwell models
	(OG, Window Cam) from doorbell-lineage Gwell (Doorbell Pro / Duo).

### HA add-on UI

- **Manual LAN IPs** for Gwell cameras (under "Gwell Protocol
	Cameras → Manual LAN IPs"): list of `{mac, lan_ip}` entries that
	feed the new `GWELL_LAN_IPS` env var, applied at discovery time.
	Use this when the Wyze cloud doesn't report a LAN IP for `GW_DUO`
	or `GW_WC` and gwell-proxy can't lock LAN-direct.
- **Camera Model Registry overrides** (new section "Camera Model
	Registry → Model Overrides"): list of `{model, name, is_*}`
	entries that override or add rows in the bridge's model registry
	at startup via the new `MODEL_OVERRIDES` env var. Lets operators
	add a brand-new Wyze model code or flip routing flags on an
	existing one without rebuilding the bridge.

### Reliability

- **Pre-registered Gwell publish slots**: gwell-proxy's RTSP PUSH no
	longer races the runtime AddStream and gets dropped with a broken
	pipe; the slot is reserved in go2rtc's startup YAML.
- **gwell-proxy reconnect**: uses the cached P2P server endpoint on
	stream-error retries instead of re-running full discovery.
- **KVS signaling double-encode fix** (`FixKVSSignalingURL`): some
	Wyze cameras (observed on LD_CFP) return a SigV4-encoded URL with
	`%252F` instead of `%2F`; AWS KVS rejects with 403. Single-decode
	when the tell-tale `%25` is present.
- **WebRTC-streamer discovery branch**: `GetCameraList` skips the
	LAN-IP / P2P-ID checks for WebRTC-only models (LD_CFP doesn't
	report either) — MAC + Model is enough.
- Atomic state-file writes (write-to-temp + rename) under a write
	mutex; concurrent state-change goroutines no longer race the file.
- Wyze API auth-lifecycle observer wires failures + recoveries to the
	issues registry (`/metrics`, `/api/health`,
	`sensor.wyze_bridge_config_errors`).
- Chronic camera-error reporting: >10 consecutive failed connects on
	a camera posts a `camera/chronic/<name>` issue; cleared on next
	stream.
- MQTT publish backpressure: bounded waiter pool prevents goroutine
	leak when the broker is unreachable; loud-then-rate-limited drop
	log.
- Graceful ffmpeg recorder shutdown (SIGINT + `WaitDelay`) — last mp4
	segment finalizes cleanly instead of being SIGKILL-truncated.

### Architecture / quality

- Single `ModelSpec` registry replaces five hardcoded maps; adding a
	new camera is one row (or one Model Override entry above).
- `webui.NewServer` takes an `Options` struct (drops several Set*
	setters); `mqtt.NewClient` is ctx-first.
- `cmd/wyze-bridge/main.go`'s `wireCameraStateChanges` split into
	per-subsystem helpers.
- `issues.Registry` methods nil-safe (callers no longer guard).
- Typed `wyzeapi.GetCameraKVSConfig` replaces raw-map parsing.
- `/api/*` errors return JSON `{"error":"…"}` via `writeJSONError`.
- `mqtt.Client` + `webui.Server` propagate the bridge's signal-
	cancellable root context to fire-and-forget handler goroutines.
- Doorbell labels aligned with Wyze marketing names ("Wyze Video
	Doorbell Pro" / "Pro 2" / "Duo").
- `DOCS/GW_BE1_Research.md` captures pcap-based protocol notes.

### Observability & UX

- README "Issues registry" subsection explaining `config_errors`,
	active categories, and the three surfaces that show them.
- Actionable hints for known Wyze API response codes (1001 bad creds,
	1003 bad API key, 2001 token expired, 3019 MFA, …).
- Network-error classifier distinguishes DNS / timeout / `OpError`
	from generic transport failures; HTTP 5xx / 429 / 401-403 render
	with actionable text.
- `/metrics` page: per-section legend captions + hover tooltips on
	every column header and summary tile.
- DEVELOPER.md "Adding a new camera model" section.

### New env vars

- `GWELL_LAN_IPS=MAC=IP,MAC=IP` — pin LAN IPs for Gwell cameras the
	Wyze cloud doesn't report. HA UI feeds this from Gwell → Manual
	LAN IPs.
- `MODEL_OVERRIDES=MODEL:flag=v,flag=v;MODEL:...` — override or add
	model registry rows at startup. HA UI feeds this from Camera
	Model Registry → Model Overrides. Flags: `name`, `is_gwell`,
	`is_gwell_p2p`, `is_webrtc`, `is_pan`, `is_doorbell`.

### Tests

- Webui smoke coverage (prometheus, dashboard, metrics page+JSON,
	route table, HLS / WS proxy).
- `mqtt.Client` (constructor defaults, callback registration,
	`publishSem` saturation).
- `/api/*` actions (audio toggle, quality validation, record
	start/stop, `/api/discover` no-hook / GET-405 / with-hook, health
	degraded mode, KVS shim happy + reject).
- Model registry (`IsGwellP2P`, `IsWebRTCStreamer` matrix,
	`ApplyModelOverrides` parser, `FixKVSSignalingURL`,
	`gwellLanIPOverride`).
- New camera-classification tests for GW_WC, LD_CFP, GW_DUO.

### Docs

- `.env.dev.example` aligned with current env-var names.
- `DOCS/DESIGN.md` MQTT topic table synced.
- `DEVELOPER.md` release flow + Adding-a-camera-model.

### Credits

- PR #111 (Grady Neely): OG cameras (GW_GC1/GC2) classified as Gwell
	P2P even when the Wyze cloud returns an empty LAN IP.
- PR #116 (wlatic): GW_DUO + `GWELL_LAN_IPS` env var +
	pre-registered Gwell publish slots + gwell-proxy reconnect fix.
- PR #118 (Daniel Quick): GW_WC, LD_CFP, KVS double-encode fix,
	GW_DUO via WebRTC.

## 4.3.0

MQTT Phase 1 release focused on control parity improvements with the legacy bridge,
while documenting go2rtc-era control boundaries.

### Added

- MQTT stream control topics:
	- `{topic}/{cam}/state/set` (`start`/`stop`)
	- `{topic}/{cam}/power/set` (`on`/`off`/`restart`)
- MQTT power state publishing on `{topic}/{cam}/power`
- Cloud-backed MQTT property control mapping (write-only mirror):
	- `night_vision`, `irled`, `status_light`, `motion_detection`,
		`motion_tagging`, `bitrate`, `fps`, `hor_flip`, `ver_flip`
- Home Assistant discovery entities for Phase 1 controls:
	- stream switch, power switch, reboot button, snapshot button
	- IR/status light/motion detection/motion tagging switches
	- bitrate/fps number controls
	- horizontal/vertical flip switches
- MQTT capability and scope reference in `DOCS/MQTT_SPEC.md`

### Changed

- Night vision mapping aligned to cloud property semantics (`auto` => `3`)
- MQTT expectations now explicitly split into `Implemented`, `Phase 1`, and `Deferred`
	in the spec to clarify what requires direct TUTK control vs. cloud API fallback

### Notes

- Live camera property readback parity from the Python bridge remains deferred because
	go2rtc owns the active TUTK session and does not expose a Wyze property-control API.
- Pan/tilt K110xx command parity and full `{prop}/get` MQTT readback are out of scope
	for this phase.

## 4.2.2

Complete rewrite in Go. See [MIGRATION.md](https://github.com/IDisposable/docker-wyze-bridge/blob/main/MIGRATION.md) for upgrade instructions.

### Added

- go2rtc-based streaming (pure Go TUTK P2P, no binary SDK)
- MFA/TOTP login support
- Server-Sent Events for real-time WebUI updates
- Structured JSON logging via zerolog
- Recording with configurable path templates and auto-pruning
- Sunrise/sunset snapshot scheduling
- Metrics endpoints

### Changed

- Docker image under 25 MB (was 200+ MB)
- WebUI completely redesigned (dark theme, grid layout, WebRTC player)
- State persistence via JSON (replaces pickle files)
- MQTT auto-detects Home Assistant Mosquitto broker

### Removed

- Python runtime and all Python dependencies
- Binary TUTK SDK (`.so` files)
- MediaMTX (replaced by go2rtc)
- FFmpeg
- Remote P2P streaming (LAN only; use VPN for remote access)
- `ON_DEMAND` setting (all cameras connect eagerly)
- `MTX_*` environment variables
- Per-camera STREAM_AUTH (global credentials only)
- Unraid template
