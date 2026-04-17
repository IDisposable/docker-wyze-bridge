# Migration Guide: Python Bridge → Go Rewrite

This guide is for existing `docker-wyze-bridge` users upgrading from the Python-based bridge to the Go rewrite.

## Quick Start

Change your Docker image and you're done. All core environment variables are the same.

```yaml
# Before (Python)
image: idisposablegithub365/wyze-bridge:latest

# After (Go rewrite)
image: idisposablegithub365/wyze-bridge:go
```

## What Works the Same

- **Wyze credentials:** `WYZE_EMAIL`, `WYZE_PASSWORD` — unchanged
- **API credentials:** `API_ID` and `API_KEY` still work (new canonical names: `WYZE_API_ID`, `WYZE_API_KEY`)
- **MQTT:** `MQTT_HOST`, `MQTT_PORT`, `MQTT_USERNAME`, `MQTT_PASSWORD`, `MQTT_TOPIC`, `MQTT_DTOPIC` — unchanged
- **Home Assistant MQTT discovery** — same entity types (camera, quality select, audio switch, night vision select)
- **Camera filtering:** `FILTER_NAMES`, `FILTER_MODELS`, `FILTER_MACS`, `FILTER_BLOCKS` — unchanged
- **Per-camera overrides:** `QUALITY_{CAM_NAME}`, `AUDIO_{CAM_NAME}`, `RECORD_{CAM_NAME}` — unchanged
- **Recording:** `RECORD_ALL`, `RECORD_PATH`, `RECORD_FILE_NAME`, `RECORD_LENGTH`, `RECORD_KEEP` — unchanged
- **Snapshots:** `SNAPSHOT_INT`, `SNAPSHOT_FORMAT`, `SNAPSHOT_CAMERAS`, `SNAPSHOT_KEEP`, `IMG_DIR` — unchanged
- **WebUI auth:** `WB_AUTH`, `WB_USERNAME`, `WB_PASSWORD` — unchanged
- **Network:** `WB_IP` — unchanged (still required for WebRTC)
- **Logging:** `LOG_LEVEL` — unchanged
- **Docker secrets:** `/run/secrets/{VAR_NAME}` — unchanged
- **HA add-on YAML:** `config.yml` with `CAM_OPTIONS` — unchanged
- **Ports:** 5080 (WebUI, changed from 5000 to avoid Frigate conflict), 8554 (RTSP), 8888 (HLS), 8889 (WebRTC HTTP), 8189/udp (WebRTC ICE)

## What's New

