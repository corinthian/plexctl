# plexctl

A standalone Go CLI for controlling a Plex Media Server and an Apple TV over the Plex Companion protocol. Every invocation prints one JSON line to stdout — built for scripts and for LLM-skill consumption (see the `/plex` Claude Code skill in `.claude/skills/plex/`).

## Build

```
./build.sh                              # universal macOS binary -> dist/plexctl
go build -o dist/plexctl ./cmd/plexctl  # single-arch dev build
```

Requires Go 1.26+. No runtime dependencies.

## Setup

```
plexctl auth login
```

Writes `~/.config/plexctl/config.toml` (mode 0600) with the server URL, auth token, default client, and a generated client ID. `queue_state.json` lives alongside it in the same directory.

- `$PLEXCTL_CONFIG_DIR` redirects the whole config directory — both `config.toml` and `queue_state.json`.
- Timeout resolution: `--timeout` > `$PLEXCTL_TIMEOUT` > config `timeout` > 10s.

## Security

HTTP is supported for a PMS URL, but only for a trusted local network — the token is sent unencrypted and `plexctl auth login` warns when you set one. Anything remote must be HTTPS. plexctl never disables certificate verification, and it never follows HTTP redirects (a redirect could otherwise be used to exfiltrate the token to an arbitrary host).

## Commands

Groups: auth, clients, transport (play/pause/stop/next/prev/seek), search/library/metadata, episodes plus audio/subtitle stream tools, targeted playback (play-latest/play-media/queue), queue control, status (now-playing/continue-watching/history/context), watch state (watched/unwatched/rate), collections, playlists. Run `plexctl --help` or `plexctl <command> --help` for the full reference, or `plexctl commands` for the machine-readable tree.

## Error model (v2)

Success: `{"ok": true, ...}`, one JSON document on stdout. Failure: `{"ok": false, "error": {"code", "message", "http_status"?, "hint"?}, "data"?}` — also on stdout. `code` comes from a closed enumeration (see `docs/error_model_v2.md`) and is the stable API; `message` is human text and may change. `hint`, when present, names the exact next command. Recovery/context fields (`staged`, `clientUnreachable`, `playQueueID`, …) live under `data`. A success envelope may carry `warnings` (error-shaped, non-fatal — e.g. `PLEX_STATE_SAVE_FAILED`).

## Exit codes

- `0` — success
- `1` — bad invocation (`BAD_REQUEST`: flags, args, validation) — never retry; fix the command
- `2` — Plex refused or errored (domain failures, HTTP 4xx/5xx semantics)
- `3` — transport (timeout, connection failure, unreachable client/cloud) — `TRANSPORT_TIMEOUT` items are safe to retry
- `4` — internal plexctl bug
- `5` — not authenticated — run `plexctl auth login`
- `6` — `NOT_APPLIED`: upstream said 2xx but verification shows nothing changed (e.g. `play` on an idle client)

## Behavior notes

- JSON text form: keys are sorted, no `", "` separators, em-dashes emitted as UTF-8. Identical after parsing — the `/plex` skill and jq consumers are unaffected.
- Inputs that never occur in normal use (malformed PMS payload shapes, a `queue_state.json` of the wrong JSON type) degrade gracefully — an empty result or a standard JSON error, never a crash.
- Usage errors — bad flag value, empty required argument, unknown flag — print one `BAD_REQUEST` envelope to stdout and exit 1.
- `queue-shuffle`, `queue-unshuffle` (PMS 1.43 404s them) and `volume` (Apple TV ignores it) refuse in-binary with `PLEX_UNSUPPORTED` — no HTTP is attempted.
- Bare `play` only resumes; against an idle client it verifies engagement and exits 6 (`NOT_APPLIED`) with a hint naming `play-media`.
- `X-Plex-Version`/`X-Plex-Platform` headers report plexctl's own identity.
- macOS Local Network privacy: the Local Network TCC grant attaches to the *terminal* the binary runs under. plexctl's LAN access to the PMS works once a terminal has been granted; background contexts (launchd, cron, a different terminal) get silently denied and black-hole TCP to the PMS.
- `queue` resolves the target client *before* creating the play queue: an unresolvable or inactive client exits without leaving an orphaned server-side queue. On a double failure (inactive client plus bad rating keys) the resolver error takes precedence.
- A `queue` bind failure doesn't emit a bare error: it resolves to `PLEX_QUEUE_STAGED` (state recorded — recover with `queue-start`) or `PLEX_QUEUE_CONFLICT` (a prior queue is still the active record — re-run `queue` once the device is back), with `playQueueID`/`selectedItemID`/`clientUnreachable` under `data`. `staged` derives from the persisted write itself, never a separate read, so it can't disagree with what's actually on disk; if that write itself fails, the error codes `INTERNAL` and says so.
- A verified `queue` success whose local state write fails reports `ok: true` with a `PLEX_STATE_SAVE_FAILED` warning instead of a false failure — playback already started; the warning explains why a later `queue-show` might read empty.
- `queue-start` binds the saved/staged queue to the client — the recovery path after a bind failure.
- An accepted bind is verified before it is reported: `queue` and `queue-start` poll the server's sessions (up to ~5.5s) until the client itself reports playing the queued content, and a verified result carries `clientEngaged: true`. If the device accepts the command but never engages (a wedged Plex app), the failure codes `PLEX_PLAYBACK_NOT_STARTED` with `clientEngaged: false` under `data`, the new queue stages as usual, and the hint says a plain retry won't help — relaunch the Plex app on the device, then `queue-start` (or re-run `queue` when a prior entry was preserved).
- Stale queue reads degrade without deleting state: `queue-show`, `queue-add`, and the `context` queue section return empty / no-active-queue on HTTP 404 but keep the saved entry, so a transient 404 can't destroy an addressable queue. Only `queue-clear` still clears on 404 (idempotent success). A genuinely pruned queue's entry therefore lingers — one wasted GET per read — until the next successful `queue` create replaces it.
- The Companion `commandID` is a flock-protected persisted counter in the config dir, not a per-process wall-clock seed, so back-to-back commands in the same second don't collide and silently drop. It is written atomically (temp+rename), floored above the in-memory high-water mark, and self-heals a corrupt or empty counter file.

## Testing

```
go test ./...   # unit suite (no network, fake PMS via httptest)
```

## License

MIT — see [LICENSE](LICENSE).
