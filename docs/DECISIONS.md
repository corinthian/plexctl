# Decisions

## B6 (resolve-inactive-client) — deferred indefinitely, 2026-07-06

plexctl can only bind to a client that PMS currently reports as reachable. No Plex API surface (PMS `/clients`, plex.tv devices, Companion protocol) supports resolving or waking an inactive client, so B6 has no implementable mechanism. Revisit only if Plex ships such an API.

## Explicit ratingKey outranks client resolution on watched/unwatched/rate — 2026-09-05

`watched`, `unwatched` and `rate` resolved the target client before reading their positional ratingKey, inherited from cli.py's statement order. Marking a known item watched therefore required the Apple TV to be awake and plex.tv reachable, for a call that touches neither: it addresses the library, not a device. The argument is now read first, and `clients.Resolve` runs only on the no-key path that genuinely needs a current session.

This is a deliberate divergence from v1/cli.py parity, and the only behaviour it changes is that calls which used to fail now work: an explicit-key invocation that previously exited 2 (`PLEX_CLIENT_UNKNOWN`, `PLEX_CLIENT_INACTIVE`) or 3 (`CLOUD_UNREACHABLE`) succeeds. Nothing that succeeded before fails now, no envelope changes, and `PLEX_NOTHING_PLAYING` (exit 2) still guards the omitted-key idle case unchanged.

`--client` passed alongside an explicit ratingKey is silently inert rather than rejected. Rejecting it would break habitual callers that always pass the flag, and there is no safety gain — no client participates in the operation.
