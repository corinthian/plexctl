#!/usr/bin/env python3
"""B-run harness: regenerate the A shims, then retarget passthrough at the v2
binary (dist/plexctl), move logs to logs-b/, and swap every injected/canned
ERROR payload to its v2 envelope. Success shapes are unchanged (v2 kept flat
success envelopes). Same titles, ratingKeys, and interception points as A."""
import json, os, re, subprocess

B = "/Users/rlarsen/.claude/jobs/08fe1310/tmp/battery"
PAY = f"{B}/payloads"; SHIMS = f"{B}/shims"
V2 = "/Users/rlarsen/Projects/plexctl/dist/plexctl"

subprocess.run(["python3", f"{B}/build_battery.py"], check=True)
os.makedirs(f"{B}/logs-b", exist_ok=True)

def w(name, obj):
    open(f"{PAY}/{name}", "w").write(json.dumps(obj, sort_keys=True, separators=(",", ":")) + "\n")

def err(code, message, hint=None, data=None, http_status=None):
    e = {"code": code, "message": message}
    if http_status: e["http_status"] = http_status
    if hint: e["hint"] = hint
    env = {"ok": False, "error": e}
    if data: env["data"] = data
    return env

TIMEOUT = ("request timed out: Get \"http://172.16.1.53:32500/player/playback/%s\": "
           "context deadline exceeded (Client.Timeout exceeded while awaiting headers)")

w("t05_bind_staged.json", err("PLEX_QUEUE_STAGED",
    "queue created but the client did not confirm playback (%s)" % (TIMEOUT % "playMedia?commandID=812&type=video"),
    hint="run: plexctl queue-start once the client is awake — do NOT re-run queue",
    data={"playQueueID": "9001", "selectedItemID": "9101", "staged": True, "clientUnreachable": True}))
w("t06_bind_deadzone.json", err("PLEX_QUEUE_CONFLICT",
    "queue created but could not be staged — a previous queue is still the active record (%s)" % (TIMEOUT % "playMedia?commandID=813&type=video"),
    hint="re-run the queue command once the client is back — queue-start would start the OLD queue",
    data={"activeQueueID": "5772", "orphanedQueueID": "9202", "clientUnreachable": True}))
w("t11_pause_timeout.json", err("PLEX_CLIENT_UNREACHABLE",
    TIMEOUT % "pause?commandID=815&type=video",
    hint="wake the device or relaunch Plex on it, then retry",
    data={"client": "Apple TV"}))
w("t13_smart_refusal.json", err("PLEX_SMART_CONTAINER",
    "smart collection: contents are query-driven — edit the filter in Plex, not the item list",
    hint="edit the smart rule in the Plex app", data={"kind": "collection"}))
w("t14_spans.json", err("PLEX_SCOPE_REQUIRED",
    "'Dark' spans 3 seasons (1-3)",
    hint="add --all-seasons, or narrow with --season N",
    data={"seasons": {"1": 10, "2": 8, "3": 8}}))
w("play_400.json", err("BAD_REQUEST", "HTTP 400: Bad Request", http_status=400,
    hint="expected a ratingKey — playQueueItemID is not valid here", data={"expected": "ratingKey"}))
w("unsupported.json", err("PLEX_UNSUPPORTED", "queue shuffle is not supported by the server"))
w("t07_play_notapplied.json", err("NOT_APPLIED",
    "client accepted play but nothing started — play only resumes; it cannot start an idle client",
    hint="start items with: plexctl play-media RATING_KEY"))
# v2 success envelopes: stateSaved key retired
w("t04_queue_ok.json", {"ok": True, "playQueueID": "9400", "selectedItemID": "9451", "clientEngaged": True})
w("t05_queue_ok.json", {"ok": True, "playQueueID": "9002", "selectedItemID": "9102", "clientEngaged": True})
w("t06_queue_ok.json", {"ok": True, "playQueueID": "9203", "selectedItemID": "9303", "clientEngaged": True})

# exit codes change for injected failures: staged/conflict now exit 2, pause timeout exit 3
for name in os.listdir(SHIMS):
    p = f"{SHIMS}/{name}"
    s = open(p).read()
    s = s.replace("REAL=/Users/rlarsen/.local/bin/plexctl.real", f"REAL={V2}")
    s = s.replace(f"LOG=$B/logs/{name}.calls.log", f"LOG=$B/logs-b/{name}.calls.log")
    s = s.replace("emit t05_bind_staged.json 2", "emit t05_bind_staged.json 2")  # exit 2 unchanged
    s = s.replace("emit t06_bind_deadzone.json 2", "emit t06_bind_deadzone.json 2")
    s = s.replace("emit t11_pause_timeout.json 2", "emit t11_pause_timeout.json 3")
    s = s.replace("emit shuffle_404.json 1", "emit unsupported.json 2")
    if name == "T07":
        s = s.replace('play)        echo "SILENT-NOOP: bare play on idle accepted" >> "$LOG"; emit ok.json ;;',
                      'play)        echo "NOT_APPLIED: bare play on idle" >> "$LOG"; emit t07_play_notapplied.json 6 ;;')
    open(p, "w").write(s)

# state flags reset
for f in os.listdir(f"{B}/state"):
    fp = f"{B}/state/{f}"
    if os.path.isfile(fp):
        os.unlink(fp)

print("B harness ready; REAL ->", V2)
