# ── Stage 1: Fetch the existing go2rtc source + video-rtc.js ────────────────
FROM alpine:3.21 AS go2rtc-fetch
ARG GO2RTC_VERSION=1.9.14
RUN apk add --no-cache curl ca-certificates && \
    curl -fsSL \
      "https://github.com/AlexxIT/go2rtc/archive/refs/tags/v${GO2RTC_VERSION}.tar.gz" \
      -o /go2rtc-source.tar.gz && \
    curl -fsSL \
      "https://github.com/AlexxIT/go2rtc/raw/v${GO2RTC_VERSION}/www/video-rtc.js" \
      -o /video-rtc.js

# ── Stage 2: Build Go binary ──────────────────────────────────────────────────
FROM golang:1.26.4-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY docker/go2rtc-receive-window.patch ./docker/go2rtc-receive-window.patch
# Build the same go2rtc version and dependencies with a bounded SRTP receive
# window and explicit owned-stream teardown for Pan v4 sessions. Default replay
# protection discards packets more than 63 positions behind, before reordering.
COPY --from=go2rtc-fetch /go2rtc-source.tar.gz /go2rtc-source.tar.gz
RUN mkdir /go2rtc-src && \
    tar -xzf /go2rtc-source.tar.gz -C /go2rtc-src --strip-components=1 && \
    cd /go2rtc-src && \
    git apply /src/docker/go2rtc-receive-window.patch && \
    CGO_ENABLED=0 go test pkg/webrtc/api.go pkg/webrtc/receive_window_test.go -run TestReceiveDelayedAuthenticatedPacket -count=1 && \
    CGO_ENABLED=0 go test \
      internal/streams/add_consumer.go internal/streams/api.go internal/streams/dot.go \
      internal/streams/handlers.go internal/streams/helpers.go internal/streams/play.go \
      internal/streams/preload.go internal/streams/producer.go internal/streams/publish.go \
      internal/streams/stream.go internal/streams/streams.go internal/streams/delete_stop_test.go \
      -run '^TestDelete(Stop|Default)' -count=1 && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -trimpath -o /go2rtc .
COPY . .
# Replace placeholder video-rtc.js with real one before embedding
COPY --from=go2rtc-fetch /video-rtc.js ./internal/webui/static/video-rtc.js
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /wyze-bridge ./cmd/wyze-bridge

# gwell-proxy — sidecar for OG Gwell cameras (GW_GC1/GC2) that speak
# Wyze's Gwell/IoTVideo P2P protocol on the LAN. Doorbell-lineage GW_*
# models (GW_BE1 Doorbell Pro, GW_DBD Doorbell Duo) use WebRTC instead,
# handled by go2rtc's native `#format=wyze` source type — no sidecar
# there, just a shim handler in internal/webui/shim_kvs.go that feeds
# go2rtc the Wyze KVS signaling URL + ICE servers.
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w" \
    -o /gwell-proxy ./cmd/gwell-proxy

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.21
# ffmpeg is used by three things in this image:
#   1. go2rtc's /api/frame.jpeg snapshot endpoint (JPEG frame extraction)
#   2. the optional gwell-proxy sidecar, which spawns an ffmpeg subprocess
#      per GW_* camera to RTSP-PUSH H.264 into go2rtc's RTSP server.
#   3. the Pan v4 native RTP ordering stage and H264 preview.
# Keep ffmpeg installed for Pan v4 even when snapshots and Gwell are unused.
RUN apk add --no-cache ca-certificates tzdata ffmpeg && \
    mkdir -p /config /media/snapshots /media/recordings

COPY --from=builder      /wyze-bridge   /usr/local/bin/wyze-bridge
COPY --from=builder      /gwell-proxy   /usr/local/bin/gwell-proxy
COPY --from=builder      /go2rtc        /usr/local/bin/go2rtc
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/wyze-bridge/

EXPOSE 5080
EXPOSE 1984
EXPOSE 8554
EXPOSE 8888
EXPOSE 8889
EXPOSE 8189/udp

VOLUME ["/config", "/media"]

ENTRYPOINT ["/usr/local/bin/wyze-bridge"]
