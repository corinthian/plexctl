# Error Model v2 (P1.1 — the contract)

Status: FINAL for the v2 migration. Everything in Phases 1–4 executes this document. If an implementation task surfaces a case this doc doesn't cover, STOP and escalate — do not improvise a code or exit class.

Inputs: `docs/error_inventory.md` (P0.1, every emission site), `docs/skill_compensations.md` (P0.2, every skill rule being absorbed). Reference: traktctl v1.1.0 `internal/output/output.go`.

## 1. Contract

### Envelope

- **Success: unchanged.** Flat `{"ok": true, ...fields}` exactly as v1, one JSON document per call. Deliberate deviation from traktctl's `{ok, data, meta}` nesting: restructuring every success payload doubles the migration surface and buys the Subtrakt classifier nothing — it routes on `error.code`, which the error side provides. Recorded as the documented reason to differ.
- **Error:**

```json
{
  "ok": false,
  "error": {
    "code": "PLEX_QUEUE_STAGED",
    "message": "made the queue but the client did not respond",
    "http_status": 0,
    "hint": "run: plexctl queue-start once the client is awake"
  },
  "data": {
    "playQueueID": "12345",
    "selectedItemID": "67890",
    "staged": true,
    "clientUnreachable": true
  }
}
```

`error.code` from the closed enumeration in §2 — **every** `ok:false` carries one; there is no uncoded error in v2. `message` stays human-readable free text (unstable, never match on it). `http_status` present only when an upstream HTTP status drove the failure. `hint` present whenever a concrete next action exists, and names the exact command. `data` holds machine-usable recovery/context fields — the v1 ad-hoc envelope keys (`staged`, `clientUnreachable`, `clientEngaged`, `partialQueueID`, `rollbackAttempted`, `added`) move here and appear ONLY under `data` on error envelopes.

