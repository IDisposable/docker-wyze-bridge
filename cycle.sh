#!/usr/bin/env bash

# Local dev cycle: test, build, run with .env.dev credentials loaded.
# Assumes you're running outside the devcontainer (for in-container dev
# see DEVELOPER.md — the devcontainer sets STATE_DIR et al. for you).

set -euo pipefail

# Local paths — overridden by anything the user puts in .env.dev.
# The bridge's own defaults (/config, /media/...) are Docker-only.
#
# Defaults are assigned to intermediate vars (not inlined into
# ${VAR:-...}) because bash parameter expansion doesn't track
# nested braces: the literal {cam_name} close-brace inside the
# default would terminate the ${...} early, producing a mangled
# path like "{cam_name/%Y/%m/%d}". Separating the default assignment
# keeps the {cam_name} token intact.
ROOT="$(cd "$(dirname "$0")" && pwd)"
DEFAULT_SNAPSHOT_PATH="$ROOT/local/snapshots/{cam_name}/%Y-%m-%d"
DEFAULT_RECORD_PATH="$ROOT/local/recordings/{cam_name}/%Y/%m/%d"
export STATE_DIR="${STATE_DIR:-$ROOT/local/config}"
export SNAPSHOT_PATH="${SNAPSHOT_PATH:-$DEFAULT_SNAPSHOT_PATH}"
export RECORD_PATH="${RECORD_PATH:-$DEFAULT_RECORD_PATH}"
mkdir -p "$STATE_DIR" "$ROOT/local/snapshots" "$ROOT/local/recordings"

# Build go2rtc to the repo root on first run from the fork. Repo/branch
# default to the docker/Dockerfile ARGs (stable = master); override with
# GO2RTC_REF=edge ./cycle.sh to test the edge branch. findGo2RTCBinary() in
# cmd/wyze-bridge/main.go prefers ./go2rtc[.exe]. Delete the binary to refresh.
dockerfile_arg() { sed -n "s/^ARG $1=\(.*\)\$/\1/p" "$ROOT/docker/Dockerfile" | head -1; }
GO2RTC_REPO="$(dockerfile_arg GO2RTC_REPO)"
GO2RTC_REF="${GO2RTC_REF:-$(dockerfile_arg GO2RTC_REF)}"
if [ -z "$GO2RTC_REPO" ] || [ -z "$GO2RTC_REF" ]; then
    echo "cycle.sh: could not extract GO2RTC_REPO/GO2RTC_REF from docker/Dockerfile" >&2
    exit 1
fi
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) GO2RTC_BIN="$ROOT/go2rtc.exe" ;;
    *)                    GO2RTC_BIN="$ROOT/go2rtc" ;;
esac
if [ ! -f "$GO2RTC_BIN" ]; then
    echo "Building go2rtc (${GO2RTC_REF}) from ${GO2RTC_REPO}..."
    GO2RTC_SRC="$(mktemp -d)"
    git clone --depth 1 -b "$GO2RTC_REF" "$GO2RTC_REPO" "$GO2RTC_SRC"
    ( cd "$GO2RTC_SRC" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$GO2RTC_BIN" )
    rm -rf "$GO2RTC_SRC"
fi

# Fast path: just run tests. Set COVERAGE=1 to also emit coverage.html.
if [ -n "${COVERAGE:-}" ]; then
    go test ./... -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html
else
    go test ./...
fi
go build -o wyze-bridge ./cmd/wyze-bridge

# Build gwell-proxy to the repo root. cmd/wyze-bridge/gwell_spawn.go's
# binary resolver checks ./gwell-proxy[.exe] before giving up, so
# putting it here means `go run ./cmd/wyze-bridge` with GWELL_ENABLED=true
# finds and spawns the sidecar exactly like the container does.
# Cheap build — shares the same Go module cache as wyze-bridge above.
GWELL_PROXY_BIN="$ROOT/gwell-proxy"
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) GWELL_PROXY_BIN="$ROOT/gwell-proxy.exe" ;;
esac
go build -o "$GWELL_PROXY_BIN" ./cmd/gwell-proxy

# Load Wyze credentials (and any overrides). Use POSIX `.` so this works
# even when the script is invoked by dash. set -a auto-exports every
# assignment in the sourced file.
if [ -f .env.dev ]; then
    set -a
    . .env.dev
    set +a
fi

rm -f local/config/gwell/token_cache.json
# Sweep zero-byte dump files (runs that never got any bytes) but keep
# non-empty ones so successful sessions can be compared.
find local/gwell-dumps -maxdepth 1 -type f -size 0 -delete 2>/dev/null || true
go run ./cmd/wyze-bridge
