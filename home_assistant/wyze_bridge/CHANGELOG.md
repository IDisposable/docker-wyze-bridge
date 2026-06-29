# Changelog

## Unreleased

Code-review pass: hardening, parity, observability, docs, and tests across
the bridge.

### Added

- **AN_RDB1 (Doorbell Pro 2)** routed to the WebRTC path (was silently
	falling through to TUTK)
- Single `ModelSpec` registry in `internal/wyzeapi/models.go` replaces
	five hardcoded maps; adding a new camera = one row
- `DEVELOPER.md` "Adding a new camera model" section
- `DOCS/GW_BE1_Research.md` capturing pcap-based Doorbell Pro protocol notes
- Atomic state-file writes (write-to-temp + rename) under a write mutex
- Wyze API auth-lifecycle observer wires failures + recoveries to the
	issues registry (visible on `/metrics`, `/api/health`,
	`sensor.wyze_bridge_config_errors`)
- Chronic camera-error reporting: >10 consecutive failed connects on a
	camera posts a `camera/chronic/<name>` issue; cleared on next stream
- MQTT publish backpressure: bounded waiter pool prevents goroutine leak
	when the broker is unreachable; loud-then-rate-limited drop log
- Graceful ffmpeg recorder shutdown (SIGINT + `WaitDelay`) — last mp4
	segment finalizes cleanly instead of being SIGKILL-truncated
- README "Issues registry" subsection explaining `config_errors`, active
	categories, and the three surfaces that show them
- Actionable hints for known Wyze API response codes (1001 bad creds,
	1003 bad API key, 2001 token expired, 3019 MFA, …)
- Network-error classifier distinguishes DNS / timeout / `OpError` from
	generic transport failures; HTTP 5xx / 429 / 401-403 render with text
	the operator can act on
- `/metrics` page: per-section legend captions + hover tooltips on every
	column header and summary tile
- Typed `wyzeapi.GetCameraKVSConfig` extracted from the previous raw-map
	parsing; `cmd/wyze-bridge/main.go`'s `kvsAdapter` shrunk to a 6-line
	type conversion
- Smoke tests for webui surfaces (`prometheus`, `dashboard`, `metrics`
	page+JSON, route table, HLS / WS proxy)
- `mqtt.Client` tests (constructor defaults, callback registration,
	`publishSem` saturation)
- Deeper `/api/*` tests (audio toggle, quality validation, record
	start/stop, `/api/discover` no-hook / GET-405 / with-hook, health
	degraded mode with a real Issue, KVS shim happy path + non-WebRTC
	rejection)

### Changed

- `webui.NewServer` takes an `Options` struct; drops `SetRootContext`,
	`SetIssuesRegistry`, `SetAuthPhoneIDFn` setters (kept
	`SetMarsMinter`/`SetKVSProvider` for test reuse)
- `mqtt.NewClient` is ctx-first (drops `SetRootContext`)
- `cmd/wyze-bridge/main.go`'s `wireCameraStateChanges` split into
	`autoToggleRecording` / `recordStateEvent` / `pushStateSSE` /
	`publishStateMQTT` / `sendStateWebhook` / `persistState` helpers
- `mqtt.Client` and `webui.Server` propagate the bridge's signal-
	cancellable root context to fire-and-forget handler goroutines
	(replaces orphaned `context.Background()`)
- `issues.Registry` methods are nil-safe (callers no longer guard with
	`if r != nil`)
- `/api/*` errors return JSON `{"error":"…"}` via `writeJSONError` for a
	consistent error shape
- Doorbell labels aligned with Wyze marketing names ("Wyze Video
	Doorbell Pro" / "Pro 2" / "Duo")
- `.env.dev.example` aligned with current env-var names (`WB_IP` →
	`BRIDGE_IP`, `TOTP_KEY` → `WYZE_TOTP_KEY`, `IMG_DIR` removed)
- `DOCS/DESIGN.md` MQTT topic table synced (added `state/set`,
	`power/set`, the cloud-set properties: `irled`, `status_light`,
	`motion_detection`, `motion_tagging`, `hor_flip`, `ver_flip`,
	`bitrate`, `fps`)
- In-code comments tightened (terse, present-tense)

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