- **Warnings on success:** a success envelope may carry `"warnings": [{"code", "message", "hint"}]` for non-fatal infrastructure failures that must not read as command failure. Sole v2 producer: `PLEX_STATE_SAVE_FAILED` (the v1 `stateSaved: false` key — queue worked, local state record didn't write). `stateSaved` disappears as a bare key.
- **Stream: errors go to stdout,** same as success. This matches what traktctl actually does (its `EmitError` writes to the Out stream; the Err writer is an unused seam). The hardening plan's "stderr" line followed the Subtrakt spec's aspiration, not traktctl reality — match-the-reference wins, and the Subtrakt spec gets corrected in P4.2. Keep an `output.Stderr` seam for a future dual-stream move if traktctl ever makes one.
- NDJSON emission (`--ndjson` rows + summary) unchanged.

### Exit codes

| Exit | Meaning | v1 equivalent |
|---|---|---|
| 0 | success | 0 |
| 1 | user error — bad flags/args/invocation (`BAD_REQUEST`) | 64 (absorbed) and the misrouted exit-1 validation errors in streams.go |
| 2 | Plex refused or errored (domain failures, HTTP 4xx/5xx semantics) | 1 |
| 3 | transport — timeout, connection failure, unreachable client/cloud | 2 (timeout) and part of 1 |
| 4 | internal plexctl bug (`INTERNAL`) | 1 |
| 5 | not authenticated (`PLEX_AUTH_REQUIRED`) | 1 |
| 6 | `NOT_APPLIED` — upstream said 2xx, verification shows nothing changed | 0 (silent no-op) or 1 |

Exit 64 is dead. Batch-retry semantics move from "exit 2" to "code is `TRANSPORT_TIMEOUT`".

## 2. Error code enumeration (closed set)

Family rule for Subtrakt: the auth code contains `_AUTH_` so its cross-tool classifier works unchanged.

| Code | Exit | Fires when | hint | data fields |
|---|---|---|---|---|
| `BAD_REQUEST` | 1 | Any invocation error: cobra unknown command/flag/arg-count, `--timeout <= 0`, `choiceError`, empty `QUERY`/`SHOW`, seek position parse (`unrecognised position format`, `invalid seek position`), mutually-exclusive flag sets in set-audio/set-subtitle, `provide RATING_KEY…`, non-movie/show section on `collection create`, wrong ID space (HTTP 400 from PMS on play-media) | usage-shaped, e.g. `expected a ratingKey — playQueueItemID is not valid here` | `expected` (ID-space case) |
| `PLEX_AUTH_REQUIRED` | 5 | `missing config key:` (never logged in / config edited), `invalid config at …` (corrupt TOML), HTTP 401/403 from PMS or plex.tv on any command | `run: plexctl auth login` | — |
| `PLEX_AUTH_FAILED` | 2 | `auth login` itself rejected: plex.tv sign-in HTTP >= 400 (bad credentials), unexpected auth response shape, non-JSON body | `check credentials and retry: plexctl auth login` | — |
| `PLEX_NOTHING_PLAYING` | 2 | No current session where one is required: `watched`/`unwatched`/`rate` with no key and idle client; relative seek with no session (`could not determine current playback position`) | `provide a ratingKey` / `nothing to seek — start playback first` | — |
| `PLEX_NOT_FOUND` | 2 | `nothing found for: <q>` (search/play-latest full miss), `no metadata found for ratingKey`, generic PMS 404 outside the queue-show/queue-add special case | — | `query` or `ratingKey` |
| `PLEX_ALL_WATCHED` | 2 | `no unwatched episodes for: <q>` — show exists, everything seen | `drop --unwatched to replay, or pick another show` | `show`, `showRatingKey` |
| `PLEX_SHOW_AMBIGUOUS` | 2 | Bulk set-audio `--show` matches multiple series | `disambiguate with --show-rating-key KEY` | `matches` (title+ratingKey list) |
| `PLEX_SCOPE_REQUIRED` | 2 | Bulk set-audio spans multiple seasons without `--all-seasons`/`--season` | `add --all-seasons, or narrow with --season N` | `seasons` |
| `PLEX_TRACK_NOT_FOUND` | 2 | `no <lang> audio/subtitle track`, no matching `--stream-id`, no media part | — | `language` or `streamID` |
| `PLEX_SEEK_FAILED` | 2 | `could not resume before seek:`, seek transport/HTTP failure mid-sequence, `seeked but failed to restore pause state:` (partial: `data.seeked: true, repaused: false`) | `try again` | `seeked`, `repaused` |
| `PLEX_CLIENT_UNKNOWN` | 2 | `client not found: <x>` — name matches no registered device | `run: plexctl clients` | `client` |
| `PLEX_CLIENT_INACTIVE` | 2 | Registered but `active: false` — Plex app not open on the device | `open (or relaunch) Plex on the device, then retry` | `client` |
| `PLEX_CLIENT_AMBIGUOUS` | 2 | Two active devices share the name | `target by machineIdentifier — run: plexctl clients` | `matches` |
| `PLEX_CLIENT_UNREACHABLE` | 3 | Transport failure on a Companion/`:32500`/`/player/` URL — device asleep or gone; includes queue bind transport failures (paired with `PLEX_QUEUE_STAGED` per §5 precedence: the queue code wins, `clientUnreachable: true` rides in `data`) | `wake the device / relaunch Plex on it, then retry` | `client`, `url` |
| `CLOUD_UNREACHABLE` | 3 | Transport failure against plex.tv (v1 `plex.tv ` prefix) | `plex.tv is unreachable — the local server is unaffected; retry shortly` | — |
| `TRANSPORT_TIMEOUT` | 3 | `request timed out:` against PMS (`:32400`) | `retry — on batches, retry only timed-out items` | `url` |
| `TRANSPORT_FAILED` | 3 | `connection failed:` / `request failed:` transport class against PMS | — | `url` |
| `PLEX_SERVER_ERROR` | 2 | PMS HTTP 5xx | — | — |
| `PLEX_HTTP_ERROR` | 2 | Any other unmapped upstream HTTP >= 400 (carries `http_status`) | — | — |
| `PLEX_QUEUE_CREATE_FAILED` | 2 | playQueue creation returned no `playQueueID`/`selectedItemID`; mid-add failure with rollback (`data.partialQueueID`, `data.rollbackAttempted`) | `retry the queue command` | `partialQueueID`, `rollbackAttempted` |
| `PLEX_QUEUE_STAGED` | 2 | Queue created server-side, bind to client failed, state record saved — recoverable | `run: plexctl queue-start once the client is awake — do NOT re-run queue` | `playQueueID`, `selectedItemID`, `staged: true`, `clientUnreachable` |
| `PLEX_QUEUE_CONFLICT` | 2 | Bind failed AND an earlier queue is still the active record — new queue could not be staged (v1's dead zone, signalled only by an *absent* key) | `re-run the queue command once the client is back — queue-start would start the OLD queue` | `activeQueueID`, `orphanedQueueID` |
| `PLEX_PLAYBACK_NOT_STARTED` | 2 | Bind accepted but engagement verification found nothing playing (v1 `playback never started`, `clientEngaged: false`) | `relaunch Plex on the client; recover with queue-start (staged) or re-run queue (not staged)` | `staged`, `clientEngaged: false` |
| `PLEX_NO_QUEUE` | 2 | `no active queue on <client>` (queue-start/shuffle/unshuffle/clear/remove/add); also queue-show/queue-add 404 (remembered queue no longer resolves — same user-facing meaning) | `queue something first` | `client` |
| `PLEX_QUEUE_PARTIAL` | 2 | queue-add applied some keys then failed (`data.added > 0`) | `retry with the remaining items` | `added`, `failedKey` |
| `PLEX_SMART_CONTAINER` | 2 | Content mutation on a smart collection/playlist (pre-checked before HTTP; PMS would 2xx-no-op) | `edit the smart rule in the Plex app` | `kind: "collection"\|"playlist"` |
| `PLEX_UNSUPPORTED` | 2 | Operation the stack cannot perform: `queue-shuffle`/`queue-unshuffle` (PMS 1.43 404s them), `volume` (Apple TV Companion accepts and ignores) | — | — |
| `PLEX_STATE_SAVE_FAILED` | n/a (warning only) | Local queue-state write failed after a successful operation — emitted in success `warnings`, never as a failure | `state file may be stale — a later queue-show can read empty` | — |
| `NOT_APPLIED` | 6 | Upstream 2xx but verification shows nothing changed: bare `play` on an idle client (P3.1), queue-add whose post-add size verify shows no growth (replaces v1 "likely unknown or invalid"), any P1.3-verified mutation that no-ops | names the effective command, e.g. `client idle — start items with: plexctl play-media RATING_KEY` | command-specific |
| `INTERNAL` | 4 | plexctl bug: impossible state, marshal failure, `could not retrieve server machineIdentifier` | `report this — plexctl bug` | — |

30 codes incl. the warning-only one. The skill's v2 translation table maps code → phrase, ~1 row per code — down from 33 free-text rows + state-machine prose.

## 3. Migration mapping (v1 emission → v2)

Keyed to `docs/error_inventory.md`. P2 agents follow this table mechanically; anything unlisted = STOP and escalate.

- **auth.go** (13 sites): URL validation → `BAD_REQUEST`. Sign-in HTTP >= 400 / response-shape / non-JSON → `PLEX_AUTH_FAILED`. Sign-in transport (`classifyAuthTransport`) → `TRANSPORT_TIMEOUT`/`TRANSPORT_FAILED`/`CLOUD_UNREACHABLE` per class. PMS-verify failures → `TRANSPORT_FAILED` (transport) or `PLEX_AUTH_FAILED` (HTTP >= 400, wrong URL/token). Config write failure → `INTERNAL`.
- **config.go** (2): both → `PLEX_AUTH_REQUIRED`, exit 5.
- **root.go `Execute` catch-all** (1): → `BAD_REQUEST`, exit 1 (was `Usage`/64).
- **api.go `ExitOnError`** (the chokepoint): classify by `api.Error.Kind` + target + status. `Kind == "timeout"` → `TRANSPORT_TIMEOUT` (PMS) / `PLEX_CLIENT_UNREACHABLE` (`:32500`//player/) — target classification happens HERE, in the binary, ending the skill's URL-sniffing rule. `Kind == "error"` transport → `TRANSPORT_FAILED` / `PLEX_CLIENT_UNREACHABLE` / `CLOUD_UNREACHABLE` (plex.tv base). HTTP statuses: 401/403 → `PLEX_AUTH_REQUIRED`; 404 → `PLEX_NOT_FOUND` (callers with a better meaning — queue-show/add — catch 404 before this layer, as today); 400 → `BAD_REQUEST`; 5xx → `PLEX_SERVER_ERROR`; other → `PLEX_HTTP_ERROR`.
- **clients.go** (4 inline sites): → `PLEX_CLIENT_AMBIGUOUS`, `PLEX_CLIENT_INACTIVE` (×2), `PLEX_CLIENT_UNKNOWN`.
- **library.go**: empty-arg Usage sites → `BAD_REQUEST`. `no metadata found` → `PLEX_NOT_FOUND`. `no unwatched episodes` → `PLEX_ALL_WATCHED`. `nothing found for` → `PLEX_NOT_FOUND`.
- **streams.go**: sites 3–6, 8, 11–12 (flag validation, wrongly exit 1 today) → `BAD_REQUEST`, exit 1. Track/metadata misses → `PLEX_TRACK_NOT_FOUND` (`PLEX_NOT_FOUND` when metadata itself missing). Bulk: ambiguous → `PLEX_SHOW_AMBIGUOUS`, span → `PLEX_SCOPE_REQUIRED`, no match → `PLEX_NOT_FOUND`, partial bulk results keep per-episode `status` rows (success shape) with overall `ok: failed == 0` → on failure `PLEX_QUEUE_PARTIAL`-style? NO — bulk audio keeps its own aggregate: `ok:false` + `BAD_REQUEST`? STOP-rule exception, resolved here: bulk set-audio partial failure emits `PLEX_TRACK_NOT_FOUND` when all failures are track-misses, else `PLEX_HTTP_ERROR`, with per-episode `data.results` preserved.
- **transport.go**: play/pause/stop/next/prev/volume Companion failures → via ExitOnError classification (`PLEX_CLIENT_UNREACHABLE`/`PLEX_HTTP_ERROR`). seek family → `BAD_REQUEST` (parse), `PLEX_NOTHING_PLAYING` (no session), `PLEX_SEEK_FAILED` (mid-sequence). `volume` → `PLEX_UNSUPPORTED` (absorbs skill ban; P3 scope).
- **queue.go**: per §2 rows `PLEX_QUEUE_*`, `PLEX_NO_QUEUE`, `PLEX_PLAYBACK_NOT_STARTED`; queue-add no-growth → `NOT_APPLIED`.
- **watch.go**: no-session sites → `PLEX_NOTHING_PLAYING`; scrobble/rate HTTP failures already route through ExitOnError.
- **collections.go / playlists.go**: zero-ratingKey / bad-section / bad-type validation → `BAD_REQUEST`. Smart refusals → `PLEX_SMART_CONTAINER`. Create-returned-nothing → `PLEX_QUEUE_CREATE_FAILED`? NO — wrong domain: new create failures use `PLEX_HTTP_ERROR` when status-driven, else `INTERNAL` (`no metadata/ratingKey in create response` is a PMS-shape surprise). machineIdentifier failures → `INTERNAL`. Per-item add errors with partial count → `PLEX_QUEUE_PARTIAL` semantics? Reuse: yes — `PLEX_QUEUE_PARTIAL` is renamed conceptually "partial batch mutation"; it applies to collection/playlist add too (`data.added`).
- **sessions.go `context`**: top-level `ok` becomes the AND of all fetched sections; per-section failures embed `{ok:false, error:{code…}}` in place. Exit = 0 only if all sections succeeded; else the exit class of the first failed section. (Fixes the inventory's now-playing-only inconsistency.)

## 4. Mechanics (P1.2)

Keep plexctl's direct-call style — commands call output and exit; no traktctl-style typed-error return refactor (that would touch every RunE for no contract gain).

New `internal/output` API:

```go
type ErrorBody struct{ Code, Message string; HTTPStatus int; Hint string }
type CLIError struct{ Code, Message string; HTTPStatus int; Hint string; Exit int; Data jsonx.J }
func FailErr(e *CLIError)            // prints error envelope, exits e.Exit (0 guards to 4)
func Warn(result jsonx.J, w ...ErrorBody) jsonx.J  // appends to result["warnings"]
```

- Exit selection lives in the `CLIError` constructor helpers, one per family (`ErrBadRequest(msg, hint)`, `ErrAuthRequired()`, `ErrTransport(kind, target, url)` …) so call sites cannot pick a wrong (code, exit) pair.
- `Fail`, `Usage`, and `Out`'s falsy-ok branch are deleted at the END of P2 (they carry a `// Deprecated: v2 migration` comment during it). `Out` remains for success emission; its failure branch panics in tests once migration completes (catches stragglers).
- `api.Error` gains `Code`, `HTTPStatus`, `Target` fields; `ExitOnError` becomes the classified `FailErr` chokepoint per §3.
- `internal/{queue,streams,playback,collections,playlists}` result-builders that today hand back `{ok:false,…}` maps return `*output.CLIError` (or a success map) instead; the command layer forwards to `FailErr`.

## 5. NOT_APPLIED verification (P1.3)

Verified cheaply (in scope):
- **bare `play` on idle client** — after Companion accept, one `now-playing` poll (≤2 tries, 1s apart); still idle → `NOT_APPLIED` exit 6, hint names `play-media`. (P3.1 may instead auto-bootstrap; if it does, `NOT_APPLIED` remains the fallback when no queue is loaded to bootstrap from.)
- **queue-add** — size-verify already implemented; no-growth becomes `NOT_APPLIED`.
- **queue create/queue-start engagement** — `ConfirmEngaged` already implemented (the `queue-engagement-verify` base commit); failure code `PLEX_PLAYBACK_NOT_STARTED`.
- **smart-container mutations** — pre-checked (`PLEX_SMART_CONTAINER`) so the 2xx-no-op never happens; the pre-check IS the verification.

Exempt (documented, not verified): transport commands pause/stop/next/prev (Apple TV accepts no-ops by design; verification would add an HTTP round trip per keypress-equivalent — the skill's "trust ok:true" rule survives for these); `watched`/`unwatched`/`rate` (scrobble endpoints lack a cheap same-call readback; a metadata refetch per write is not worth it); `seek` (post-state already reported via `PLEX_SEEK_FAILED`/`data.repaused`); collection/playlist non-smart mutations (PMS reflects these reliably; the smart case was the known 2xx-no-op).

Queue code precedence (one failure, one code): `PLEX_QUEUE_CONFLICT` > `PLEX_QUEUE_STAGED` > `PLEX_PLAYBACK_NOT_STARTED` > `PLEX_CLIENT_UNREACHABLE`. Transport detail always rides in `data.clientUnreachable`, never as the code, when a queue-scoped code applies.

## 6. Success-side invariants shipping with v2 (Phase 3 scope)

- `durationNominal: true` on show-level rows carrying a typical-episode `duration` (P3.4).
- `search` results: `truncated: true` + `totalSize` when PMS capped the result set.
- `search` rejects `--min-score` with `BAD_REQUEST` (P3.2); `PLEXCTL_SEARCH_MIN_SCORE` env is ignored and warned about in `warnings`.
- `seek` success reports resulting `playState`.
- Deferred (candidate P3.7+, NOT in v2 scope): authoritative per-item `watched: bool` / per-show `unwatchedCount` synthesis; `search --id-type` GUID resolution (P3.6 stretch).

## 7. Versioning

This is plexctl v2.0.0. `--version` reports `2.0.0-dev` on this branch until release. The plex skill rewrite (P4) gates on `plexctl --version` >= 2.0.0 and ships in the same change set as the release merge.
