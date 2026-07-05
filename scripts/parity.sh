#!/usr/bin/env bash
# Parity harness: run the Python plexctl (pipx) and the Go plexctl side by
# side on the same read-only commands against the live PMS, and diff
# jq-normalized output. Exit 0 only when every command matches on both
# normalized stdout and exit code.
#
# Usage: scripts/parity.sh
# Env:   PY_BIN (default ~/.local/bin/plexctl), GO_BIN (default dist/plexctl)
#        GO_CONFIG_DIR — optional PLEXCTL_CONFIG_DIR override for the Go
#        binary only (used to route it through a loopback forwarder when
#        macOS Local Network TCC denies the unsigned dev binary direct LAN
#        access; see the port plan's cutover notes).
set -u
PY_BIN="${PY_BIN:-$HOME/.local/bin/plexctl}"
GO_BIN="${GO_BIN:-dist/plexctl}"
GO_CONFIG_DIR="${GO_CONFIG_DIR:-}"

# Volatile fields that legitimately differ between two immediate runs.
NORM='del(.elapsedMs?, .fetchedAt?, .viewOffset?) | if .nowPlaying? then .nowPlaying |= del(.viewOffset?) else . end'

if [ ! -x "$GO_BIN" ]; then
  echo "GO_BIN not found: $GO_BIN (build with: go build -o dist/plexctl ./cmd/plexctl)" >&2
  exit 2
fi

SHOW=$("$PY_BIN" library list --section 3 | jq -r '.items[0].title')
MOVIEKEY=$("$PY_BIN" library list --section 1 | jq -r '.items[0].ratingKey')
echo "probe: SHOW='$SHOW' MOVIEKEY=$MOVIEKEY"

run_case() {
  local name=$1; shift
  local py_out go_out py_exit go_exit py_n go_n
  py_out=$("$PY_BIN" "$@" 2>/dev/null); py_exit=$?
  if [ -n "$GO_CONFIG_DIR" ]; then
    go_out=$(PLEXCTL_CONFIG_DIR="$GO_CONFIG_DIR" "$GO_BIN" "$@" 2>/dev/null); go_exit=$?
  else
    go_out=$("$GO_BIN" "$@" 2>/dev/null); go_exit=$?
  fi
  # Normalize every line (NDJSON-safe), slurp into a sorted array.
  py_n=$(printf '%s\n' "$py_out" | jq -S -c "$NORM" 2>/dev/null | jq -s -S . 2>/dev/null)
  go_n=$(printf '%s\n' "$go_out" | jq -S -c "$NORM" 2>/dev/null | jq -s -S . 2>/dev/null)
  if [ "$py_exit" -ne "$go_exit" ]; then
    echo "FAIL $name — exit: py=$py_exit go=$go_exit"
    fail=$((fail + 1)); return
  fi
  if [ "$py_n" != "$go_n" ]; then
    echo "FAIL $name — output diff:"
    diff <(printf '%s\n' "$py_n") <(printf '%s\n' "$go_n") | head -40
    fail=$((fail + 1)); return
  fi
  echo "PASS $name"
  pass=$((pass + 1))
}

pass=0; fail=0

run_case clients clients
run_case library-sections library sections
run_case library-list-shows library list --section 3
run_case library-list-movies library list --section 1
run_case library-list-shows-unwatched library list --section 3 --unwatched
run_case search-show search "$SHOW" --type show
run_case search-empty-note search "zzzyyyxxx-no-match"
run_case search-empty-arg search ""
run_case metadata-movie metadata "$MOVIEKEY"
run_case metadata-bogus metadata 999999999
run_case now-playing now-playing
run_case continue-watching continue-watching
run_case history-5 history --limit 5
run_case context context
run_case context-no-history context --no-history
run_case queue-show queue-show
run_case collection-list collection list
run_case playlist-list playlist list
run_case episodes episodes "$SHOW"
run_case episodes-unwatched episodes "$SHOW" --unwatched
run_case episodes-ndjson episodes "$SHOW" --ndjson
run_case episodes-empty episodes "zzzyyyxxx-no-such-show"
run_case audit-audio audit-audio "$SHOW"
run_case audit-audio-ndjson audit-audio "$SHOW" --ndjson

echo "----"
echo "parity: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
