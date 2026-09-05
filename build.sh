#!/usr/bin/env bash
# Build the universal macOS binary (arm64 + x86_64) into dist/plexctl.
#
# Version resolution, in order:
#   1. $PLEXCTL_BUILD_VERSION, if set — an explicit stamp always wins.
#   2. The exact tag on HEAD, if there is one (leading "v" stripped).
#   3. internal/api/api.go's own default.
#
# (3) is the one that matters: this script used to hardcode 1.0.3 against
# api.go's 2.0.0-dev, so every released binary reported a version the /plex
# skill's >= 2.0.0 gate refuses — a skill-level outage produced by the build,
# not by the code.
#
# Set PLEXCTL_RELEASE=1 to make a codesign failure fatal.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist

VERSION="${PLEXCTL_BUILD_VERSION:-}"
if [ -z "${VERSION}" ]; then
  # The one legitimate `|| true` here: a non-git export (or an untagged
  # commit) must not kill the build under `set -e`.
  VERSION="$(git describe --tags --exact-match 2>/dev/null || true)"
  VERSION="${VERSION#v}"
fi
if [ -z "${VERSION}" ]; then
  VERSION="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/api/api.go)"
fi
if [ -z "${VERSION}" ]; then
  # Fail loudly rather than stamping an empty version: a rename in api.go
  # would otherwise ship a binary that reports nothing.
  echo "build.sh: could not determine a version — no PLEXCTL_BUILD_VERSION, no exact tag, and no 'var Version = \"...\"' in internal/api/api.go" >&2
  exit 1
fi

LDFLAGS="-s -w -X github.com/corinthian/plexctl/internal/api.Version=${VERSION}"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/plexctl-arm64 ./cmd/plexctl
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/plexctl-amd64 ./cmd/plexctl
lipo -create -output dist/plexctl dist/plexctl-arm64 dist/plexctl-amd64

# An unsigned binary is usable locally but not shippable, and the old
# `2>/dev/null || true` hid both cases identically.
if ! codesign -s - -f dist/plexctl; then
  if [ "${PLEXCTL_RELEASE:-}" = "1" ]; then
    echo "build.sh: codesign failed and PLEXCTL_RELEASE=1 — refusing to ship an unsigned binary" >&2
    exit 1
  fi
  echo "build.sh: codesign failed — continuing with an unsigned dev binary" >&2
fi

# Prove the stamp landed. Running the artefact is fine here: it is a
# universal macOS binary on macOS, and --version touches nothing.
REPORTED="$(dist/plexctl --version)"
if [[ "${REPORTED}" != *"${VERSION}"* ]]; then
  echo "build.sh: binary reports '${REPORTED}', expected version '${VERSION}' — the ldflags stamp did not land" >&2
  exit 1
fi

echo "dist/plexctl ($(lipo -archs dist/plexctl)) — ${REPORTED}"
