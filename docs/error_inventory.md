# Error Emission Site Inventory

Generated 2026-07-19 for error-model v2 migration (P0.1), branch `feature/error-model-v2`.

Scope: every call to `output.Fail`, `output.Usage`, `output.Out` under `internal/` and
`cmd/`, plus every error-envelope construction that bypasses those three functions
(direct `output.Print`+`output.Exit` pairs). Test files (`*_test.go`) are excluded from
the table — they exercise the output package itself, not a command path.

**Exit codes** (from `internal/output/output.go`): 0 success, 1 domain failure
(`Fail`, `Out` on falsy `ok`), 2 request timeout (`Out` when error has the
`"request timed out"` prefix), 64 usage/validation error (`Usage`).

## IMPORTANT — two bypass mechanisms outside output.Fail/Usage/Out

Before the per-site tables: the `output.Fail`/`Usage`/`Out` contract is **not** the
only place the CLI exits non-zero and prints an error envelope. Two other code paths
hand-construct `{"ok": false, "error": ...}` and call `output.Print` + `output.Exit`
directly, never going through `output.Fail`/`output.Usage`/`output.Out`:

### Bypass 1 — `internal/api/api.go:236-249` (`ExitOnError`)

```go
func ExitOnError(method, base, path string, params url.Values, prefix string) any {
	v, err := Request(method, base, path, params, 0)
	if err != nil {
		...
		output.Print(jsonx.J{"ok": false, "error": prefix + e.Message})
		if e.Kind == "timeout" { output.Exit(2) } else { output.Exit(1) }
		return jsonx.J{}
	}
	return v
}
```

Backs `api.Get`/`api.Post`/`api.Put`/`api.Delete`/`api.PlexTVGet` — the "print-and-exit"
PMS/plex.tv HTTP call variants (as opposed to `api.TryGet`/`TryPut`/`TryDelete`, which
return `(result, error)` for callers that recover). **This one function is the actual
failure point for the large majority of PMS-touching commands** — any transport error,
timeout, or HTTP 4xx/5xx on these calls exits the process before the calling
command's own `output.Out(...)` line ever evaluates truthiness. Confirmed callers (non-test):
`internal/clients/clients.go` (`/clients`, `/devices.json`), `internal/streams/streams.go`,
`internal/library/library.go` (search, sections, list, metadata, scrobble/unscrobble/rate),
`internal/playlists/playlists.go`, `internal/sessions/sessions.go` (now-playing, history,
continue-watching), `internal/queue/queue.go` (create, shuffle, unshuffle, remove),
`internal/collections/collections.go`. Practical effect: commands like `watch`,
`unwatched`, `rate`, `library sections`, `now-playing`, `history`, `continue-watching`
never actually deliver `"ok": false` through their `output.Out(...)` call — they either
exit 1/2 earlier inside `ExitOnError`, or always construct a hardcoded `"ok": true`.

### Bypass 2 — `internal/clients/clients.go` (`resolveIn` / `bailAmbiguous`)

Four inline `output.Print({"ok": false, ...}); output.Exit(1)` sites, none going through
`output.Fail`:
- `clients.go:145-151` `bailAmbiguous` — ambiguous client name (two active devices share
  a name, target given by name not machineIdentifier).
- `clients.go:167-171` — target matched by exact name/machineIdentifier but `active` is
  false (registered device, Plex app not open).
- `clients.go:186-190` — same "registered but not active" case via case-insensitive name
  match.
- `clients.go:195-197` — no match at all: `"client not found: %s"`.

Reached via `clients.Resolve(name)`, called from virtually every playback-touching
command: `play-media`, `queue`, `queue-start`, `queue-show/shuffle/unshuffle/clear/
remove/add`, `now-playing`, `context`, `play`, `pause`, `stop`, `next`, `prev`, `seek`,
`volume`, `watched`, `unwatched`, `rate`, `play-latest`.

These two bypasses mean roughly half of the 68 `output.Out(...)` call sites below never
actually see `"ok": false` in practice — the real failure/exit already happened one or
two frames down the stack, through a mechanism the v2 error model needs to unify with
`output.Fail`/`Usage`/`Out` (or explicitly document as a third, intentional lane).

---

## `internal/auth/auth.go` — `output.Fail` (11 sites), `output.Out` (2 sites)

All reachable only via `plexctl auth login` (`internal/commands/auth.go:30`, `auth.Login()`).

