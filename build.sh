#!/usr/bin/env bash
# Build the universal macOS binary (arm64 + x86_64) into dist/plexctl.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
VERSION="${PLEXCTL_BUILD_VERSION:-1.0.3}"
LDFLAGS="-s -w -X github.com/corinthian/plexctl/internal/api.Version=${VERSION}"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/plexctl-arm64 ./cmd/plexctl
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/plexctl-amd64 ./cmd/plexctl
lipo -create -output dist/plexctl dist/plexctl-arm64 dist/plexctl-amd64
codesign -s - -f dist/plexctl 2>/dev/null || true
echo "dist/plexctl ($(lipo -archs dist/plexctl))"
