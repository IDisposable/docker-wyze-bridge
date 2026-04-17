# wyze-bridge-go: Design Document

**Project:** Complete reimplementation of `IDisposable/docker-wyze-bridge` in Go  
**Source repo:** https://github.com/IDisposable/docker-wyze-bridge (replaced in-place on new branch `go-rewrite`)  
**Status:** First phase testing  
**Date:** April 2026

---

## 1. Executive Summary

The existing `docker-wyze-bridge` is a Python application that wraps:

- A proprietary TUTK binary SDK (`.so` file, platform-specific, Wyze-controlled)
- MediaMTX (Go binary sidecar) for RTSP/WebRTC/HLS streaming
- A Flask WebUI
- An MQTT client
- A Wyze cloud API client

Wyze's changes to their camera access model have broken or destabilized the Python bridge for many users. The `go2rtc` project merged a pure-Go TUTK P2P implementation (v1.9.14, January 2026) with no binary SDK dependency that handles modern Wyze firmware.

**The plan:** Rewrite entirely in Go. go2rtc runs as a managed sidecar and handles all TUTK streaming. The new Go binary owns camera discovery, authentication, MQTT, configuration, recording, and a fresh WebUI. No Python. No binary SDK. Docker image under 50MB. Drop-in environment-variable replacement for existing users.

---

## 2. Confirmed Design Decisions

| Decision | Choice | Rationale |
| ---------- | -------- | ----------- |
| go2rtc relationship | Sidecar (managed subprocess) | `internal/` packages not importable; stable API surface |
| Logging library | **zerolog** (`github.com/rs/zerolog`) | Zero-allocation, structured, fast |
| WebUI | **Rewrite from scratch** | Existing Flask/Jinja UI is not worth porting |
| Port 1984 | Exposed but not user-facing | Power users get go2rtc native UI; bridge UI stays at 5080 |
| Recording | **Full feature, Phase 2, high priority** | go2rtc handles backend |
| Repo strategy | **Replace in-place on branch `go-rewrite`** | Preserve stars/issues/history |
| P2P mode | LAN-only | go2rtc constraint; VPN path documented |

---

## 3. Background

### 3.1 What Wyze Changed

1. **Authentication:** Shifted to API Key + API ID; deprecated direct email/password SDK access.
2. **Firmware-level encryption:** Newer firmwares require DTLS 1.2 (ChaCha20-Poly1305). The old binary SDK cannot negotiate it.
3. **Remote P2P:** Cloud relay stopped working reliably. LAN mode still works.
4. **Binary SDK lifecycle:** The TUTK `.so` is a black box. Backend changes silently break it.

### 3.2 Why go2rtc Solves This

PR #2011 by @seydx (merged Jan 18, 2026, released in v1.9.14) implemented TUTK IOTC P2P from scratch in Go via protocol reverse engineering:

- DTLS 1.2 with ChaCha20-Poly1305 (what modern firmware requires)
- No Wyze or TUTK binary dependency
- Handles Wyze cloud API for camera credential discovery
- H.264/H.265 video, AAC/PCM/PCMU/PCMA/Opus audio
- Two-way audio (intercom) via WebRTC
- Local P2P only (camera and go2rtc must be on same LAN subnet)

**Confirmed working as of go2rtc v1.9.14:**

- Wyze Cam V3 (WYZE_CAKP2JFUS) — fw 4.36.14.3497+
- Wyze Cam V4 (HL_CAM4) — fw 4.52.9.4188+ and 4.52.9.5332+
- Wyze Cam Doorbell v2 (HL_DB2) — fw 4.51.3.4992+
- Wyze Cam Pan v3 (HL_PAN3) — mostly working, some UDP intermittency

**Needs hardware test before Phase 1 commit:**

- Wyze Cam Doorbell v1 (WYZEDB3) — TUTK protocol, DTLS support unconfirmed. See `DOORBELL_TEST.md`.

**Not supported (Gwell protocol, different implementation needed):**

- Wyze Cam OG, Doorbell Pro, Pan v4, Battery Cam Pro

---

## 4. Architecture

### 4.1 Overview

```goat
Docker Container
├── wyze-bridge (Go binary — our code)
│   ├── Wyze API Client      — auth, token refresh, camera discovery, commands
│   ├── go2rtc Manager       — subprocess start/stop, config gen, API client
│   ├── Camera Manager       — per-camera state machines, reconnection
│   ├── MQTT                 — publish state/info, subscribe to commands, HA discovery
│   ├── WebUI                — HTTP server, REST API, SSE, fresh UI (port 5080)
│   ├── Snapshot Manager     — periodic capture, sunrise/sunset, pruning
│   ├── Recording Manager    — config gen, segment pruning
│   └── Config               — env vars, config.yml, Docker secrets
└── go2rtc (Go binary — managed sidecar)
    ├── Wyze TUTK P2P source — pure Go, no binary SDK, no Python
    ├── RTSP server    :8554
    ├── WebRTC API/UI  :1984  (exposed; used by bridge WebUI player)
    ├── WebRTC ICE     :8889 / :8189 UDP
    └── HLS            :8888
```

### 4.2 What Is Replaced

