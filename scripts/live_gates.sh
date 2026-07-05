#!/usr/bin/env bash
# Live cutover gates for the Go port — run from a context with Local Network
# access (iTerm). Exercises the real PMS and Apple TV:
#   gate 1: read-only parity, direct LAN (scripts/parity.sh)
#   gate 2: Apple TV player sequence (queue/pause/seek-dance/add/remove/clear/stop)
#           + cross-binary queue_state.json read
#   gate 3: writes on scratch targets (collection + playlist lifecycle,
#           bulk set-audio --dry-run diff vs Python, one reverted real write)
# Every mutation is reverted or targets a scratch object created here.
set -u
cd "$(dirname "$0")/.."
GO=./dist/plexctl
PY="$HOME/.local/bin/plexctl"
LOG="${LIVE_GATE_LOG:-/tmp/live_gates.log}"
PASS=0; FAIL=0

say()  { echo "### $*" >> "$LOG"; }
run()  { echo "\$ $*" >> "$LOG"; "$@" >> "$LOG" 2>&1; }
check() { # check NAME JQ_EXPR JSON
  local name=$1 expr=$2 json=$3
  if printf '%s' "$json" | jq -e "$expr" > /dev/null 2>&1; then
    echo "PASS $name" >> "$LOG"; PASS=$((PASS+1))
  else
    echo "FAIL $name — jq '$expr' on: $json" >> "$LOG"; FAIL=$((FAIL+1))
  fi
}

: > "$LOG"
say "gate 1: read-only parity, direct LAN"
GO_BIN="$GO" PY_BIN="$PY" ./scripts/parity.sh >> "$LOG" 2>&1

say "gate 2: player sequence pre-check"
NP=$("$GO" now-playing)
echo "$NP" >> "$LOG"
STATE=$(printf '%s' "$NP" | jq -r '.state // "unknown"')
if [ "$STATE" = "playing" ] && [ "${FORCE:-0}" != "1" ]; then
  say "gate 2 SKIPPED — something is actively playing (state=playing); rerun with FORCE=1"
else
  SHOW=$("$GO" library list --section 3 | jq -r '.items[0].title')
  KEYS=$("$GO" episodes "$SHOW" | jq -r '[.episodes[].ratingKey][:4] | @tsv')
  K1=$(cut -f1 <<<"$KEYS"); K2=$(cut -f2 <<<"$KEYS"); K3=$(cut -f3 <<<"$KEYS"); K4=$(cut -f4 <<<"$KEYS")
  say "gate 2: queue $K1 $K2 $K3 from '$SHOW', pause immediately"
  QR=$("$GO" queue "$K1" "$K2" "$K3"); echo "$QR" >> "$LOG"
  check queue-create '.ok == true and (.playQueueID | length > 0)' "$QR"
  PR=$("$GO" pause); echo "$PR" >> "$LOG"; check pause-immediately '.ok == true' "$PR"
  QS=$("$GO" queue-show); echo "$QS" >> "$LOG"
  check queue-show-3 '.ok == true and (.items | length == 3)' "$QS"
  say "gate 2: seek while paused (the resume→seek→re-pause dance)"
  sleep 2
  SR=$("$GO" seek 0:10); echo "$SR" >> "$LOG"; check seek-paused-dance '.ok == true' "$SR"
  say "gate 2: queue-add size-delta, then cross-binary state read"
  AR=$("$GO" queue-add "$K4"); echo "$AR" >> "$LOG"
  check queue-add '.ok == true and .added == 1' "$AR"
  PYQS=$("$PY" queue-show); echo "PYTHON: $PYQS" >> "$LOG"
  check python-reads-go-queue-state '.ok == true and (.items | length == 4)' "$PYQS"
  RM_ID=$(printf '%s' "$PYQS" | jq -r '.items[-1].playQueueItemID')
  RR=$("$GO" queue-remove "$RM_ID"); echo "$RR" >> "$LOG"; check queue-remove '.ok == true' "$RR"
  QS3=$("$GO" queue-show); check queue-show-back-to-3 '.items | length == 3' "$QS3"
  say "gate 2: clear + stop"
  CR=$("$GO" queue-clear); echo "$CR" >> "$LOG"; check queue-clear '.ok == true' "$CR"
  QSE=$("$GO" queue-show); echo "$QSE" >> "$LOG"; check queue-show-empty '.state == "empty"' "$QSE"
  SP=$("$GO" stop); echo "$SP" >> "$LOG"; check stop '.ok == true' "$SP"
fi

