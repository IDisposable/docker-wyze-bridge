# wyze-bridge-go: Design Document

Pure-Go reimplementation of `IDisposable/docker-wyze-bridge`. Canonical
description of what the bridge is and how it works. User-facing
upgrade notes live in [MIGRATION.md](../MIGRATION.md).

---

## 1. Summary

The bridge is a single Go binary that discovers Wyze cameras via their
cloud API and hands them to [go2rtc](https://github.com/AlexxIT/go2rtc)
for local RTSP/WebRTC/HLS streaming. go2rtc runs as a managed
subprocess; the bridge does not link or import it.

Three streaming paths, chosen per-camera from the model ID:

| Path | Used for | How |
| ---- | -------- | --- |
| **TUTK** | Most models (V1, V2, V3, V3 Pro, V4, Doorbell v2, Pan V1/V2/V3/Pro, Floodlight v1/v2, Outdoor v1/v2) | go2rtc's built-in `wyze://` source speaks Wyze's pure-Go TUTK P2P. LAN-direct UDP; no binary SDK. |
| **Gwell P2P** | OG family (`GW_GC1`, `GW_GC2`) | `gwell-proxy` sidecar handles the Gwell/IoTVideo handshake and RTSP-PUBLISH-es H.264 into go2rtc. |
| **WebRTC (KVS)** | Doorbell lineage (`GW_BE1` Doorbell Pro, `GW_DBD` Doorbell Duo) | go2rtc's native `#format=wyze` source fetches the signaling URL + ICE servers from our loopback shim (`/internal/wyze/webrtc/<cam>`, which calls Wyze's `/v4/camera/get_streams`) and dials `wyze-mars-webcsrv.wyzecam.com` itself. No sidecar. |

The bridge orchestrates: Wyze API auth, camera discovery, go2rtc
subprocess + stream registration (via API, not YAML), MQTT
state/discovery, a WebUI on port 5080, interval + sunrise/sunset
snapshots, and per-camera ffmpeg recording. FFmpeg is the only C
dependency and is used only for snapshot frame extraction and MP4
segment writing.

## 2. Design Decisions

