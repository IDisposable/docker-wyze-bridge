# Cam Pan v4: integrated receiver

This implementation uses the existing Bridge container and existing go2rtc and
FFmpeg executables. The Bridge process handles Wyze authentication and Agora
WebRTC gateway signaling. go2rtc receives standard WebRTC/SRTP. The existing go2rtc 1.9.14 source is
built with a dedicated 4096-packet SRTP receive window for this path; replay
protection remains enabled and other transports keep their default windows. FFmpeg copies the HEVC video through a bounded RTP reorder queue
before publishing native RTSP and deriving the H264 preview.

There is no Agora SDK, Python receiver, MediaMTX service, second container or
additional Go module or OS package. The existing gorilla/websocket module is
reused for signaling; encryption and HTTP use the Go standard library.

The normal Dockerfile builds both executables in one image. Bridge's `go.mod`
and `go.sum` are unchanged, and go2rtc retains the same upstream dependency
versions. No companion image is needed. External go2rtc mode is not supported
for this receive path because it lacks the required window and templates.

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

Both outputs share one internal `<camera>_lake` transport and one camera
gateway session. The `_4k` output copies HEVC through FFmpeg to restore RTP
ordering; it does not decode, scale or re-encode that native video. The reorder
queue is bounded to 4096 packets with a one-second maximum demuxing delay.
The H264 preview requests a keyframe every two seconds, including when the
camera reduces its frame rate in low light. This keeps playback/recording starts
responsive without imposing a different frame rate on the camera.
The internal transport is also removed on stop, reconnect and rename.
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

A local receive test captured 1200 native HEVC frames: 445 initial 640x360
frames followed by 755 decoded 3840x2160 frames. It also captured 1000 H264
1920x1080 preview frames and completed two accelerated token renewals.

The first integrated NAS tests produced damaged frames and gray flashes. A
private RTP trace found late packets capped at 63 positions behind; the default
SRTP replay window is 64. Increasing the window to 4096 recovered every sequence
number in the next trace, including packets 277 positions behind. Replay
protection still rejects duplicates.

With that receive window and the FFmpeg reorder queue, a 45-second NAS test
recorded native 3840x2160 HEVC and 1920x1080 H264. Both passed strict decoding
without errors, and sampled preview frames showed no damaged bands. This was a
bounded diagnostic build. The ordinary NAS image subsequently passed one-minute native/preview capture checks before and after a container restart, with clean sampled frames. A longer capture then exposed repeated gateway reconnections and an early recording end. Continuous stability and final Home recording validation remain unresolved. The
previous working receiver was restored between tests. No completed 24-hour soak
is claimed.

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
