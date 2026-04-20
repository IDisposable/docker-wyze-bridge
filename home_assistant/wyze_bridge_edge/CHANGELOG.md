# Changelog

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