| Decision | Choice | Why |
| -------- | ------ | --- |
| go2rtc relationship | Managed subprocess | `internal/` packages aren't importable; the HTTP API is stable |
| Stream registration | HTTP API (`PUT /api/streams`) | YAML rewrites required a restart for any camera-list change |
| Logging | [`zerolog`](https://github.com/rs/zerolog) | Zero-allocation, structured, fast; good `io.Writer` interop for piping go2rtc stdout |
| WebUI | Plain `net/http` + `html/template` + embedded static assets | No web framework; SSE for live updates instead of polling |
| Port 1984 | Exposed, not user-facing | Power users get go2rtc's native UI; the bridge's own UI stays on 5080 |
| Recording | `ffmpeg -c copy -f segment` per camera pulling from our own RTSP endpoint | go2rtc v1.9.x doesn't expose per-stream recording via YAML or API |
| P2P mode | LAN-only | go2rtc's TUTK requires same-subnet; remote P2P is a VPN job |
| Repo strategy | Replaced the Python bridge in-place on branch `go-rewrite` | Preserves stars, issue history, CI setup |

## 3. Background

### 3.1 Why a rewrite

The Python bridge depended on a proprietary TUTK `.so` that Wyze
periodically broke server-side. It also linked Flask, MediaMTX, pickle
caching, and a ~200 MB dependency graph. go2rtc's [PR #2011](https://github.com/AlexxIT/go2rtc/pull/2011)
(@seydx, merged Jan 2026) added a pure-Go TUTK P2P implementation that
speaks modern Wyze firmware's DTLS 1.2 + ChaCha20-Poly1305. The
rewrite leans on that for all TUTK cameras.

Gwell-protocol cameras (OG family, doorbell lineage) don't use TUTK at
all — they use two different, unrelated protocols that we handle
separately. See the "Camera Support" section of the README for the
full model-to-path mapping.

### 3.2 go2rtc TUTK feature scope

- DTLS 1.2 with ChaCha20-Poly1305 (what modern firmware requires)
- H.264/H.265 video, AAC/PCM/PCMU/PCMA/Opus audio
- Two-way audio (intercom) via WebRTC
- Camera credential discovery via the Wyze cloud API
- Local P2P only (camera and go2rtc must be on same LAN subnet)

---

## 4. Architecture

### 4.1 Overview

```goat
Docker Container
├── wyze-bridge (Go binary — our code)
│   ├── Wyze API Client      — auth, token refresh, camera discovery, commands
│   ├── go2rtc Manager       — subprocess start/stop, config gen, API client
│   ├── Camera Manager       — per-camera state machines, reconnection, go2rtc stream registration
│   ├── MQTT                 — publish state/info, subscribe to commands, HA discovery
│   ├── WebUI                — HTTP server, REST API, SSE, embedded UI (port 5080)
│   ├── Snapshot Manager     — periodic capture, sunrise/sunset, pruning
│   ├── Recording Manager    — per-camera ffmpeg supervisors, segment pruning
│   └── Config               — env vars, config.yml, Docker secrets
├── go2rtc (managed subprocess)
│   ├── wyze://    source    — pure-Go TUTK P2P
│   ├── rtsp:      source    — receives publishes from gwell-proxy (for OG cameras)
│   ├── webrtc:    source    — native Wyze KVS handler (for doorbell lineage)
│   ├── RTSP       :8554     — consumer endpoint; also where recording ffmpegs pull from
│   ├── WebRTC     :8889 / :8189 UDP
│   ├── HLS        :8888
│   └── API / UI   :1984     — control plane; also the only interface the bridge talks to go2rtc through
└── gwell-proxy (subprocess — spawned only when an OG camera is discovered)
    └── Gwell P2P → ffmpeg → RTSP PUBLISH into go2rtc
```

### 4.2 Subprocess strategy

go2rtc is a separate process because its `internal/` packages aren't
importable and its HTTP API is stable. `gwell-proxy` is a separate
process because we vendor `github.com/wlatic/hacky-wyze-gwell` as
its own Go module (its `go.mod` path doesn't match its repo URL, so
it can't be `go get`'d) and because it owns ffmpeg per OG camera.

Every interaction with go2rtc is through `http://127.0.0.1:1984/api/`.
The bridge writes a skeletal YAML at startup (listener ports, auth,
STUN/ICE config, no streams) and then uses `PUT /api/streams` to
register each camera once discovered. Mid-run discovery additions are
an API call, not a restart.

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
    Auth      AuthState              `json:"auth"`
    Cameras   map[string]CameraInfo  `json:"cameras"`              // keyed by MAC
    Ignored   []IgnoredCamera        `json:"ignored,omitempty"`    // WebUI ignore list, by MAC (4.6)
    Layout    *Layout                `json:"layout,omitempty"`     // dashboard order / lock / rotation (4.6)
    PinnedIPs map[string]string      `json:"pinned_ips,omitempty"` // operator-pinned LAN addresses, by MAC (4.6)
    Rules     *CameraRules           `json:"rules,omitempty"`      // Settings page camera rules (4.6)
    Updated   time.Time              `json:"updated"`
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
    apiURL     string   // "http://127.0.0.1:1984"
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

**Supervision (4.6+):** go2rtc is the whole streaming plane, so an unexpected exit (a Go panic exits with status 2) no longer leaves the bridge serving "connection refused" until someone restarts the container. The exit watcher starts it again after 2s, doubling to a 1 min ceiling while it keeps dying and starting over once a run lasts 5 min; a restarted go2rtc that doesn't answer its API within 15s is killed and goes round again. Once it answers, `OnRestart` fires and `camera.Manager.Reregister` adds every runtime stream back at once (Gwell slots return from `go2rtc.yaml`). A crash dump has no level prefix, so the `panic:` / `fatal error:` line and the next 60 lines are logged at error instead of trace, and go2rtc's output goes through plain `os.Pipe`s read to EOF so `Wait` can't cut the dump short. While go2rtc is down, `AddStream` fails with a dial error (`go2rtcmgr.Unreachable`); those don't count toward the chronic-error report or the TUTK → WebRTC fallback.

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

**LAN IP resolution (`internal/lanresolve/`, 4.6+):** The cloud's IP is a hint, not the truth — it can lag a DHCP change by hours. Before a TUTK camera's `wyze://` URL is handed to go2rtc, `Manager.resolveLanIP` asks the resolver where the camera's MAC is actually answering: (1) probe the cloud IP and the last verified IP over ARP; (2) try addresses an earlier sweep or the kernel's neighbor cache (netlink `RTM_GETNEIGH`) already associate with the MAC; (3) a paced ARP sweep of the camera's `/24`, then the rest of the local subnet (capped at a `/22`, one sweep at a time LAN-wide, per-camera backoff doubling to 1h); (4) give up and keep the address the camera already had. ARP requests for a camera are sent unicast to its MAC (probes also send a broadcast copy, so a stranger holding the address is still noticed): a Wi-Fi camera in power save can sleep through a broadcast, which is transmitted once without acknowledgement, while a unicast frame is queued and retried by the access point — and a unicast sweep costs the rest of the LAN nothing, since the switch delivers it to one port. The periodic recheck skips cameras whose video is arriving, since bytes from the address are better proof than any ARP reply, and runs for the first time 20s after startup (`firstLanRecheck`) because a few Wi-Fi cameras miss the startup probe, when every camera is asked at once right after the previous bridge let go of them; a camera only the recheck ever verified reports source `recheck`. A probe for a known MAC first sends the address a throwaway UDP datagram and then the unicast request, so the kernel has a neighbor entry for the camera's reply to land in: the kernel resolves a new address by broadcast only, which these cameras sleep through, and go2rtc's dial packets can't leave until the kernel knows the MAC — the actual mechanism behind many a `discovery timeout` on a camera that was right there. A sweep hit gets one more probe for the same reason. A camera that answered ARP or sent video within `dozeWindow` (30 min) and then goes quiet is resolved with `Target.NoSweep`: its own addresses and the caches only. On Wi-Fi that silence means dozing, not moved, and a sweep is hundreds of ARP requests addressed to the camera's MAC; aimed at a camera already struggling to keep a stream up, that airtime was enough to leave it unable to answer go2rtc's next dial. A verified address that differs from the cloud's is stored on the `Camera` as an override that `GetInfo` / `Snapshot` / `StreamURL` / `StatusJSON` all apply, so the stream URL, WebUI, MQTT and webhooks agree, and that survives the 30-minute cloud refresh. The ARP engine needs `CAP_NET_RAW` (Docker default); otherwise a neighbor-cache engine nudges the kernel with a UDP datagram and reads its cache, which trusts STALE entries and so can trail a moved camera by about a minute. Env: `LAN_IP_RESOLVE`, `LAN_IP_SWEEP`, `LAN_IP_RECHECK`. Every outcome is kept on the `Camera` (`LanStatus`: the rung that verified the address, when it last verified — resolve or recheck — and `lanresolve.Result.Misses`, the camera's own sweeps in a row that didn't hear its MAC) and surfaces in the detail page's Network box, in `/api/cameras` (`lan_ip_source`, `lan_ip_verified_at`, `lan_sweep_misses`) and in the `camera_ip` SSE payload (`cloud_ip`, `source`, `source_label`, `verified_at`, `sweep_misses`). `Snapshot.LanAbsent` (`lan_absent` in `/api/cameras`) turns true at `lanAbsentAfter` (2) consecutive misses for a camera that hasn't verified at all this run (a dozing Wi-Fi camera that verified earlier can miss sweeps without being at another house); `Manager.OnLanStatus` fires after every resolve so main.go can push a `camera_lan` SSE event (`absent`, `sweep_misses`, `source`) and the dashboard can show or hide the card's "Not on this LAN?" hint, whose button is the ordinary ignore action. An operator can also pin a TUTK camera's address (`Manager.PinLanIP`, `pin.go`; persisted as `pinned_ips` by MAC through `OnPinChange`): a pin beats both the cloud's and the resolver's address in `GetInfo`, the ladder is skipped for a pinned camera (one ARP probe just keeps the Verified line honest and never counts as absent), the periodic recheck leaves it alone, and pinning or unpinning a streaming camera re-registers its go2rtc stream and fires `OnLanIPChange` with source `pinned` / `unpinned`.

**LAN IP recheck:** Every `LAN_IP_RECHECK` (default 2m) `Manager.RecheckLanIPs` probes each streaming TUTK camera at its current address (one ARP round trip). A camera that no longer answers gets the full resolver; only a *verified, different* address triggers a go2rtc re-register (`DeleteStream` + `AddStream`), so an unreachable or sleeping camera never flaps. go2rtc only dials when a consumer pulls and never reports a failed dial back, which is why this runs on a timer rather than on an error.

**Health polling:** Every 30s, `GET /api/streams` from go2rtc API. Any camera expected to be streaming but showing no active producer transitions to reconnecting.

**Video status (4.6+, `video.go`):** `StateStreaming` only means go2rtc accepted the stream; go2rtc dials lazily when a consumer pulls and never reports a failed dial through its API. The same 30s poll therefore also reads each producer's `remote_addr` and per-receiver byte counters and folds them into `Camera.video`: `live` (bytes arrived since the previous poll), `idle` (no consumer, not dialed — normal), `no_video` (pulled or connected but nothing new for two polls). A browser holding a `/ws` proxy socket for the stream counts as a consumer too (`Camera.AddPuller`, from `handleWSProxy`), because go2rtc only lists a consumer it could feed, so a dashboard waiting on a dead camera would otherwise read as idle; and the proxy reads go2rtc's error frames to the player (`{"type":"error","value":"mse: streams: wyze: connect failed: discovery timeout"}`, `wssniff.go`, a TeeReader on the go2rtc → browser direction that walks the WebSocket frames) and feeds them to `RecordStreamError`, since those dial failures never reach the log with a stream name. A pulled stream that isn't connected yet is left alone (go2rtc is still dialing, and retrying a failed pull is the player's job, below). A TUTK stream that is connected but whose byte counters haven't moved for two polls while someone pulls it is different: go2rtc keeps such a session open indefinitely (a V3 that drops off Wi-Fi mid-stream), and every new viewer joins the dead producer. `restartIfFrozen` puts the stream back (a PUT, which gives the name a fresh stream) at most once per `frozenRestartGap` (2 min), then `OnStreamRestart` → `Server.DropStreamSockets` closes that camera's `/ws` proxy sockets so the players reconnect to the new stream; an MSE player does that by itself, and `video-stream.js` restarts a WebRTC player (whose `onconnect` refuses while a peer connection is attached) without counting it against its backoff. On the page side, `static/video-stream.js` starts players 400ms apart (a grid opening every player at once dialed every camera twice in the same instant, and some lost the race), grid tiles carry `mode="mse"` so each opens one go2rtc consumer instead of WebRTC and MSE racing, and `buffer="2.5"`, which retargets video-rtc's MSE rate control (it sets `playbackRate` to the seconds buffered, so it sits about 1s behind live and plays up to 5x after a burst) to 2.5s ahead with the rate held between 0.8 and 1.25, because H.264 from these cameras arrives as a large keyframe every couple of seconds and a 1s buffer ran dry between them; go2rtc drops a frame it didn't get every packet of (`[DROP] ... INCOMPLETE` in its log), often a whole HD keyframe on Wi-Fi, which leaves the frames after it undecodable, so a buffered player that hasn't moved for 750ms with at least 1s buffered skips to 0.5s short of the newest data (`holeSkip`); the same players also move every seek into the newest buffered range (video-rtc's catch-up seek to `end - 5` lands just before the data its own keyframe-aligned trim left, so it waited, seeked again on the next append and sat on the spinner indefinitely), and runs a watchdog that restarts a visible player whose `currentTime` hasn't moved for 20s, one restart per 1.5s page-wide, doubling the wait for a player that keeps failing up to 5 min: go2rtc's video-rtc.js logs a failed dial and then waits on the socket forever. Consumer-side errors such as `codecs not matched` (a snapshot asking an H264 stream for JPEG) are logged but not recorded against the camera. go2rtc's `WRN [rtsp] error="..." stream=<name>` log lines are the other signal: `go2rtcmgr.Manager.OnWarning` → `ParseStreamError` → `Manager.RecordStreamError`, which stores the text on the camera (`last_error`, `last_error_at`), flips the status to `no_video` at once, and — for a TUTK camera reporting `discovery timeout` — runs the LAN IP recheck immediately (at most once a minute per camera) instead of waiting for the next scheduled one. Exposed as `video`, `last_video`, `last_error`, `last_error_at` in `/api/cameras`, as a second badge on the card, and as `camera_video` SSE events; transitions into or out of `no_video` land in the event log with go2rtc's reason.

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

