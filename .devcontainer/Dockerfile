FROM mcr.microsoft.com/devcontainers/base:bookworm

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

# go2rtc is built from the fork by .devcontainer/setup.sh (postCreate), where
# the Go toolchain feature is available.

# Create local dev directories
RUN mkdir -p /workspaces/docker-wyze-bridge/local/config \
             /workspaces/docker-wyze-bridge/local/img
