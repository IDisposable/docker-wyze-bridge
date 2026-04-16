# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Docker Wyze Bridge is a Go application that bridges Wyze cameras to standard streaming protocols (WebRTC/RTSP/HLS). It uses go2rtc as a managed sidecar for all TUTK camera streaming, plus an optional `gwell-proxy` sidecar for Wyze's newer Gwell/IoTVideo P2P cameras (GW_BE1 Doorbell Pro, GW_GC1 OG, GW_GC2 OG 3X, GW_DBD Doorbell Duo) — no Python, no binary SDK. FFmpeg is bundled solely for go2rtc's JPEG snapshot endpoint; the bridge itself never invokes it.

The design document is at `DOCS/DESIGN.md`. Implementation notes at `DOCS/IMPLEMENTATION_NOTES.md`. Gwell protocol integration plan at `DOCS/GWELL_INTEGRATION.md`.

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
└── gwell-proxy (optional sidecar for GW_* cameras — loopback RTSP + control API)
```

go2rtc handles MOST camera streaming (TUTK P2P, RTSP, WebRTC, HLS). For Wyze's newer Gwell/IoTVideo cameras (GW_BE1/GC1/GC2/DBD) that don't speak TUTK, an optional `gwell-proxy` sidecar (vendored from `github.com/wlatic/hacky-wyze-gwell`, MIT) handles the Gwell P2P handshake and republishes as a loopback RTSP stream which is then fed into go2rtc like any other source. Our Go binary orchestrates: Wyze API auth, camera discovery, go2rtc config generation, MQTT, WebUI, snapshots, recording config, and state persistence.

### Entry Point

`cmd/wyze-bridge/main.go` — wires all components, handles signal-based graceful shutdown.

### Package Map

| Package | Purpose |
|---------|---------|
| `internal/config/` | Env vars, Docker secrets, YAML config, per-camera overrides |
| `internal/wyzeapi/` | Wyze API client: auth (HMAC-MD5, triple-MD5), camera discovery, commands, TOTP |
| `internal/go2rtcmgr/` | go2rtc subprocess management, YAML config generation, HTTP API client |
| `internal/gwell/` | gwell-proxy subprocess management + Producer that feeds Gwell (GW_*) cameras into go2rtc as loopback RTSP streams |
| `internal/camera/` | Per-camera state machine (offline→discovering→connecting→streaming), filter, manager |
| `internal/mqtt/` | Paho MQTT client, pub/sub, Home Assistant discovery messages |
| `internal/webui/` | net/http server, REST API, SSE for real-time updates, embedded static assets |
| `internal/snapshot/` | Interval + sunrise/sunset snapshot capture via go2rtc API, file pruning |
| `internal/recording/` | Recording config generation for go2rtc, mp4 file pruning |
| `internal/webhooks/` | HTTP POST notifications on camera state changes |

### Key Design Patterns

- **go2rtc as sidecar**: Managed subprocess, communication via HTTP API on localhost:1984. Dynamic stream add/remove without restart.
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
- `WB_IP` — host IP for WebRTC ICE candidates
- `MQTT_HOST` — enables MQTT (presence implies `MQTT_ENABLED=true`)
- `LOG_LEVEL` — trace/debug/info/warn/error
- `FORCE_IOTC_DETAIL` — verbose go2rtc + bridge logging
- `WEBHOOK_URLS` — comma-separated URLs for state change notifications
- `GWELL_ENABLED` — master switch for Gwell protocol proxy (default `true`)
- `GWELL_BINARY` / `GWELL_RTSP_PORT` (`8564`) / `GWELL_CONTROL_PORT` (`18564`) / `GWELL_LOG_LEVEL` — see `DOCS/GWELL_INTEGRATION.md`

## Dependencies

Minimal: zerolog, paho.mqtt.golang, go-sunrise, go-isatty, yaml.v3. No web framework, no ORM.

## Docker

`docker/Dockerfile` — 3-stage Alpine build. Target image < 25MB. Multi-arch via `TARGETARCH`.

## Git

- `origin` — IDisposable/docker-wyze-bridge
- `upstream` — mrlt8/docker-wyze-bridge
