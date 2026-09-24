ARG BUILD_FROM=ghcr.io/home-assistant/base:3.21
ARG WYZE_BRIDGE_IMAGE=ghcr.io/idisposable/docker-wyze-bridge:latest

# Pull pre-built wyze-bridge, go2rtc, and gwell-proxy from the CI image
# (multi-arch manifest). The add-on has no GWELL_BINARY option, so the
# sidecar must be on the image. Without it, Window Cam and any model
# flipped back to Gwell P2P log "gwell-proxy binary not found".
FROM ${WYZE_BRIDGE_IMAGE} AS bridge-bin

FROM ${BUILD_FROM}

# ffmpeg serves go2rtc JPEG snapshots and the gwell-proxy RTSP publish.
# ca-certificates + tzdata for Wyze API TLS and timestamp handling.
RUN apk add --no-cache ca-certificates tzdata ffmpeg && \
    mkdir -p /config /media/wyze_bridge/snapshots /media/wyze_bridge/recordings

COPY --from=bridge-bin /usr/local/bin/wyze-bridge /usr/local/bin/wyze-bridge
COPY --from=bridge-bin /usr/local/bin/go2rtc      /usr/local/bin/go2rtc
COPY --from=bridge-bin /usr/local/bin/gwell-proxy /usr/local/bin/gwell-proxy

COPY run.sh /
RUN chmod +x /run.sh

CMD [ "/run.sh" ]
