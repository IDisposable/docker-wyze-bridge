# Go Rewrite: Review Notes

**Date:** April 17, 2026 (updated)
**Branch:** `go-rewrite`
**Status:** all tests passing, `go vet` clean, old Python code removed, 4.0-beta HA addon shipping

---

## What Was Built

~70 Go files + HA addon + Dockerfiles across all three design phases.

| Package | Source | Tests |
| ------- | ------ | ----- |
| `internal/config/` | 4 | 4 |
| `internal/wyzeapi/` | 6 | 7 |
| `internal/go2rtcmgr/` | 3 | 4 |
| `internal/camera/` | 3 | 4 |
| `internal/mqtt/` | 5 | 5 |
| `internal/webui/` | 6 + static | 5 |
| `internal/snapshot/` | 2 | 3 |
| `internal/recording/` | 1 | 2 |
| `internal/webhooks/` | 1 | 1 |
| `internal/gwell/` | 5 | 4 |
| `internal/gwell/upstream/` | 14 (vendored) | vendored upstream |
| `cmd/wyze-bridge/` | 1 | (integration) |
| `home_assistant/` | config + run.sh + Dockerfile + translations | — |
| `docker/` | Dockerfile | — |

---

## Feature Parity Checklist

### Phase 1 — Core Streaming: Complete

- [x] Config: env vars, Docker secrets (`/run/secrets/`), YAML, per-camera overrides
- [x] Config: `API_ID`/`API_KEY` backward-compat aliases via `secretWithAlias()`
- [x] Wyze API: login with triple-MD5 password hash, HMAC-MD5 signing
- [x] Wyze API: MFA/TOTP login flow (`totp.go` — RFC 4226/6238, no external dep)
- [x] Wyze API: camera discovery from `get_object_list`, Gwell filtering
- [x] Wyze API: token refresh, state persistence (`wyze-bridge.state.json`)
- [x] go2rtc: subprocess start/stop/wait, stdout relay through zerolog with level remapping
- [x] go2rtc: YAML config generation (streams, RTSP, WebRTC, ICE, STUN, recording)
- [x] go2rtc: HTTP API client (list/add/delete streams, snapshot, health check)
- [x] Camera: state machine (Offline → Discovering → Connecting → Streaming → Error)
- [x] Camera: exponential backoff (`min(5s × 2^n, 5min)`), reset on success
- [x] Camera: filter by name/model/MAC with block mode, never filter to empty
- [x] Camera: health polling (30s), reconnect errored cameras (10s tick with backoff check)
- [x] WebUI: camera grid with snapshot preview + state badges
- [x] WebUI: single camera page with go2rtc WebRTC player (`video-rtc.js`)
- [x] WebUI: REST API — `/api/cameras`, `/api/cameras/{name}/{action}`, `/api/snapshot/{name}`
- [x] WebUI: M3U8 playlist generation + `/cams.m3u8` and `/stream/{name}.m3u8` compat aliases
- [x] WebUI: Basic Auth + Bearer token + `BRIDGE_AUTH` toggle
- [x] WebUI: SSE (`/events`) with camera_state, camera_added, camera_removed, snapshot_ready, bridge_status
- [x] WebUI: request logging middleware (method, path, status, duration)
- [x] Dockerfile: 3-stage Alpine, multi-arch via `TARGETARCH`, `video-rtc.js` fetched + embedded at build
- [x] Signal handling (SIGTERM/SIGINT) with graceful shutdown and state save

### Phase 2 — MQTT + Recording: Complete

- [x] MQTT: paho client, auto-reconnect, LWT (`{topic}/bridge/state`)
- [x] MQTT: publish state, quality, audio, net_mode, camera_info, stream_info, thumbnail
- [x] MQTT: subscribe to `set/quality`, `set/audio`, `set/night_vision`, `snapshot/take`, `stream/restart`
- [x] MQTT: snapshot trigger wired to `snapMgr.CaptureOne` via `OnSnapshotRequest` callback
- [x] MQTT: night_vision command wired through to `wyzeapi.SetProperty("P3", ...)` with PID mapping
- [x] MQTT: Home Assistant discovery — camera, quality select, audio switch, night vision select entities
- [x] MQTT: MQTT Client carries `*wyzeapi.Client` reference for cloud API commands
- [x] Recording: go2rtc YAML config generation with per-stream `record`/`record_path`/`record_duration`
- [x] Recording: path template validation (auto-append `_%s` if time vars missing)
- [x] Recording: file pruning (15min interval, remove `.mp4` > `RECORD_KEEP`, clean empty dirs)
- [x] STREAM_AUTH: `ParseStreamAuth()` parses `user:pass@cam1,cam2|user2:pass2` format
- [x] STREAM_AUTH: single-user global auth injected into go2rtc `RTSP.Username`/`RTSP.Password`
- [x] SSE heartbeat + `bridge_status` (uptime, streaming count, total) every 30s