| Component removed | Replacement |
| ------------------- | ------------- |
| Python + pip | Nothing — Go binary |
| `wyzecam` Python library | `internal/wyzeapi/` |
| TUTK binary SDK `.so` | go2rtc's `pkg/wyze/tutk` (pure Go) |
| MediaMTX | go2rtc (handles both input AND output) |
| Flask WebUI | `internal/webui/` (Go net/http + embedded assets) |
| FFmpeg subprocesses | Not needed — go2rtc reads camera directly |

The Python→FFmpeg→MediaMTX pipe that caused most latency and instability is entirely eliminated.

### 4.3 go2rtc Sidecar Strategy

go2rtc uses `internal/` packages (not importable) and was designed as a standalone application. Managing it as a subprocess is the correct integration strategy — identical in pattern to how the Python bridge managed MediaMTX.

The key difference from the current setup: MediaMTX handled streaming *output* while Python/TUTK handled *input*. go2rtc handles **both**. Our bridge only orchestrates.

Interaction with go2rtc happens via its HTTP API on `:1984`. Stream URLs (`wyze://...`) are added/removed dynamically via API; no restart needed when cameras change.

---

## 5. Component Design

### 5.1 Wyze API Client (`internal/wyzeapi/`)

#### 5.1.1 Authentication

```go
type Credentials struct {
    Email    string
    Password string   // plain or "md5:" prefixed triple-hash
    APIID    string
    APIKey   string
    TOTPKey  string   // optional
}

type AuthState struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token"`
    UserID       string    `json:"user_id"`
    ExpiresAt    time.Time `json:"expires_at"`
}
```

Auth endpoint: `POST https://auth-prod.api.wyze.com/api/user/login`

Requests must include an HMAC signature derived from the API Key/ID. The exact signing scheme is documented in the existing Python `wyze_api.py` and must be ported to Go.

Token is refreshed when `ExpiresAt - 5 minutes < now`. `AuthState` is persisted to `$STATE_DIR/auth.json` to survive container restarts without re-auth.

#### 5.1.2 Camera Discovery

```go
type CameraInfo struct {
    Name        string  // user-assigned display name
    Nickname    string  // normalized: lowercase, spaces→underscores, url-safe
    Model       string  // e.g. "WYZE_CAKP2JFUS", "WYZEDB3", "HL_CAM4"
    MAC         string  // uppercase, no separators: "AABBCCDDEEFF"
    LanIP       string  // current LAN IP
    P2PID       string  // uid: 20-char P2P identifier
    ENR         string  // encryption key (XOR-decoded from Wyze obfuscation)
    DTLS        bool    // true for modern firmware
    FWVersion   string
    Online      bool
    ProductType string
}

func (c CameraInfo) StreamURL(quality string) string {
    return fmt.Sprintf(
        "wyze://%s?uid=%s&enr=%s&mac=%s&model=%s&subtype=%s&dtls=%v",
        c.LanIP, c.P2PID, url.QueryEscape(c.ENR),
        c.MAC, c.Model, quality, c.DTLS,
    )
}
```

Device list: `POST https://api.wyzecam.com/app/v2/home_page/get_object_list`

P2P params: `POST https://api.wyzecam.com/app/v2/device/get_property_list`

The `ENR` value is stored obfuscated in the Wyze API response. The decode logic (XOR with a key derived from the device MAC and a Wyze-internal constant) must be ported from `wyzecam/iotc.py`. This is the most important piece of the API client to get right.

#### 5.1.3 State Persistence

```go
type StateFile struct {
    Auth    AuthState              `json:"auth"`
    Cameras map[string]CameraInfo  `json:"cameras"` // keyed by MAC
    Updated time.Time              `json:"updated"`
}
```

Path: `$STATE_DIR/wyze-bridge.state.json`

Refreshed on startup and every `REFRESH_INTERVAL` (default 30 min). Fresh P2P params mean go2rtc can be reconfigured on restart without a cloud round-trip if cache is recent.

#### 5.1.4 Camera Commands

For MQTT-driven camera control, use the Wyze cloud API:

```go
// POST https://api.wyzecam.com/app/v2/device/set_property_list
const (
    PIDResolution  = "P2"    // "1"=HD "2"=SD
    PIDAudio       = "P1"    // "1"=on "0"=off
    PIDNightVision = "P3"    // "0"=auto "1"=on "2"=off
    PIDMotionAlert = "P1047"
)
```

Pan/tilt, cruise points, and other commands require the TUTK command channel (K1xxxx messages), which go2rtc does not expose via API. These are explicitly out of scope for Phases 1-3.

### 5.2 go2rtc Manager (`internal/go2rtcmgr/`)

#### 5.2.1 Process Management

```go
type Manager struct {
    log        zerolog.Logger
    binaryPath string
    configPath string
    apiURL     string   // "http://localhost:1984"
    cmd        *exec.Cmd
    ready      chan struct{}
    mu         sync.Mutex
}

func (m *Manager) Start(ctx context.Context) error
func (m *Manager) Stop() error
func (m *Manager) WaitReady(ctx context.Context, timeout time.Duration) error
func (m *Manager) IsHealthy(ctx context.Context) bool
```

go2rtc stdout/stderr is captured and re-emitted through zerolog:

