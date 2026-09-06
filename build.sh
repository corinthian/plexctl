#!/usr/bin/env bash
# Build the universal macOS binary (arm64 + x86_64) into dist/plexctl.
#
# Version resolution, highest first:
#   1. The positional argument.
#   2. $PLEXCTL_BUILD_VERSION, if set — an explicit stamp always wins.
#   3. The exact tag on HEAD, if there is one (leading "v" stripped).
#   4. internal/api/api.go's own default.
#
# (4) is the one that matters: this script used to hardcode 1.0.3 against
# api.go's 2.0.0-dev, so every released binary reported a version the /plex
# skill's >= 2.0.0 gate refuses — a skill-level outage produced by the build,
# not by the code.
#
# Runs the same gates CI runs before building. Set PLEXCTL_RELEASE=1 to make
# a codesign failure fatal.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist

VERSION="${1:-}"
if [ -z "${VERSION}" ]; then
  VERSION="${PLEXCTL_BUILD_VERSION:-}"
fi
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
  echo "build.sh: could not determine a version — no positional argument, no PLEXCTL_BUILD_VERSION, no exact tag, and no 'var Version = \"...\"' in internal/api/api.go" >&2
  exit 1
fi

GOVULNCHECK="$(command -v govulncheck || true)"
if [ -z "$GOVULNCHECK" ] && [ -x "$(go env GOPATH)/bin/govulncheck" ]; then
  GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"
fi
if [ -z "$GOVULNCHECK" ]; then
  echo "[build] govulncheck not found; installing..."
  go install golang.org/x/vuln/cmd/govulncheck@latest
  GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"
fi
echo "[build] govulncheck ./..."
"$GOVULNCHECK" ./...

echo "[build] go vet ./..."
go vet ./...

echo "[build] gofmt -l internal cmd"
GOFMT_OUT="$(gofmt -l internal cmd)"
if [ -n "$GOFMT_OUT" ]; then
  echo "build.sh: gofmt found unformatted files:" >&2
  echo "$GOFMT_OUT" >&2
  exit 1
fi

echo "[build] go test ./..."
go test ./...

echo "[build] go test -race ./..."
go test -race ./...

echo "[build] go mod tidy -diff"
if ! go mod tidy -diff; then
  echo "build.sh: go mod tidy -diff reports a change; run go mod tidy and commit it" >&2
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
# universal macOS binary on macOS, and --version touches nothing. Exact
# string equality against the whole --version line, not containment: a
# prefix match would let "plexctl version 2.0.0-dev" pass for a build that
# asked for "2.0.0".
REPORTED="$(dist/plexctl --version)"
EXPECTED="plexctl version ${VERSION}"
if [ "${REPORTED}" != "${EXPECTED}" ]; then
  echo "build.sh: binary reports '${REPORTED}', expected '${EXPECTED}'" >&2
  exit 1
fi

echo "dist/plexctl ($(lipo -archs dist/plexctl)) — ${REPORTED}"
