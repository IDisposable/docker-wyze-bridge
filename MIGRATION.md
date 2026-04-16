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
| **Smaller image** | ~65 MB vs ~200+ MB. No Python, no binary SDK. FFmpeg is included only for go2rtc's JPEG snapshot endpoint; the bridge itself never invokes it. |
| **Faster startup** | No pip install, no ffmpeg probe chain. Cold start ~15-20s. |
| **JSON state file** | Auth and camera data persisted to `$STATE_DIR/wyze-bridge.state.json` instead of pickle files. Survives restarts. |
| **SSE real-time updates** | WebUI updates via Server-Sent Events instead of polling. |
| **MFA/TOTP support** | Set `TOTP_KEY` to your authenticator app's secret for 2FA login. |

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

### Changed: State Persistence

Python bridge: pickle files in `/tokens/`
Go rewrite: JSON file at `$STATE_DIR/wyze-bridge.state.json` (default: `/config/wyze-bridge.state.json`)

Old pickle files are not migrated. The bridge will re-authenticate and re-discover cameras on first run.

### Not Supported: Gwell Protocol Cameras

Same as before — OG, Doorbell Pro, Pan v4, Battery Cam Pro use the Gwell protocol and are not supported.

## Environment Variable Reference

### New Variables

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `WYZE_API_ID` | — | Canonical name for API ID (alias: `API_ID`) |
| `WYZE_API_KEY` | — | Canonical name for API Key (alias: `API_KEY`) |
| `TOTP_KEY` | — | TOTP secret for MFA login |
| `STATE_DIR` | `/config` | Directory for state file and go2rtc config |
| `STUN_SERVER` | `stun:stun.l.google.com:19302` | STUN server for WebRTC |
| `WB_API` | — | Bearer token for REST API access |
| `FORCE_IOTC_DETAIL` | `false` | Verbose TUTK/go2rtc logging |

### Ignored Variables (silently dropped)

`MTX_*`, `ON_DEMAND`, `CONNECT_TIMEOUT`, `OFFLINE_ERRNO`, `IGNORE_OFFLINE`, `SUBSTREAM`, `RTSP_FW`, `LLHLS`, `SUBJECT_ALT_NAME`, `FRESH_DATA`, `SUPERVISOR_TOKEN`
