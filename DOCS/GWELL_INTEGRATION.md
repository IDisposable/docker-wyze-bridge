# Gwell Protocol Integration

Status: **Scaffold landed on branch `go-rewrite`** — producer subprocess wiring, camera-manager routing, and cloud-token cache are in place. The upstream protocol code (`pkg/gwell/*`, `pkg/stream/*` from `github.com/wlatic/hacky-wyze-gwell`) must be vendored into `internal/gwell/vendor/` in a follow-up commit — the subprocess executable it produces is expected at `./gwell-proxy` (or `$GWELL_BINARY`).

## 1. Why Gwell?

Some newer Wyze cameras no longer speak the TUTK / IOTC P2P protocol that `go2rtc`'s `wyze://` producer was built around. Instead they use Wyze's newer **Gwell / IoTVideo** stack:

| Product Model | Friendly Name   | Protocol |
|---------------|-----------------|----------|
| `GW_BE1`      | Doorbell Pro    | Gwell    |
| `GW_GC1`      | OG              | Gwell    |
| `GW_GC2`      | OG 3X           | Gwell    |
| `GW_DBD`      | Doorbell Duo    | Gwell    |

Today `internal/camera/manager.go` hard-filters these out (`cam.IsGwell() → continue`). Users with these cameras see nothing in the bridge.

## 2. Source Material

Upstream reverse-engineering work: <https://github.com/wlatic/hacky-wyze-gwell>.

- **License:** MIT (compatible with our GPL, one-way MIT → GPL is fine).
- **Dependencies:** None declared in its `go.mod` — pure stdlib.
- **Scope:** `wyze-p2p/pkg/gwell/{certify,discovery,frame,hash,kcp,mtp,rc5,session,xor}.go` plus tests, `wyze-p2p/pkg/stream/{extractor,ffmpeg}.go`, `wyze-p2p/pkg/wyze/` API client, and `wyze-p2p/cmd/gwell-proxy/` as the entry point.

Protocol characteristics (from upstream `IMPLEMENTATION_STATUS.md` and code review):

- **Transport:** UDP primary + KCP reliability; TCP fallback; MTP relay last resort.
- **Crypto:** RC5-32/6 and RC5-64/6, XOR cipher, HMAC-MD5, and a `giot_hash_string` PRF, all with a hardcoded password key `"www.gwell.cc"`.
- **Handshake:** Seven phases — connection, certification, device discovery, network detection, subscription, calling/relay negotiation, video streaming.
- **Cloud auth:** Uses the standard Wyze API `access_token` we already mint in `internal/wyzeapi/auth.go`; cached server-side for ~7 days.
- **Media:** H.264 Annex-B inside `AVSTREAMCTL` frame sequence. A viewer `AV_INIT` packet (type=0x04, sub=0x02) must be sent after `START` to trigger the camera's encoder.
- **Notable subtleties:** non-zero `streamID` in `AVSTREAMCTL` `INITREQ` and `START`; `avKey` is `[0x02, 0, ..., 0]` 32 bytes (not a key exchange); bit 21 of GUTES frame flags skips checksum verification.

## 3. Integration Options Considered

### Option A — Subprocess (chosen)

Ship `gwell-proxy` as a sidecar binary. For each Gwell camera, start an instance that:

1. Hits the Wyze cloud with the bridge's `access_token` to obtain the Gwell session bootstrap.
2. Runs the full P2P + KCP + RC5 dance against the camera's LAN IP (UDP → TCP → MTP fallback).
3. Extracts H.264 and publishes it as **a local RTSP stream**, e.g. `rtsp://127.0.0.1:<port>/<cam>`.

The bridge then calls `go2rtcAPI.AddStream(ctx, name, "rtsp://127.0.0.1:<port>/<cam>")`, identical to how TUTK cameras are exposed, and go2rtc relays it to WebRTC/HLS/RTSP consumers the exact same way.

**Why A wins:**

1. **Clean separation.** Our supervisor code already speaks subprocess fluently (`internal/go2rtcmgr/manager.go`). We mirror that pattern verbatim.
2. **Crash isolation.** The reverse-engineered P2P stack is fiddly (KCP window tuning, heartbeat timing, fallback transports). If it panics, segfaults, or hangs, it dies in its own process — not ours.
3. **Vendoring safety.** The upstream repo has ~12 protocol files with byte-level RC5 / frame / KCP code. Pulling it in as a *module we build* (Go build vendors + lints it) guarantees the bytes are exact. Mechanically transcribing them into our tree risks silent corruption that fails only at packet-crypto time.
4. **Mirrors the upstream entry point.** The upstream already ships `cmd/gwell-proxy/` that publishes RTSP to a local port — we literally build that binary.
5. **Reuses go2rtc for everything downstream.** Recording, snapshots, WebRTC, HLS, stream-auth, SSE state — all of it already works on top of an RTSP source. Zero new plumbing.
6. **Incremental testability.** We can test the proxy standalone with a single camera and a vanilla RTSP viewer before ever wiring it into the bridge.