**Runtime rules (Settings page, 4.6+, `rules.go`):** `Manager.SetRules` compiles a `wyzeapi.CameraRules` (scope, extra CIDRs, nickname patterns with `*`, MACs, models, block/allow) and `Discover` runs `applyRules` after the env filters and the ignore list. Scope `local` drops a camera whose cloud-reported IPv4 address sits on none of the host's subnets (`lanresolve.LocalSubnets`, injected through `SetLocalSubnets`) nor on the extra CIDRs; a camera with no address is kept. Lists then work like `Filter` (block drops matches, allow keeps only matches) with case-insensitive `*` globbing on the nickname. Dropped cameras already in the registry are removed by `dropExcludedLocked` exactly as an ignore removes them (go2rtc stream deleted, state offline, `OnCameraRemoved` with reason `excluded`) and listed by `Excluded()` with the reason, for the dashboard strip, `/api/excluded` and the Settings page; when the rules change they come back through the normal added path. Persisted as `rules` through `OnRulesChange`; `PUT /api/settings` kicks the same discovery the Rediscover button runs so a save applies at once.

**Ignore list (runtime, 4.6+):** `Manager.Ignore(name)` / `Unignore(mac)` maintain a MAC-keyed set applied after the env filters on every `Discover`. Ignoring deletes the go2rtc stream, marks the camera offline (so recording, MQTT state and webhooks see a normal shutdown), removes it from the registry and fires `OnCameraRemoved`, which main.go uses to drop the dashboard card (SSE `camera_removed`), stop the recorder and retract the HA entities (`RemoveDiscovery`). The list is persisted through `OnIgnoreChange` → `StateFile.Ignored` and installed at startup with `SetIgnored` before the first discovery, so an ignored camera never gets a stream at all. Restoring removes the MAC and triggers the same async rediscovery as the Rediscover button; `OnCameraAdded` (SSE `camera_added` + `PublishDiscovery`) brings the card and entities back. Nickname/model on each entry are refreshed from discovery so the WebUI strip stays readable after a rename.