say "gate 3: scratch collection lifecycle (Go writes, Python cross-reads)"
MK=$("$GO" library list --section 1 | jq -r '[.items[].ratingKey][:3] | @tsv')
M1=$(cut -f1 <<<"$MK"); M2=$(cut -f2 <<<"$MK"); M3=$(cut -f3 <<<"$MK")
CC=$("$GO" collection create "plexctl-go-gate-scratch" 1 "$M1" "$M2"); echo "$CC" >> "$LOG"
check collection-create '.ok == true and .count == 2' "$CC"
CKEY=$(printf '%s' "$CC" | jq -r '.ratingKey')
if [ -n "$CKEY" ] && [ "$CKEY" != "null" ]; then
  CS=$("$GO" collection show "$CKEY"); check collection-show-2 '.count == 2' "$CS"
  CA=$("$GO" collection add "$CKEY" "$M3"); check collection-add '.ok == true and .added == 1' "$CA"
  PYC=$("$PY" collection show "$CKEY"); echo "PYTHON: $PYC" >> "$LOG"
  check python-sees-collection '.count == 3' "$PYC"
  CRM=$("$GO" collection remove "$CKEY" "$M3"); check collection-remove-DELETE-verb '.ok == true' "$CRM"
  CRN=$("$GO" collection rename "$CKEY" "plexctl-go-gate-renamed"); check collection-rename '.ok == true' "$CRN"
  CD=$("$GO" collection delete "$CKEY"); echo "$CD" >> "$LOG"; check collection-delete '.ok == true' "$CD"
else
  echo "FAIL collection-create returned no key — skipping dependent steps" >> "$LOG"; FAIL=$((FAIL+1))
fi

say "gate 3: scratch playlist lifecycle"
PC=$("$GO" playlist create "plexctl-go-gate-scratch" "$M1" "$M2"); echo "$PC" >> "$LOG"
check playlist-create '.ok == true and .count == 2' "$PC"
PKEY=$(printf '%s' "$PC" | jq -r '.ratingKey')
if [ -n "$PKEY" ] && [ "$PKEY" != "null" ]; then
  PA=$("$GO" playlist add "$PKEY" "$M3"); check playlist-add '.ok == true' "$PA"
  PS=$("$GO" playlist show "$PKEY"); check playlist-show-3 '.count == 3' "$PS"
  PID=$(printf '%s' "$PS" | jq -r '.items[-1].playlistItemID')
  PRM=$("$GO" playlist remove "$PKEY" "$PID"); check playlist-remove '.ok == true' "$PRM"
  PCL=$("$GO" playlist clear "$PKEY"); check playlist-clear '.ok == true' "$PCL"
  PD=$("$GO" playlist delete "$PKEY"); echo "$PD" >> "$LOG"; check playlist-delete '.ok == true' "$PD"
else
  echo "FAIL playlist-create returned no key — skipping dependent steps" >> "$LOG"; FAIL=$((FAIL+1))
fi

say "gate 3: bulk set-audio --dry-run diff (Go vs Python)"
SHOW=${SHOW:-$("$GO" library list --section 3 | jq -r '.items[0].title')}
GDRY=$("$GO" set-audio --show "$SHOW" --all-seasons --dry-run | jq -S .)
PDRY=$("$PY" set-audio --show "$SHOW" --all-seasons --dry-run | jq -S .)
if [ "$GDRY" = "$PDRY" ]; then echo "PASS bulk-dry-run-parity" >> "$LOG"; PASS=$((PASS+1));
else echo "FAIL bulk-dry-run-parity" >> "$LOG"; diff <(printf '%s\n' "$PDRY") <(printf '%s\n' "$GDRY") | head -20 >> "$LOG"; FAIL=$((FAIL+1)); fi

say "gate 3: one real set-audio write, verified and reverted"
TARGET=""
for RK in $("$GO" library list --section 1 | jq -r '[.items[].ratingKey][:20][]'); do
  META=$("$GO" metadata "$RK")
  N=$(printf '%s' "$META" | jq '[.metadata.Media[].Part[].Stream[] | select(.streamType == 2)] | length')
  if [ "${N:-0}" -ge 2 ]; then TARGET=$RK; break; fi
done
if [ -z "$TARGET" ]; then
  echo "SKIP real-write — no movie with 2+ audio tracks among first 20; acceptable" >> "$LOG"
else
  STREAMS=$("$GO" metadata "$TARGET" | jq '[.metadata.Media[].Part[].Stream[] | select(.streamType == 2)]')
  ORIG=$(printf '%s' "$STREAMS" | jq -r '(map(select(.selected)) | .[0].id) // (.[0].id)')
  OTHER=$(printf '%s' "$STREAMS" | jq -r --argjson o "$ORIG" '[.[] | select(.id != $o)][0].id')
  echo "target=$TARGET orig-stream=$ORIG other-stream=$OTHER" >> "$LOG"
  W1=$("$GO" set-audio "$TARGET" --stream-id "$OTHER"); echo "$W1" >> "$LOG"
  check set-audio-write '.ok == true' "$W1"
  sleep 1
  SEL=$("$GO" metadata "$TARGET" | jq --argjson w "$OTHER" '[.metadata.Media[].Part[].Stream[] | select(.streamType == 2 and .selected)][0].id == $w')
  [ "$SEL" = "true" ] && { echo "PASS set-audio-verified" >> "$LOG"; PASS=$((PASS+1)); } || { echo "FAIL set-audio-verified" >> "$LOG"; FAIL=$((FAIL+1)); }
  W2=$("$GO" set-audio "$TARGET" --stream-id "$ORIG"); echo "$W2" >> "$LOG"
  check set-audio-reverted '.ok == true' "$W2"
fi

say "GATES COMPLETE: $PASS passed, $FAIL failed"
