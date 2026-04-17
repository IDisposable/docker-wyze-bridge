# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Docker Wyze Bridge is a Go application that bridges Wyze cameras to standard streaming protocols (WebRTC/RTSP/HLS). Three streaming paths, picked per-camera:

- **TUTK** (most of the fleet): go2rtc connects directly via its `wyze://` source.
- **Gwell P2P** (OG: `GW_GC1`, `GW_GC2`): optional `gwell-proxy` sidecar handles the LAN-direct UDP P2P and republishes to go2rtc as loopback RTSP.
- **WebRTC (KVS)** (doorbell lineage: `GW_BE1` Doorbell Pro, `GW_DBD` Doorbell Duo): go2rtc's native `#format=wyze` source pulls the Wyze KVS signaling URL + ICE servers from our loopback shim at `/internal/wyze/webrtc/<cam>` and dials Wyze's `wyze-mars-webcsrv.wyzecam.com` itself. **No sidecar.**

No Python, no binary SDK. FFmpeg is bundled solely for go2rtc's JPEG snapshot endpoint; the bridge itself never invokes it.

Canonical design doc: `DOCS/DESIGN.md`. User-facing upgrade notes: `MIGRATION.md`. `DOCS/GWELL_RELAY_HANDOFF.md` is historical context for the GW_BE1 streaming investigation — kept for archaeological value only.

## Build & Test

**Run tests:**
```bash
go test ./...
```

**Build binary:**
```bash
go build -o wyze-bridge ./cmd/wyze-bridge
```

**Docker build and run:**
```bash
docker compose up --build
```

**Docker build only (multi-arch):**
```bash
docker buildx build -f docker/Dockerfile -t wyze-bridge .
```

**Developer setup:** See `DEVELOPER.md` for local dev and devcontainer instructions.

## Architecture

### System Overview

```
Docker Container
├── wyze-bridge (Go binary — our code, port 5080)
├── go2rtc      (managed sidecar — ports 1984, 8554, 8888, 8889, 8189/udp)
└── gwell-proxy (optional sidecar — only spawned when an OG-family
                 Gwell camera is discovered; loopback RTSP + control API)
```

go2rtc handles all TUTK cameras and all WebRTC (doorbell-lineage) cameras. For OG-family Gwell cameras (LAN-direct UDP P2P), the `gwell-proxy` sidecar (vendored from `github.com/wlatic/hacky-wyze-gwell`) handles the handshake and republishes as a loopback RTSP stream feeding go2rtc like any other source. Our Go binary orchestrates: Wyze API auth, camera discovery, go2rtc config generation, the `/internal/wyze/webrtc/<cam>` shim that feeds go2rtc's `#format=wyze` source, MQTT, WebUI, snapshots, recording config, and state persistence.

### Entry Point

`cmd/wyze-bridge/main.go` — wires all components, handles signal-based graceful shutdown.

### Package Map

| Package | Purpose |
|---------|---------|
| `internal/config/` | Env vars, Docker secrets, YAML config, per-camera overrides |
| `internal/wyzeapi/` | Wyze API client: auth (HMAC-MD5, triple-MD5), camera discovery, commands, TOTP |
| `internal/go2rtcmgr/` | go2rtc subprocess management, YAML config generation, HTTP API client |
| `internal/gwell/` | gwell-proxy subprocess management + vendored Gwell P2P protocol for OG-family cameras (GW_GC1/GC2) |
| `internal/webui/shim_kvs.go` | `/internal/wyze/webrtc/<cam>` shim — feeds go2rtc's native `#format=wyze` source with signaling URL + ICE servers minted from `/v4/camera/get_streams`. Used for `GW_BE1` / `GW_DBD` doorbells. |
| `internal/camera/` | Per-camera state machine (offline→discovering→connecting→streaming), filter, manager |
| `internal/mqtt/` | Paho MQTT client, pub/sub, Home Assistant discovery messages |
| `internal/webui/` | net/http server, REST API, SSE for real-time updates, embedded static assets |
| `internal/snapshot/` | Interval + sunrise/sunset snapshot capture via go2rtc API, file pruning |
| `internal/recording/` | Recording config generation for go2rtc, mp4 file pruning |
| `internal/webhooks/` | HTTP POST notifications on camera state changes |

### Key Design Patterns

- **go2rtc as sidecar**: Managed subprocess, communication via HTTP API on 127.0.0.1:1984. Dynamic stream add/remove without restart.
- **State machine per camera**: `StateOffline → StateDiscovering → StateConnecting → StateStreaming → StateError` with exponential backoff (`min(5s * 2^n, 5min)`).
- **Config precedence**: Environment variables > YAML config > defaults. Per-camera overrides via `QUALITY_{CAM_NAME}`, `AUDIO_{CAM_NAME}`, `RECORD_{CAM_NAME}`.
- **State persistence**: `$STATE_DIR/wyze-bridge.state.json` survives container restarts.
- **SSE for WebUI**: Real-time camera state updates via Server-Sent Events, no polling.
- **Non-blocking notifications**: State change callbacks fire SSE, MQTT, webhooks, and state save each in their own goroutine. Camera connections and snapshots fan out with `sync.WaitGroup`.

### Critical Crypto (internal/wyzeapi/auth.go)

Ported from Python `wyzecam/api.py`:
- `hashPassword()` — triple MD5 hash
- `signMsg()` — HMAC-MD5 with key = `MD5(token + secret)`
- `sortDict()` — deterministic JSON serialization with sorted keys, compact separators
- `generateTOTP()` — RFC 4226/6238 TOTP for MFA login

### Configuration

All env vars documented in `DOCS/DESIGN.md` section 5.8. Key ones:
- `WYZE_EMAIL`, `WYZE_PASSWORD`, `WYZE_API_ID`, `WYZE_API_KEY` — auth (required)
- `BRIDGE_IP` — host IP for WebRTC ICE candidates
- `MQTT_HOST` — enables MQTT (presence implies `MQTT_ENABLED=true`)
- `LOG_LEVEL` — trace/debug/info/warn/error
- `FORCE_IOTC_DETAIL` — verbose go2rtc + bridge logging
- `WEBHOOK_URLS` — comma-separated URLs for state change notifications
- `GWELL_ENABLED` — master switch for Gwell protocol proxy (default `true`)
- `GWELL_BINARY` / `GWELL_RTSP_PORT` (`8564`) / `GWELL_CONTROL_PORT` (`18564`) / `GWELL_LOG_LEVEL` — gwell-proxy sidecar knobs (only relevant for OG-family cameras)

## Dependencies

Minimal: zerolog, paho.mqtt.golang, go-sunrise, go-isatty, yaml.v3. No web framework, no ORM.

## Docker

`docker/Dockerfile` — 3-stage Alpine build. Target image < 25MB. Multi-arch via `TARGETARCH`.

## Git

- `origin` — IDisposable/docker-wyze-bridge
- `upstream` — mrlt8/docker-wyze-bridge
