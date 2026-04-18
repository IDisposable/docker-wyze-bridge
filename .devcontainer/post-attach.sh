#!/bin/bash
set -e

if [[ -n "$SSH_AUTH_SOCK" && -S "$SSH_AUTH_SOCK" ]]; then
    echo "SSH agent keys:"
    ssh-add -l || true
else
    echo "SSH agent not forwarded (SSH_AUTH_SOCK missing or not a socket)."
fi

if command -v gpg >/dev/null 2>&1; then
    gpgconf --launch gpg-agent >/dev/null 2>&1 || true
    echo "GPG keyring:"
    gpg --list-secret-keys --keyid-format=long 2>/dev/null || echo "No GPG secret keys visible in container."
fi
