# Changelog

## 4.7.0

Cameras that moved on the LAN, a WebUI that tells the truth about
video, and a dashboard you can arrange and tune. The Wyze cloud only
knows a camera's IP from the last time it phoned home; after a DHCP
change the bridge kept dialing the old address while the card said
"streaming". The bridge now checks the LAN itself.

- **LAN IP resolver** (`internal/lanresolve`): before a TUTK camera
	is handed to go2rtc, its MAC is verified over ARP at the cloud's
	IP, the last good IP, anything the kernel's neighbor cache or an
	earlier sweep already tied to it, and finally a paced sweep of the
	subnet (capped at a `/22`, backed off per camera). Streaming
	cameras are re-verified every `LAN_IP_RECHECK` (default `2m`) and
	re-registered when they move. ARP requests go unicast to the
	camera's MAC and seed the kernel's neighbor cache, because V3s in
	Wi-Fi power save sleep through broadcasts (which is also why
	go2rtc's own dials timed out on cameras that were right there).
	Needs `CAP_NET_RAW` (Docker default) for ARP; falls back to the
	kernel neighbor cache without it. New env vars `LAN_IP_RESOLVE`,
	`LAN_IP_SWEEP`, `LAN_IP_RECHECK`, also in the add-on's **camera**
	section.
- **Network box** on the detail page: what the cloud reported, how and
	when the bridge verified the camera (cloud address, last known,
	neighbor cache, sweep), how many sweeps missed it, which engine is
	running. Same fields in `/api/cameras` (`lan_ip_source`,
	`lan_ip_verified_at`, `lan_sweep_misses`, `lan_absent`).
- **Pinned address**: for a camera ARP can't reach, type its address
	in the Network box and Pin it. Beats the cloud and the resolver,
	kept by MAC in the state file. `POST /api/cameras/{name}/pin`.
- **Honest video status**: a second badge per camera — `live` /
	`idle` / `no video` — from go2rtc's producer byte counters, its
	`stream=` warnings, and the error frames it sends dashboard players
	(read off the bridge's websocket proxy). Last go2rtc error on the detail page and in
	`/api/cameras` (`video`, `last_video`, `last_error`,
	`last_error_at`); a TUTK `discovery timeout` triggers an immediate
	LAN IP recheck.
- **Dashboard players that recover**: tiles start a moment apart and
	ask for one MSE stream each instead of WebRTC and MSE at once; a
	player whose picture stops moving reconnects by itself, backing
	off for a camera that keeps failing. A stream whose connection is
	up but silent (a V3 that dropped off Wi-Fi mid-stream) is
	restarted by the bridge, at most every two minutes, and its
	players sent to reconnect. Tiles keep 2.5s buffered and skip past
	the holes go2rtc leaves when it drops a frame with lost packets.
- A camera heard from in the last 30 minutes that goes quiet is no
	longer swept for (hundreds of ARP requests at a dozing camera were
	knocking it off the bridge for minutes).
- **go2rtc kept alive**: if go2rtc exits on its own the bridge logs
	why (the crash dump used to be dropped), starts it again with a
	backoff and puts every stream back. Calls that change go2rtc's
	stream table now go one at a time, since go2rtc doesn't lock that
	table when it lists or deletes from it, and removing a camera now
	really removes its go2rtc stream (the delete used the wrong
	parameter and did nothing).
- **Ignore list**: 🚫 on any card drops the camera, retracts its HA
	entities and remembers its MAC in the state file; restore from the
	strip at the bottom of the dashboard. A card two LAN sweeps in a
	row couldn't hear shows a "Not on this LAN?" note with the same
	button. `GET /api/ignored`, `POST /api/cameras/{name}/ignore`,
	`POST /api/ignored/{mac}/restore`.
- **Settings page** with camera rules: network scope (any address, or
	local subnets only so another house's cameras are skipped by
	address), extra CIDRs, and name (`*` wildcard) / MAC / model lists
	in block or allow mode. Saved in the state file, applied right
	away, excluded cameras listed with the reason on the dashboard.
	The `FILTER_*` env vars still run first. `GET`/`PUT /api/settings`,
	`GET /api/excluded`.
- **Video quality per camera** on the Settings page (and the detail
	page's HD / SD buttons, MQTT, the API): saved in the state file and
	winning over `QUALITY_<CAM>` / `QUALITY`, applied with one stream
	restart. Changing quality on a doorbell no longer breaks its
	stream.
- **Arrange, lock and rotate tiles**: drag the ⠿ handle, rotate a
	sideways camera's picture with ⟳ (view only), lock the grid. Saved
	in the state file (`GET`/`PUT /api/layout`).
- **Camera controls** on the detail page: flip horizontal/vertical,
	night vision auto/on/off, reboot — via the Wyze cloud, mirrored to
	the MQTT state topics so HA agrees. `POST
	/api/cameras/{name}/property`, `POST /api/cameras/{name}/reboot`.
- **WebUI restyle**: light/dark (follows the OS, ◐ to override),
	header chips, filter box, 16:9 tiles. No new dependencies.
- Cameras sharing a nickname get `_` + the last four of their MAC
	instead of collapsing into one entry.
- `camera_added` / `camera_removed` SSE events now actually fire, and
	a camera dropped after a rename retracts its MQTT entities.

## 4.5.0

Runtime **TUTK → WebRTC auto-fallback** for cameras Wyze crippled
via firmware. When a TUTK-path camera fails to stream 5 times in
a row (configurable via `TUTK_FALLBACK_THRESHOLD`), the bridge
promotes it to the WebRTC path for the rest of the process
lifetime — no operator intervention, no config edits, no restart.
Primarily targets `HL_CAM4` (Wyze V4) hit by the early-2025
firmware update but works for any model Wyze exposes over
mars-webcsrv.

- New env var `TUTK_FALLBACK_THRESHOLD` (default `5`, `0` disables).
- Per-camera `forceWebRTC` flag surfaces in `/api/metrics` as
	`protocol_forced: true` and on the metrics page as `webrtc
	(forced)`.
- Auto-promotion fires the issues registry entry
	`camera/fallback/<name>` (warn severity) so the change is
	visible in `/metrics`, `/api/health`, and
	`sensor.wyze_bridge_config_errors`.
- Manual `MODEL_OVERRIDES=HL_CAM4:is_webrtc=true` still works and
	takes precedence — operator intent wins over auto-fallback.
- Full design + testing notes in `DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md`.
- MIGRATION.md → Known Issues updated to reflect auto-recovery
	is now the default fix path.

## 4.4.2

Reliability + doc pass driven by open issue triage.

- **Stream auto-recovery** ([#100](https://github.com/IDisposable/docker-wyze-bridge/issues/100)):
	HealthCheck now routes dropped streams through the error/backoff
	path so the 10s reconnect ticker actually picks them up (was
	logging "reconnecting" without reconnecting). `connectCamera` also
	clears any prior go2rtc entry before re-registering so a stuck
	source pool gets fully torn down. Rename-in-Wyze-app orphans are
	reaped by MAC each discovery cycle instead of accumulating stale
	streams forever.
- **Grid audio stays muted** ([#112](https://github.com/IDisposable/docker-wyze-bridge/issues/112)):
	`<video-rtc>` now honors a `muted` attribute proactively, so the
	grid stays quiet even after the browser grants autoplay
	permission from a single-camera click. Detail page is unchanged.
- **Camera-name doc** ([#99](https://github.com/IDisposable/docker-wyze-bridge/issues/99)):
	Expanded MIGRATION.md → "Changed: Camera names" with the specific
	Frigate / HA `camera:` / Lovelace / Node-RED surfaces to update.
- **HL_CAM4 (V4) auto-hint** ([#117](https://github.com/IDisposable/docker-wyze-bridge/issues/117),
	[#92](https://github.com/IDisposable/docker-wyze-bridge/issues/92),
	[#87](https://github.com/IDisposable/docker-wyze-bridge/issues/87)):
	Wyze's early-2025 firmware disabled TUTK on newer V4 units. The
	issues registry now emits a targeted hint when it sees chronic
	TUTK timeouts on an `HL_CAM4` — the fix is a one-line override
	(`MODEL_OVERRIDES=HL_CAM4:is_webrtc=true`) documented in README +
	MIGRATION.md → "Known Issues". Runtime auto-fallback is designed
	for 4.5.

## 4.4.1

Hotfix for [#119](https://github.com/IDisposable/docker-wyze-bridge/issues/119):
OG cameras (`GW_GC1` / `GW_GC2`) worked on 4.3.0 but broke on 4.4.0
for many users. Field reports confirm Wyze's mars-webcsrv WebRTC
backend serves OG hardware reliably, so the default has flipped to
WebRTC. Users on 4.4.0 already applying the `is_webrtc=true` override
can drop it.

- **OG default is now WebRTC** (`GW_GC1`, `GW_GC2`) — no more
	gwell-proxy sidecar spawn for OG-only fleets.

## 4.4.0

Code-review pass + community PRs: hardening, parity, observability,
new camera models, and tests across the bridge.

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
