#!/usr/bin/env bash
# Build the patched go2rtc sidecar (see README.md in this dir).
# Output: ./go2rtc in this directory.
set -euo pipefail
cd "$(dirname "$0")"

BASE_TAG=v1.9.14
BASE_COMMIT=b5948cfb25404cc5cb37b166ecaa2dca20b11d4b
DIFF=go2rtc-pr2324-webrtc-profile.diff

command -v go >/dev/null || { echo "go toolchain required (mise use -g go@1.27.3)"; exit 1; }

rm -rf go2rtc-src
git clone --depth 1 --branch "$BASE_TAG" https://github.com/AlexxIT/go2rtc go2rtc-src
cd go2rtc-src

actual=$(git rev-parse HEAD)
if [ "$actual" != "$BASE_COMMIT" ]; then
  echo "warning: $BASE_TAG resolved to $actual, expected $BASE_COMMIT" >&2
fi

git apply "../$DIFF"

# Loose *_arch.c helper sources (see pkg/alsa/README.md) break the
# Go build when cgo is disabled; they are compiled by hand, not here.
mv pkg/alsa/device/asound_arch.c /tmp/asound_arch.c
mv pkg/v4l2/device/videodev2_arch.c /tmp/videodev2_arch.c

go build -trimpath -ldflags="-s -w" -o ../go2rtc .
cd ..
rm -rf go2rtc-src
echo "built ./go2rtc ($(/usr/bin/file -b go2rtc 2>/dev/null || file -b go2rtc))"
echo "install (docker): run the bridge with a bind-mount override, e.g."
echo "  volumes: - ./patches/go2rtc:/usr/local/bin/go2rtc:ro"
echo "then restart the bridge. (Native: replace the go2rtc binary on PATH.)"