#### 5.3.2a Dashboard layout (`internal/webui/layout.go`, 4.6+)

The tile order and lock flag are server-side (`StateFile.Layout`), not a browser preference, so every device shows the same grid. `orderCameras` renders listed cameras first in saved order and the rest by name; names in the saved order that no longer exist are skipped. The dashboard drags tiles with pointer events from a ⠿ handle that only exists while the grid is in arranging mode, PUTs the full order after each drop, and the lock button PUTs `locked`. Unknown names are kept on PUT so an offline camera keeps its slot. The layout also carries a per-camera view `rotation` (quarter turns only; 0 is dropped) that the ⟳ button sets while arranging; it's a CSS transform on the tile and the detail player, never a change to the stream.

#### 5.3.3 Per-Camera Overrides

`CAM_OPTIONS` in config.yml or env vars `RECORD_{CAM_NAME}`, `QUALITY_{CAM_NAME}`, `AUDIO_{CAM_NAME}` override global defaults per camera. Names are normalized (uppercase, spaces→underscores) to match env var conventions.

**Saved quality (`quality.go`):** `Manager.SetQuality` (Settings page table, detail page buttons, MQTT `set/quality`, `POST /api/cameras/{name}/quality`) stores the operator's `hd` / `sd` by MAC, persisted as `qualities` in the state file through `OnQualityChange` and installed with `SetSavedQualities` before the first discovery; `""` deletes the entry and the camera falls back to `CamQuality` (`QUALITY_<CAM>`, then `QUALITY`). Only TUTK cameras take it (`ErrQualityNotTUTK` otherwise; the `subtype` in the `wyze://` URL is the only knob, and go2rtc maps `hd` to the model's best frame size and `sd` to 360p at a low bitrate). A change that alters the effective quality of a streaming camera re-registers it (`registerStream`, no delete) and fires `OnStreamRestart` with the reason, so its players reconnect to the new stream.

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
{topic}/{cam}/set/irled               "on" | "off"
{topic}/{cam}/set/status_light        "on" | "off"
{topic}/{cam}/set/motion_detection    "on" | "off"
{topic}/{cam}/set/motion_tagging      "on" | "off"
{topic}/{cam}/set/hor_flip            "on" | "off"
{topic}/{cam}/set/ver_flip            "on" | "off"
{topic}/{cam}/set/bitrate             integer kbps
{topic}/{cam}/set/fps                 integer fps
{topic}/{cam}/state/set               "start" | "stop" (alias: on|off|1|0|true|false)
{topic}/{cam}/power/set               "on" | "off" | "restart"
{topic}/{cam}/snapshot/take           any payload → trigger snapshot
{topic}/{cam}/stream/restart          any payload → force reconnect
{topic}/{cam}/record/set              "start"|"on"|"1"|"true" → start; anything else → stop
{topic}/bridge/discover/set           any payload → re-poll Wyze API
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
GET  /settings                   Camera rules page (network scope, name / MAC / model lists)

