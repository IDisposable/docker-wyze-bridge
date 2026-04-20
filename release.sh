#!/usr/bin/env bash

set -euo pipefail

# Releases from dev to main, tagging the release with the version number.

# Check if a version argument was provided
if [ -z "${1:-}" ]; then
  echo "Usage: $0 <version_number> (e.g., 4.2.0)"
  exit 1
fi

VERSION=$1

# Ensures we are on 'dev' before merging into 'main'
CURRENT_BRANCH=$(git branch --show-current)
if [ "$CURRENT_BRANCH" != "dev" ]; then
  echo "Error: You must be on the 'dev' branch to start the release."
  exit 1
fi

# The --porcelain flag returns an empty string if the directory is clean
if [ -n "$(git status --porcelain)" ]; then
  echo "Error: You have uncommitted changes. Please commit or stash them before releasing."
  git status --short
  exit 1
fi

# Requires a 'y' or 'Y' to proceed
read -p "Ready to release v$VERSION from dev to main? (y/n): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Release cancelled."
    exit 1
fi

git checkout main && git pull
git merge --no-ff dev -m "Release v$VERSION"
git tag v$VERSION
git push origin main v$VERSION

echo "Successfully released v$VERSION!"