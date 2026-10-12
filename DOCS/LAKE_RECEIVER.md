# Cam Pan v4: integrated receiver

This implementation uses the existing Bridge container and existing go2rtc and
FFmpeg executables. The Bridge process handles Wyze authentication and Agora
WebRTC gateway signaling. go2rtc receives standard WebRTC/SRTP and publishes
RTSP. FFmpeg produces the H264 preview from that same native source.

There is no Agora SDK, Python receiver, MediaMTX service, second container or
additional Go module or OS package. The existing gorilla/websocket module is
reused for signaling; encryption and HTTP use the Go standard library.

## Camera outputs

A discovered `HL_PAN4` automatically gets two outputs:

| Output | Purpose |
| --- | --- |
| `rtsp://HOST:8554/<camera>` | H264 preview, 1920x1080 for HD; 640x360 for SD |
| `rtsp://HOST:8554/<camera>_4k` | Native HEVC camera video, without scaling/re-encoding |

The receiver requests the camera property `camera::resolution=2k`. On the tested
Pan v4 this property delivers 3840x2160. The camera can initially send 360p while
joining or changing quality. Verify actual decoded dimensions rather than
assuming that a quality value or stream name proves resolution.

HomeKit/Scrypted can retain the H264 preview URL; an HEVC-capable recorder can
use the native output. This does not add native 4K recording to Apple Home or
import recordings from the camera's SD card. Audio and PTZ are not implemented
by this initial receive path. Other camera models retain their existing routing.

## Session lifecycle and recovery

Both outputs share one native go2rtc producer and one camera gateway session.
The session starts when a consumer requests video. Its short local websocket
exchanges SDP with go2rtc; the Agora websocket remains alive after go2rtc closes
that initial signaling connection. Stopping the camera closes both outputs and
the camera's gateway session. Shutdown cancels all owned sessions.

The receiver periodically sends camera ownership and quality controls. Tokens
renew in the existing channel. A failed renewal, lost gateway or stalled video
must recover through camera reconnection. The camera health check observes
received bytes, and distinguishes idle sources from missing registrations or
an active source that is no longer receiving data. The existing STREAMING badge
still indicates registration; validate arriving frames for actual playback.

A second camera session or the Wyze app can compete over quality. Test with a
spare camera or stop its existing receiver before opening another session. The
receiver still connects through Wyze/Agora cloud services.

Only loopback go2rtc requests can reach internal signaling. Browser-origin
requests are rejected. Account credentials and encryption keys do not appear
in stream URLs, SDP answers or errors. HTTPS/WSS certificate verification stays
enabled. Existing stream-access authentication settings still apply.

## Upgrading from the earlier companion prototype

Back up container inspection data, images and complete mounted configuration
before switching images. Preserve account credentials, ports and mounts. Remove
only the obsolete `RTSP_SOURCE_<CAM>` and native-output extra-stream entries
that point at the old companion's port 8555. Stop the old receiver before
starting the integrated receiver, so they never compete for the same camera.

Use the ordinary Bridge Dockerfile and deployment. No separate Lake service or
additional ports are required. Preserve a stopped copy of the previous Bridge
and receiver for rollback; do not delete their parameters or mounted data.

## Validation status

The Go receive path recorded 1200 native 3840x2160 HEVC frames and 1000 H264
1920x1080 preview frames using the existing go2rtc/FFmpeg binaries. Live token
renewal also succeeded under a deliberately shortened test timer. The complete
container deployment and recovery are undergoing separate validation. Camera
quality changes can reset timing during initial startup; a settled recording is
checked separately. This document does not claim a completed 24-hour soak.

Offline checks:

```sh
go test -race ./...
go vet ./...
docker build -f docker/Dockerfile -t wyze-bridge:pan-v4-streaming .
```

Protocol research credits: Jezza34000/homeassistant_petkit (MIT),
alphabin/ClaudeWyze4Sln, and mitchross's earlier Pan v4 receiver design. Reusing
the existing go2rtc media stack is the difference from the native-SDK companion
prototype. See THIRD_PARTY_NOTICES.md for preserved attribution.