GET  /api/cameras                JSON: all cameras
GET  /api/cameras/{name}         JSON: one camera
POST /api/cameras/{name}/restart Force reconnect
POST /api/cameras/{name}/quality body: {"quality":"hd"|"sd"}
POST /api/cameras/{name}/audio   body: {"enabled":true|false}
POST /api/cameras/{name}/record  body: {"action":"start"|"stop"} — toggles ffmpeg recorder
POST /api/cameras/{name}/snapshot Force capture to SNAPSHOT_PATH
POST /api/cameras/{name}/property body: {"property":"hor_flip","value":"on"} — camera-side setting via Wyze cloud (wyzeapi.CloudProperties, same table as MQTT set topics); mirrored to the MQTT state topic
POST /api/cameras/{name}/reboot   Reboot the camera itself (Wyze run_action "restart"), then re-register the stream
POST /api/cameras/{name}/pin      body: {"ip":"192.168.4.151"} — pin the LAN address (TUTK only; "" unpins); persisted by MAC
POST /api/cameras/{name}/ignore   Drop the camera + remember its MAC (persisted; see 5.3.2)
GET  /api/ignored                JSON: the ignore list
POST /api/ignored/{mac}/restore  Take a MAC off the ignore list + rediscover (async)
GET  /api/settings               Runtime camera rules: {"scope":"any"|"local","subnets":[cidr...],"names":[...],"macs":[...],"models":[...],"block":bool}
PUT  /api/settings               Replace them (POST accepted too); validated, persisted in the state file, applied by an immediate discovery
GET  /api/excluded               Cameras the rules dropped at the last discovery, with the reason for each
GET  /api/layout                 {"order":[names...],"locked":bool,"rotation":{name:deg}} — dashboard tile arrangement
PUT  /api/layout                 Replace it (POST accepted too); persisted in the state file. rotation: 0/90/180/270, view only

POST /api/discover               Re-poll Wyze API for added/removed cameras (202 + async)

GET  /api/snapshot/{name}        Latest snapshot JPEG (proxied from go2rtc)

GET  /api/streams                Combined M3U8 playlist (all cameras)
GET  /api/streams/{name}.m3u8    Per-camera M3U8

GET  /cams.m3u8                  Backward-compat alias for /api/streams
GET  /stream/{name}.m3u8         Backward-compat alias

GET  /api/health                 {"status":"ok"|"degraded","version":..,"uptime":..,"config_errors":N,"issues":[...]}
GET  /api/version                {"version":"x.y.z","go2rtc_version":"1.9.14"}
GET  /api/metrics                Full MetricsSnapshot JSON (same data backing /metrics)

GET  /metrics                    HTML dashboard — issues / cameras / API / storage / events
GET  /metrics.prom               Prometheus text exposition (unauthenticated by default)
GET  /dashboard.yaml             Auto-generated Lovelace YAML referencing MQTT entities

GET  /events                     SSE stream: camera_state, camera_added, camera_removed,
                                  snapshot_ready, bridge_status, recording_state, camera_ip, camera_video, camera_lan
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

