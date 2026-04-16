ARG BUILD_FROM
ARG WYZE_BRIDGE_IMAGE=ghcr.io/idisposable/docker-wyze-bridge:edge

# Pull pre-built wyze-bridge + go2rtc binaries from CI image (multi-arch manifest)
FROM ${WYZE_BRIDGE_IMAGE} AS bridge-bin

FROM ${BUILD_FROM}

# ffmpeg is only needed by go2rtc's JPEG snapshot endpoint.
# ca-certificates + tzdata for Wyze API TLS and timestamp handling.
RUN apk add --no-cache ca-certificates tzdata ffmpeg && \
    mkdir -p /config /img /record

COPY --from=bridge-bin /usr/local/bin/wyze-bridge /usr/local/bin/wyze-bridge
COPY --from=bridge-bin /usr/local/bin/go2rtc      /usr/local/bin/go2rtc

COPY run.sh /
RUN chmod +x /run.sh

CMD [ "/run.sh" ]