| Feature | Details |
| --------- | --------- |
| **Port 1984** | go2rtc native UI. Optional — power users can access go2rtc directly. The bridge WebUI at port 5080 is the primary interface. |
| **Smaller image** | ~65 MB vs ~200+ MB. No Python, no binary SDK. FFmpeg is included only for go2rtc's JPEG snapshot endpoint + per-camera recording. |
| **Faster startup** | No pip install, no ffmpeg probe chain. Cold start ~15-20s. |
| **JSON state file** | Auth and camera data persisted to `$STATE_DIR/wyze-bridge.state.json` instead of pickle files. Survives restarts. |
| **SSE real-time updates** | WebUI updates via Server-Sent Events instead of polling. |
| **MFA/TOTP support** | Set `WYZE_TOTP_KEY` to your authenticator app's secret for 2FA login. |
| **Gwell / WebRTC support** | OG-family Gwell cameras (GW_GC1/GC2) + Doorbell lineage (GW_BE1 / GW_DBD) now work end-to-end. See [Gwell Cameras Now Supported](#new-gwell-cameras-now-supported). |
| **Record start/stop in the UI** | Every camera card has a record button. Click starts per-camera `ffmpeg -f segment`; click again stops it. Also flipped via MQTT or `POST /api/cameras/<name>/record`. |
| **Observability surfaces** | `/metrics` HTML dashboard, `/metrics.prom` Prometheus exposition, `/api/metrics` JSON, expanded `/api/health`, auto-generated `/dashboard.yaml` for Home Assistant. |
| **MQTT metric topics** | Bridge-wide gauges at `<topic>/bridge/*` (uptime, camera counts, config errors, recordings size) + per-camera `<topic>/<cam>/recording`. All auto-created via HA discovery. |

## Breaking Changes

### Removed: Remote P2P

The Python bridge sometimes supported remote P2P streaming (camera on a different network). The Go rewrite is **LAN-only** — your cameras and the bridge must be on the same network subnet.

**If you need remote access:** Use a VPN (Tailscale, WireGuard, OpenVPN). Sample compose files for Tailscale and OpenVPN are provided.

### Removed: `MTX_*` Environment Variables

MediaMTX has been replaced by go2rtc. All `MTX_*` variables are silently ignored. go2rtc's streaming configuration is managed automatically.

### Removed: `ON_DEMAND` Environment Variable

All cameras are connected eagerly at startup. The `ON_DEMAND` env var is ignored. This is faster and more reliable — cameras are immediately available when you open a stream.

### Removed: Unraid Template

The Unraid community app template (`unraid/wyze-bridge.xml`) is no longer maintained. Use the standard Docker Compose setup or install manually via Unraid's Docker UI.

### Removed: FFmpeg-based Features

The bridge no longer invokes FFmpeg directly. Features that depended on bridge-controlled FFmpeg subprocesses (custom transcoding, audio re-encoding, RTSP firmware proxy, FFmpeg flag overrides) are not available. go2rtc handles codec negotiation natively and uses its own bundled ffmpeg only for the JPEG snapshot endpoint (`/api/frame.jpeg`).

Ignored vars: `FFMPEG_CMD`, `FFMPEG_FLAGS`, `FFMPEG_LOGLEVEL`, `AUDIO_CODEC`, `AUDIO_FILTER`, `H264_ENC`, `FORCE_ENCODE`.

### Changed: `STREAM_AUTH` Behavior

The Python bridge supported per-camera stream credentials:

```bash
STREAM_AUTH=user1:pass1@cam1,cam2|user2:pass2@cam3
```

The Go rewrite supports **global stream credentials only**:

```bash
STREAM_AUTH=user:pass
```

go2rtc's RTSP server uses a single username/password for all streams. Per-camera credential scoping is not supported. If you set a multi-user `STREAM_AUTH`, only the first user's credentials are applied globally.

**Workaround:** Use your reverse proxy (nginx, Traefik) for per-path access control if needed.

### Changed: WebUI Appearance

The WebUI is a complete rewrite — dark theme, grid layout, WebRTC player via go2rtc. The URL structure is similar, but the default port moved from 5000 to **5080** (to avoid conflicting with Frigate, AirPlay, and Flask's dev default), and the look and feel is different. Override with `WB_PORT` if you need 5000 back.

### Changed: Env Variable Renames (4.0 reorg)

4.0 reorganizes the env-var namespace for clarity. Names are grouped by subsystem (Wyze / Bridge / Camera / Snapshot / Record / MQTT / Filter / Location / …) and the HA addon schema is nested under those same groups (Configuration tab shows collapsible sections). **No aliases** — old names are silently ignored; update your docker-compose / env file.

| Old (3.x) | New (4.0) | Notes |
| ----------- | ----------- | ------- |
| `TOTP_KEY` | `WYZE_TOTP_KEY` | grouped with Wyze creds |
| `WB_IP` | `BRIDGE_IP` | bridge HTTP server group |
| `WB_PORT` | `BRIDGE_PORT` | same |
| `WB_AUTH` | `BRIDGE_AUTH` | same |
| `WB_USERNAME` | `BRIDGE_USERNAME` | same |
| `WB_PASSWORD` | `BRIDGE_PASSWORD` | same |
| `WB_API` | `BRIDGE_API_TOKEN` | "api" was ambiguous; now clearly a bearer token |
| `IMG_DIR` | `SNAPSHOT_PATH` | parallels `RECORD_PATH` |
| `SNAPSHOT_INT` | `SNAPSHOT_INTERVAL` | "INT" was cryptic |
| `SNAPSHOT_FORMAT` | **removed** | split into `SNAPSHOT_PATH` + `SNAPSHOT_FILE_NAME` |
| `MQTT_DTOPIC` | `MQTT_DISCOVERY_TOPIC` | "DTOPIC" was opaque |

Unchanged: `WYZE_EMAIL`/`PASSWORD`/`API_ID`/`API_KEY`, `STREAM_AUTH`, `QUALITY`, `AUDIO`, all `MQTT_*` (except DTOPIC), `FILTER_*`, all `RECORD_*`, `LATITUDE`/`LONGITUDE`, `WEBHOOK_URLS`, `LOG_LEVEL`, `FORCE_IOTC_DETAIL`, `STATE_DIR`, `STUN_SERVER`, `GWELL_*`. Per-camera overrides (`QUALITY_<CAM>`, `AUDIO_<CAM>`, `RECORD_<CAM>`) also unchanged.

### External go2rtc Mode

Set `GO2RTC_URL=http://host:1984` (or `bridge.go2rtc_url` in the HA addon) to have the bridge feed into an existing go2rtc instance — useful when you're already running go2rtc for Frigate, Scrypted, or another consumer and don't want two copies fighting. When set:

- The bridge **does not spawn** its own go2rtc subprocess.
- The bridge **does not write** a `go2rtc.yaml`.
- Streams are registered via the remote go2rtc's `/api/streams` endpoint (same code path as embedded mode).
- **Recording (`RECORD_*`) is ignored** — the remote yaml controls recording; a warning is logged at startup if you have `RECORD_ALL=true` or any `RECORD_<CAM>=true` while `GO2RTC_URL` is set.
- **`STREAM_AUTH` is ignored** — same reason; configure RTSP auth on the remote.
- Stream name collisions: if the remote go2rtc already has a stream named `front_door`, our `PUT /api/streams?name=front_door` overwrites it. Namespace your Wyze camera names (or the remote's) if this is a concern.

The bridge probes the URL with a `ListStreams` call at startup and fails fast if unreachable, so a bad URL shows up as a clear boot error instead of silent "no cameras."

### Changed: Default On-Disk Layout

Bare-Docker defaults collapsed to a single `/media` volume:

| | 3.x | 4.0 |
| --- | --- | --- |
| snapshots | `/img/{cam_name}.jpg` (flat, overwrites) | `/media/snapshots/{cam_name}/%Y-%m-%d/%H-%M-%S.jpg` (structured, time-lapse) |
| recordings | `/record/{cam_name}/%Y/%m/%d/%H-%M-%S.mp4` | `/media/recordings/{cam_name}/%Y/%m/%d/%H-%M-%S.mp4` |
| docker-compose volumes | `./img:/img`, `./record:/record` | `./media:/media` |
| VOLUME directive | `/config`, `/img`, `/record` | `/config`, `/media` |

Single-volume mount is the main win — one host directory holds everything. Override any of `SNAPSHOT_PATH`, `SNAPSHOT_FILE_NAME`, `RECORD_PATH` to keep the 3.x layout if you need to.

HA addon unaffected — it continues to write under `/media/wyze_bridge/` (scoped sub-path because HA's `/media` is shared across addons).

### Changed: Snapshot Path Layout

The single-field `SNAPSHOT_FORMAT` is gone. Two replacement fields mirror the recording side:

| Field | Purpose | Tokens | Default (bare Docker) | Default (HA add-on) |
| ------- | --------- | -------- | ----------------------- | --------------------- |
| `SNAPSHOT_PATH` | directory template | `{cam_name}`, `{CAM_NAME}`, `%Y %m %d %H %M %S %s` | `/img` | `/media/wyze_bridge/snapshots/{cam_name}/%Y/%m/%d` |
| `SNAPSHOT_FILE_NAME` | filename template; `.jpg` auto-appended | same tokens | `{cam_name}` | `%H-%M-%S` |

The bridge `MkdirAll`s the full parent chain, so nested strftime subdirs in either field Just Work.

**HA users:** snapshots now land under `/media/wyze_bridge/snapshots/<cam>/<YYYY>/<MM>/<DD>/<HH-MM-SS>.jpg` by default — a real time-lapse instead of one overwriting file — parallel to recordings at `/media/wyze_bridge/recordings/...`.

### Changed: State Persistence

Python bridge: pickle files in `/tokens/`
Go rewrite: JSON file at `$STATE_DIR/wyze-bridge.state.json` (default: `/config/wyze-bridge.state.json`)

Old pickle files are not migrated. The bridge will re-authenticate and re-discover cameras on first run.

### New: Gwell Cameras Now Supported

Previously unsupported in the Python bridge, now handled in two ways:

- **OG family** (`GW_GC1`, `GW_GC2`) — `gwell-proxy` sidecar speaks
  Gwell P2P directly (LAN-direct UDP) and republishes to go2rtc via
  RTSP. Enabled by default; the sidecar only spawns when an OG camera
  is actually discovered, so users without OG cameras pay zero cost.
  Set `GWELL_ENABLED=false` to opt out.
- **Doorbell lineage** (`GW_BE1` Doorbell Pro, `GW_DBD` Doorbell Duo)
  — go2rtc's native `#format=wyze` source dials Wyze's
  `wyze-mars-webcsrv.wyzecam.com` WebRTC signaling server itself; our
  shim provides the per-camera signaling URL and ICE servers. No
  sidecar.

### Not Supported

Battery Cam Pro, Floodlight Pro (LD_CFP) — different protocol than
either of the above.

## Default Behavior Changes (4.0)

The rewrite is a good opportunity to pick sensible defaults. If you
rely on an earlier behavior, set the env var explicitly.

| Variable | 3.x default | 4.0 default | Reason |
| -------- | ----------- | ----------- | ------ |
| `GWELL_ENABLED` | n/a (Gwell cameras unsupported) | `true` | Zero cost when no OG camera is present; enables doorbell OGs out-of-the-box |
| `SNAPSHOT_PATH` | `/img` (flat, one overwritten JPEG per camera) | `/media/snapshots/{cam_name}/%Y-%m-%d` (time-lapse archive) | Consumers that need "latest frame" should query `/api/snapshot/<cam>` — filesystem archive is more useful for most users |
| `SNAPSHOT_FILE_NAME` | n/a (filename fixed) | `%H-%M-%S` | Time-of-day stems, date comes from the path |
| `RECORD_PATH` | `/record/{cam_name}/%Y/%m/%d` | `/media/recordings/{cam_name}/%Y/%m/%d` | Consolidated under a single `/media` volume mount |
| Recording/snapshot strftime timezone | local (container TZ) | **UTC** | DST spring-forward created a missing hour of recordings, fall-back created name clashes. UTC paths are unambiguous; browsers render JSON timestamps in local time for free via RFC3339 `Z` suffix |
| Recording mechanism | MediaMTX `record:` block in mediamtx.yml | `ffmpeg -c:v copy -c:a aac -f segment` per camera, started by the bridge on `StateStreaming` | MediaMTX is gone; go2rtc doesn't support the YAML `record:` property we'd have used. Audio is transcoded to AAC because Wyze TUTK delivers PCM s16be which mp4 rejects under `-c copy` |
| Network mode | remote P2P worked via cloud relay | LAN-only, all P2P | Remote P2P dropped with the Python/TUTK-SDK removal — use a VPN |
| WebUI auth | `WB_AUTH=true` with auto-generated password | `BRIDGE_AUTH=false` (no gate) — pending review | Being raised for discussion; a follow-up commit may flip this |

## Environment Variable Reference

### New Variables

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `WYZE_API_ID` | — | Canonical name for API ID (alias: `API_ID`) |
| `WYZE_API_KEY` | — | Canonical name for API Key (alias: `API_KEY`) |
| `WYZE_TOTP_KEY` | — | TOTP secret for MFA login (renamed from `TOTP_KEY` in 4.0) |
| `STATE_DIR` | `/config` | Directory for state file and go2rtc config |
| `STUN_SERVER` | `stun:stun.l.google.com:19302` | STUN server for WebRTC |
| `BRIDGE_API_TOKEN` | — | Bearer token for REST API access (renamed from `WB_API` in 4.0) |
| `FORCE_IOTC_DETAIL` | `false` | Verbose TUTK/go2rtc logging |

### Ignored Variables (silently dropped)

`MTX_*`, `ON_DEMAND`, `CONNECT_TIMEOUT`, `OFFLINE_ERRNO`, `IGNORE_OFFLINE`, `SUBSTREAM`, `RTSP_FW`, `LLHLS`, `SUBJECT_ALT_NAME`, `FRESH_DATA`, `SUPERVISOR_TOKEN`