Snapshot acquisition: `GET http://127.0.0.1:1984/api/frame.jpeg?src={name}`. The bridge never invokes FFmpeg itself, but go2rtc's JPEG endpoint uses FFmpeg internally to decode H.264 → JPEG, so the container ships with `ffmpeg` installed. Streaming protocols (RTSP/HLS/WebRTC) work without ffmpeg; only the snapshot endpoint depends on it.

Sunrise/sunset: `github.com/nathan-osman/go-sunrise` (pure Go, no CGO). Compute next event, schedule one-shot timer, reschedule after firing.

Pruning goroutine runs every 5 min, removes files in `SNAPSHOT_PATH` older than `SNAPSHOT_KEEP`. Publishes new JPEG to MQTT `{topic}/{cam}/thumbnail` after each successful snapshot.

### 5.7 Recording (`internal/recording/`)

Per-camera `ffmpeg` supervisor. Spawns one ffmpeg per recording-enabled
camera, pulling from our own loopback RTSP (`rtsp://127.0.0.1:8554/<cam>`),
running `-c copy -f segment -strftime 1` to write dated MP4 segments.

Started on the camera's `StateStreaming` transition from the state
machine callback in `wireCameraStateChanges`, stopped on any other
state. `Shutdown()` runs on bridge exit so segments close cleanly.
Each supervisor has an exponential backoff loop (2s → 60s) that
restarts ffmpeg if it exits for any reason other than our own cancel —
handles go2rtc not-yet-ready, transient I/O, camera flap.

#### 5.7.1 Configuration

```text
RECORD_ALL=true                               # or RECORD_<CAM>=true
RECORD_PATH=/media/recordings/{cam_name}/%Y/%m/%d
RECORD_FILE_NAME=%H-%M-%S                     # no extension; .mp4 appended
RECORD_LENGTH=60s                             # -segment_time
RECORD_KEEP=7d                                # retention; 0 = keep forever
```

Template variables in `RECORD_PATH` and `RECORD_FILE_NAME`:

- `{cam_name}`, `{CAM_NAME}` — normalized camera name
- `%Y`, `%m`, `%d`, `%H`, `%M`, `%S` — strftime
- `%s` — Unix epoch integer

**Constraint:** Combined path must contain either `%s` OR all six of
`%Y %m %d %H %M %S`. The builder validates this and auto-appends
`_%s` with a warning if violated.

**Timezone:** all strftime expansion is **UTC**. The bridge expands
tokens with `time.Now().UTC()` and pins ffmpeg's subprocess env to
`TZ=UTC` so its internal `-strftime 1` agrees. Two reasons: (1) no
DST spring-forward gaps or fall-back clashes in the directory tree;
(2) UTC paths compare correctly across timezone changes. UIs that
want local-time presentation get it for free — `time.Time` JSON
marshals in UTC emit the `Z` suffix and browsers `toLocaleString()`
it automatically.

#### 5.7.2 ffmpeg argv

```
ffmpeg -hide_banner -loglevel warning
       -rtsp_transport tcp
       -i rtsp://127.0.0.1:8554/<cam>
       -c:v copy -c:a aac -b:a 128k
       -f segment
       -segment_time <RECORD_LENGTH seconds>
       -reset_timestamps 1
       -strftime 1
       <expanded RECORD_PATH/RECORD_FILE_NAME>.mp4
```

Spawned with `TZ=UTC` in its environment.

- `-c:v copy` keeps video CPU near zero — no decode/re-encode.
- `-c:a aac -b:a 128k` transcodes audio. Wyze TUTK streams deliver
  PCM s16be which the mp4 muxer refuses under `-c copy`. AAC
  re-encode costs ~1% of a core per camera and leaves recordings
  playable everywhere.
- `-rtsp_transport tcp` avoids UDP-packet-loss on localhost (a real
  problem on the earlier Python pipeline).
- `-reset_timestamps 1` so each segment starts at t=0; clean seek
  tables.
- Stdout/stderr relay into zerolog at Debug as `c=record-ffmpeg`.

**Directory maintenance.** The segment muxer creates files but not
directory trees, and its own `-strftime 1` expansion happens inside
the muxer. The supervisor goroutine MkdirAlls today's AND tomorrow's
strftime-expanded directory before launching ffmpeg, then re-MkdirAlls
both every hour while ffmpeg runs (same goroutine, `select` on
`cmd.Wait()` + `time.Ticker`). Covers midnight rollovers without
dropping a segment.

#### 5.7.3 Recording pruning

Background goroutine, 15-min interval:

- Walk the `RECORD_PATH` prefix.
- Delete `.mp4` files older than `RECORD_KEEP`.
- Clean up empty directories left behind.
- Summary log (count, bytes freed) at Info.

### 5.9 Observability

The bridge exposes four cooperating surfaces for inspecting live
state. All share the same `MetricsSnapshot` struct so the numbers
match across formats.

#### 5.9.1 Issues Registry (`internal/issues/`)

Process-wide, goroutine-safe error registry. Subsystems Report
soft failures with a canonical dedup ID (`scope/camera/topic`);
repeats bump `LastSeen` + `Count` instead of piling up. Resolve(id)
clears an entry when the reporter verifies the failure went away.

