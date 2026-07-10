# Decisions

## B6 (resolve-inactive-client) — deferred indefinitely, 2026-07-06

plexctl can only bind to a client that PMS currently reports as reachable. No Plex API surface (PMS `/clients`, plex.tv devices, Companion protocol) supports resolving or waking an inactive client, so B6 has no implementable mechanism. Revisit only if Plex ships such an API.