| # | Line | Message | Trigger condition |
|---|------|---------|--------------------|
| 1 | 148 | `err.Error()` (from `validatePMSURL`) | Entered PMS URL fails scheme/host/userinfo/fragment/query validation. |
| 2 | 181 | `"auth request failed: " + err.Error()` | `http.NewRequest` for the plex.tv sign-in POST fails to construct (malformed method/URL — effectively unreachable in practice). |
| 3 | 210 | `"auth failed: HTTP %d"` | plex.tv sign-in responds with status >= 400 (bad credentials, etc.). |
| 4 | 216 | `"plex.tv returned non-JSON response: %s"` | Sign-in response body doesn't parse as JSON. |
| 5 | 221 | `"unexpected auth response shape from plex.tv"` | Parsed JSON body is not a `map[string]any`. |
| 6 | 226 | `"unexpected auth response shape from plex.tv"` | Body parses but has no `"user"` object. |
| 7 | 231 | `"unexpected auth response shape from plex.tv"` | `user` object has no string `authToken`. |
| 8 | 238 | `"PMS unreachable at %s: %s"` | Building the PMS verification GET request fails. |
| 9 | 248 | `"PMS unreachable at %s: %s"` | PMS verification GET transport-fails (dial/timeout/connection). |
| 10 | 253 | `"PMS unreachable at %s: %d %s"` | PMS verification GET returns HTTP >= 400. |
| 11 | 270 | `"failed to write config at %s: %s"` | `config.Save` fails (filesystem error writing `config.toml`). |
| 12 | 197 (Out) | dynamic — `classifyAuthTransport(err)` | plex.tv sign-in POST transport-fails (dial timeout → "connection failed"; read timeout → "request timed out"; other → "auth request failed"). |
| 13 | 206 (Out) | dynamic — `classifyAuthTransport(err)` | Reading the sign-in response body fails (transport error mid-read). |

## `internal/config/config.go` — `output.Fail` (2 sites)

Reached by **every command that talks to PMS or plex.tv**, i.e. everything except
`auth login` itself — via `config.Load()`/`config.Require()`, called from
`internal/api/api.go` (`Request`, `DefaultTimeout`, `ServerBase`), `internal/clients/clients.go`
(`Resolve` → `config.Require("default_client")`), and `internal/playback/playback.go`.

| # | Line | Message | Trigger condition |
|---|------|---------|--------------------|
| 1 | 49 | `"invalid config at %s: %v — run plexctl auth login"` | `config.toml` exists but fails TOML parsing (hand-edited/corrupt file). |
| 2 | 89 | `"missing config key: %s — run plexctl auth login"` | A required key (`token`, or `default_client` when `-c/--client` omitted) is absent or falsy in the loaded config — i.e. never logged in, or config was hand-edited to remove it. |

## `internal/commands/root.go` — `output.Usage` (1 site)

| # | Line | Message | CLI command | Trigger condition |
|---|------|---------|-------------|--------------------|
| 1 | 68 | `err.Error()` | any/all (catch-all in `Execute()`) | Any `RunE` returns a non-nil error and cobra itself surfaces it: unknown command/flag, wrong arg count, the root `--timeout <= 0` guard (root.go:44), `choiceError` (helpers.go:32, `--type`/`--playlist-type` validators), `seek`'s hand-parsed flag errors (transport.go:148,158,173,191,194), `volume`'s range check (transport.go:210), `history --limit`/`context --history-limit` range checks (sessions.go:58,79), `playlist create --type` validator (playlists.go:89). |

## `internal/commands/library.go` — `output.Usage` (2), `output.Out` (9)

| # | Line | Message | CLI command | Trigger condition |
|---|------|---------|-------------|--------------------|
| 1 | 49 (Usage) | `"query cannot be empty"` | `search QUERY` | `QUERY` arg is empty/whitespace-only. |
| 2 | 250 (Usage) | `"show cannot be empty"` | `episodes SHOW` | `SHOW` arg is empty/whitespace-only. |
| 3 | 116 (Out) | `{"ok": true, "sections": ...}` | `library sections` | Never false — `library.Sections()` always returns a slice; a real PMS failure exits earlier via api.go Bypass 1. |
| 4 | 136 (Out) | `{"ok": true, "count", "items"}` | `library list` | Never false — same as above (`library.ListSection`). |
| 5 | 159 (Out) | `"no metadata found for ratingKey: %s"` | `metadata RATING_KEY` | `library.Metadata(ratingKey)` returns an empty map — PMS's `/library/metadata/<key>` responded but with no `Metadata` entries (unknown/stale ratingKey). |
| 6 | 162 (Out) | `{"ok": true, "metadata": item}` | `metadata RATING_KEY` | Success path. |
| 7 | 187 (Out) | `"no unwatched episodes for: %s"` | `play-latest QUERY --unwatched` | `library.LatestUnwatchedEpisode` finds no unwatched episode and `--unwatched` was passed (no movie fallback). |
| 8 | 192 (Out) | `"nothing found for: %s"` | `play-latest QUERY` | No show/episode match AND no movie match either. |
| 9 | 198 (Out) | `{"ok": true, ...}` (`--key-only`) | `play-latest QUERY --key-only` | Never false — resolution already succeeded by this point. |
| 10 | 221 (Out) | dynamic — `playback.PlayMedia(...)` result | `play-latest QUERY` | Companion play command fails: transport error, HTTP error, or (via `GetServerMachineID`) `"could not retrieve server machineIdentifier"`. |
| 11 | 298 (Out) | `{"ok": true, "episodes"/"results", ...}` | `episodes SHOW` | Never false — always constructed `ok: true`; empty result set adds a `note`, not a failure. |

