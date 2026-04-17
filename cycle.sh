#!/usr/bin/env bash

# Local dev cycle: test, build, run with .env.dev credentials loaded.
# Assumes you're running outside the devcontainer (for in-container dev
# see DEVELOPER.md — the devcontainer sets STATE_DIR et al. for you).

set -euo pipefail

# Local paths — overridden by anything the user puts in .env.dev.
# The bridge's own defaults (/config, /media/...) are Docker-only.
ROOT="$(cd "$(dirname "$0")" && pwd)"
export STATE_DIR="${STATE_DIR:-$ROOT/local/config}"
export SNAPSHOT_PATH="${SNAPSHOT_PATH:-$ROOT/local/snapshots}"
export RECORD_PATH="${RECORD_PATH:-$ROOT/local/recordings/{cam_name}/%Y/%m/%d}"
mkdir -p "$STATE_DIR" "$ROOT/local/snapshots" "$ROOT/local/recordings"

# Fetch go2rtc binary to the repo root on first run. Version is read from
# docker/Dockerfile so container and local dev stay in lockstep — that
# ARG is the single source of truth. findGo2RTCBinary() in
# cmd/wyze-bridge/main.go checks ./go2rtc[.exe] before falling back to
# PATH, so this drops in without any env var.
GO2RTC_VERSION="$(sed -n 's/^ARG GO2RTC_VERSION=\(.*\)$/\1/p' "$ROOT/docker/Dockerfile" | head -1)"
if [ -z "$GO2RTC_VERSION" ]; then
    echo "cycle.sh: could not extract GO2RTC_VERSION from docker/Dockerfile" >&2
    exit 1
fi
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) GO2RTC_ASSET="go2rtc_win64.exe"; GO2RTC_BIN="$ROOT/go2rtc.exe" ;;
    Linux)
        case "$(uname -m)" in
            x86_64)         GO2RTC_ASSET="go2rtc_linux_amd64" ;;
            aarch64|arm64)  GO2RTC_ASSET="go2rtc_linux_arm64" ;;
            armv7l)         GO2RTC_ASSET="go2rtc_linux_arm" ;;
            *)              echo "cycle.sh: unsupported Linux arch $(uname -m)"; exit 1 ;;
        esac
        GO2RTC_BIN="$ROOT/go2rtc" ;;
    Darwin)
        case "$(uname -m)" in
            x86_64) GO2RTC_ASSET="go2rtc_mac_amd64" ;;
            arm64)  GO2RTC_ASSET="go2rtc_mac_arm64" ;;
        esac
        GO2RTC_BIN="$ROOT/go2rtc" ;;
    *) echo "cycle.sh: unsupported OS $(uname -s)"; exit 1 ;;
esac
if [ ! -f "$GO2RTC_BIN" ]; then
    echo "Downloading go2rtc v${GO2RTC_VERSION} (${GO2RTC_ASSET})..."
    curl -fsSL \
      "https://github.com/AlexxIT/go2rtc/releases/download/v${GO2RTC_VERSION}/${GO2RTC_ASSET}" \
      -o "$GO2RTC_BIN"
    chmod +x "$GO2RTC_BIN"
fi

# Fast path: just run tests. Set COVERAGE=1 to also emit coverage.html.
if [ -n "${COVERAGE:-}" ]; then
    go test ./... -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html
else
    go test ./...
fi
go build -o wyze-bridge ./cmd/wyze-bridge

# Load Wyze credentials (and any overrides). Use POSIX `.` so this works
# even when the script is invoked by dash. set -a auto-exports every
# assignment in the sourced file.
if [ -f .env.dev ]; then
    set -a
    . .env.dev
    set +a
fi

go run ./cmd/wyze-bridge