- go2rtc `debug` → our `Trace`
- go2rtc `info` → our `Debug` (less noise by default)
- go2rtc `warn`/`error` → pass-through at same level

`WaitReady` polls `GET /api/streams` until it returns 200 or times out. On startup this gives go2rtc up to 10 seconds to be ready before cameras are added.

#### 5.2.2 Config Generation

The go2rtc YAML config is generated from the current camera list and configuration. Written to `$STATE_DIR/go2rtc.yaml`.

```go
type Go2RTCConfig struct {
    Log     LogConfig             `yaml:"log"`
    API     APIConfig             `yaml:"api"`
    RTSP    RTSPConfig            `yaml:"rtsp"`
    WebRTC  WebRTCConfig          `yaml:"webrtc"`
    Streams map[string][]string   `yaml:"streams"`
    Record  *RecordGlobalConfig   `yaml:"record,omitempty"`
}
```

Example generated output:

```yaml
log:
  level: warn     # set to debug when FORCE_IOTC_DETAIL=true

api:
  listen: :1984
  origin: "*"     # needed for bridge WebUI on :5080 to use WebRTC player

rtsp:
  listen: :8554

webrtc:
  listen: :8889
  ice_servers:
    - urls: [stun:stun.l.google.com:19302]
  # BRIDGE_IP: candidates: ["192.168.1.50:8889"]

streams:
  front_door:
    - wyze://192.168.1.10?uid=XXX&enr=YYY&mac=AABBCCDDEEFF&model=WYZEDB3&subtype=hd&dtls=true
  backyard:
    - wyze://192.168.1.11?uid=XXX&enr=YYY&mac=001122334455&model=WYZE_CAKP2JFUS&subtype=hd&dtls=true
```

#### 5.2.3 go2rtc HTTP API Client

```go
type APIClient struct {
    baseURL    string
    httpClient *http.Client
    log        zerolog.Logger
}

func (c *APIClient) ListStreams(ctx context.Context) (map[string]Stream, error)
func (c *APIClient) AddStream(ctx context.Context, name, url string) error
func (c *APIClient) DeleteStream(ctx context.Context, name string) error
func (c *APIClient) GetStreamInfo(ctx context.Context, name string) (*StreamInfo, error)
func (c *APIClient) GetSnapshot(ctx context.Context, name string) ([]byte, error)  // returns JPEG
func (c *APIClient) HasActiveProducer(ctx context.Context, name string) (bool, error)
```

`StreamInfo` mirrors go2rtc's probe JSON — producers, consumers, codecs. Used for MQTT `stream_info` messages and WebUI status display.

Dynamic stream management (add/remove without restart) uses `POST /api/streams?src={url}&name={name}` and `DELETE /api/streams?name={name}`.

### 5.3 Camera Manager (`internal/camera/`)

#### 5.3.1 State Machine

```goat
OFFLINE ──(startup/retry timer)──► DISCOVERING
                                        │
                              (Wyze API returns P2P params)
                                        │
                                        ▼
                                   CONNECTING
                                   (add wyze:// to go2rtc)
                                        │
                            (go2rtc has active producer)
                                        │
                                        ▼
                                   STREAMING ──(drop/timeout)──► OFFLINE
                                                                     ▲
                                   Any state ──(repeated failures)──┘
                                   (backoff timer)
```

```go
type CameraState int

const (
    StateOffline     CameraState = iota
    StateDiscovering
    StateConnecting
    StateStreaming
    StateError        // max backoff, waiting
)

type Camera struct {
    Info        wyzeapi.CameraInfo
    State       CameraState
    Quality     string        // "hd" | "sd"
    AudioOn     bool
    Record      bool
    ConnectedAt time.Time
    LastSeen    time.Time
    ErrorCount  int
    mu          sync.RWMutex
}
```

**Reconnection backoff:** `min(5s * 2^errorCount, 5min)`. Resets to 0 on successful stream.

**IP refresh:** On connection failure, re-query Wyze API for current device IP before next attempt. DHCP assigns can change.

**Health polling:** Every 30s, `GET /api/streams` from go2rtc API. Any camera expected to be streaming but showing no active producer transitions to reconnecting.

#### 5.3.2 Camera Filter

```go
type Filter struct {
    Names  []string  // FILTER_NAMES
    Models []string  // FILTER_MODELS
    MACs   []string  // FILTER_MACS
    Block  bool      // FILTER_BLOCKS inverts: listed cameras are EXCLUDED
}
```

Filtered-out cameras are never added to go2rtc and never appear in MQTT or WebUI.

#### 5.3.3 Per-Camera Overrides

`CAM_OPTIONS` in config.yml or env vars `RECORD_{CAM_NAME}`, `QUALITY_{CAM_NAME}`, `AUDIO_{CAM_NAME}` override global defaults per camera. Names are normalized (uppercase, spaces→underscores) to match env var conventions.

### 5.4 MQTT (`internal/mqtt/`)

#### 5.4.1 Connection

```go
type Client struct {
    log        zerolog.Logger
    paho       paho.Client
    topic      string         // MQTT_TOPIC, default "wyzebridge"
    dtopic     string         // MQTT_DISCOVERY_TOPIC, default "homeassistant"
    cams       *camera.Manager
}
```