Current consumers:

- `recording.Manager.RecordFileNameForCamera` — flags missing
  strftime tokens in `RECORD_PATH` / `RECORD_FILE_NAME` so bad
  templates show up on `/metrics` instead of just scrolling past in
  logs.

Future consumers plug in uniformly — any subsystem wanting visible
configuration/runtime failures holds a `*issues.Registry` and calls
Report.

#### 5.9.2 Metrics Page (`/metrics`)

One-flat-HTML dashboard, auto-refresh 10s. Sections:

- Issues panel (only shown when `Registry.Count() > 0`)
- Bridge summary: camera count, streaming count, error count,
  uptime, SSE client count
- Per-camera table: state pill, quality, audio, error count,
  recording indicator with session byte count
- Wyze cloud API call stats (`apiMetrics.EndpointStats`): per-
  endpoint count, errors, avg latency, last status
- Storage footprint: total + per-camera recording bytes, from the
  `StorageSampler` goroutine
- Recent events log: ring buffer of the last 200 state/record
  events

Same data backs `/api/metrics` (JSON).

#### 5.9.3 Prometheus Exposition (`/metrics.prom`)

Hand-rolled text format, no `client_golang` dep. All metric names
prefixed `wyze_bridge_`. Unauthenticated by default so standard
scrapers work without Basic-Auth secrets.

Key metrics:

- `wyze_bridge_uptime_seconds`
- `wyze_bridge_cameras_total` / `_streaming` / `_errored`
- `wyze_bridge_camera_error_count{camera,model,protocol,state}`
- `wyze_bridge_camera_recording{camera}`
- `wyze_bridge_camera_recording_bytes{camera}`
- `wyze_bridge_wyzeapi_calls_total{endpoint}`
- `wyze_bridge_wyzeapi_errors_total{endpoint}`
- `wyze_bridge_wyzeapi_latency_seconds{endpoint}`
- `wyze_bridge_recordings_bytes_total`
- `wyze_bridge_recordings_bytes{camera}`
- `wyze_bridge_issues_total`
- `wyze_bridge_sse_clients`

#### 5.9.4 Health Endpoint (`/api/health`)

```json
{
  "status": "ok" | "degraded",
  "version": "4.0-beta",
  "uptime": 3601,
  "config_errors": 0,
  "issues": []
}
```

`status` flips to `"degraded"` whenever the issues registry is
non-empty, which maps cleanly to a HA `binary_sensor` for "bridge
has problems." Always unauthenticated.

#### 5.9.5 Event Log (`internal/webui/eventlog.go`)

Fixed-size ring buffer (default 200) of `{time, kind, camera,
message}` records. In-memory only — state transitions are already
durable via MQTT `<cam>/state` topics + logs; a persisted log would
trade disk I/O for nothing users actually need.

Fed from `wireCameraStateChanges` (state flips) and
`recording.Manager.OnChange` (record start/stop). Rendered on
`/metrics` under a collapsible "Recent Events" section.

#### 5.9.6 Dashboard Generator (`/dashboard.yaml`)

Emits a ready-to-paste Home Assistant Lovelace dashboard that
references the MQTT discovery entities. One `glance` card for
bridge-wide status + one `picture-glance` per camera. HA add-on's
`run.sh` auto-fetches this at startup and drops it at
`/config/wyze_bridge_dashboard.yaml`.

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

# ── LAN IP resolution (TUTK cameras; see 5.3.1) ───────────────────────────────
LAN_IP_RESOLVE      bool     verify LAN IPs by MAC over ARP before go2rtc dials, default true
LAN_IP_SWEEP        bool     allow the last-resort ARP sweep of the local subnet, default true
LAN_IP_RECHECK      string   re-verify streaming cameras this often, "0" disables, default "2m"

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
cmd/
├── wyze-bridge/               main binary — DI wiring, signal handling, shutdown
└── gwell-proxy/               OG Gwell P2P sidecar binary

internal/
├── wyzeapi/                   Wyze cloud API: auth, discovery, commands, Mars,
│                              /v4/camera/get_streams (WebRTC bootstrap)
├── go2rtcmgr/                 go2rtc subprocess + HTTP API client
├── camera/                    Camera state machine, discovery loop, streamSourceFor routing
├── mqtt/                      Broker connection, HA discovery, command subscription
├── webui/                     net/http server, REST API, SSE, embedded UI,
│                              /internal/wyze shims (gwell-proxy + WebRTC signaling)
├── snapshot/                  Periodic + sunrise/sunset capture, pruning
├── recording/                 Per-camera ffmpeg supervisor, segment pruning
├── webhooks/                  HTTP POST on camera state change
├── config/                    Env / YAML / secrets → Config struct
└── gwell/
    ├── upstream/              Vendored github.com/wlatic/hacky-wyze-gwell
    │                          (separate Go module — protocol code only used
    │                          by cmd/gwell-proxy; not linked into wyze-bridge)
    └── (producer glue)

