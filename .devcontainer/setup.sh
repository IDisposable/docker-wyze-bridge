#!/bin/bash
set -e

echo "=== wyze-bridge dev container setup ==="

# Install Go tools
go install golang.org/x/tools/gopls@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Download Go module dependencies
cd /workspaces/docker-wyze-bridge
go mod download

# Verify go2rtc is available
if command -v go2rtc &>/dev/null; then
    echo "go2rtc: $(go2rtc --version 2>&1 || echo 'installed')"
else
    echo "WARNING: go2rtc not found"
fi

# Create local dirs if not present
mkdir -p local/config local/img

# Fetch the real video-rtc.js from go2rtc (overwrites the placeholder stub).
# The Docker build does this at build time; for dev we do it here.
GO2RTC_VERSION=1.9.14
echo "Fetching video-rtc.js from go2rtc v${GO2RTC_VERSION}..."
curl -fsSL "https://github.com/AlexxIT/go2rtc/raw/v${GO2RTC_VERSION}/www/video-rtc.js" \
    -o internal/webui/static/video-rtc.js \
    && echo "video-rtc.js installed" \
    || echo "WARNING: could not fetch video-rtc.js (WebRTC player will not work)"

# Remind about .env.dev
if [ ! -f .env.dev ]; then
    echo ""
    echo "=== NEXT STEP ==="
    echo "Copy .env.dev.example to .env.dev and add your Wyze credentials:"
    echo "  cp .env.dev.example .env.dev"
    echo ""
    echo "Then run the bridge:"
    echo "  set -a; source .env.dev; set +a"
    echo "  go run ./cmd/wyze-bridge"
    echo ""
fi

ssh-add -l || echo "WARNING: no SSH keys added to ssh-agent (git operations may fail)"

echo "=== setup complete ==="