Uses `github.com/eclipse/paho.mqtt.golang`. Auto-reconnect with exponential backoff. On reconnect: re-publish all camera states, re-subscribe to command topics, re-publish bridge LWT online.

LWT: `{topic}/bridge/state` = `"offline"` on disconnect.

#### 5.4.2 Topics Published

```goat
{topic}/bridge/state                  "online" | "offline"

{topic}/{cam}/state                   "connected" | "disconnected"
{topic}/{cam}/net_mode                "lan"  (always in new bridge)
{topic}/{cam}/quality                 "hd" | "sd"
{topic}/{cam}/audio                   "true" | "false"
{topic}/{cam}/fps                     integer
{topic}/{cam}/bitrate                 integer kbps
{topic}/{cam}/camera_info             JSON: {ip, model, fw_version, mac}
{topic}/{cam}/stream_info             JSON: {rtsp_url, webrtc_url, hls_url}
{topic}/{cam}/thumbnail               JPEG bytes (latest snapshot)
```

State messages are only published on change (no spam). On reconnect, full state is republished.

#### 5.4.3 Topics Subscribed (Commands)

```goat
{topic}/{cam}/set/quality             "hd" | "sd"
{topic}/{cam}/set/audio               "true" | "false"
{topic}/{cam}/set/night_vision        "auto" | "on" | "off"
{topic}/{cam}/snapshot/take           any payload → trigger snapshot
{topic}/{cam}/stream/restart          any payload → force reconnect
```

Quality changes update the `wyze://` URL `subtype` param in go2rtc (delete + re-add stream) and call the Wyze cloud API `set_property_list`.

#### 5.4.4 Home Assistant Discovery

Published on startup and on camera add/remove to `{dtopic}/...`:

**Camera:**

```json
// {dtopic}/camera/{mac}/config
{
  "name": "Front Door",
  "unique_id": "wyze_AABBCCDDEEFF",
  "stream_source": "rtsp://192.168.1.50:8554/front_door",
  "topic": "wyzebridge/front_door/",
  "availability_topic": "wyzebridge/front_door/state",
  "payload_available": "connected",
  "payload_not_available": "disconnected",
  "device": {
    "identifiers": ["wyze_AABBCCDDEEFF"],
    "name": "Front Door",
    "model": "WYZEDB3",
    "manufacturer": "Wyze"
  }
}
```

**Select entity** for quality (`hd`/`sd`), **switch entity** for audio, **select entity** for night vision — matching current bridge HA entities for backward compatibility.

### 5.5 WebUI (`internal/webui/`)

**Complete rewrite.** No Flask, no Jinja2, no Python template porting.

#### 5.5.1 Tech Stack

- Go `net/http` — HTTP server
- `embed.FS` — static assets compiled into binary
- Vanilla JS — no framework, no build step, no npm
- CSS: hand-written, minimal — dark-mode capable, grid layout
- `video-rtc.js` — from go2rtc release, embedded as static asset
- Real-time updates via Server-Sent Events (not polling)

#### 5.5.2 Routes

```goat
GET  /                           Camera grid — all cameras with status + stream links
GET  /camera/{name}              Single camera page with live player

GET  /api/cameras                JSON: all cameras
GET  /api/cameras/{name}         JSON: one camera
POST /api/cameras/{name}/restart Force reconnect
POST /api/cameras/{name}/quality body: {"quality":"hd"|"sd"}
POST /api/cameras/{name}/audio   body: {"enabled":true|false}

GET  /api/snapshot/{name}        Latest snapshot JPEG (proxied from go2rtc)
POST /api/snapshot/{name}        Force new snapshot, return JPEG

GET  /api/streams                Combined M3U8 playlist (all cameras)
GET  /api/streams/{name}.m3u8    Per-camera M3U8

GET  /cams.m3u8                  Backward-compat alias for /api/streams
GET  /stream/{name}.m3u8         Backward-compat alias

GET  /api/health                 {"status":"ok","version":"x.y.z","uptime":123}
GET  /api/version                {"version":"x.y.z","go2rtc_version":"1.9.14"}

GET  /events                     SSE stream for camera state changes
```

#### 5.5.3 WebRTC Player

Per-camera page embeds go2rtc's `video-rtc.js` custom element:

```html
<video-rtc src="http://BRIDGE_HOST:1984/api/webrtc?src=front_door"></video-rtc>
```

If `BRIDGE_IP` is set, the `src` attribute uses that host instead of `localhost`. This is the only reason go2rtc port 1984 needs to be accessible to the browser — the bridge WebUI itself doesn't handle WebRTC, it delegates to go2rtc.

End users interact only with port 5080. Port 1984 is also exposed in Docker for power users who want go2rtc's native interface, but nothing in the normal flow requires it.

#### 5.5.4 Authentication

```go
type AuthMiddleware struct {
    enabled  bool
    username string
    passHash []byte   // bcrypt
    apiKey   string   // for Bearer token access
}
```

All routes except `/api/health` are behind auth when `BRIDGE_AUTH=true`.

Default password: derived from the username part of `WYZE_EMAIL` (same as current bridge behavior — backward compatible).

#### 5.5.5 Stream Authentication (`STREAM_AUTH`)