### Phase 3 — Snapshots, Polish, Packaging: Complete

- [x] Snapshots: interval-based capture via go2rtc `GET /api/frame.jpeg`
- [x] Snapshots: sunrise/sunset scheduling (`go-sunrise`, reschedule after each event)
- [x] Snapshots: file pruning by age (5min tick, JPEG only)
- [x] Snapshots: per-camera filtering (`SNAPSHOT_CAMERAS`)
- [x] Snapshots: `OnCapture` callback → MQTT thumbnail publish
- [x] Snapshots: `SNAPSHOT_PATH` + `SNAPSHOT_FILE_NAME` split mirroring `RECORD_*`; MkdirAll full parent chain
- [x] Per-camera overrides: `QUALITY_{CAM}`, `AUDIO_{CAM}`, `RECORD_{CAM}` env vars
- [x] `CAM_OPTIONS` in `config.yml` (YAML per-camera overrides)
- [x] Webhooks: `internal/webhooks/` — POST JSON on state change (offline/streaming/error), `WEBHOOK_URLS` CSV
- [x] HA add-on: nested config schema (wyze / bridge / camera / snapshot / record / mqtt / filter / location / webhooks / gwell / debug)
- [x] HA add-on: `init: false` for s6-overlay, Dockerfile pulls pre-built binaries from CI GHCR image
- [x] HA add-on: `run.sh` bashio → env with jq fan-out for `camera.options` + list-typed fields
- [x] HA add-on: `/media/wyze_bridge/{snapshots,recordings}/...` defaults via run.sh
- [x] HA add-on: translations/en.yaml with nested labels + descriptions
- [x] `MIGRATION.md` — full 4.0 rename table, snapshot layout change, removed features
- [x] `README.md` — rewritten for Go bridge
- [ ] ~~Unraid template~~ — intentionally dropped; see MIGRATION.md "Removed: Unraid Template"

### Phase 4 — Gwell Protocol Cameras (GW_BE1/GC1/GC2/DBD): Scaffolded, not runtime-ready

- [x] `internal/gwell/`: Manager (subprocess lifecycle), Producer (per-camera register), APIClient (HTTP control API), Config
- [x] `internal/camera/manager.go`: routes `IsGwell()` cameras to the producer
- [x] Upstream `github.com/wlatic/hacky-wyze-gwell` vendored at `internal/gwell/upstream/` (pinned SHA 9c1b99f8)
- [x] `GWELL_ENABLED` defaults to `false` — current 4.0-beta silently skips GW_* cameras (matches 3.x behavior)
- [ ] `cmd/gwell-proxy/main.go` — needs ~400 LOC of glue: HTTP control API + session orchestration + ffmpeg publisher
- [ ] Mars credentials endpoint — upstream `ParseAccessToken` wants a device-scoped accessId/accessToken minted via `POST wyze-mars-service.wyzecam.com/plugin/mars/v2/regist_gw_user/<deviceID>` (Wpk-signed). Not currently in `internal/wyzeapi/`.
- [ ] Dockerfile stage to `go build ./cmd/gwell-proxy` and ship the binary

See [GWELL_INTEGRATION.md](GWELL_INTEGRATION.md) for the full design; note that doc predated the upstream-reality discovery and is partially aspirational — specifically the `cmd/gwell-proxy` binary and its `POST /cameras` HTTP API are our design, not upstream's.

---

## Remaining Actionable Work

### Blocks Gwell camera support (not a 4.0 blocker)

1. **Mars credential minting in `internal/wyzeapi/`** — implement `MarsRegisterGWUser(deviceID) (accessID, accessToken string)` hitting `wyze-mars-service.wyzecam.com` with Wyze's Wpk HMAC request signing. This is the one non-mechanical piece.

2. **`cmd/gwell-proxy/main.go`** — HTTP control API, per-camera session lifecycle around `gwell.NewSession(...)`, spawn `stream.FFmpegPublisher` per camera pushing to loopback go2rtc:8554, shut down cleanly on DELETE. Glue only once #1 lands.

