# patches/

Reproducible build of the patched **go2rtc** sidecar needed to unlock
the Wyze Bulb Cam's full 2K feed.

## Why

The Wyze Bulb Cam (`HL_BC`) serves its lowest publish profile
(640x360) over the Wyze KVS WebRTC path until a viewer requests a
higher one via a data-channel `switch/resolution` message. Upstream
go2rtc never asks. [AlexxIT/go2rtc PR #2324] adds an optional
`#profile=` fragment to the `webrtc:...#format=wyze` source URL that
sends exactly that message. The bridge requests it by setting
`WEBRTC_PROFILE` (see `internal/camera/manager.go` `streamSourceFor`).

Without the fragment (or `WEBRTC_PROFILE` unset), go2rtc behaves
exactly like upstream v1.9.14.

## Reproduce

```bash
cd patches
./build-go2rtc.sh          # writes ./go2rtc, then:
cp go2rtc ../bin-linux/go2rtc
systemctl --user restart wyze-bridge
```

Or step by step:

```bash
git clone --depth 1 --branch v1.9.14 https://github.com/AlexxIT/go2rtc
cd go2rtc
git apply <this-dir>/go2rtc-pr2324-webrtc-profile.diff
# The two *_arch.c files are standalone helper sources (see
# pkg/alsa/README.md), not part of the Go build, but Go's build
# system rejects loose .c files when cgo is off — move them aside:
mv pkg/alsa/device/asound_arch.c pkg/v4l2/device/videodev2_arch.c /tmp/
go build -trimpath -ldflags="-s -w" -o go2rtc .
```

Requires a Go toolchain (this repo was built with mise's go 1.27.3).

## Provenance

- Base: `AlexxIT/go2rtc` tag `v1.9.14`, commit
  `b5948cfb25404cc5cb37b166ecaa2dca20b11d4b`
- Patch: `go2rtc-pr2324-webrtc-profile.diff` — verbatim PR diff
  (sha256 `ea4a5437d3b83dcc729d776db0e1dd035c4e2112f0187cc30af85fed95edee43`),
  touches only `internal/webrtc/client.go` and
  `internal/webrtc/kinesis.go`.
- The PR is open upstream (author jdrusso, tested by multiple
  HL_BC owners incl. 6-cam fleets); if it ever merges, bump to the
  release containing it and delete this dir.
- Patched binary that was verified live (2304x1296 h264 @15fps on
  HL_BC FW 21.1.7.1256) has sha256
  `e6f28e4b3f4fa5fb1716acd8ceaac812ab238b1edaba914f21bd4cb1df6404f7`.

## Verify after rebuild

```bash
curl -s http://localhost:1984/api/streams | grep profile=2k
timeout 45 ffprobe -v info -rtsp_transport tcp \
  -i rtsp://127.0.0.1:8554/wyze_bulb_cam \
  -show_streams -select_streams v 2>&1 | grep -E "width|height"
# expect 2304 / 1296
```

[AlexxIT/go2rtc PR #2324]: https://github.com/AlexxIT/go2rtc/pull/2324