`STREAM_AUTH=user:pass@cam1,cam2|user2:pass2` is parsed and translated into go2rtc path credentials in the generated `go2rtc.yaml`. Our bridge WebUI still renders all cameras regardless of stream auth; the per-user restrictions apply at the RTSP/WebRTC level.

#### 5.5.6 Server-Sent Events

```goat
GET /events → Content-Type: text/event-stream

Events:
  camera_state     data: {"name":"front_door","state":"streaming","quality":"hd"}
  camera_added     data: {"name":"backyard","model":"WYZE_CAKP2JFUS"}
  camera_removed   data: {"name":"backyard"}
  snapshot_ready   data: {"name":"front_door"}
  bridge_status    data: {"uptime":3600,"streaming":2,"total":2}
```

Browser JS subscribes to SSE and updates the grid without polling. Heartbeat event every 30s to keep connections alive through proxies.

#### 5.5.7 M3U8 Generation

```text
#EXTM3U
#EXT-X-VERSION:3
#EXTINF:-1,Front Door
rtsp://192.168.1.50:8554/front_door
#EXTINF:-1,Backyard
rtsp://192.168.1.50:8554/backyard
```

Also generates an enhanced variant with `#EXT-X-STREAM-INF` for multi-quality (hd + sd streams if both configured).

### 5.6 Snapshot Manager (`internal/snapshot/`)

```go
type SnapshotConfig struct {
    Interval   time.Duration  // SNAPSHOT_INTERVAL seconds, 0=off
    Path       string         // dir template, default "/media/snapshots/{cam_name}/%Y/%m/%d"
    FileName   string         // filename template, default "%H-%M-%S"; .jpg appended
    Keep       time.Duration  // SNAPSHOT_KEEP, 0=never prune
    Cameras    []string       // SNAPSHOT_CAMERAS, empty=all
    Latitude   float64        // for sunrise/sunset
    Longitude  float64
    DoSunrise  bool
    DoSunset   bool
}
```

Snapshot acquisition: `GET http://localhost:1984/api/frame.jpeg?src={name}`. The bridge never invokes FFmpeg itself, but go2rtc's JPEG endpoint uses FFmpeg internally to decode H.264 → JPEG, so the container ships with `ffmpeg` installed. Streaming protocols (RTSP/HLS/WebRTC) work without ffmpeg; only the snapshot endpoint depends on it.

Sunrise/sunset: `github.com/nathan-osman/go-sunrise` (pure Go, no CGO). Compute next event, schedule one-shot timer, reschedule after firing.

Pruning goroutine runs every 5 min, removes files in `SNAPSHOT_PATH` older than `SNAPSHOT_KEEP`. Publishes new JPEG to MQTT `{topic}/{cam}/thumbnail` after each successful snapshot.

### 5.7 Recording (`internal/recording/`)

Recording is handled by go2rtc; the bridge configures it and manages cleanup.

#### 5.7.1 Configuration

```go
type RecordConfig struct {
    Global    bool          // RECORD_ALL
    PerCamera map[string]bool  // RECORD_{cam_name}
    Dir       string        // RECORD_PATH template
    FileName  string        // RECORD_FILE_NAME template
    Length    time.Duration // RECORD_LENGTH, default 60s
    Keep      time.Duration // RECORD_KEEP, 0=never
}
```

Template variables in `RECORD_PATH` and `RECORD_FILE_NAME`:

- `{cam_name}`, `{CAM_NAME}` — normalized camera name
- `%path` — go2rtc stream name (identical to cam_name)
- `%Y`, `%m`, `%d`, `%H`, `%M`, `%S` — strftime
- `%s` — Unix epoch integer
- `%f` — microseconds

**Constraint:** Combined path must contain either `%s` OR all six of `%Y %m %d %H %M %S`. The recording config builder validates this and auto-appends `_%s` with a zerolog warning if it fails validation.

Defaults:

```text
RECORD_PATH      = /record/{cam_name}/%Y/%m/%d
RECORD_FILE_NAME = %H-%M-%S
→ /record/front_door/2026/04/13/14-30-00.mp4
```

#### 5.7.2 go2rtc Recording Config

Per-stream recording is injected into the generated `go2rtc.yaml` for enabled cameras:

```yaml
streams:
  front_door:
    - wyze://...
    record: true
    recordPath: /record/front_door/%Y/%m/%d/%H-%M-%S_%s
    recordSegmentDuration: 60s
    recordDeleteAfter: 0       # 0 = keep forever; > 0 = auto-delete (seconds)
```

When `RECORD_KEEP` is set to a non-zero duration, two things happen: go2rtc's `recordDeleteAfter` is set, AND our own pruning goroutine also watches the directory. The redundancy is intentional — go2rtc's deletion is segment-granular; our pruner can clean up empty directories.

#### 5.7.3 Recording Pruning

Background goroutine, interval: 15 min.

- Walk `RECORD_PATH` directory tree
- Delete `.mp4` files older than `RECORD_KEEP`
- Remove empty directories after file deletion

Logs each deletion at `Debug` level; logs a summary at `Info` level (count, bytes freed).

### 5.8 Configuration (`internal/config/`)

#### 5.8.1 Complete Environment Variable Reference

