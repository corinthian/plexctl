# plexctl (Go)

Native Go port of [plex-voice](../plex-voice)'s `plexctl` — control a Plex Media Server and an Apple TV from the command line, JSON on every call, built for scripts and LLM consumption (the `/plex` Claude Code skill).

This is a behavior-frozen port: the command surface, JSON shapes, exit codes (0 ok / 1 error / 2 timeout), config file, and queue-state file are identical to the Python original. The Python repo's docs remain the reference during the transition: [DOCS.md](../plex-voice/DOCS.md), [LLM_REFERENCE.md](../plex-voice/LLM_REFERENCE.md), [STATUS.md](../plex-voice/STATUS.md). The port plan and behavior contract live in the Obsidian vault (`Projects/Subtrakt/Plexctl/Plexctl_Go_Port_Plan.md`).

## Status: v0.9

Fully ported and tested at the unit level (httptest fakes, all frozen strings and PMS quirks pinned by tests), plus read-only parity verified against the live PMS with the Python binary side by side (`scripts/parity.sh`). Not yet cut over: the live Apple TV player gates (transport, queue playback) and live write gates (collections/playlists mutation, set-audio) run at cutover to 1.0. Until then the pipx Python install stays the deployed binary.

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