**Trade-offs we accept:**

- One extra process per Gwell camera. For 1–4 cameras this is nothing; the upstream already ships multi-camera per-process, so we pass all of them to one instance.
- Binary distribution: the Docker image needs `gwell-proxy` built during the image build. That's one more build stage — we already have a 3-stage Alpine build, so it's an additive stage.
- Slight latency cost vs. in-process handoff (negligible for RTSP over loopback).

### Option B — Import library directly (rejected)

Pull in `github.com/wlatic/hacky-wyze-gwell/wyze-p2p/pkg/gwell` + `pkg/stream` and feed H.264 to go2rtc via an `exec:` source or a WebSocket ingress. 

Rejected because:

- The upstream `cmd/gwell-proxy` already is the "library users" entry point — it's not a stable library API.
- In-process failure of the reverse-engineered stack takes the bridge down.
- We'd need to replace the upstream's FFmpeg RTSP writer with a Go-native H.264 → go2rtc sink. That's additional new code that carries no existing tests.
- Harder to swap the implementation later (e.g. if Wyze rotates keys or upstream forks).
- The hacky-wyze-gwell module path is `github.com/wlatic/wyze-gwell-bridge/wyze-p2p` — nested inside a repo rather than a clean `pkg`. Pulling it as a direct dep pulls the whole tree.

### Option C — Vendor as a nested Go module (also rejected)

`go mod vendor`-style copy into `internal/gwell/vendor/` and keep it in-tree.

Rejected for this first landing because: (a) we still need to build *something* from that source — either we build it in-process (back to Option B) or we build it as a separate binary (back to Option A). (b) The upstream has active development (Feb 2026) — keeping a live module path with a pinned SHA is better hygiene than a fork.

## 4. The Chosen Shape

### 4.1 Process Layout

```
Docker Container
├── wyze-bridge   (our Go binary, port 5080)
├── go2rtc        (sidecar, ports 1984/8554/8888/8889/8189)
└── gwell-proxy   (sidecar, spawned only if ≥1 Gwell camera exists,
                   publishes rtsp://127.0.0.1:<GWELL_RTSP_PORT>/<cam>)
```

The proxy is spawned **on demand** — if no Gwell camera is discovered, it is never started.

### 4.2 Package Map (new)

```
internal/gwell/
    doc.go           // package documentation
    config.go        // GwellConfig struct: RTSP port, binary path, log level, state dir
    manager.go       // subprocess lifecycle (mirrors go2rtcmgr/manager.go)
    producer.go      // Producer: given a CameraInfo + auth token, registers
                     //   it with the proxy and returns the rtsp:// URL for go2rtc
    client.go        // small HTTP/JSON-RPC client to the proxy's control API
                     //   (if upstream exposes one) OR config-file writer
    manager_test.go
    producer_test.go
    config_test.go
```

### 4.3 Data Flow (per Gwell camera)

```
Wyze Cloud Login (existing wyzeapi.Client)
    │
    │ access_token, user_id, phone_id
    ▼
gwell.Manager.Start(ctx)           ◄── spawns gwell-proxy binary once
    │
    │ registers camera {mac, enr, lan_ip, model, access_token}
    ▼
gwell.Producer.Connect(ctx, cam)
    │
    │ returns "rtsp://127.0.0.1:<port>/<cam_normalized_name>"
    ▼
go2rtcAPI.AddStream(ctx, name, rtspURL)    ◄── identical path to TUTK
    │
    ▼
StateStreaming → SSE / MQTT / webhooks (unchanged)
```

### 4.4 Subprocess Control Surface

`gwell-proxy` spawns with:

```
gwell-proxy
  --listen 127.0.0.1:<GWELL_CONTROL_PORT>
  --rtsp   127.0.0.1:<GWELL_RTSP_PORT>
  --state  <STATE_DIR>/gwell
  --log    (level)
```

And exposes an HTTP control API (either upstream already has one, or we add one as a thin wrapper):

- `POST /cameras` — register camera, returns RTSP path
- `DELETE /cameras/{mac}` — unregister
- `GET /cameras` — list + status

Logs go to stdout/stderr; our manager relays them through zerolog exactly like `go2rtcmgr`.

### 4.5 Authentication Hand-off

