FROM mcr.microsoft.com/devcontainers/base:bookworm

ARG GO2RTC_VERSION=1.9.14

# ffmpeg is required by go2rtc's JPEG snapshot endpoint (/api/frame.jpeg).
# The bridge itself never calls ffmpeg — go2rtc uses it only to decode
# H.264 keyframes into JPEG when a snapshot is requested.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ffmpeg \
      gnupg2 \
      gpg-agent \
      dirmngr \
      pinentry-curses && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# go2rtc binary — fetch for the container's architecture
RUN ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "arm64" ]; then GO2RTC_ARCH="arm64"; \
    else GO2RTC_ARCH="amd64"; fi && \
    curl -fsSL \
      "https://github.com/AlexxIT/go2rtc/releases/download/v${GO2RTC_VERSION}/go2rtc_linux_${GO2RTC_ARCH}" \
      -o /usr/local/bin/go2rtc && \
    chmod +x /usr/local/bin/go2rtc

# Create local dev directories
RUN mkdir -p /workspaces/docker-wyze-bridge/local/config \
             /workspaces/docker-wyze-bridge/local/img