```text
# ── Wyze Auth ────────────────────────────────────────────────────────────────
WYZE_EMAIL          string   Wyze account email
WYZE_PASSWORD       string   Wyze account password
WYZE_API_ID         string   API ID from developer.wyze.com  (REQUIRED)
WYZE_API_KEY        string   API Key from developer.wyze.com (REQUIRED)
WYZE_TOTP_KEY            string   TOTP seed for 2FA (optional)

# ── Network ──────────────────────────────────────────────────────────────────
BRIDGE_IP               string   Host IP for WebRTC ICE candidates
BRIDGE_PORT             int      WebUI port, default 5080
STUN_SERVER         string   default "stun:stun.l.google.com:19302"

# ── WebUI Auth ───────────────────────────────────────────────────────────────
BRIDGE_AUTH             bool     Enable WebUI auth, default false
BRIDGE_USERNAME         string   default "wyze"
BRIDGE_PASSWORD         string   default = username-part of WYZE_EMAIL
BRIDGE_API_TOKEN              string   Bearer token for REST API

# ── Stream Auth ──────────────────────────────────────────────────────────────
STREAM_AUTH         string   "user:pass@cam1,cam2|user2:pass2"

# ── MQTT ─────────────────────────────────────────────────────────────────────
MQTT_ENABLED        bool     default false
MQTT_HOST           string   broker hostname/IP
MQTT_PORT           int      default 1883
MQTT_USERNAME       string
MQTT_PASSWORD       string
MQTT_TOPIC          string   default "wyzebridge"
MQTT_DISCOVERY_TOPIC         string   HA discovery prefix, default "homeassistant"

# ── Camera Filtering ─────────────────────────────────────────────────────────
FILTER_NAMES        string   comma-separated camera names to include
FILTER_MODELS       string   comma-separated model strings to include
FILTER_MACS         string   comma-separated MACs to include
FILTER_BLOCKS       bool     invert: listed cameras are excluded

# ── Camera Defaults ───────────────────────────────────────────────────────────
QUALITY             string   "hd" | "sd", default "hd"
AUDIO               bool     default true
OFFLINE_TIME        int      seconds before marked offline, default 30

# ── Recording ────────────────────────────────────────────────────────────────
RECORD_ALL          bool     enable recording for all cameras
RECORD_PATH         string   dir template, default "/media/recordings/{cam_name}/%Y/%m/%d"
RECORD_FILE_NAME    string   filename template, default "%H-%M-%S"
RECORD_LENGTH       string   segment duration "60s"/"1h", default "60s"
RECORD_KEEP         string   auto-delete age "0s"(never)/"72h"/"7d", default "0s"
RECORD_{CAM_NAME}   bool     per-camera recording (uppercase, spaces→underscores)

# ── Snapshots ────────────────────────────────────────────────────────────────
SNAPSHOT_PATH       string   dir template, default "/media/snapshots/{cam_name}/%Y/%m/%d"
SNAPSHOT_FILE_NAME  string   filename template, default "%H-%M-%S"; .jpg auto-appended
SNAPSHOT_INTERVAL   int      interval seconds, 0=disabled
SNAPSHOT_KEEP       string   auto-delete age, default "0s"
SNAPSHOT_CAMERAS    string   comma-separated camera names, empty=all

# ── Sunrise/Sunset ───────────────────────────────────────────────────────────
LATITUDE            float    decimal degrees (for sunrise/sunset snapshots)
LONGITUDE           float    decimal degrees

# ── Camera env overrides (uppercase cam name, spaces→underscores) ─────────────
QUALITY_{CAM_NAME}  string   "hd" | "sd"
AUDIO_{CAM_NAME}    bool
RECORD_{CAM_NAME}   bool

# ── Paths ────────────────────────────────────────────────────────────────────
STATE_DIR           string   state/config dir, default "/config"

# ── Debugging ────────────────────────────────────────────────────────────────
LOG_LEVEL           string   "trace"|"debug"|"info"|"warn"|"error", default "info"
FORCE_IOTC_DETAIL   bool     verbose TUTK tracing (sets go2rtc debug + our trace)
```

#### 5.8.2 Docker Secrets

If any credential env var is unset, the loader checks `/run/secrets/{VAR_NAME}` (case-insensitive). Supported: `WYZE_EMAIL`, `WYZE_PASSWORD`, `WYZE_API_ID`, `WYZE_API_KEY`, `MQTT_PASSWORD`, `BRIDGE_PASSWORD`.

#### 5.8.3 config.yml (HA Add-on)

Retained for Home Assistant add-on users. Env vars take precedence.

```yaml
WYZE_EMAIL: user@example.com
WYZE_API_ID: "your-id"
WYZE_API_KEY: "your-key"
MQTT_HOST: core-mosquitto
MQTT_ENABLED: true
MQTT_DISCOVERY_TOPIC: homeassistant
LATITUDE: 38.6270
LONGITUDE: -90.1994
RECORD_ALL: false
SNAPSHOT_INTERVAL: 0
CAM_OPTIONS:
  - CAM_NAME: front-door
    RECORD: true
    QUALITY: hd
  - CAM_NAME: backyard
    AUDIO: false
```

---

## 6. Logging

All logging uses `github.com/rs/zerolog`.