## `internal/commands/collections.go` — `output.Out` (7)

All under `collection <subcommand>`.

| # | Line | CLI command | Trigger condition (delegated to `internal/collections/collections.go`) |
|---|------|-------------|--------------------------------------------------------------------------|
| 1 | 42 | `collection list` | Never false — `collections.ListAll` returns a plain slice. |
| 2 | 58 | `collection show RATING_KEY` | Never false — `collections.Show` returns a plain slice. |
| 3 | 73 | `collection create TITLE SECTION_ID RATING_KEYS...` | `collections.Create`: fails if 0 ratingKeys given ("create requires at least one ratingKey"); server machineIdentifier unavailable; `SECTION_ID` is not a movie/show section; PMS create call returns no metadata/no ratingKey. |
| 4 | 85 | `collection delete RATING_KEY` | `collections.Delete` — no explicit `ok:false` branch found; failure surfaces via api.go Bypass 1 on the underlying `api.Delete`. |
| 5 | 97 | `collection rename RATING_KEY NEW_TITLE` | `collections.Rename` — same as above (api.Put, Bypass 1). |
| 6 | 109 | `collection add COLLECTION_KEY RATING_KEYS...` | `collections.AddItems`: 0 ratingKeys ("add requires at least one ratingKey"); target is a smart collection (`smartRefusal`); server machineIdentifier unavailable; per-item add error (`err.Error()`, partial `"added"` count). |
| 7 | 121 | `collection remove COLLECTION_KEY ITEM_RATING_KEY` | `collections.RemoveItem`: smart-collection refusal (`smartRefusal`) is the only local `ok:false`; otherwise Bypass 1 via `api.Delete`. |

## `internal/commands/playlists.go` — `output.Out` (9)

All under `playlist <subcommand>`.

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 57 | `playlist list [--type]` | `playlists.ListAll` returns an error (e.g. underlying request error not routed through Bypass 1 — `err.Error()` passed through directly). |
| 2 | 60 | `playlist list [--type]` | Success path (`ok: true`). |
| 3 | 75 | `playlist show RATING_KEY` | Never false — `playlists.Show` returns a plain slice. |
| 4 | 91 | `playlist create TITLE RATING_KEYS...` | `playlists.Create`: 0 ratingKeys; invalid playlist type; server machineIdentifier unavailable; PMS create returns no metadata/no ratingKey. |
| 5 | 105 | `playlist delete RATING_KEY` | `playlists.Delete` — no local `ok:false`; failure via Bypass 1 (`api.Delete`). |
| 6 | 117 | `playlist rename RATING_KEY NEW_TITLE` | `playlists.Rename` — no local `ok:false`; failure via Bypass 1 (`api.Put`). |
| 7 | 129 | `playlist add PLAYLIST_KEY RATING_KEYS...` | `playlists.AddItems`: 0 ratingKeys; smart-playlist refusal; server machineIdentifier unavailable; per-item add error with partial `"added"` count. |
| 8 | 141 | `playlist remove PLAYLIST_KEY ITEM_ID` | `playlists.RemoveItem`: smart-playlist refusal is the only local `ok:false`; otherwise Bypass 1. |
| 9 | 153 | `playlist clear PLAYLIST_KEY` | `playlists.Clear`: smart-playlist refusal is the only local `ok:false`; otherwise Bypass 1. |

## `internal/commands/playmedia.go` — `output.Out` (1)

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 25 | `play-media RATING_KEY` | `playback.PlayMedia(...)`: `"could not retrieve server machineIdentifier"`, or Companion play command transport/HTTP failure (`playerCmd`). Client resolution itself can exit earlier via clients.go Bypass 2. |

