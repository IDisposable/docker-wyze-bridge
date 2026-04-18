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

# Ensure signing config survives by using mounted host ~/.gnupg and ~/.gitconfig.
# Keep gpg-agent available in this shell and future interactive shells.
if command -v gpgconf &>/dev/null; then
    mkdir -p /home/vscode/.gnupg
    chmod 700 /home/vscode/.gnupg || true
    gpgconf --launch gpg-agent || true
fi

if ! grep -q 'export GPG_TTY=$(tty)' /home/vscode/.bashrc; then
    echo 'export GPG_TTY=$(tty)' >> /home/vscode/.bashrc
fi

if ! grep -q 'gpgconf --launch gpg-agent' /home/vscode/.bashrc; then
    echo 'gpgconf --launch gpg-agent >/dev/null 2>&1 || true' >> /home/vscode/.bashrc
fi

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

if command -v gpg &>/dev/null; then
    echo "GPG secret keys (if any):"
    gpg --list-secret-keys --keyid-format=long 2>/dev/null || true
fi

# Fallback for environments where GPG keys are unavailable in the container:
# enable SSH-based commit signing in this repo only.
if command -v git &>/dev/null && command -v ssh-add &>/dev/null; then
    has_gpg_secret_key=false
    if command -v gpg &>/dev/null && gpg --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec'; then
        has_gpg_secret_key=true
    fi

    if [[ "$has_gpg_secret_key" == false ]]; then
        existing_signing_key="$(git config --local --get user.signingkey || true)"
        if [[ -z "$existing_signing_key" ]]; then
            ssh_pub_key=""
            for candidate in /home/vscode/.ssh/id_ed25519.pub /home/vscode/.ssh/id_ecdsa.pub /home/vscode/.ssh/id_rsa.pub; do
                if [[ -f "$candidate" ]]; then
                    ssh_pub_key="$candidate"
                    break
                fi
            done

            if [[ -n "$ssh_pub_key" ]]; then
                git config --local gpg.format ssh
                git config --local user.signingkey "$ssh_pub_key"
                git config --local commit.gpgsign true
                echo "Configured local SSH signing fallback with key: $ssh_pub_key"
            else
                echo "No GPG secret key found and no SSH public key file found for fallback signing."
            fi
        else
            echo "Existing local user.signingkey detected; leaving signing config unchanged."
        fi
    fi
fi

echo "=== setup complete ==="
