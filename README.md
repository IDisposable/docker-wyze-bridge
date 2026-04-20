# Docker Wyze Bridge

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/idisposable/docker-wyze-bridge?logo=github)](https://github.com/idisposable/docker-wyze-bridge/releases/latest)
[![GHCR Package](https://img.shields.io/badge/ghcr-package-blue?logo=github)](https://ghcr.io/idisposable/docker-wyze-bridge)
[![Docker](https://github.com/IDisposable/docker-wyze-bridge/actions/workflows/docker-image.yml/badge.svg)](https://github.com/IDisposable/docker-wyze-bridge/actions/workflows/docker-image.yml)
[![Home Assistant Add-on](https://img.shields.io/badge/home_assistant-add--on-blue.svg?logo=homeassistant&logoColor=white)](./docs/user_guide/install_ha.md)

Local WebRTC / RTSP / HLS bridge for Wyze cameras. No modifications, no
special firmware, no cloud dependency for streaming (TUTK + OG cameras
stay entirely on-LAN; doorbell-lineage cameras use Wyze's own WebRTC
signaling but the media still flows directly to you).

Built in **pure Go** on top of [go2rtc](https://github.com/AlexxIT/go2rtc).
No Python, no binary SDKs, no C code beyond the `ffmpeg` used for
snapshot extraction and MP4 recording. Docker image ~65 MB.

![Supports amd64](https://img.shields.io/badge/amd64-yes-success.svg)
![Supports arm64v8](https://img.shields.io/badge/arm64v8-yes-success.svg)
![Supports arm32v7](https://img.shields.io/badge/arm32v7-yes-success.svg)
![Apple Silicon](https://img.shields.io/badge/apple_silicon-yes-success.svg)
![Home Assistant Add-on](https://img.shields.io/badge/home_assistant-add--on-blue.svg?logo=homeassistant&logoColor=white)

> [!IMPORTANT]
> You need a Wyze Developer API ID **and** Key, not just your account
> password. Get them from the [Wyze Developer Console](https://developer-api-console.wyze.com/#/apikey/view).

> [!WARNING]
> Do **not** forward the bridge's ports on your public router/firewall
> or enable DMZ access. The WebUI and RTSP endpoints are designed for
> local use. For remote access, use a VPN (Tailscale, Wireguard, OpenVPN)
> or put a reverse proxy with authentication in front.

> **Upgrading from the Python bridge (mrlt8 v2.x / earlier 3.x)?** See
> [MIGRATION.md](MIGRATION.md) for the env-var rename table, breaking
> changes, and dropped features.

## Screenshots

Click any thumbnail for the full-size image.

<table>
  <tr>
    <td align="center">
      <a href="DOCS/screenshots/01-All%20Cameras.png"><img src="DOCS/screenshots/01-All%20Cameras.png" alt="All Cameras" width="280"></a>
      <br><sub><b>All Cameras</b></sub>
    </td>
    <td align="center">
      <a href="DOCS/screenshots/02-One%20Camera.png"><img src="DOCS/screenshots/02-One%20Camera.png" alt="One Camera" width="280"></a>
      <br><sub><b>Single Camera</b></sub>
    </td>
    <td align="center">
      <a href="DOCS/screenshots/03-Metrics%20%281%29.png"><img src="DOCS/screenshots/03-Metrics%20%281%29.png" alt="Metrics (top)" width="280"></a>
      <br><sub><b>Metrics — top</b></sub>
    </td>
  </tr>
  <tr>
    <td align="center">
      <a href="DOCS/screenshots/04-Metrics%20%282%29.png"><img src="DOCS/screenshots/04-Metrics%20%282%29.png" alt="Metrics (bottom)" width="280"></a>
      <br><sub><b>Metrics — bottom</b></sub>
    </td>
    <td align="center">
      <a href="DOCS/screenshots/05-Prometheus.png"><img src="DOCS/screenshots/05-Prometheus.png" alt="Prometheus export" width="280"></a>
      <br><sub><b>Prometheus export</b></sub>
    </td>
    <td align="center">
      <a href="DOCS/screenshots/06-Metrics%20JSON.png"><img src="DOCS/screenshots/06-Metrics%20JSON.png" alt="Metrics JSON" width="280"></a>
      <br><sub><b>Metrics JSON</b></sub>
    </td>
  </tr>
</table>

## Camera Support

Three streaming paths, picked automatically per camera — you don't
configure the path, the bridge routes based on the model.

- **TUTK** — go2rtc's built-in `wyze://` source. Direct LAN P2P (or
  cloud-relayed P2P if the camera is remote) for most of the fleet.
- **Gwell P2P** — `gwell-proxy` sidecar handles Wyze's newer
  Gwell/IoTVideo LAN-direct UDP protocol. Enabled by default (set
  `GWELL_ENABLED=false` to opt out); the sidecar only actually spawns
  when an OG-family camera is discovered, so users without OG cameras
  pay zero cost.
- **WebRTC (KVS)** — go2rtc's native `#format=wyze` handler dials
  Wyze's `wyze-mars-webcsrv.wyzecam.com` signaling server itself and
  speaks AWS-KVS WebRTC. Used by doorbell-lineage hardware that skips
  both TUTK and Gwell P2P.

| Camera | Wyze ID | Path | Status |
| ------ | ------- | ---- | ------ |
| Wyze Cam V1 (HD only) | `WYZEC1` | TUTK | Should work |
| Wyze Cam V2 | `WYZEC1-JZ` | TUTK | Should work |
| Wyze Cam V3 | `WYZE_CAKP2JFUS` | TUTK | Confirmed |
| Wyze Cam V3 Pro (2K) | `HL_CAM3P` | TUTK | Should work |
| Wyze Cam V4 (2K) | `HL_CAM4` | TUTK | Confirmed |
| Wyze Cam Floodlight | `WYZE_CAKP2JFUS` | TUTK | Should work |
| Wyze Cam Floodlight V2 (2K) | `HL_CFL2` | TUTK | Should work |
| Wyze Cam Pan | `WYZECP1_JEF` | TUTK | Should work |
| Wyze Cam Pan V2 | `HL_PAN2` | TUTK | Should work |
| Wyze Cam Pan V3 | `HL_PAN3` | TUTK | Mostly working |
| Wyze Cam Pan Pro (2K) | `HL_PANP` | TUTK | Should work |
| Wyze Cam Outdoor | `WVOD1` | TUTK | Should work |
| Wyze Cam Outdoor V2 | `HL_WCO2` | TUTK | Should work |
| Wyze Cam Doorbell V1 | `WYZEDB3` | TUTK | Needs support in go2rtc |
| Wyze Cam Doorbell V2 (2K) | `HL_DB2` | TUTK | Confirmed |
| Wyze Cam Doorbell Pro | `GW_BE1` | WebRTC | **Confirmed 2026-04-20** |
| Wyze Cam Doorbell Duo | `GW_DBD` | WebRTC | Expected (same lineage) |
| Wyze Cam OG | `GW_GC1` | Gwell P2P | Expected (LAN-direct UDP) |
| Wyze Cam OG Telephoto 3X | `GW_GC2` | Gwell P2P | Expected (LAN-direct UDP) |
| Wyze Battery Cam Pro | `AN_RSCW` | — | Not supported |
| Wyze Cam Floodlight Pro (2K) | `LD_CFP` | — | Not supported |

"Expected" means the code path is plumbed and equivalent hardware is
known to work, but we don't own a unit to confirm end-to-end. Bug
reports welcome.

## Quick Start

```bash
docker run -p 5080:5080 -p 8554:8554 -p 8888:8888 -p 8889:8889 -p 8189:8189/udp \
  -e WYZE_EMAIL=you@example.com \
  -e WYZE_PASSWORD=yourpass \
  -e WYZE_API_ID=your-api-id \
  -e WYZE_API_KEY=your-api-key \
  idisposablegithub365/wyze-bridge:go
```

Then open `http://localhost:5080` for the WebUI.

## Docker Compose

```yaml
services:
  wyze-bridge:
    image: idisposablegithub365/wyze-bridge:go
    restart: unless-stopped
    ports:
      - 5080:5080            # WebUI + REST API
      - 8554:8554            # RTSP
      - 8888:8888            # HLS
      - 8889:8889            # WebRTC HTTP
      - 8189:8189/udp        # WebRTC ICE
    volumes:
      - ./config:/config                   # state, go2rtc.yaml
      - ./media:/media                     # snapshots + recordings land here
    environment:
      - WYZE_EMAIL=you@example.com
      - WYZE_PASSWORD=yourpass
      - WYZE_API_ID=your-api-id
      - WYZE_API_KEY=your-api-key
      # - BRIDGE_IP=192.168.1.50           # Required for WebRTC in-browser playback
      # - STREAM_AUTH=viewer:secret        # See Authentication section
      # - RECORD_ALL=true
      # - MQTT_HOST=homeassistant.local    # Enables MQTT auto-discovery
```

## Stream URLs

Each camera is available at:

| Protocol | URL |
| -------- | --- |
| RTSP | `rtsp://HOST:8554/<camera_name>` |
| HLS | `http://HOST:8888/<camera_name>` |
| WebRTC (browser) | `http://HOST:5080/camera/<camera_name>` |
| Snapshot (JPEG) | `http://HOST:5080/api/snapshot/<camera_name>` |

Camera names are normalized: lowercase, spaces replaced with underscores
(`Front Door` → `front_door`).

## Home Assistant

[![Open your Home Assistant instance and show the add add-on repository dialog with a specific repository URL pre-filled.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FIDisposable%2Fdocker-wyze-bridge)

Click the badge (on a machine that can reach your HA instance) to
open the add-on store with this repo pre-filled. Then pick a channel:
**Wyze Bridge** (stable, tracks `main`) or **Wyze Bridge (Edge)**
(experimental, tracks `dev`). Setup docs live next to each add-on:
[stable](home_assistant/wyze_bridge/DOCS.md) /
[edge](home_assistant/wyze_bridge_edge/DOCS.md). If you also have
the Mosquitto broker add-on, cameras appear automatically via MQTT
discovery.

Auto-created entities per camera:

- `camera.<mac>` — live stream + snapshot
- `switch.<mac>_audio`, `switch.<mac>_quality` — runtime controls
- `binary_sensor.<mac>_recording` — flips with the record button

Bridge-wide entities:

- `sensor.bridge_cameras` / `sensor.bridge_streaming` / `sensor.bridge_errored` — counts
- `sensor.bridge_uptime` — seconds since start
- `sensor.bridge_recordings_size` — total MP4 bytes
- `sensor.bridge_config_errors` — non-zero = check the `/metrics` page

The add-on's `run.sh` also drops an auto-generated Lovelace dashboard
at `/config/wyze_bridge_dashboard.yaml` at startup — add it as a
dashboard resource in HA and you get a ready-made view with glance
cards for every camera, no manual config.

## Configuration

Everything is controlled by environment variables. See
[MIGRATION.md](MIGRATION.md) for the full reference; the common ones:

| Variable | Required? | Description |
| -------- | --------- | ----------- |
| `WYZE_EMAIL` | yes | Wyze account email |
| `WYZE_PASSWORD` | yes | Wyze account password |
| `WYZE_API_ID` | yes | API ID from Wyze Developer Console |
| `WYZE_API_KEY` | yes | API Key from Wyze Developer Console |
| `WYZE_TOTP_KEY` | optional | TOTP secret for accounts with 2FA enabled |
| `BRIDGE_IP` | for WebRTC | Host IP that browsers reach the bridge at; injected as a WebRTC ICE candidate |
| `MQTT_HOST` | for MQTT | MQTT broker hostname (enables MQTT) |
| `LOG_LEVEL` | no | `trace` / `debug` / `info` / `warn` / `error` (default `info`) |

### Per-Camera Overrides

Uppercase the normalized camera name, then prefix:

```bash
QUALITY_FRONT_DOOR=sd      # hd (default) or sd
AUDIO_BACKYARD=false       # audio off for this camera
RECORD_GARAGE=true         # recording on for just this camera
```

### Camera Filtering

```bash
FILTER_NAMES=Front Door,Backyard    # Include these cameras only
FILTER_BLOCKS=true                  # Or exclude the listed ones
FILTER_MODELS=WYZECP1_JEF           # By model code (comma-separated)
FILTER_MACS=AA:BB:CC:DD:EE:FF       # By MAC
```

### Snapshots

JPEG frames grabbed periodically or on sunrise/sunset events via go2rtc's
frame API.

```bash
SNAPSHOT_INTERVAL=60s                       # periodic capture interval; 0 disables
SNAPSHOT_PATH=/media/snapshots/{cam_name}/%Y-%m-%d
SNAPSHOT_FILE_NAME=%H-%M-%S                 # no extension; .jpg is appended
SNAPSHOT_KEEP=14d                           # retention; 0 = keep forever
SNAPSHOT_CAMERAS=front_door,backyard        # optional allowlist
LATITUDE=37.7749                            # enables sunrise/sunset capture
LONGITUDE=-122.4194
```

### Recording

`ffmpeg -c copy -f segment` per recording-enabled camera, pulling from
our own RTSP endpoint on loopback. Runs only while the camera is
streaming; stops automatically when the camera goes offline.

```bash
RECORD_ALL=true                                 # or RECORD_<CAM>=true
RECORD_PATH=/media/recordings/{cam_name}/%Y/%m/%d
RECORD_FILE_NAME=%H-%M-%S                       # no extension; .mp4 is appended
RECORD_LENGTH=60s                               # segment duration
RECORD_KEEP=7d                                  # retention; 0 = keep forever
```

#### Custom ffmpeg command (`RECORD_CMD` / `RECORD_CMD_<CAM>`)

Replace the built-in ffmpeg argv entirely when you need a different
container, hardware encoder, metadata overlay, or anything else:

```bash
# Global — all recording-enabled cameras use this
RECORD_CMD=ffmpeg -hide_banner -rtsp_transport tcp -i {rtsp_url} -c:v h264_nvenc -preset p6 -b:v 4M -f segment -segment_time {segment_sec} -strftime 1 {output_stem}.webm

# Or per-camera — this wins over the global for one camera
RECORD_CMD_FRONT_DOOR=sh -c 'ffmpeg -i {rtsp_url} -c copy {output} | tee /mnt/archive/front_door.mp4'
```

Supported tokens:

| Token | Value |
| --- | --- |
| `{cam_name}` / `{CAM_NAME}` | Normalized camera name (lower/upper) |
| `{rtsp_url}` | `rtsp://127.0.0.1:8554/<cam_name>` — our loopback RTSP |
| `{rtsp_host}` / `{rtsp_port}` | Host + port separately, for custom URL schemes |
| `{output}` | Expanded `RECORD_PATH/RECORD_FILE_NAME.mp4` |
| `{output_dir}` / `{output_stem}` | Split of the above — stem is everything before `.mp4` so you can write a different extension |
| `{segment_sec}` | `RECORD_LENGTH` in integer seconds |
| `{quality}` | `hd` or `sd` per camera |

**Invocation semantics:**

- The template is tokenized respecting double/single quotes (no shell
  by default, no variable expansion, no pipes). The program in the
  first position is `exec`'d directly.
- For pipes / shell features, prefix with `sh -c "..."` — you're
  opting in explicitly.
- Unknown tokens (like `{typo_here}`) fail parsing at the first spawn
  attempt; the bridge falls back to the built-in default argv for that
  camera and raises a config error visible on `/metrics` and
  `/api/health`. Recording for other cameras is unaffected.

### Authentication

- `BRIDGE_AUTH=true` + `BRIDGE_USERNAME` + `BRIDGE_PASSWORD` — gates the
  WebUI + REST API.
- `STREAM_AUTH=viewer:secret` — RTSP / HLS / WebRTC streams require
  these credentials. Per-camera auth is not supported in this rewrite.

## Ports

| Port | Purpose |
| ---- | ------- |
| 5080 | Bridge WebUI + REST API |
| 1984 | go2rtc native UI (optional; useful for probing/debugging) |
| 8554 | RTSP |
| 8888 | HLS |
| 8889 | WebRTC HTTP |
| 8189/udp | WebRTC ICE |

## Observability

The bridge exposes several operational endpoints at `http://HOST:5080`:

| Path | Purpose |
| ---- | ------- |
| `/metrics` | Human-readable HTML dashboard: issues panel, bridge summary, per-camera table, Wyze cloud API call stats, storage footprint, recent events log. Auto-refreshes every 10 seconds. |
| `/metrics.prom` | Prometheus-format exposition. Gauges + counters under the `wyze_bridge_` prefix with labels for camera, protocol, endpoint. Unauthenticated by default so Grafana / VictoriaMetrics scrapers work without any extra config. |
| `/api/metrics` | Same snapshot as `/metrics` but as JSON — convenient for scripting or piping into other tooling. |
| `/api/health` | Compact health check: `{status, version, uptime, config_errors, issues}`. `status` flips to `"degraded"` whenever the issues list is non-empty, which maps cleanly to a HA `binary_sensor`. |
| `/dashboard.yaml` | Auto-generated Home Assistant Lovelace dashboard referencing the MQTT discovery entities the bridge publishes — one glance card per camera with snapshot, recording indicator, and audio/quality controls. See [Home Assistant](#home-assistant) below. |

### Recording controls

Every camera card in the WebUI (and the single-camera page) has a
record pulse button. Clicking starts a per-camera ffmpeg segment
writer pulling from loopback RTSP; clicking again stops it. The same
toggle is available over:

- MQTT: `<topic>/<cam>/recording` (ON/OFF, retained). HA discovery
  publishes a `binary_sensor` by default.
- REST: `POST /api/cameras/<name>/record` with body
  `{"action":"start"|"stop"}` (empty body = start). Returns the
  resulting state, so a client can render from the authoritative
  answer rather than optimistically.

Whatever toggles recording fires an SSE `recording_state` event, so
all open browser tabs + the metrics page update live.

### Configuration issues surface on `/metrics`

When a subsystem hits a soft failure — a bad `RECORD_PATH` template,
an unreachable MQTT broker, a future `RECORD_CMD` with an unknown
token — it reports into the process-wide issues registry instead of
silently logging a warning. The metrics page renders open issues in a
red panel at the top; `/api/health` includes a `config_errors` count;
the MQTT `wyze_bridge/bridge/config_errors` topic lets HA wire an
alert on it.

## FAQ

**How does this work?**
For TUTK cameras, go2rtc speaks Wyze's P2P protocol directly (no SDK).
For OG cameras, a small `gwell-proxy` sidecar handles the Gwell/IoTVideo
handshake. For doorbell-lineage cameras (Doorbell Pro, Doorbell Duo),
go2rtc's built-in Wyze WebRTC handler calls our internal shim for the
KVS signaling URL and dials Wyze's server itself. The bridge
orchestrates auth, discovery, MQTT, and the WebUI.

**Does it use internet bandwidth when streaming?**
For TUTK and Gwell cameras on the same LAN: effectively zero — media
flows directly between the camera and the bridge over UDP. For WebRTC
cameras (Doorbell Pro / Duo): the signaling goes through Wyze's cloud,
and media may route through Wyze's AWS TURN servers if the direct path
can't be negotiated.

**Can this work offline?**
Streaming alone can survive without internet for TUTK/Gwell cameras
already paired, but tokens and some commands eventually need the cloud.
WebRTC cameras need the internet every session to fetch the signaling
URL from Wyze.

**Why isn't `GW_BE1` / `GW_DBD` "supported" on upstream mrlt8?**
Upstream is TUTK-only; doorbell-lineage cameras don't expose TUTK at
all. We handle them via Wyze's cloud WebRTC path, which upstream
doesn't implement.

**What about Battery Cam Pro?**
Not yet. Uses a different protocol than any of the three we handle.

## Architecture

```goat
Docker Container
├── wyze-bridge (Go binary, port 5080)
│   ├── Wyze API Client        — auth, camera discovery, get_streams
│   ├── go2rtc Manager         — subprocess, skeletal YAML, AddStream API
│   ├── Camera Manager         — state machines, reconnection, routing
│   ├── MQTT Client            — HA discovery, commands, state
│   ├── WebUI Server           — REST API, SSE, embedded UI, WebRTC shim
│   ├── Snapshot Manager       — interval, sunrise/sunset, pruning
│   └── Recording Manager      — ffmpeg per camera, segment pruning
├── go2rtc (managed sidecar, ports 1984 / 8554 / 8888 / 8889 / 8189)
│   ├── wyze://   source       — TUTK P2P (built in)
│   ├── rtsp:     source       — receives publish from gwell-proxy
│   └── webrtc:   source       — native Wyze KVS (via our shim)
└── gwell-proxy (only spawned for OG cameras)
    └── Gwell P2P → ffmpeg → RTSP PUBLISH to loopback go2rtc
```

## Development

```bash
go test ./...                     # run tests
go build -o wyze-bridge ./cmd/wyze-bridge
docker compose up --build         # Docker build + run
./cycle.sh                        # local dev cycle with .env.dev
```

See [DEVELOPER.md](DEVELOPER.md) for local dev / devcontainer setup.

## Attribution

This fork builds on work from several upstream projects:

- `idisposable/docker-wyze-bridge`
- `akeslo/docker-wyze-bridge`
- `kroo/wyzecam`
- `AlexxIT/go2rtc`
- `wlatic/hacky-wyze-gwell`
- `idisposable/docker-wyze-bridge`

The `go2rtc` sidecar uses [github.com/AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc), which is licensed under MIT.
The `gwell` sidecar uses a vendored copy of the Go protocol packages from [github.com/wlatic/hacky-wyze-gwell](https://github.com/wlatic/hacky-wyze-gwell), which is licensed under MIT.

 See [THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md).

## License

[GNU AGPL v3](LICENSE)
