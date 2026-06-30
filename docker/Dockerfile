# ── Stage 1: Build go2rtc from the fork + provide video-rtc.js ──────────────
# Both channels build from the IDisposable fork; only the branch differs.
# Stable (default): master, kept in sync with upstream. Edge: GO2RTC_REF=edge.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS go2rtc-fetch
ARG GO2RTC_REPO=https://github.com/IDisposable/go2rtc
ARG GO2RTC_REF=master
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN apk add --no-cache git ca-certificates
RUN git clone --depth 1 -b "${GO2RTC_REF}" "${GO2RTC_REPO}" /src && cd /src && \
    case "${TARGETVARIANT}" in v7) GOARM=7 ;; *) GOARM= ;; esac && \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH}" GOARM="${GOARM}" \
      go build -trimpath -ldflags="-s -w" -o /go2rtc && \
    cp /src/www/video-rtc.js /video-rtc.js

# ── Stage 2: Build Go binary ──────────────────────────────────────────────────
# Cross-compile from the native build platform (no QEMU) to the target arch.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace placeholder video-rtc.js with real one before embedding
COPY --from=go2rtc-fetch /video-rtc.js ./internal/webui/static/video-rtc.js
ARG VERSION=dev
RUN case "$TARGETVARIANT" in v7) GOARM=7 ;; *) GOARM= ;; esac && \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="$TARGETARCH" GOARM="$GOARM" \
    go build -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /wyze-bridge ./cmd/wyze-bridge

# gwell-proxy — sidecar for OG Gwell cameras (GW_GC1/GC2) that speak
# Wyze's Gwell/IoTVideo P2P protocol on the LAN. Doorbell-lineage GW_*
# models (GW_BE1 Doorbell Pro, GW_DBD Doorbell Duo) use WebRTC instead,
# handled by go2rtc's native `#format=wyze` source type — no sidecar
# there, just a shim handler in internal/webui/shim_kvs.go that feeds
# go2rtc the Wyze KVS signaling URL + ICE servers.
RUN case "$TARGETVARIANT" in v7) GOARM=7 ;; *) GOARM= ;; esac && \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="$TARGETARCH" GOARM="$GOARM" \
    go build -ldflags="-s -w" \
    -o /gwell-proxy ./cmd/gwell-proxy

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.21
# ffmpeg is used by two things in this image:
#   1. go2rtc's /api/frame.jpeg snapshot endpoint (JPEG frame extraction)
#   2. the optional gwell-proxy sidecar, which spawns an ffmpeg subprocess
#      per GW_* camera to RTSP-PUSH H.264 into go2rtc's RTSP server.
# Alpine's ffmpeg package adds ~40 MB, bringing the total image to ~60–65 MB
# (still well under the 200+ MB Python bridge). If you have no GW_* cameras
# AND don't need JPEG snapshots, you could omit ffmpeg and everything else
# (RTSP/HLS/WebRTC playback) keeps working.
RUN apk add --no-cache ca-certificates tzdata ffmpeg && \
    mkdir -p /config /media/snapshots /media/recordings

COPY --from=builder      /wyze-bridge   /usr/local/bin/wyze-bridge
COPY --from=builder      /gwell-proxy   /usr/local/bin/gwell-proxy
COPY --from=go2rtc-fetch /go2rtc        /usr/local/bin/go2rtc

EXPOSE 5080
EXPOSE 1984
EXPOSE 8554
EXPOSE 8888
EXPOSE 8889
EXPOSE 8189/udp

VOLUME ["/config", "/media"]

ENTRYPOINT ["/usr/local/bin/wyze-bridge"]