## `internal/commands/queue.go` — `output.Out` (9)

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 53 | `queue RATING_KEYS...` | `queue.Create` failed (`q["ok"]` falsy): no server machineIdentifier; PMS create returned no `playQueueID`/no `playQueueSelectedItemID`; per-item `Add` failure mid-loop (with rollback attempted, `partialQueueID` set). |
| 2 | 131 | `queue RATING_KEYS...` | Post-create bind/engagement failure: Companion bind fails (`clientUnreachable` if transport-shaped); bind succeeds but `queue.ConfirmEngaged` fails ("device accepted the playback command but playback never started"); `stateSaved`/`staged` keys added but do not themselves flip `ok`. |
| 3 | 162 | `queue-start` | `queue.Start`: no saved/staged queue (`noActiveQueue`); bind failure (`clientUnreachable` flag via `AnnotateBind`); bind succeeds but engagement confirmation fails. |
| 4 | 176 | `queue-show` | `queue.Show`: PMS GET on the saved queue ID fails with non-404 status/transport error (404 degrades to `ok:true` empty state, not a failure). |
| 5 | 190 | `queue-shuffle` | `queue.Shuffle`: no active/resolvable saved queue (`noActiveQueue`). |
| 6 | 204 | `queue-unshuffle` | `queue.Unshuffle`: no active/resolvable saved queue. |
| 7 | 218 | `queue-clear` | `queue.Clear`: no active/resolvable saved queue; or clearing the state file itself fails after a 404/successful delete. |
| 8 | 232 | `queue-remove ITEM_ID` | `queue.RemoveItem`: no active/resolvable saved queue. |
| 9 | 246 | `queue-add RATING_KEYS...` | `queue.AddToClient`: 0 ratingKeys; no active queue; queue-size GET fails (non-404); per-key add fails; post-add size-verify GET fails; PMS accepts the PUT but queue size doesn't grow ("likely unknown or invalid" ratingKey). |

## `internal/commands/sessions.go` — `output.Out` (4)

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 32 | `now-playing` | Never false in practice — `sessions.NowPlaying` uses `api.Get` (Bypass 1); a real PMS failure exits there, not here. |
| 2 | 44 | `continue-watching` | Same — `sessions.ContinueWatching` uses `api.Get` (Bypass 1). |
| 3 | 60 | `history [--limit]` | Same — `sessions.History` uses `api.Get` (Bypass 1). |
| 4 | 81 | `context [--history-limit] [--no-history]` | `sessions.Context`'s top-level `ok` mirrors **only** the now-playing section's success; queue/history sections can independently be `ok:false` (embedded `failureSection`) without flipping the exit code — an inconsistency worth flagging for v2. |

## `internal/commands/streams.go` — `output.Usage` (1), `output.Out` (14)

| # | Line | Message | CLI command | Trigger condition |
|---|------|---------|-------------|--------------------|
| 1 | 46 (Usage) | `"show cannot be empty"` | `audit-audio SHOW` | `SHOW` arg empty/whitespace. |
| 2 | 77 (Out) | `{"ok": true, "audit": rows}` | `audit-audio SHOW` | Never false — always constructed `ok:true`. |
| 3 | 120 (Out) | `"--show and --show-rating-key are mutually exclusive"` | `set-audio` | Both `--show` and `--show-rating-key` passed. |
| 4 | 124 (Out) | `"provide RATING_KEY (single) or --show/--show-rating-key (bulk), not both"` | `set-audio` | Positional `RATING_KEY` given together with `--show`/`--show-rating-key`. |
| 5 | 128 (Out) | `"provide RATING_KEY (single) or --show/--show-rating-key (bulk)"` | `set-audio` | Neither `RATING_KEY` nor `--show`/`--show-rating-key` given. |
| 6 | 134 (Out) | `"--stream-id is single-item only; not valid with --show"` | `set-audio --show ... --stream-id` | `--stream-id` combined with bulk mode. |
| 7 | 149 (Out) | dynamic — `bulkSetAudio(...)` | `set-audio --show/--show-rating-key` | Ambiguous show match; no show match; episode set spans >1 season without `--all-seasons`; `results` partial-failure (`"ok": failed == 0`) from `streams.ExecuteBulkAudio`. |
| 8 | 155 (Out) | `"--language and --stream-id are mutually exclusive"` | `set-audio RATING_KEY` | Both `--language` and `--stream-id` set on single-item form. |
| 9 | 159 (Out) | dynamic — `streams.SetAudioStream(ratingKey, "", &streamID)` | `set-audio RATING_KEY --stream-id N` | `streams.go` (package): no metadata for ratingKey; no media part on ratingKey; no matching audio track for the given stream id. |
| 10 | 166 (Out) | dynamic — `streams.SetAudioStream(ratingKey, lang, nil)` | `set-audio RATING_KEY [--language]` | Same family: no metadata; no audio track matching the language. |
| 11 | 299 (Out) | `"--off and --language/--stream-id are mutually exclusive"` | `set-subtitle RATING_KEY --off` | `--off` combined with `--language`/`--stream-id`. |
| 12 | 303 (Out) | `"--language and --stream-id are mutually exclusive"` | `set-subtitle RATING_KEY` | Both `--language` and `--stream-id` set. |
| 13 | 307 (Out) | dynamic — `streams.SetSubtitleStream(ratingKey, "", nil, true)` | `set-subtitle RATING_KEY --off` | No metadata for ratingKey. |
| 14 | 311 (Out) | dynamic — `streams.SetSubtitleStream(ratingKey, "", &streamID, false)` | `set-subtitle RATING_KEY --stream-id N` | No metadata; no matching subtitle track for stream id. |
| 15 | 318 (Out) | dynamic — `streams.SetSubtitleStream(ratingKey, lang, nil, false)` | `set-subtitle RATING_KEY [--language]` | No metadata; no matching subtitle track for language. |