```go
// main.go: root logger initialization
output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
if !isatty.IsTerminal(os.Stdout.Fd()) {
    // JSON in production (Docker, not a TTY)
    log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
} else {
    // Human-readable when running standalone / in dev
    log.Logger = zerolog.New(output).With().Timestamp().Logger()
}
zerolog.SetGlobalLevel(cfg.LogLevel)  // from LOG_LEVEL env var
```

Component sub-loggers:

```go
cameraLog  := log.With().Str("c", "camera").Logger()
apiLog     := log.With().Str("c", "wyzeapi").Logger()
mqttLog    := log.With().Str("c", "mqtt").Logger()
go2rtcLog  := log.With().Str("c", "go2rtc").Logger()
webuiLog   := log.With().Str("c", "webui").Logger()
```

go2rtc stdout/stderr re-emitted through `go2rtcLog`:

- `[DEBUG]` lines → `Trace()`
- `[INFO]` lines → `Debug()`
- `[WARN]` lines → `Warn()`
- `[ERROR]` lines → `Error()`

Camera-specific log lines include `Str("cam", name)` for easy filtering.

`FORCE_IOTC_DETAIL=true` sets `zerolog.GlobalLevel = TraceLevel` and passes `verbose=true` in the go2rtc stream URLs.

---

## 7. Module Structure

```ascii
(github.com/IDisposable/docker-wyze-bridge, branch: go-rewrite)

cmd/wyze-bridge/
└── main.go                    entry point, DI wiring, signal handling, shutdown

internal/
├── wyzeapi/
│   ├── auth.go                login, HMAC signature, token refresh
│   ├── cameras.go             device list, P2P param extraction, ENR decode
│   ├── commands.go            set_property_list camera control
│   ├── models.go              model string constants, feature matrix per model
│   └── state.go               StateFile load/save
├── go2rtcmgr/
│   ├── manager.go             subprocess start/stop/wait, stdout relay to zerolog
│   ├── config.go              Go2RTCConfig struct, YAML generation
│   └── apiclient.go           go2rtc HTTP API client (streams CRUD + snapshot)
├── camera/
│   ├── camera.go              Camera struct, state machine goroutine
│   ├── manager.go             CameraManager: owns all cameras, discovery loop
│   └── filter.go              FILTER_* evaluation
├── mqtt/
│   ├── client.go              broker connection, reconnect, LWT
│   ├── publish.go             all outbound message helpers
│   ├── subscribe.go           inbound command dispatch
│   └── discovery.go           HA MQTT discovery message builders
├── webui/
│   ├── server.go              http.Server setup, route registration, middleware
│   ├── api.go                 REST API handlers
│   ├── m3u8.go                M3U8 playlist generation
│   ├── sse.go                 Server-Sent Events hub + handler
│   ├── auth.go                Basic auth + Bearer middleware
│   └── static/                (embed.FS)
│       ├── index.html         camera grid
│       ├── camera.html        single camera detail
│       ├── app.js             vanilla JS
│       ├── video-rtc.js       from go2rtc release
│       └── style.css
├── snapshot/
│   ├── manager.go             interval + sunrise/sunset scheduling
│   └── pruner.go              file age pruning
├── recording/
│   └── config.go              RECORD_* → go2rtc config translation + pruning
└── config/
    ├── config.go              Config struct (single canonical config)
    ├── env.go                 env var parsing and defaults
    ├── yaml.go                config.yml parsing
    └── secrets.go             Docker /run/secrets/ support

docker/
├── Dockerfile                 multi-stage, multi-arch (amd64/arm64/armv7)
└── scripts/
    └── verify-go2rtc.sh       verify downloaded binary hash during build

home_assistant/
├── config.json                HA add-on manifest
├── translations/en.yaml
└── README.md

unraid/
└── wyze-bridge.xml

docker-compose.sample.yml
docker-compose.tailscale.yml
docker-compose.ovpn.yml

DESIGN.md                      this document
DOORBELL_TEST.md               hardware test instructions for Doorbell v1
MIGRATION.md                   upgrade guide for existing users
go.mod
go.sum
README.md                      full rewrite
```

---

## 8. Dockerfile

```dockerfile
# ── Stage 1: Build Go binary ──────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /wyze-bridge ./cmd/wyze-bridge

# ── Stage 2: Fetch go2rtc binary ─────────────────────────────────────────────
FROM debian:bookworm-slim AS go2rtc-fetch
ARG GO2RTC_VERSION=1.9.14
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    curl -fsSL \
      "https://github.com/AlexxIT/go2rtc/releases/download/v${GO2RTC_VERSION}/go2rtc_linux_${TARGETARCH}" \
      -o /go2rtc && \
    chmod +x /go2rtc

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /config /media/snapshots /media/recordings

COPY --from=builder      /wyze-bridge   /usr/local/bin/wyze-bridge
COPY --from=go2rtc-fetch /go2rtc        /usr/local/bin/go2rtc

EXPOSE 5080        # Bridge WebUI + REST API
EXPOSE 1984        # go2rtc native API/WebUI (power users, WebRTC player src)
EXPOSE 8554        # RTSP
EXPOSE 8888        # HLS
EXPOSE 8889        # WebRTC HTTP
EXPOSE 8189/udp    # WebRTC ICE

VOLUME ["/config", "/media"]

ENTRYPOINT ["/usr/local/bin/wyze-bridge"]
```