docker/Dockerfile              3-stage Alpine build, multi-arch via TARGETARCH
home_assistant/                HA add-on repo (two channels)
├── wyze_bridge/               stable add-on (from `main`, semver-tagged)
└── wyze_bridge_edge/          edge add-on (from `dev`, rolling)
repository.yaml                HA add-on store manifest
docker-compose.sample.yml
docker-compose.tailscale.yml
docker-compose.ovpn.yml
cycle.sh                       local dev loop (tests → build → run with .env.dev)

DOCS/
├── DESIGN.md                  this document
├── CAMERA_MODEL_TEST.md       user-facing camera-support test recipe
└── GWELL_RELAY_HANDOFF.md     historical — GW_BE1 investigation archive

MIGRATION.md                   user-facing 3.x → 4.x guide
README.md                      landing page
```

---

## 8. Dockerfile

Three-stage Alpine build: fetch go2rtc release binary + `video-rtc.js`,
build our two Go binaries (`wyze-bridge`, `gwell-proxy`), then assemble
a runtime image with ffmpeg bundled. Multi-arch via `TARGETARCH`
(`amd64`, `arm64`). `linux/arm/v7` isn't shipped — the CI cross-build
failed on a go2rtc-fetch / base-image incompatibility and hasn't been
re-attempted. The pinned `GO2RTC_VERSION` ARG is the single source of
truth — `cycle.sh` greps it out so local dev downloads the matching
release. See `docker/Dockerfile` for the current recipe.

Volumes: `/config` (state file, generated go2rtc.yaml, gwell token
cache), `/media` (snapshots + recordings). HA add-on re-roots `/media`
under `/media/wyze_bridge/` in its `run.sh`.

## 9. Go dependencies

Kept deliberately minimal:

- `github.com/rs/zerolog` — logging
- `github.com/eclipse/paho.mqtt.golang` — MQTT client
- `github.com/nathan-osman/go-sunrise` — sunrise/sunset math for snapshots
- `gopkg.in/yaml.v3` — `config.yml` parsing and go2rtc YAML generation
- `github.com/mattn/go-isatty` — TTY detection to pick log format

No web framework, no ORM. `net/http` stdlib for the WebUI. Everything
else is orchestration and I/O. The vendored Gwell protocol code in
`internal/gwell/upstream/` is a separate Go module with its own
dependency graph; nothing outside `cmd/gwell-proxy` links it.

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

## 11. Known Risks

### 11.1 go2rtc API stability

`GO2RTC_VERSION` is pinned in the Dockerfile and surfaced in `cycle.sh`
so dev and container stay in lockstep. The HTTP API has been stable
across v1.x; bump and test together.

### 11.2 Wyze cloud API changes

State file caches P2P params so streaming survives short cloud
outages. `ENR` decode + the `/v4/camera/get_streams` signaling
bootstrap are the most likely breakage points; both are isolated
(`internal/wyzeapi/cameras.go`, `internal/webui/shim_kvs.go`).

### 11.3 go2rtc HLS-from-TUTK stops after ~1 second

Upstream bug in `pkg/tutk/frame.go`'s `FrameHandler.handleVideo`: on
a `FrameNo` mismatch the function re-seeds state with `waitSeq=0`,
then the `PktIdx != waitSeq` check fails for any mid-frame packet and
calls `cs.reset()` which clobbers `frameNo`, returns — every
subsequent continuation packet re-enters the same doomed path. RTSP
is unaffected (frame-passthrough); only HLS's fMP4 segment assembly
falls off the cliff.

Reported upstream as [Issue #2215](https://github.com/AlexxIT/go2rtc/pull/2215);
fix [PR #2217](https://github.com/AlexxIT/go2rtc/pull/2217) submitted.

**Workaround:** the WebUI player uses WebRTC/MSE via `/ws?src=...`
(unaffected). RTSP and snapshots also work. HLS URLs are still
displayed for external-player compatibility but shouldn't be
recommended until the upstream fix lands. The `[OOO]` log spam from
the broken path is demoted to trace level by `emitLogLine` in
`internal/go2rtcmgr/manager.go` so it doesn't drown the bridge log.

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

All on `http://127.0.0.1:1984` (loopback; the bridge is the only
client and the Docker image doesn't expose the port publicly).

| Method | Path | Purpose |
| ------ | ---- | ------- |
| `GET`    | `/api/streams` | List streams + producer/consumer status |
| `PUT`    | `/api/streams?name={n}&src={url}` | Create or replace a stream. Empty `src` = publish-only slot (used for gwell-proxy's RTSP PUBLISH target). |
| `PATCH`  | `/api/streams?name={n}&src={url}` | Append an additional source to an existing stream |
| `DELETE` | `/api/streams?name={n}` | Remove a stream |
| `GET`    | `/api/streams?src={n}` | Probe / detail for one stream |
| `GET`    | `/api/frame.jpeg?src={n}` | Snapshot JPEG |
| `GET`    | `/api/stream.m3u8?src={n}` | HLS manifest (see §11.3 caveat for TUTK sources) |
| `GET`    | `/api/config` | Current running config (for verification) |

---
