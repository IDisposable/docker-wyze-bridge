# Docker Wyze Bridge

[![Docker](https://github.com/IDisposable/docker-wyze-bridge/actions/workflows/docker-image.yml/badge.svg)](https://github.com/IDisposable/docker-wyze-bridge/actions/workflows/docker-image.yml)

WebRTC/RTSP/HLS bridge for Wyze cameras. Stream locally with no modifications, no special firmware, no cloud dependency.

Built in Go. Uses [go2rtc](https://github.com/AlexxIT/go2rtc) for camera streaming via pure-Go TUTK P2P — no binary SDK, no Python. Docker image ~65 MB (the only remaining C dependency is ffmpeg, used solely by go2rtc's JPEG snapshot endpoint).

> **Upgrading from the Python bridge?** See [MIGRATION.md](MIGRATION.md).

## Quick Start

```bash
docker run -p 5080:5080 -p 8554:8554 -p 8888:8888 -p 8889:8889 -p 8189:8189/udp \
  -e WYZE_EMAIL=you@example.com \
  -e WYZE_PASSWORD=yourpass \
  -e WYZE_API_ID=your-api-id \
  -e WYZE_API_KEY=your-api-key \
  idisposablegithub365/wyze-bridge:go
```

Open `http://localhost:5080` for the WebUI.

> **API Key required:** Get your API ID and Key from the [Wyze Developer Console](https://developer-api-console.wyze.com/#/apikey/view).

## Supported Cameras

| Model | Status |
| ------- | -------- |
| Wyze Cam V3 | Confirmed |
| Wyze Cam V4 | Confirmed |
| Wyze Cam Doorbell V2 | Confirmed |
| Wyze Cam Pan V3 | Mostly working |
| Wyze Cam Doorbell V1 | Needs support from go2rtc |
| Wyze Cam V1, V2, Pan V1/V2 | Should work (TUTK) |
| OG, Doorbell Pro, Battery Cam Pro | Not supported (Gwell protocol) |

## Docker Compose

```yaml
services:
  wyze-bridge:
    image: idisposablegithub365/wyze-bridge:go
    restart: unless-stopped
    ports:
      - 5080:5080
      - 8554:8554
      - 8888:8888
      - 8889:8889
      - 8189:8189/udp
    volumes:
      - ./config:/config
      - ./media:/media      # snapshots land in /media/snapshots, recordings in /media/recordings
    environment:
      - WYZE_EMAIL=you@example.com
      - WYZE_PASSWORD=yourpass
      - WYZE_API_ID=your-api-id
      - WYZE_API_KEY=your-api-key
      # - BRIDGE_IP=192.168.1.50  # Required for WebRTC
```

## Home Assistant

Install as an add-on. See [home_assistant/DOCS.md](home_assistant/DOCS.md) for setup instructions.

MQTT auto-detection: if you have the Mosquitto broker add-on, cameras are automatically discovered in Home Assistant.

## Stream URLs

Once running, each camera is available at:

| Protocol | URL |
| ---------- | ----- |
| RTSP | `rtsp://HOST:8554/camera_name` |
| HLS | `http://HOST:8888/camera_name` |
| WebRTC | `http://HOST:5080/camera/camera_name` |
| Snapshot | `http://HOST:5080/api/snapshot/camera_name` |

Camera names are normalized: lowercase, spaces replaced with underscores (e.g., "Front Door" becomes `front_door`).

## Ports

| Port | Purpose |
| ------ | --------- |
| 5080 | Bridge WebUI + REST API |
| 1984 | go2rtc native UI (optional, for power users) |
| 8554 | RTSP |
| 8888 | HLS |
| 8889 | WebRTC HTTP |
| 8189/udp | WebRTC ICE |

## Configuration

All configuration is via environment variables. See [MIGRATION.md](MIGRATION.md) for the complete reference.

### Key Variables

| Variable | Required | Description |
| ---------- | ---------- | ------------- |
| `WYZE_EMAIL` | Yes | Wyze account email |
| `WYZE_PASSWORD` | Yes | Wyze account password |
| `WYZE_API_ID` | Yes | API ID from Wyze Developer Console |
| `WYZE_API_KEY` | Yes | API Key from Wyze Developer Console |
| `BRIDGE_IP` | For WebRTC | Host IP for WebRTC ICE candidates |
| `MQTT_HOST` | For MQTT | MQTT broker hostname (enables MQTT) |
| `LOG_LEVEL` | No | trace/debug/info/warn/error (default: info) |

### Per-Camera Overrides

```bash
QUALITY_FRONT_DOOR=sd
AUDIO_BACKYARD=false
RECORD_GARAGE=true
```

### Camera Filtering

```bash
FILTER_NAMES=Front Door,Backyard    # Only include these
FILTER_BLOCKS=true                  # Or set to exclude them
```

### Recording

```bash
RECORD_ALL=true
RECORD_PATH=/record/{cam_name}/%Y/%m/%d
RECORD_LENGTH=60s
RECORD_KEEP=7d
```

### RTSP Authentication

```bash
STREAM_AUTH=user:password
```

All RTSP/WebRTC consumers must provide these credentials. Per-camera credentials are not supported.

## Network Requirements

Cameras and the bridge must be on the **same LAN subnet**. Remote P2P is not supported.

For remote access, use a VPN (Tailscale, WireGuard, OpenVPN).

## Architecture

```goat
Docker Container
├── wyze-bridge
│   ├── Wyze API Client — auth, camera discovery
│   ├── go2rtc Manager  — subprocess, config generation
│   ├── Camera Manager  — state machines, reconnection
│   ├── MQTT Client     — HA discovery, commands
│   ├── WebUI Server    — REST API, SSE, embedded UI
│   ├── Snapshot Manager — interval, sunrise/sunset
│   └── Recording Manager — config, pruning
└── go2rtc (managed sidecar)
    └── TUTK P2P → RTSP/WebRTC/HLS
```

## Development

```bash
# Run tests
go test ./...

# Build
go build -o wyze-bridge ./cmd/wyze-bridge

# Docker build
docker compose up --build
```

## License

[GNU GPL v3](LICENSE)
