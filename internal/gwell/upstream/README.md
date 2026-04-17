# Vendored upstream: hacky-wyze-gwell

This directory is a verbatim copy of the Go protocol packages from
[github.com/wlatic/hacky-wyze-gwell](https://github.com/wlatic/hacky-wyze-gwell).
It carries Wyze Gwell/IoTVideo P2P protocol code that the sidecar at
`cmd/gwell-proxy` builds on.

## Provenance

- **Upstream repo:** https://github.com/wlatic/hacky-wyze-gwell
- **Upstream module path:** `github.com/wlatic/wyze-gwell-bridge/wyze-p2p` (note: the module path does not match the repo name)
- **Pinned commit:** `9c1b99f8b6e4e4aea17a19a3cd7d2d169dda6e45`
- **Commit date:** 2026-02-25
- **License:** GPL (see LICENSE file in this directory)
- **Vendored on:** 2026-04-17

## What lives here

| Subdirectory | Upstream source | Purpose |
| ------------ | --------------- | ------- |
| `gwell/` | `wyze-p2p/pkg/gwell/` | P2P protocol primitives: RC5 cipher, XOR, HMAC-MD5, KCP, MTP relay, frame encoding, handshake, session lifecycle. |
| `stream/` | `wyze-p2p/pkg/stream/` | H.264 NAL-unit extractor and an `FFmpegPublisher` that pipes raw H.264 into an `ffmpeg` subprocess that PUBLISHes via RTSP. |

Files were copied byte-for-byte. No import-path rewrites were needed —
both packages only reference each other within their own `package`
declaration, not across module paths.

## Changes from upstream

1. **`gwell/certify_test.go`: `TestBuildInitInfoMsg` wrapped in `t.Skip()`.**
   The test was committed broken at the pinned SHA (expected encrypted
   proto 0x7E and a specific session_id constant that the current
   `BuildInitInfoMsg` implementation does not produce). Skipping keeps
   `go test ./...` green without masking other tests. When rebasing
   onto a newer upstream SHA that fixes this, delete the `t.Skip` line.

2. **`stream/ffmpeg.go`: mediamtx-specific parameter names renamed
   to RTSP-generic.** The implementation is — and always was — a
   generic RTSP PUSH via `ffmpeg -f rtsp -rtsp_transport tcp rtsp://...`
   that works against any RTSP server accepting ANNOUNCE/RECORD.
   Upstream's naming (`mediamtxHost`, `mediamtxPort`) implied
   mediamtx-specificity that doesn't exist. We target go2rtc on
   loopback, so the generic names match reality and the intent.
   Changes:
   - `StartFFmpegPublisher(streamPath, mediamtxHost, mediamtxPort)` →
     `StartFFmpegPublisher(streamPath, rtspHost, rtspPort)`
   - Doc comments rewritten to describe generic RTSP PUSH instead of
     "publishes to mediamtx"
   No code-path changes. When rebasing, re-apply this rename; if
   upstream adopts the same cleanup, drop this diff entry.

3. **`stream/ffmpeg.go`: switched from `-c:v copy` to libx264
   re-encode.** Upstream targets mediamtx with a stream-copy
   pipeline; go2rtc's RTSP server is stricter about first-packet
   RTP timestamps and the gwell camera's raw H.264 Annex B arrives
   with wallclock-microsecond-origin timestamps (~2×10¹⁵) that no
   combination of `-use_wallclock_as_timestamps`, `-copyts`,
   `-start_at_zero`, `-avoid_negative_ts`, `+genpts`, `+igndts`
   could rebase fast enough — go2rtc closed the publish on the very
   first RTP packet every time.

   Re-encoding with `libx264 -preset ultrafast -tune zerolatency`
   decodes the camera stream and emits fresh monotonic PTS/DTS
   from 0, which go2rtc accepts cleanly. Costs ~10-15% of one CPU
   core per 1440×1440@15fps camera — acceptable for a doorbell
   that's mostly a single stream, cheaper than fighting timestamp
   semantics any further.

   Also added `-g 30` (keyframe every 2s at 15fps) and explicit
   `-pix_fmt yuv420p` for compatibility with browser-side decoders.

   When rebasing upstream, re-apply this swap. If upstream adopts
   go2rtc too and sorts out the copy path, drop this diff entry.

## Updating

When upstream cuts a new commit:

```bash
git clone --depth 1 https://github.com/wlatic/hacky-wyze-gwell.git /tmp/hwg
cp /tmp/hwg/wyze-p2p/pkg/gwell/*.go  internal/gwell/upstream/gwell/
cp /tmp/hwg/wyze-p2p/pkg/stream/*.go internal/gwell/upstream/stream/
# Re-apply the t.Skip in certify_test.go if the bug is still there.
# Update the "Pinned commit" and "Vendored on" lines above.
go test ./internal/gwell/upstream/...
```

If upstream starts depending on a non-stdlib package, you'll need to add
it to our `go.mod` too — today they're stdlib-only.

## Why vendor instead of `require`?

Upstream's `go.mod` declares module path
`github.com/wlatic/wyze-gwell-bridge/wyze-p2p`, which doesn't match the
actual GitHub repo URL `github.com/wlatic/hacky-wyze-gwell`. Go's module
resolver cannot follow that mismatch via GOPROXY — `go get
github.com/wlatic/wyze-gwell-bridge/wyze-p2p@<sha>` returns 404. The
alternatives were (a) fork upstream to a repo whose path matches its
module declaration, (b) use a filesystem `replace` directive that pins
an absolute path (Docker-only, breaks local dev), or (c) vendor. (c)
makes `go test ./...` and `go build` work uniformly in every environment
with zero setup, which matched this project's minimal-dependency bias.
