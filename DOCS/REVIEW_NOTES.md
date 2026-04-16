# Go Rewrite: Review Notes

**Date:** April 16, 2026 (updated)
**Branch:** `go-rewrite`
**Status:** 194 tests passing, `go vet` clean, old Python code removed

---

## What Was Built

65 files across all three design phases:

| Package | Source | Tests | Coverage |
| ------- | ------ | ----- | -------- |
| `internal/config/` | 4 | 4 | 95.2% |
| `internal/wyzeapi/` | 6 | 7 | 44.4% |
| `internal/go2rtcmgr/` | 3 | 4 | 61.4% |
| `internal/camera/` | 3 | 4 | 68.7% |
| `internal/mqtt/` | 5 | 5 | 10.3% |
| `internal/webui/` | 6 + static | 5 | 66.7% |
| `internal/snapshot/` | 2 | 3 | 47.6% |
| `internal/recording/` | 1 | 2 | 81.2% |
| `cmd/wyze-bridge/` | 1 | — | (integration) |
| Docker + compose | 2 | — | — |

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
- [x] WebUI: Basic Auth + Bearer token + `WB_AUTH` toggle
- [x] WebUI: SSE (`/events`) with camera_state, camera_added, camera_removed, snapshot_ready, bridge_status
- [x] WebUI: request logging middleware (method, path, status, duration)
- [x] Dockerfile: 3-stage Alpine, multi-arch via `TARGETARCH`, `video-rtc.js` fetched + embedded at build
- [x] Signal handling (SIGTERM/SIGINT) with graceful shutdown and state save

### Phase 2 — MQTT + Recording: Complete

- [x] MQTT: paho client, auto-reconnect, LWT (`{topic}/bridge/state`)
- [x] MQTT: publish state, quality, audio, net_mode, camera_info, stream_info, thumbnail
- [x] MQTT: subscribe to `set/quality`, `set/audio`, `set/night_vision`, `snapshot/take`, `stream/restart`
- [x] MQTT: night_vision command wired through to `wyzeapi.SetProperty("P3", ...)` with PID mapping
- [x] MQTT: Home Assistant discovery — camera, quality select, audio switch, night vision select entities
- [x] MQTT: MQTT Client carries `*wyzeapi.Client` reference for cloud API commands
- [x] Recording: go2rtc YAML config generation with per-stream `record`/`record_path`/`record_duration`
- [x] Recording: path template validation (auto-append `_%s` if time vars missing)
- [x] Recording: file pruning (15min interval, remove `.mp4` > `RECORD_KEEP`, clean empty dirs)
- [x] STREAM_AUTH: `ParseStreamAuth()` parses `user:pass@cam1,cam2|user2:pass2` format
- [x] STREAM_AUTH: single-user global auth injected into go2rtc `RTSP.Username`/`RTSP.Password`
- [x] SSE heartbeat + `bridge_status` (uptime, streaming count, total) every 30s

### Phase 3 — Snapshots + Polish: Mostly Complete

- [x] Snapshots: interval-based capture via go2rtc `GET /api/frame.jpeg`
- [x] Snapshots: sunrise/sunset scheduling (`go-sunrise`, reschedule after each event)
- [x] Snapshots: file pruning by age (5min tick, JPEG only)
- [x] Snapshots: per-camera filtering (`SNAPSHOT_CAMERAS`)
- [x] Snapshots: `OnCapture` callback → MQTT thumbnail publish
- [x] Per-camera overrides: `QUALITY_{CAM}`, `AUDIO_{CAM}`, `RECORD_{CAM}` env vars
- [x] `CAM_OPTIONS` in `config.yml` (YAML per-camera overrides)
- [ ] Webhook support (not started — Phase 3 stretch)
- [ ] HA add-on manifest update (`home_assistant/config.json`)
- [ ] Unraid template update (`unraid/wyze-bridge.xml`)
- [x] `MIGRATION.md` — created with full breaking changes, env var reference, STREAM_AUTH limitation
- [ ] `README.md` rewrite

---

## Remaining Actionable Work

### Must Do Before First Test With Real Cameras

1. **MQTT snapshot trigger wiring** — `handleSnapshotCommand` logs the request but doesn't call the snapshot manager. Fix: add `OnSnapshotRequest` callback to MQTT Client, wire `snapMgr.CaptureOne` in `main.go`. (~10 lines)

2. **Wyze API base URL injectability** — `Login()`, `RefreshToken()`, `GetCameraList()`, `SetProperty()` use hardcoded `const` URLs. Make `authAPI`, `wyzeAPI`, `cloudAPI` fields on `Client` with production defaults. This unblocks both real-camera testing (no code change needed there) and full unit test coverage (~40% more wyzeapi coverage). (~15 lines)

### Should Do Before Users See This

3. ~~MIGRATION.md~~ — Done. Covers env var changes, STREAM_AUTH limitation, removed features, new variables.

4. **README.md rewrite** — Replace Python-focused content with Go rewrite docs.

5. **HA add-on manifest** — Update `home_assistant/config.json` for Go binary entrypoint, remove Python deps, update port list.

6. **Unraid template** — Update `unraid/wyze-bridge.xml`.

### Nice to Have

7. ~~go2rtc version detection~~ — Dropped. Hardcoded version is fine; pinned in Dockerfile.

8. ~~STREAM_AUTH per-camera~~ — Resolved. go2rtc source confirms single global RTSP credentials only (loopback exempt). Per-camera scoping documented as a feature loss in MIGRATION.md.

9. **Webhook support** — `internal/webhooks/` package that POSTs camera state changes to user-configured URLs. Last remaining feature.

10. ~~Config hot-reload~~ — Dropped. Not in design, not needed.

---

## Coverage Improvement Roadmap

The biggest unlock is item #2 (wyzeapi URL injectability). With that single change:

| Package | Current | After URL fix | Limiting factor |
| ------- | ------- | ------------- | --------------- |
| config | 95.2% | 95.2% | loadYAML edge cases |
| recording | 81.2% | 81.2% | `RunPruner` loop |
| camera | 68.7% | ~75% | `Discover()` needs mock Wyze API |
| webui | 66.7% | ~70% | `logMiddleware` + serve loop |
| go2rtcmgr | 61.4% | 61.4% | `Start()`/`Stop()` need real binary |
| snapshot | 47.6% | ~50% | `Run()` loop, `runSunEvents()` |
| **wyzeapi** | **44.4%** | **~85%** | Currently blocked by const URLs |
| mqtt | 10.3% | 10.3% | paho methods need broker |

The mqtt 10.3% is structural — the tested code is `format.go` (pure functions) while the remaining ~90% is paho glue (`Connect`, `publish`, `subscribe`). Options to improve:
- In-process test broker (`github.com/mochi-mqtt/server`) — adds a dep but enables full integration tests
- Accept 10% and rely on end-to-end Docker testing

---

## Architecture Notes (unchanged observations)

**Clean separation** — each package has a single responsibility, no circular imports, all dependencies flow downward from `cmd/` through `internal/`.

**Design deviations** — documented in `IMPLEMENTATION_NOTES.md`. Key ones: `Streams` type changed to `interface{}` for recording support, WebUI templates inline instead of embedded HTML files, `mqtt/format.go` added for testability.

**Deliberately dropped** from Python bridge: remote P2P, binary TUTK SDK, FFmpeg, MediaMTX, Flask/Jinja, pickle caching.

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