3. **Dockerfile** — add `RUN go build -o /gwell-proxy ./cmd/gwell-proxy` in the existing `builder` stage; `COPY --from=builder /gwell-proxy /usr/local/bin/gwell-proxy` in runtime. Flip `GWELL_ENABLED` default back to `true`.

4. **Integration test** on real OG / Doorbell Pro hardware.

### Coverage (low priority, not tracked here)

Wyze API client's const URLs still make a chunk of that package untestable without network. Not a release blocker — defer until we need the coverage number.

---

## Architecture Notes

**Clean separation** — each package has a single responsibility, no circular imports, all dependencies flow downward from `cmd/` through `internal/`.

**Design deviations** — documented in `IMPLEMENTATION_NOTES.md`. Key ones: `Streams` type changed to `interface{}` for recording support, WebUI templates inline instead of embedded HTML files, `mqtt/format.go` added for testability.

**Deliberately dropped** from Python bridge: remote P2P, binary TUTK SDK, FFmpeg (in the bridge), MediaMTX, Flask/Jinja, pickle caching, Unraid template.

**4.0 rename pass (breaking)**: env vars reorganized by subsystem (`WB_*`→`BRIDGE_*`, `TOTP_KEY`→`WYZE_TOTP_KEY`, `IMG_DIR`→`SNAPSHOT_PATH`, `SNAPSHOT_INT`→`SNAPSHOT_INTERVAL`, `MQTT_DTOPIC`→`MQTT_DISCOVERY_TOPIC`, dropped `SNAPSHOT_FORMAT`). No aliases — 3.x configs must update. Full table in `MIGRATION.md`.

---

## Outstanding Upstream Issues (go2rtc)

### HLS from Wyze TUTK source stops after ~1 second

**Reproduction:** play `http://localhost:1984/api/stream.m3u8?src={cam}` directly against go2rtc (bypassing our bridge proxy) in VLC → one frame renders at 0:00, playback halts at 0:01. Same behavior for WYZE_CAKP2JFUS (V3) and HL_CAM4 (V4). RTSP from the same source plays cleanly, so the TUTK stream *is* producing frames — go2rtc just can't assemble them into HLS segments.

**Likely cause:** packet-reassembly bug in go2rtc's Wyze TUTK handler. The `[OOO]` reset branch never advances its `expected` counter:

```text
[OOO] ch=0x05 #3643 frameType=0x00 pktTotal=107 expected pkt 0, got 102 - reset
[OOO] ch=0x05 #3643 frameType=0x00 pktTotal=107 expected pkt 0, got 103 - reset
[OOO] ch=0x05 #3643 frameType=0x00 pktTotal=107 expected pkt 0, got 104 - reset
[OOO] ch=0x05 #3643 frameType=0x01 pktTotal=107 expected pkt 0, got 106 - reset
```

Each new packet in the same burst re-triggers the same reset because the counter stays at 0. That destroys frame integrity, which in turn prevents HLS from building valid fMP4 segments past the first. RTSP can still pass packets through because it's frame-passthrough, not segment-assembly.

**Status:** **Not our bug.** Upstream location: [`pkg/tutk/frame.go`, `FrameHandler.handleVideo`](https://github.com/AlexxIT/go2rtc/blob/v1.9.14/pkg/tutk/frame.go) — unchanged from v1.9.14 through master/dev. Root cause: on `FrameNo` mismatch the function re-seeds state with `waitSeq=0`, then the `PktIdx != waitSeq` check fails for any mid-frame packet, calls `cs.reset()` clobbering `frameNo`, returns — every subsequent continuation packet re-enters the same doomed path. Fix: gate re-init on `hdr.PktIdx == 0`; on real OOO inside a started frame, set `cs.waitSeq = cs.pktTotal` as a sentinel so stray continuations for a poisoned frame are silently dropped instead of resetting. Reported upstream as [Issue #2215](https://github.com/AlexxIT/go2rtc/pull/2215) on 2026-04-16. [PR #2217](https://github.com/AlexxIT/go2rtc/pull/2217) submitted.

**Workaround in our bridge:** HLS URL still displayed for external-player compatibility, but WebRTC/MSE via `/ws?src=...` is the primary in-browser path (unaffected — it delivers frames directly). RTSP works fine too.

**Noise reduction:** The `[OOO]` log spam itself was made invisible at `LOG_LEVEL=debug` in [internal/go2rtcmgr/manager.go](../internal/go2rtcmgr/manager.go) (`emitLogLine` — unprefixed stdout routes to our trace level).
