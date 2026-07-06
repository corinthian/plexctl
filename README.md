# plexctl (Go)

Native Go port of [plex-voice](../plex-voice)'s `plexctl` — control a Plex Media Server and an Apple TV from the command line, JSON on every call, built for scripts and LLM consumption (the `/plex` Claude Code skill).

Ported as a behavior-frozen mirror: the command surface, JSON shapes, exit codes (0 ok / 1 error / 2 timeout), config file, and queue-state file match the Python original, except for the deliberate post-v1 divergences noted below. The Python repo's docs remain the reference during the transition: [DOCS.md](../plex-voice/DOCS.md), [LLM_REFERENCE.md](../plex-voice/LLM_REFERENCE.md), [STATUS.md](../plex-voice/STATUS.md). The port plan and behavior contract live in the Obsidian vault (`Projects/Subtrakt/Plexctl/Plexctl_Go_Port_Plan.md`).

## Status: cutover complete (soak week → v1.0.0)

The Go binary is the deployed plexctl as of 2026-07-05. All 28 live gates passed that day from the iTerm Local Network context: 24/24 direct-LAN read parity against the Python binary, the full Apple TV player sequence (transport + queue playback, including a cross-binary `queue_state.json` read where Python read the Go-written state live), scratch collection/playlist lifecycles, bulk dry-run byte-parity, and one real `set-audio` write verified and reverted. Unit coverage stays complete (httptest fakes, every frozen string and PMS quirk pinned).

The deployed binary is a static copy at `~/.local/bin/plexctl`. Deploy is a manual `cp ~/Projects/plexctl/dist/plexctl ~/.local/bin/plexctl` — never a symlink (an editable/symlinked install re-introduces the Python-era trap). Rollback: `rm ~/.local/bin/plexctl && pipx install -e ~/Projects/plex-voice` restores the frozen Python baseline.

Currently tagged `v0.9.0`; versioning stays parked until a fresh full soak passes. The `post-v1-fixes` branch carries the deliberate post-v1 divergences from the frozen Python baseline (see Known deviations): the initial fix stream (bind-failure staging, `queue-start`, 404 handling, `commandID` persistence) plus the C-series review-fix pass (commits C1–C7, 2026-07-06) closing the 10 findings from that day's high-effort code review — resolve-before-create, no-clobber bind-failure staging, read-path 404 no longer deleting queue state, a hardened `commandID` counter, `flock`'d queue-state writes, and the annotation/error cleanups. The branch is unmerged; a fresh soak is the next gate before any merge, version, or deploy decision.

## Known deviations from the Python original

Verified by adversarial review of every module; everything not listed here matched line-for-line, and 24/24 read-only commands diff identical (jq-normalized) against the Python binary on the live PMS.

- JSON text form: key order differs (Go sorts), no `", "` separators, em-dashes emitted as UTF-8 instead of `—` escapes. Identical after parsing; the `/plex` skill and jq consumers are unaffected.
- Where Python would crash with a traceback on inputs that never occur (malformed PMS payload shapes, corrupt `queue_state.json` of the wrong JSON type), the Go port stays graceful (empty result or standard JSON error).
- Usage-error wording on stderr differs (cobra vs click); exit code 2 and empty stdout match.
- `X-Plex-Version`/`X-Plex-Platform` headers report the Go port's identity.
- `$PLEXCTL_CONFIG_DIR` redirects `config.toml` too (Python honored it only for `queue_state.json`).
- macOS Local Network privacy: the Local Network TCC grant attaches to the *terminal* the binary runs under. plexctl's LAN access to the PMS therefore works from iTerm — where the `/plex` skill runs — once granted; only non-iTerm background contexts (launchd, cron, a different terminal) get silently denied and black-hole TCP to the PMS. `scripts/parity.sh` supports `GO_CONFIG_DIR` for routing a sandboxed run through a loopback forwarder.

Deliberate post-v1 divergences from the Python baseline (the `post-v1-fixes` stream — failure paths and new commands, not the read-path shapes parity covers):

- `queue` resolves the target client *before* creating the play queue (Python created first): an unresolvable or inactive client now exits without leaving an orphaned server-side queue. On a double failure (inactive client plus bad rating keys) the resolver error takes precedence.
- `queue` bind failure no longer emits a bare error: it attaches `playQueueID`/`selectedItemID` and sets `clientUnreachable: true` for transport-shaped failures. With no queue already recorded for the client it stages the new queue and marks the output `staged: true` (recoverable via `queue-start`); when an entry already exists it is preserved with no `staged` key, so a failed bind never clobbers the bound queue — recovery there is re-running `queue` once the device is back.
- New `queue-start` command binds the saved/staged queue to the client — the recovery path after a bind failure.
- Stale queue reads degrade without deleting state: `queue-show`, `queue-add`, and the `context` queue section return empty / no-active-queue on HTTP 404 but keep the saved entry, so a transient 404 can't destroy an addressable queue. Only `queue-clear` still clears on 404 (idempotent success). A genuinely pruned queue's entry therefore lingers — one wasted GET per read — until the next successful `queue` create replaces it.
- The Companion `commandID` is a flock-protected persisted counter in the config dir, not a per-process wall-clock seed, so back-to-back commands in the same second no longer collide and silently drop. It is written atomically (temp+rename), floored above the in-memory high-water mark, and self-heals a corrupt or empty counter file.

## Build

```
./build.sh                              # universal macOS binary -> dist/plexctl
go build -o dist/plexctl ./cmd/plexctl  # single-arch dev build
```

Requires Go 1.26+. No runtime dependencies.

## Configuration

Drop-in with the Python version: reads `~/.config/plexctl/config.toml` (written by `plexctl auth login`, mode 0600) and shares `queue_state.json` in the same directory. `$PLEXCTL_CONFIG_DIR` redirects the whole config directory (the Python honored it for queue state only; the Go port extends it to the config file). Timeout resolution: `--timeout` > `$PLEXCTL_TIMEOUT` > config `timeout` > 10s.

## Testing

```
go test ./...        # unit suite (no network, fake PMS via httptest)
scripts/parity.sh    # live read-only diff vs the Python binary (needs both binaries + reachable PMS)
```

## Commands

Identical to the Python plexctl — see `plexctl --help` and the Python repo's [README](../plex-voice/README.md) and [LLM_REFERENCE.md](../plex-voice/LLM_REFERENCE.md) for the full reference. Groups: auth, clients, transport (play/pause/stop/next/prev/seek/volume), search/library/metadata, episodes + audio/subtitle stream tools, targeted playback (play-latest/play-media/queue), queue control, status (now-playing/continue-watching/history/context), watch state (watched/unwatched/rate), collections, playlists.