From the Wyze API payload we already have: `MAC`, `ENR`, `Nickname`, `FWVersion`, `Model`, `ProductType`, plus `LanIP` *sometimes* (often empty for Gwell — the proxy's P2P discovery recovers it). 

What the proxy additionally needs and we must hand it:

- `access_token` — current bridge auth token (refreshed by our existing `EnsureAuth`).
- `phone_id` / `user_id` — from `AuthState`.
- `app_version` / `app_name` — the two we already use for cloud calls.

These are passed via the `POST /cameras` body (per-camera) plus a process-global startup env/flag for whichever the proxy wants to treat as account-scoped.

### 4.6 Camera Manager Integration

In `internal/camera/manager.go`:

```go
// Old:
if cam.IsGwell() {
    m.log.Debug()...Msg("skipping Gwell camera (unsupported)")
    continue
}
supported = append(supported, cam)

// New:
supported = append(supported, cam) // include unconditionally
```

Then in `connectCamera`, split on protocol:

```go
var streamURL string
if cam.Info.IsGwell() {
    if m.gwell == nil {
        // Lazy-start the Gwell manager on first Gwell camera.
        if err := m.ensureGwellManager(ctx); err != nil { … }
    }
    u, err := m.gwell.Producer.Connect(ctx, cam.Info, m.api.Auth())
    if err != nil { /* error path identical to existing */ }
    streamURL = u
} else {
    streamURL = cam.StreamURL() // existing wyze:// URL
}
m.go2rtc.AddStream(ctx, cam.Name(), streamURL)
```

### 4.7 Parallelism

`ConnectAll` already uses `sync.WaitGroup` — Gwell cameras participate in that fan-out unchanged. The proxy is a singleton; its `RegisterCamera` call is the only mutex point, and it's fast (< 100ms HTTP round-trip locally).

### 4.8 State Persistence

The Gwell cloud session/token is cached for 7 days upstream. We'll persist it at `$STATE_DIR/gwell/session.json` via the proxy — matches our existing `$STATE_DIR` convention.

## 5. Environment Variables (new)

| Var | Default | Meaning |
|-----|---------|---------|
| `GWELL_ENABLED` | `true` | Master switch; when `false` Gwell cameras fall back to today's skip behavior. |
| `GWELL_BINARY`  | `gwell-proxy` (PATH lookup, plus `./gwell-proxy[.exe]`) | Override for dev. |
| `GWELL_RTSP_PORT` | `8564` | Loopback RTSP port the proxy publishes on (not exposed outside the container). |
| `GWELL_CONTROL_PORT` | `18564` | Loopback HTTP control API port. |
| `GWELL_LOG_LEVEL` | inherit | Optional override (`debug` / `info` / `warn`). |

These are documented in `DOCS/DESIGN.md` §5.8 in the follow-up commit.

## 6. Dockerfile

Add a build stage that runs `go build -o /out/gwell-proxy ./cmd/gwell-proxy` inside a clone of upstream pinned to a commit SHA; copy the resulting binary into the final stage next to `go2rtc`. Target size delta < 6 MB (pure-Go static binary).

## 7. Testing

Unit tests we own (in `internal/gwell/`):

- `TestProducer_ConnectBuildsCorrectURL` — mocks the control API, asserts the returned RTSP URL encodes cam name correctly.
- `TestManager_StartStop` — spawns `sleep 30` as the "proxy" (or the actual binary if `$GWELL_BINARY` is set in CI), asserts lifecycle.
- `TestManager_LazySpawn` — asserts the subprocess is *not* started until the first `EnsureStarted` call.
- `TestCameraManager_RoutesGwellToProducer` — injects a Gwell `CameraInfo`, asserts the producer is asked instead of the `wyze://` URL being built.

Protocol unit tests (RC5, XOR, giot_hash, frame encode/decode) live upstream and will run as part of that module's build inside the Docker stage. We don't re-host them.

Integration test (marked `-tags=integration`, off by default):

- `TestGwellIntegration_OGCam` — requires real credentials + a real OG camera on the LAN; asserts we get a 200 from the go2rtc `/api/streams` endpoint and that RTSP has > 0 producers within 15s.

## 8. Phased Delivery

1. **Land the scaffold** (this commit):
   - `internal/gwell/` package with `Manager`, `Producer`, `Config`, tests.
   - Camera manager routes Gwell cameras to the producer.
   - Feature-flagged behind `GWELL_ENABLED` so the skip behavior returns if the flag is off or the binary is missing.
   - All existing tests keep passing; new tests cover wiring, not protocol.
2. **Follow-up**: Dockerfile stage + `gwell-proxy` binary built from pinned upstream SHA; documentation in `DESIGN.md`.
3. **Follow-up**: Integration test on real hardware; tune KCP window / timeouts if needed.

## 9. Non-goals

- Re-implementing the Gwell protocol natively in this repo (see rejected Option B).
- Replacing `go2rtc` for Gwell cameras (the proxy produces RTSP; go2rtc still does WebRTC/HLS/snapshot serving).
- Supporting Gwell *cloud relay* (MTP) in the first cut — local-LAN P2P only; cloud relay is a follow-up.