Multi-arch: `linux/amd64`, `linux/arm64`, `linux/arm/v7` via `docker buildx`.

`TARGETARCH` from buildx maps directly to go2rtc's release naming (`amd64`, `arm64`, `arm`).

---

## 9. Go Dependencies

```toml
# go.mod (planned — keep minimal)
github.com/rs/zerolog                   # logging
github.com/eclipse/paho.mqtt.golang    # MQTT client
github.com/nathan-osman/go-sunrise     # sunrise/sunset calculation
gopkg.in/yaml.v3                       # config.yml parsing
github.com/mattn/go-isatty             # TTY detection for log format
```

No web framework. No ORM. No reflection-heavy dependency. `net/http` stdlib for the WebUI. Everything else is orchestration and I/O.

---

## 10. Ports Summary

| Port | Protocol | Purpose | Configurable | Exposed to users? |
| ------ | ---------- | --------- | ------------ | ------------------- |
| 5080 | TCP | Bridge WebUI + REST API | `BRIDGE_PORT` | Yes (primary) |
| 1984 | TCP | go2rtc API + native WebUI | — | Optional (power users) |
| 8554 | TCP | RTSP streaming | — | Yes |
| 8888 | TCP | HLS streaming | — | Yes |
| 8889 | TCP | WebRTC HTTP | — | Yes (needed for in-browser player) |
| 8189 | UDP | WebRTC ICE | — | Yes |

---

## 11. Migration Guide Summary (MIGRATION.md)

### What Works the Same

All env var names are identical (except FILTER_BLOCK renamed to FILTER_BLOCKS for clarity). Change only the Docker image name.

### Breaking Changes

| Feature | Old | New |
| --------- | ----- | ----- |
| Remote P2P | Sometimes worked | **Removed.** Use VPN. |
| `MTX_*` vars | Configured MediaMTX | **Ignored.** MediaMTX removed. |
| Gwell cameras | Unsupported | Still unsupported |
| Port layout | 5000 WebUI, 1984 unused | 5080 WebUI (changed from 5000 to avoid Frigate), 1984 go2rtc |
| WebUI appearance | Flask UI | Fresh Go UI |
| `ON_DEMAND` | Connected to cameras as-needed | All cameras connect eagerly at startup for reliability and speed. |

### What Stranded Users Get Back

- Working RTSP/WebRTC/HLS streams on TUTK cameras
- No binary SDK dependency
- Smaller Docker image
- Active maintenance path

---

## 12. Updates Planned

### Remaining Work

- Two-way audio trigger via MQTT
- Webhook support (`internal/webhooks/`)
- BOA HTTP proxy
- Doorbell v1 non-DTLS support (upstream go2rtc contribution if needed)

---

## 13. Risks and Unknowns

### 13.1 Doorbell v1 (WYZEDB3) DTLS Support

**Risk:** May not support DTLS on current firmware.  
**Status:** Unknown — Marc tests morning of April 14. See `DOORBELL_TEST.md`.  
**If fails:**

- Option A: Contribute non-DTLS TUTK path to go2rtc upstream
- Option B: Implement direct TUTK connection for WYZEDB3 in bridge as fallback
- Option C (worst): Document as known limitation for Phase 1, address in Phase 4

### 13.2 go2rtc API Stability

**Mitigation:** Pin `GO2RTC_VERSION` in Dockerfile. go2rtc API has been stable throughout v1.x. Test each go2rtc version bump before adopting.

### 13.3 Wyze API Changes

**Mitigation:** State file caches P2P params — streaming survives cloud outages after initial setup. ENR decode logic is the most likely target of Wyze API changes; keep it isolated in `cameras.go` for easy updates.

---

## Appendix A: go2rtc Stream URL Reference

```text
wyze://[IP]?uid=[P2P_ID]&enr=[ENR]&mac=[MAC]&model=[MODEL]&subtype=[hd|sd]&dtls=true
```

| Param | Source | Notes |
| ------- | -------- | ------- |
| `IP` | `device.ip` from Wyze API | LAN IP only |
| `uid` | `device.p2p_id` | 20-char P2P identifier |
| `enr` | `device.enr` XOR-decoded | DTLS encryption key |
| `mac` | `device.mac` | Uppercase, no separators |
| `model` | `device.product_model` | e.g. `WYZE_CAKP2JFUS` |
| `subtype` | `hd` or `sd` | Quality selection |
| `dtls` | always `true` | Modern firmware requirement |

---

## Appendix B: go2rtc HTTP API Reference

All on `http://localhost:1984` (bridge is the only client).

| Method | Path | Purpose |
| -------- | ------ | --------- |
| `GET` | `/api/streams` | List streams + producer/consumer status |
| `POST` | `/api/streams?src={url}&name={n}` | Add stream |
| `DELETE` | `/api/streams?name={n}` | Remove stream |
| `GET` | `/api/streams?src={n}` | Probe/detail for one stream |
| `GET` | `/api/frame.jpeg?src={n}` | Snapshot JPEG |
| `GET` | `/api/config` | Current running config (for verification) |

---
