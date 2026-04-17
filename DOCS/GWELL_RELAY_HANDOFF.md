# Gwell Relay-Only Camera Handoff Document

> **RESOLVED 2026-04-20.** The premise below (that GW_BE1 Doorbell Pro uses
> Gwell P2P for media) was wrong. It streams over WebRTC via Wyze's
> mars-webcsrv signaling server, handled natively by go2rtc's
> `#format=wyze` source. See commits `fca2f7d` (diagnostic pass that
> cracked it) and `ced42cb` (final wiring). The notes below are
> preserved for historical context on what we tried; don't use them
> as a starting point for new work.

**Branch:** `go-rewrite` (all changes squashed into final commit)
**Baseline:** `handoff` branch (the "before" state)
**Date:** 2025-06-15
**Camera under test:** GW_BE1_7C78B2A53C69 — Wyze Doorbell Pro (front_door)

---

## 1. Problem Statement

The Wyze Doorbell Pro (model `GW_BE1`) is a **relay-only** Gwell/IoTVideo camera. Unlike other Gwell cameras (OG, OG 3X) that have a LAN IP and can be reached via direct UDP P2P, the Doorbell Pro:

- Has **no LAN IP** (even though it's wired-power, it reports `lanIP=""`)
- Returns `peer=0.0.0.0:0` in the CALLING ACK from the P2P server
- Can only communicate through the P2P server's **TCP relay** or possibly **PASSTHROUGH** routing

The camera completes the full Gwell handshake (Certify → InitInfo → Subscribe → CALLING) but **never sends an AVSTREAM ACCEPT** in response to our INITREQ. Without ACCEPT, streaming cannot begin.

## 2. Camera Details

| Field | Value |
|-------|-------|
| Model | GW_BE1 (Wyze Doorbell Pro) |
| MAC | 7C78B2A53C69 |
| Firmware | 1.0.77 |
| Power | Wired (but battery-design lineage firmware) |
| LAN IP | None (empty string) |
| P2P Server | 3.13.212.24:28800 (hardcoded fallback) |
| Dynamic P2P discovery | 34.215.36.59:51701, 18.118.90.161:51701, 35.85.21.174:51701 — **always times out** |
| TID | Device list returns 2 entries: the doorbell + an unnamed second device (TID=0x000186D600139C81) |

## 3. Protocol Flow (What Works)

```
CertifyReq → CertifyResp (OK)
  → InitInfoMsg → InitInfoResp:
      0x0D: routing session ID ✓
      0xA7: device list (2 devices) ✓
  → Subscribe (OK)
  → CALLING(0xA4) → CALLING ACK: peer=0.0.0.0:0 ← NORMAL for relay-only
  → Phase 5 (transport setup)
  → streamLoop:
      TCP relay connects to P2P server ✓
      BuildTCPRelayRegister sent ✓
      INITREQ sent via ctrl KCP + raw MTP + PASSTHROUGH ✓
      ... waiting for ACCEPT ... ← NEVER ARRIVES
      TCP relay EOF after ~5-10s ← relay server closes connection
```

## 4. What Was Changed (handoff → go-rewrite)

### 4.1 Core P2P Session (`internal/gwell/upstream/gwell/session.go`)

**CALLING ACK 0.0.0.0:0 handling (reverted to upstream):**
The calling() function previously fast-failed when CALLING ACK returned `0.0.0.0:0`. This was wrong — it's the *expected* response for relay-only cameras. Now matches upstream: single CALLING attempt, no retry, proceeds to Phase 5 regardless of peer address. Added an early break for relay-only cameras after the CALLING ACK (one 500ms read for a late MTP_RES then break, instead of burning 3s×20 iterations).

**probeAndWait skipped for relay-only:**
When both `lanMTPAddrs` and `relayAddrs` are empty (relay-only camera), the 12-second `probeAndWait()` is skipped entirely. It runs UDP-only operations (PortStatReq, detect probes) that are useless for TCP-relay-only cameras, and the idle time was killing the TCP relay connection (~10s server idle timeout) before we could send INITREQ.

**TCP relay fallback (`startRelayActivation`):**
- Removed 500ms sleep before TCP dial — every millisecond counts for relay matching
- Added keepalive: re-sends `BuildTCPRelayRegister` every 3s to prevent relay idle timeout
- Added `SessionSocket` activation goroutine (10 rounds of 0xCA sub_type=1/2 to P2P server) — attempts to tell the server to set up relay forwarding

**TCP relay reader started before INITREQ loop:**
Critical fix. Previously the TCP relay reader goroutine (`readTCPRelay`) was started *after* the INITREQ loop. This meant the camera's ACCEPT (which arrives on TCP relay for relay-only cameras) was never read, causing a 60s timeout. The reader now starts before the first INITREQ.

**INITREQ loop improvements:**
- TCP frame channel (`tcpFrameCh`) is drained during INITREQ for ACCEPT detection on TCP path
- PASSTHROUGH DATA (0xB9) frames are parsed during INITREQ — the P2P server may route the camera's ACCEPT via routing session
- Rate-limited to 200ms between INITREQ sends (was unbounded)
- Reduced logging frequency (every 50 retries instead of 10)

**PASSTHROUGH send path (`sendMTP`):**
Every MTP frame sent to the camera is also wrapped in `BuildPassthroughData(0xB9)` and sent via UDP to the P2P server. The server can theoretically forward it to the camera via the routing session. This is a parallel transport path alongside TCP relay.

**AVSTREAM INITREQ renewal (15s interval):**
The Doorbell Pro firmware (battery-design lineage) has a ~20-25s live-view session timeout. Without periodic INITREQ renewal, the camera silently stops sending video after ~22s. The main streaming loop now re-sends INITREQ via ctrl KCP every 15 seconds.

**KCP SyncSendState:**
After the INITREQ retry loop (which manually builds KCP push segments bypassing the KCP state machine), the ctrl KCP's `sndNxt` is synced to the actual next sequence number. Without this, subsequent `ctrlKCP.Send()` calls reused SN=0 and the camera dropped them as duplicates.

**Silent-skip foreign-session packets:**
During `initInfo()`, the P2P server broadcasts 0xAA push-notification frames to all sessions sharing an endpoint. These fail to decrypt (wrong session key) and were spamming "decrypt FAILED" logs. Now silently skipped. The raw-header log line is moved to after successful decrypt.

**TCP relay frame draining in main stream loop:**
Non-blocking drain of `tcpFrameCh` into KCP during the main streaming loop, with meter REQ/ACK handling.

**Relay re-registration in main loop:**
During streaming, the online-socket keepalive now also re-registers with relay servers (`BuildTCPRelayRegister` to each `udpRelayTarget`) and sends a `SessionSocket` with relay ports to the P2P server.

**`readTCPRelay` goroutine:**
New function that reads MTP frames (0xC0 magic, length-prefixed) from the TCP relay connection and sends them to a channel. Handles framing, resync on invalid lengths, and exits on EOF.

### 4.2 New MTP Frame Builders (`internal/gwell/upstream/gwell/mtp.go`)

- `BuildTCPRelayRegister`: 74-byte 0xC0/0x80 MTP frame for TCP relay registration
- `BuildPassthroughData`: GUTES 0xB9 DATA frame wrapping MTP payload for server-routed transport
- `BuildSessionSocket`: 0x7F/0xCA GUTES frame for P2P server session notifications

### 4.3 KCP Enhancement (`internal/gwell/upstream/gwell/kcp.go`)

- `SyncSendState(nextSN uint32)`: Advances sndNxt and sndUna to account for segments sent outside the KCP state machine

### 4.4 gwell-proxy (`cmd/gwell-proxy/main.go`)

**H.264 filter (`h264Filter`):**
Strips IoTVideo/Gwell HDLC framing (0x7E delimiters, ~320 bytes of 0x7E/0xFF padding) from the raw avPayload stream before it reaches ffmpeg. The camera sends framing before every SPS NAL. Pre-sync: buffers until first Annex B SPS (00 00 00 01 x7). Post-sync: drops chunks that have no start codes and consist mostly of framing bytes (≥80% 0x7E/0xFF/0xFE).

**Raw H.264 dump (`--dump-h264 <dir>`):**
Tees raw (unfiltered) bytes per-camera into `<dir>/<cam>-<unix-ms>.h264` for offline `ffprobe` analysis. Controlled by `GWELL_DUMP_DIR` env var.

**Configurable ffmpeg loglevel (`--ffmpeg-loglevel`):**
Upstream hard-coded `-loglevel debug` which produces thousands of lines/second. Now threaded through from `GWELL_FFMPEG_LOGLEVEL` env (default `warning`).

**Configurable deadman timeout (`--deadman-timeout`):**
Max no-data interval before forcing reconnect. Controlled by `GWELL_DEADMAN_TIMEOUT` env (default 2m).

**Force re-discovery on stream error:**
When `streamCamera()` fails, `refreshDiscovery(forceDiscover=true)` is called before reconnect, bypassing the token cache.

### 4.5 ffmpeg Pipeline (`internal/gwell/upstream/stream/ffmpeg.go`)

**Switched from libx264 re-encode to `-c:v copy`:**
The previous session added libx264 re-encode to fix timestamp issues with go2rtc's RTSP server. This has been reverted to copy mode to reduce CPU cost and avoid ffmpeg's decoder choking on residual IoTVideo framing bytes. The h264Filter in gwell-proxy now strips framing before ffmpeg sees it.

**`StartFFmpegPublisher` signature change:**
Added `logLevel` parameter (was hardcoded to `debug`).

### 4.6 Bridge Startup Reordering (`cmd/wyze-bridge/main.go`)

Major refactoring of `main()` for earlier WebUI availability:

1. **WebUI starts immediately** with nil go2rtc API — shows UI, serves SSE, accepts shim endpoints while go2rtc boots
2. **Camera discovery runs before go2rtc launch** so the YAML config can pre-declare all stream slots
3. **go2rtc YAML pre-declares Gwell camera slots** with empty sources arrays (`name: []`), eliminating the need for runtime `AddStream` calls
4. **Late-bind go2rtc API** via `SetGo2RTCAPI()` on both `camera.Manager` and `webui.Server` (uses `atomic.Pointer`)
5. **Handlers return 503** ("bridge still starting") if go2rtc isn't attached yet

Extracted helper functions: `loadOrInitState`, `setupGo2RTC`, `startGwellProxyIfEnabled`, `setupMQTT`, `setupWebhooks`, `wireCameraStateChanges`, `wireSnapshotHandlers`, `startBridgeHeartbeat`, `shutdownBridge`.

### 4.7 Camera Manager (`internal/camera/manager.go`)

- `go2rtc` field changed from direct pointer to `atomic.Pointer[go2rtcmgr.APIClient]`
- `SetGo2RTCAPI()` / `go2rtcClient()` for late-binding
- `connectCamera()` for Gwell cameras: no longer calls `AddStream` (slot is in YAML); just transitions to StateStreaming
- All go2rtc operations (HealthCheck, SetQuality, RestartStream) gate on `go2rtcClient() != nil`

### 4.8 go2rtc Config Builder (`internal/go2rtcmgr/config.go`)

- Empty URL now emits `name: []` (publish-only slot) instead of `name: [""]` which go2rtc treated as a malformed source
- go2rtc `[OOO] ... - reset` log lines are now suppressed (previously trace-level noise)

### 4.9 WebUI: HA Ingress Support (`internal/webui/`)

- `ingressBasePath(r)` reads `X-Ingress-Path` header for Home Assistant ingress proxy
- All template URLs prefixed with `{{.BasePath}}` / `{{$.BasePath}}`
- `app.js`: `window.__BASE_PATH` injected via `<script>` tag, used in all fetch/EventSource URLs
- `proxy.go`: go2rtc late-bind checks with 503 fallback

### 4.10 Config (`internal/config/config.go`)

New fields: `GwellFFmpegLogLevel`, `GwellDumpDir`, `GwellDeadmanTimeout` with env vars `GWELL_FFMPEG_LOGLEVEL`, `GWELL_DUMP_DIR`, `GWELL_DEADMAN_TIMEOUT`.

### 4.11 Home Assistant Add-on

- `config.yaml`: version bumped to 4.0.3-beta, added `image:` field for pre-built images
- `Dockerfile`: default `BUILD_FROM` for standalone builds
- `run.sh`: switched to `#!/usr/bin/with-contenv bashio` + `set -euo pipefail`
- CI workflow: added HA add-on image build step

### 4.12 DevContainer / CI

- GPG signing support in devcontainer (packages, mounts, setup scripts)
- `gwell-proxy` binary added to `.gitignore`
- `cycle.sh`: clears gwell token cache and dumps before each run

## 5. What's Still Broken

### The Core Issue: Camera Never Sends ACCEPT

The TCP relay connects successfully to the P2P server. INITREQ is sent via three parallel paths:
1. **ctrl KCP** (UDP to server) — the standard path
2. **Raw MTP via TCP relay** — directly on the TCP connection
3. **PASSTHROUGH (0xB9)** — wrapped MTP sent via UDP routing session

The camera never responds with AVSTREAM ACCEPT on any path. The TCP relay connection dies with EOF after ~5-10s (the relay server's idle timeout closes it because there's no matching camera-side registration).

### TCP Relay Keepalive Fails

Re-sending `BuildTCPRelayRegister` every 3s was added to prevent idle timeout. It fails with "broken pipe" — the server has already closed the connection before the first keepalive fires. The sequence is:
1. TCP connect + register (t=0)
2. ~5s of INITREQ sends on all paths
3. Server closes TCP (EOF at ~t=5s)
4. Keepalive at t=3s succeeds, but t=6s fails (broken pipe)

## 6. Hypotheses for Investigation

### H1: Camera Needs a Different Notification Mechanism
The CALLING message forwarded by the P2P server may not be sufficient for GW_BE1. The Wyze app may use a different mechanism (push notification, MQTT, or a proprietary wakeup command) to tell the camera to start its P2P session. Without this, the camera never initiates its side of the relay connection.

### H2: P2P Server Doesn't Support TCP Relay for This Model
The relay server closes TCP after ~5s because no matching camera-side registration arrives. The GW_BE1 may use a completely different relay mechanism than what `BuildTCPRelayRegister` implements.

### H3: PASSTHROUGH is the Correct Path but Needs Format Adjustments
The 0xB9 PASSTHROUGH DATA frame wraps MTP correctly per the protocol spec, but the P2P server may expect different fields (different linkID placement, different sub-type, or different encryption). Packet captures from the Wyze app would clarify.

### H4: Missing Camera-Side Registration Step
There may be a step the Wyze app performs between CALLING ACK and INITREQ that tells the camera to register its side on the relay. This could be:
- A specific SessionSocket sub-type we're not sending
- A command sent via the routing session (not PASSTHROUGH)
- An entirely different GUTES frame type

### H5: The Second Device in the Device List
InitInfoResp returns 2 devices — the doorbell and an unnamed device (TID=0x000186D600139C81). This could be a chime unit, a gateway, or a relay intermediary. The CALLING message might need to target *both* devices, or the second device might need to be involved in the relay setup.

### H6: Firmware Session Timeout
Even when video starts flowing, the Doorbell Pro firmware has a ~20-25s live-view session timeout (battery-design lineage). The INITREQ renewal (every 15s) was added to address this but has not been tested with actual streaming because we never get past ACCEPT.

## 7. Debugging Tools Available

- **`GWELL_DUMP_DIR`**: Set to a path to get raw H.264 dumps per session (pre-filter). Useful for verifying the camera is actually sending video data.
- **`GWELL_FFMPEG_LOGLEVEL=debug`**: Enables verbose ffmpeg output (warning: very noisy).
- **`GWELL_DEADMAN_TIMEOUT`**: Configurable deadman interval (default 2m).
- **`FORCE_IOTC_DETAIL=true`**: Enables debug-level logging in both go2rtc and the bridge.
- **Session.go logging**: The code has detailed `log.Printf` at every protocol step. All MTP frame types, sizes, and KCP sequence numbers are logged.
- **Packet captures**: The most valuable next step would be capturing the Wyze app's traffic to the P2P server (3.13.212.24:28800) during a successful Doorbell Pro live view, then comparing the frame sequence against our implementation.

## 8. Key Files to Read

| File | What to look at |
|------|-----------------|
| `internal/gwell/upstream/gwell/session.go` | `calling()`, `startRelayActivation()`, `streamLoop()` (INITREQ loop), `sendMTP()`, `readTCPRelay()` |
| `internal/gwell/upstream/gwell/mtp.go` | `BuildTCPRelayRegister`, `BuildPassthroughData`, `BuildSessionSocket`, `MTPPayloadOffset`, `FeedMTPToKCP` |
| `internal/gwell/upstream/gwell/certify.go` | `ParseInitInfoResp` (device list parsing), `BuildCallingMsg` |
| `internal/gwell/upstream/gwell/kcp.go` | `SyncSendState` |
| `cmd/gwell-proxy/main.go` | `h264Filter`, `writeTracker`, `streamCamera()` |
| `internal/gwell/upstream/stream/ffmpeg.go` | ffmpeg command-line construction |
| `internal/gwell/upstream/README.md` | Upstream divergence tracking — documents every change from upstream |

## 9. Upstream Reference

Upstream repo: `github.com/wlatic/hacky-wyze-gwell`
Pinned commit: `9c1b99f8b6e4e4aea17a19a3cd7d2d169dda6e45`

The upstream code works for OG cameras (GW_GC1) that have LAN IPs and use direct UDP P2P. It does NOT support relay-only cameras. All relay/TCP/PASSTHROUGH code is our addition.

## 10. Recommended Next Steps

1. **Packet capture the Wyze app** connecting to the Doorbell Pro. Compare frame-by-frame against our implementation. Focus on what happens between CALLING ACK and the first video frame.
2. **Check if the Wyze app uses a different P2P server** for Doorbell Pro. Our dynamic discovery to ports 51701 always times out — the app may use different discovery endpoints.
3. **Investigate the second device** in the InitInfoResp device list. Determine if it's a chime, gateway, or something that needs separate handling.
4. **Test PASSTHROUGH format variations** — different encryption modes, different payload layouts, with/without the MTP 0xC0 header.
5. **Consider alternative relay architectures** — the Wyze app might not use TCP relay at all for GW_BE1; it might use a WebRTC-style relay through Wyze's cloud infrastructure.
