# test/goldens

Two unrelated fixture families share this directory:

## PMS response fixtures (`queue-show.json`, `history-5.json`)

Fake-PMS response bodies, loaded by `internal/queue/queue_test.go`'s
`loadGolden` and fed into the fake server as the JSON a real PMS would
return. Format: raw PMS-shaped JSON (whatever the command under test expects
to receive).

## v1 CLI contract fixtures (`v1_*.golden`)

Added for P0.3 (error-model-v2 hardening). These pin the CURRENT (v1)
stdout/exit-code behavior for a representative set of commands, ahead of the
v2 error-model rebuild that will deliberately change some of these shapes.
Loaded by `internal/commands/v1_golden_test.go`'s `loadV1Golden`.

Format: `{"argv": [...], "stdout": "...", "exit_code": N}` — `argv` includes
the `plexctl` program-name element for documentation purposes (the test
strips it before calling `root.SetArgs`); `stdout` is the verbatim captured
output including the trailing newline; `exit_code` is the real process exit
code (`testutil.Capture`'s `-1` "no `output.Exit` call" sentinel is mapped to
`0` before comparing — see `execute_test.go`'s `--help`/`--version` case for
why `-1` means "exited 0 by falling off the end", not "exit code -1").

Each fixture was captured by running its scenario against the existing
fakePMS/httptest harness and recording the exact bytes `testutil.Capture`
returned — not hand-written. If a v2 change is intentional, re-record rather
than hand-editing the fixture, then diff the fixture change itself in review.

Covered scenarios:

| Fixture | Command | What it pins |
|---|---|---|
| `v1_search_hit.golden` | `search "The Birds"` | Confident single-hit summary shape (ratingKey/title/type/year), exit 0 |
| `v1_search_no_results.golden` | `search zzzyyyxxx-no-match` | `ok:true` + empty results + `"note":"no matches"`, exit 0 |
| `v1_play_idle_client.golden` | `play` | `play` against a resolvable client with no wired session data (idle) still reports unconditional `ok:true` — `playerCmd` never checks playback state before sending the Companion command. A v2 error model that wants "nothing to resume" on an idle client would change this line. |
| `v1_queue_bind_http500_staged.golden` | `queue 123` | Queue lifecycle bind-failure (HTTP error, not transport): new queue's IDs surfaced, `staged:true`, `clientUnreachable` absent, exit 1 |
| `v1_auth_missing_token.golden` | `now-playing` (no token in config) | Bootstrap failure before any network dial: `missing config key: token — run plexctl auth login`, exit 1 |
| `v1_request_timeout.golden` | `now-playing` (slow-responding fake PMS) | Request-timeout contract, exit 2. The fake server's ephemeral `host:port` is nondeterministic per run, so both the stored fixture and the freshly captured stdout are normalized (`host:port` → the literal token `<SERVER>`) before comparing — an unnormalized exact-string match would pass once and flake on every subsequent run. |

The transport-timeout **staged + `clientUnreachable`** shape (bind hangs past
the timeout, not just an HTTP error) is not re-captured as a golden here —
`TestQueueBindTimeoutStagesQueueWithClientUnreachable` in
`internal/commands/queue_test.go` already covers it thoroughly via inline
key-assertions, and entangling it with `v1_request_timeout.golden` would
test two contracts (queue staging + exit-2 timeout classification) in one
fixture instead of one each.