**Note:** sites 3, 4, 5, 6, 11, 12 are hand-rolled invocation/flag validation errors — the
same *kind* of error `library.go`'s `"query cannot be empty"`/`"show cannot be empty"`
route through `output.Usage` (exit 64) — but here they go through `output.Out` (exit 1)
instead. Inconsistent exit code for the same error class; flag for v2.

## `internal/commands/transport.go` — `output.Out` (7)

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 38 | `play` | `playback.Play` (`playerCmd`): Companion transport error (timeout/connection failed/request failed) or HTTP >= 400 from the client. |
| 2 | 52 | `pause` | Same, `/player/playback/pause`. |
| 3 | 66 | `stop` | Same, `/player/playback/stop`. |
| 4 | 80 | `next` | Same, `/player/playback/stepForward`. |
| 5 | 94 | `prev` | Same, `/player/playback/stepBack`. |
| 6 | 176 | `seek POSITION` | `playback.Seek`: unrecognised position format; seconds/minutes out of range; no current session/viewOffset for a relative seek; pre-seek resume fails; seek itself fails (transport/HTTP); post-seek re-pause fails. |
| 7 | 212 | `volume LEVEL` | `playback.SetVolume` (`playerCmd`): Companion transport/HTTP failure. |

## `internal/commands/watch.go` — `output.Out` (6)

| # | Line | CLI command | Trigger condition |
|---|------|-------------|--------------------|
| 1 | 56 | `watched [RATING_KEY]` | No explicit `RATING_KEY` and no current session on the resolved client (`resolveTargetKey` fails) — `"nothing playing — provide a ratingKey"`. |
| 2 | 59 | `watched [RATING_KEY]` | Never false — `library.Scrobble` always returns `ok:true` (uses `api.Get`, Bypass 1, for the actual failure). |
| 3 | 80 | `unwatched [RATING_KEY]` | Same as #1, for unwatched. |
| 4 | 83 | `unwatched [RATING_KEY]` | Never false — `library.Unscrobble`, same as #2. |
| 5 | 108 | `rate RATING [RATING_KEY]` | Same as #1, for rate. |
| 6 | 111 | `rate RATING [RATING_KEY]` | Never false — `library.Rate`, same as #2. |

---

## Acceptance check

```
grep -rn "output.Fail(" internal/ cmd/ | wc -l   → 13
grep -rn "output.Usage(" internal/ cmd/ | wc -l  →  4
```

Table rows: `internal/auth/auth.go` Fail = 11, `internal/config/config.go` Fail = 2 → **13
total Fail rows**, matching the grep count exactly once each. `internal/commands/root.go`
Usage = 1, `internal/commands/library.go` Usage = 2, `internal/commands/streams.go`
Usage = 1 → **4 total Usage rows**, matching the grep count exactly once each.

`output.Out` non-test call sites: `grep -rn "output.Out(" internal/ cmd/ | grep -v _test.go | wc -l` → 68.
Table rows: auth 2 + collections 7 + library 9 + playlists 9 + playmedia 1 + queue 9 +
sessions 4 + streams 14 + transport 7 + watch 6 = **68**, matching exactly.
