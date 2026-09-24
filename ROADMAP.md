# Roadmap

Deliverables the maintainers plan to work on, biggest-picture items
first. Each entry names the tracking issue(s), the reason it's still
open (i.e. what shipped versions already do or don't cover), and the
concrete work it involves. Items graduate to a release CHANGELOG when
they ship.

## 4.6 — WebUI grid lite mode

**Tracks:** [#97](https://github.com/IDisposable/docker-wyze-bridge/issues/97)

The current grid ([internal/webui/templates.go:174](internal/webui/templates.go#L174))
renders a live `<video-rtc>` element per camera tile, backed by MSE
or WebRTC. On modern desktop browsers this is fine; on mobile Safari
and low-end Android WebViews the concurrent decode load exceeds the
device's video pipeline once a couple of high-resolution tiles are
in flight (Deach01's report: two 2560×1440 HL_CAM4 tiles push an
iPad Mini 5 / iPhone 15 Safari past the ceiling — same six-camera
grid works fine on Firefox Linux, Edge Windows, Chrome on Shield).
Streams themselves are healthy end-to-end (RTSP to VLC / HA at HD
+ SD simultaneously); the failure is purely browser-side decode.

The plan is a **grid-lite mode**: JPEG-snapshot tiles refreshed on a
short interval (SSE `snapshot_ready` already exists in [app.js:36](internal/webui/static/app.js#L36)),
with the live `<video-rtc>` reserved for the focused/hovered card or
the camera detail page. The backend snapshot pipe at
`/api/snapshot/{name}` is already wired ([internal/snapshot/](internal/snapshot/)),
so the change is confined to the WebUI templates + `app.js`.

**Scope:**
- Add a per-server flag (env + HA option) `WEBUI_GRID_MODE=live|lite|auto`.
  `auto` detects `navigator.userAgent` / touch-capability at render
  time and picks lite for mobile Safari + low-power Android, live
  everywhere else.
- Under lite, the grid tile is an `<img>` refreshed by the existing
  `snapshot_ready` SSE handler (already implemented, just currently
  gated on the tile being non-streaming). Live becomes an opt-in
  click-to-preview overlay per tile, plus the detail page unchanged.
- Optional follow-on: pagination as the reporter suggested (4-per-page
  live tiles), covered by the same flag with an additional value.

**Non-goals for 4.6:**
- Rewriting `<video-rtc>` itself. go2rtc's element is upstream; we
  only choose whether to instantiate it.
- Changing the backend snapshot cadence. Default 5–10s is enough
  for a grid overview; higher-frequency polling can wait.

**Not-a-fix:** the 4.5.0 TUTK → WebRTC auto-fallback ([DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md](DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md))
is orthogonal to this issue. Streams already work end-to-end; the
bottleneck is display-side, so retesting on 4.5 will not resolve
#97 by itself. The chronic-error registry entry for HL_CAM4 was the
right hint for the firmware block; it doesn't apply here.

## Deferred (post-4.6)

- **MQTT Phase 2** ([#105](https://github.com/IDisposable/docker-wyze-bridge/issues/105),
  [#90](https://github.com/IDisposable/docker-wyze-bridge/issues/90),
  [#81](https://github.com/IDisposable/docker-wyze-bridge/issues/81)) —
  pan/tilt/cruise/live-property readback. All require direct TUTK
  write access go2rtc doesn't expose. Consider a small TUTK sidecar
  or waiting for upstream go2rtc to add write support.
- **ONVIF** ([#34](https://github.com/IDisposable/docker-wyze-bridge/issues/34)) —
  go2rtc has partial ONVIF; layering our discovery + credential
  passthrough on top is non-trivial. `help-wanted`.
- **Doorbell V1 (WYZEDB3)** ([#101](https://github.com/IDisposable/docker-wyze-bridge/issues/101)) —
  upstream at [AlexxIT/go2rtc#2097](https://github.com/AlexxIT/go2rtc/issues/2097),
  active investigation, frames captured. Waiting on go2rtc.
- **UNRAID Community Applications template** ([#60](https://github.com/IDisposable/docker-wyze-bridge/issues/60)) —
  small; publish `unraid/docker-wyze-bridge.xml` when there's cycles.
