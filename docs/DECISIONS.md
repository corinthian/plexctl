# Decisions

## B6 (resolve-inactive-client) — deferred indefinitely, 2026-07-06

plexctl can only bind to a client that PMS currently reports as reachable. No Plex API surface (PMS `/clients`, plex.tv devices, Companion protocol) supports resolving or waking an inactive client, so B6 has no implementable mechanism. Revisit only if Plex ships such an API.

## Explicit ratingKey outranks client resolution on watched/unwatched/rate — 2026-09-05

`watched`, `unwatched` and `rate` resolved the target client before reading their positional ratingKey, inherited from cli.py's statement order. Marking a known item watched therefore required the Apple TV to be awake and plex.tv reachable, for a call that touches neither: it addresses the library, not a device. The argument is now read first, and `clients.Resolve` runs only on the no-key path that genuinely needs a current session.

This is a deliberate divergence from v1/cli.py parity, and the only behaviour it changes is that calls which used to fail now work: an explicit-key invocation that previously exited 2 (`PLEX_CLIENT_UNKNOWN`, `PLEX_CLIENT_INACTIVE`) or 3 (`CLOUD_UNREACHABLE`) succeeds. Nothing that succeeded before fails now, no envelope changes, and `PLEX_NOTHING_PLAYING` (exit 2) still guards the omitted-key idle case unchanged.

`--client` passed alongside an explicit ratingKey is silently inert rather than rejected. Rejecting it would break habitual callers that always pass the flag, and there is no safety gain — no client participates in the operation.

## Oversize responses map to `DECODE_ERROR`, not a new code — 2026-09-06

A response body over plexctl's bound reports `DECODE_ERROR` at exit 4 with a message naming the bound, rather than a `RESPONSE_TOO_LARGE` of its own. Contract 2.5 fixed the new-code set at `TRANSPORT_FAILED` and `DECODE_ERROR`, and every additional public code is another row the deployed plex skill does not map — its code table is closed and has no catch-all.

The `cause.Cause` stays distinct: `Oversize` and `Decode` are separate values, so tests and hints can tell a body that was too big from one that was malformed. The CLI code does not need the distinction, because the recovery is the same for both: report it, do not retry. A decode failure is deterministic and a body that exceeded the bound will exceed it again.

## A rejected timeout is `BAD_REQUEST` at exit 1, not `PLEX_AUTH_REQUIRED` — 2026-09-06

Timeout values now come from one parser under one grammar (whole seconds, 1 to 86400) across `--timeout`, `$PLEXCTL_TIMEOUT` and the config file's `timeout` key, and a rejected value from any of the three is `BAD_REQUEST` at exit 1.

arrctl and traktctl route the environment and config sources to their config family, `BAD_CONFIG`. plexctl has no config family: its closed map sends every config failure through `PLEX_AUTH_REQUIRED` at exit 5, whose hint is `run: plexctl auth login`. Telling someone to sign in again because they typed `timeout = 10.5` is worse than useless, and splitting one error across two exit classes by source would be worse still. Exit 1 also matches what arrctl and traktctl return for the same input.

Config failures that genuinely are auth failures — a missing token, an unreadable file, unparseable TOML — keep `PLEX_AUTH_REQUIRED` at exit 5, unchanged.

The resolved value is a process-scoped `time.Duration` in `internal/api`, set once by root's `PersistentPreRunE`. It is a resolved value with no parsing left in it; phase C3a-2 moves it onto a per-invocation `App`, and C3b is where an injected seam would go if one is ever wanted.

## A failed write is never reported as success — 2026-09-06

`output.Print` discarded the `Fprintln` error, so a command whose output never reached stdout still exited 0 with `ok:true`. `Print` now returns the error and no caller ignores it.

On the success path a failed write is `INTERNAL` at exit 4. On the NDJSON path the run stops at the failing row: rows already written stand, nothing further is written, and no summary line is emitted — a summary after a lost row would report a count that never left the process.

On the error path the envelope is not retried. A single plain-text line goes to stderr — `plexctl: cannot write output: <io error> (original error: <CODE>)` — and the exit becomes 4 rather than the original code's class. The failure the caller now has is that plexctl could not report anything, which is an internal failure whatever the original was.

`EPIPE` is deliberately not special-cased. The Go runtime re-raises `SIGPIPE` on a broken fd 1 and the process dies before the write returns, so `plexctl … | head` never reaches this path.
