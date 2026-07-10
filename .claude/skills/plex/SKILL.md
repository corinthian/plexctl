---
name: plex
description: >
  Plex Media Server control via plexctl CLI. TRIGGER when: user wants to
  find, play, queue, or rate movies and TV episodes, control playback,
  check what's on, or says "/plex". Parses voice/text intent into plexctl
  commands. Hides verbose plexctl internals — translates errors to plain
  English. Default client: Apple TV.
argument-hint: "[voice phrase | command | query]"
allowed-tools:
  - "Bash(plexctl:*)"
  - "Bash(jq:*)"
---

# Plex Skill

Goal: smooth find / watch / play UX. Hide plexctl noise. Translate raw errors to plain English. Never surface internal IDs, partial-failure dicts, raw HTTP bodies, or rollback flags unless `debug_mode`.

---

## Hard Rules

- **Never run `curl` (or python/requests, or any direct HTTP) against the Plex API.** Stay inside `plexctl`. If a task seems to need a raw call to any Plex endpoint, that's a plexctl gap — surface it to the user as a bug ("plexctl can't do X — that's a missing feature, worth reporting") and stop. Do not work around it.
- **Don't invent plexctl limitations.** Never refuse a user-requested command because you "know" it'll return empty from a pattern noticed earlier. An `ok:true` empty result isn't proof of a missing feature — collections/searches can genuinely be empty or broken. Run it, report plainly, let the user disambiguate. Treat a gap as real only if it's documented here or the user confirms it.
- **Confirm before any bulk write.** `set-audio --show` / `--show-rating-key` changes selected audio across many episodes of real account state. Always run `--dry-run` first, show the user the plan (show + season breakdown + change count), and get an explicit go-ahead before the real write. The CLI does not prompt — this gate is yours.

---

## Debug Mode

Triggered when `$ARGUMENTS` contains:
- leading `debug` token (`debug search Rocky`), OR
- `--debug` anywhere

Strip trigger before parsing. In debug mode:
1. Echo exact shell command(s) in fenced code block **before** output.
2. Restore internal columns to tables (`ratingKey`, `playQueueItemID`, `machineIdentifier`).
3. Show raw `error` field verbatim instead of translating.

Default mode: hide all internal IDs. Use row numbers.

---

## Row-Number Pattern

Any list containing `ratingKey` or `playQueueItemID` gets a leading `#` column (`1, 2, 3, ...`). Keep `#`→ID mapping in conversation context for follow-ups ("play #2", "remove #3"). Never display IDs in default mode.

Debug: append ID column on the right.

---

## Clients

| Client | Controllable |
|---|---|
| Apple TV | Yes (default — omit `--client`) |
| iPad | No |
| Plex for Mac | No |
| Plex Web | No |

If user asks for non-controllable client: "<x> isn't controllable from here. Use Apple TV instead?"

---

## List Formatting Rules

There are exactly **two table formats**. Don't invent variants.

### Format 1 — Title table (movies + episodes)

Every list of individual movies/episodes uses this one shape:

| # | Title | Detail | Runtime | <Context> |
|---|---|---|---|---|
| 1 | Rocky (2021) | movie | 2h 35m | … |
| 2 | Stranger Things | S05E01 — Chapter One | 1h 12m | … |

Total: 3h 47m

- **Title** — movie name + year `Rocky (2021)`, or the show name alone `Stranger Things` (no S/E here).
- **Detail** — `movie` for a movie; the episode locator `S0xE0y — Episode Title` for an episode.
- **Runtime** — always, on every row. No row-count cutoff.
- **Context** — the single right-most column, swapped per command (label varies): `Description` (search), `Selected` (queue-show), `Viewed` (history), `Progress` (continue-watching), `Watched` (library / collection show / playlist show).
- **Total** — always end with a `Total:` line on its own row beneath the table, summing every row's runtime in `Nh Mm`.
- Debug: append the ID column (`ratingKey` / `playQueueItemID`) on the right.

### Format 2 — Container table (collection list, playlist list)

Lists of collections/playlists — not individual titles. Keeps its own columns (`Items`, `Smart`, etc.), no per-row Runtime, no Total line. Library `sections` is a trivial `# | Title | Type` list — leave it.

**Runtime source** (fetch only when not already present):
- `search --json` / `continue-watching` / `playlist list` → `duration` is free in the result.
- `play-latest` result → fetch `metadata <ratingKey>` once for summary; pull `duration` same call.
- `library list` / `queue-show` / `history` / `collection show` / `playlist show` → `duration` is now returned **inline** by the command itself. Use it directly. Do **not** fetch `metadata` per row, and never loop `plexctl metadata` over rows — it's unnecessary. For `history` / `collection show` rows whose item was deleted from the library, `duration` may be `null`; render that row's runtime blank (it can't be recovered).

**Runtime format**: `duration` in ms → hours = ms // 3600000, minutes = (ms // 60000) % 60. Render `1h 32m` or `47m`. `Total:` sums raw ms then formats the same way.

**Movie title**: always append year — `Title (Year)`.
**now-playing line** (not a table): plain `Episode Title`; Show / S/E on separate lines (see Now Playing).

---

## Error Translation

Default mode: never show raw plexctl `error` strings. Translate per table. Debug mode: show raw verbatim.

Transport-shaped errors (`request timed out:` / `connection failed:` / `request failed:`) carry the target URL — route on it. URL contains `/player/` or port `32500` → the Apple TV client. Port `32400` → the Plex server. Error starts with `plex.tv ` → the cloud, not your server. Inspect the raw error to route it even in default mode; still never show it to the user.

| plexctl error contains | Show user |
|---|---|
| `nothing playing — provide a ratingKey` | `Nothing playing — give me a row number.` |
| `nothing found for: '<q>'` | `Nothing found for "<q>".` |
| `no unwatched episodes for: '<q>'` | `All caught up on "<q>".` |
| `client not found: <x>` | `Don't know any client called "<x>".` |
| `is registered but not active — open the Plex app` | `Plex isn't seeing <x> right now. If the app is already open, quit and relaunch it.` |
| `ambiguous client name` | `Two clients named "<x>". Need to pick one — say "list clients" to see them.` |
| transport error on a `/player/`/`:32500` URL | `Apple TV isn't responding. If Plex is open on it, relaunch the app.` Do NOT blind-retry device commands — see Queue Create. |
| error starts `plex.tv ` | `plex.tv isn't reachable — your Plex server is fine. Likely an internet blip; try again shortly.` |
| `query cannot be empty` | `Need something to search for.` |
| `show cannot be empty` | `Which show?` |
| `ambiguous show` | `Several shows match "<x>" — which one? (I'll pick by its ID.)` |
| `spans N seasons` | `That's <N> seasons. Want all of them, or just one season?` |
| `no <lang> audio track on` / `no <lang> subtitle` | `No <lang> track on that one.` |
| `--stream-id is single-item only` / `mutually exclusive` / `provide RATING_KEY` | (internal arg error — retry the command correctly; don't surface raw) |
| `could not determine current playback position` | `Nothing playing — can't seek relative.` |
| `could not resume before seek:` | `Couldn't resume to seek. Try again.` |
| `seeked but failed to restore pause state:` | `Seeked, but couldn't re-pause.` |
| `unrecognised position format:` | `Don't recognise that time. Try "1:30" or "+30s".` |
| `invalid seek position:` | `Bad seek time.` |
| `playQueue creation returned no playQueueID` | `Couldn't create queue.` |
| Error dict has `partialQueueID` / `rollbackAttempted` | `Couldn't queue everything. Nothing added.` |
| Result has `staged: true` (a `queue` bind failure) | Recoverable bind failure — see Queue Create: report "made the queue, say 'start the queue' once the Apple TV is awake" and recover with `queue-start`. |
| Result has `clientUnreachable: true` | Device didn't answer (asleep/unreachable), vs an HTTP-status bind error (reachable, rejected). Route the wording on this flag, not just the URL. |
| `no active queue on <client>` from `queue-start` | `Nothing staged to start — queue something first.` |
| `HTTP 404` (raw body included) | `Plex couldn't find that.` |
| `connection failed:` / `request failed:` | `Can't reach Plex right now.` |
| `request timed out:` | `Plex was slow to answer — trying again may work.` (PMS/`:32400` URLs only — client timeouts route to the Apple TV row above. Exit code 2; on a batch, retry just the timed-out items.) |
| `missing config key:` | `plexctl not set up. Run \`plexctl auth login\`.` |
| `smart collection: contents are query-driven` | `That's a smart collection — edit the search rule in the Plex app.` |
| `smart playlist: contents are query-driven` | `That's a smart playlist — edit the search rule in the Plex app.` |
| `section <ID> is not a movie or show section` | `Can't make a collection there — only movies and TV.` |
| Anything else `ok:false` | `Something went wrong. Run with \`debug\` for details.` |

---

## Refused Upfront (Don't Run)

Not supported — refuse without invoking plexctl:

| Intent | Response |
|---|---|
| shuffle queue / `queue-shuffle` | `Shuffle isn't supported. Shuffle from the Plex app UI.` |
| unshuffle queue / `queue-unshuffle` | `Unshuffle isn't supported. Use the Plex app UI.` |
| set volume / volume up / volume down | `Volume isn't supported — use the Apple TV remote or your TV.` |

In debug mode, run anyway and surface raw 404 (shuffle) / `ok:true` no-op (volume).

---

## Command Reference

### Transport

| Intent | Command |
|---|---|
| pause / pause it | `plexctl pause` |
| play / resume / unpause | `plexctl play` |
| stop | `plexctl stop` |
| next / skip / next episode | `plexctl next` |
| previous / back | `plexctl prev` |

Default output: one-line confirmation (`Paused.`, `Playing.`, `Stopped.`, `Skipped.`, `Back.`).
Trust `ok:true` — no post-verify (Apple TV silently accepts no-ops; not worth extra HTTP). **Exception: bootstrapping playback from idle — see below.**

**Back-to-back device commands don't collide.** plexctl seeds its Companion commandID from a flock-protected, persisted monotonic counter (`~/.config/plexctl/commandid`) rather than the wall-clock second, so two invocations issued within the same second still get strictly increasing IDs — no collision, no dropped command, no post-verify needed here.

Debug: confirmation + echoed command.

**Starting from idle — `play` won't work.** `plexctl play` only *resumes* a paused client; it can't start a loaded-but-idle queue (Apple TV accepts it as a silent no-op and still returns `ok:true`). When the user says "play" / "start" / "play that" / "go" with a queue present but `now-playing` is `idle`, skip `plexctl play` — run `plexctl play-media <ratingKey>` against the queue's `selectedItemID`-mapped ratingKey instead, then verify `now-playing` flips to `playing` before reporting success.

### Seek

| Intent | Command |
|---|---|
| `seek to 1:30` / `go to 1 minute 30` | `plexctl seek 1:30` |
| `skip ahead 30 seconds` / `forward 30` | `plexctl seek +30s` |
| `back 10 seconds` / `rewind 10` | `plexctl seek -10s` |

Parse natural language: `two minutes` → `2:00`, `a minute thirty` → `1:30`.
Output: `Seeked to mm:ss.` Debug: + echoed command.

**Verify play state after a seek.** plexctl only touches play state when the client was already paused: it resumes, seeks, and re-pauses, so paused state is restored. A playing client gets a bare seek — if it ends up paused afterwards, that's Apple TV behavior, not plexctl. Procedure unchanged: if the media was playing before the seek, check `now-playing` and follow with `plexctl play` if it stopped; if it was paused, leave it paused.

### Library Search

```
plexctl search --json "QUERY" [--type show|movie|episode]
```

Always `--json` — needed for `summary` and `duration`. Don't show flag to user.

**Default table** (Format 1; runtime on every row, Total line at end):

| # | Title | Detail | Runtime | Description |
|---|---|---|---|---|
| 1 | Rocky (2021) | movie | 2h 35m | A noble family becomes embroiled in a war for control over the galaxy's most valuable asset. |
| 2 | Stranger Things | S05E01 — Chapter One | 1h 12m | First sentence of summary. |

Total: 3h 47m

- Title carries the movie year, or the show name alone — the Detail column holds `movie` or `S0xE0y — Episode Title`.
- Description: first sentence of `summary`, ≤120 chars, `…` if cut. Omit column if all rows lack summary.
- Empty result → `Nothing found for "<q>".`
- Niche show with empty result → silently retry once with `--min-score 0` before reporting empty. **`--min-score` is a `search` flag only** — never append it to `play-latest`, `episodes`, or any non-`search` command. They reject it.
- **`search` caps at ~10 results.** For *whole-show* episode lists (e.g. "show me 30 unwatched"), don't lean on `search` — use `episodes "<show>"` (below), which enumerates the full show. `search` is for finding a title; `episodes` is for listing a show's episodes. Don't curl and don't iteratively mark-watched/restore to enumerate (both still banned).

Debug: append `ratingKey` column.

User says `tell me more about #N` / `full description` → return full `summary` + `tagline` if present. No editorialising.

### Episodes (per-show enumeration)

```
plexctl episodes "SHOW" [--season N] [--unwatched] [--json]
```

Lists every episode of one show, ordered by `(season, episode)`. Use this — not `search` — whenever the user wants a show's episode list ("list every episode", "what's unwatched in X", "show me season 4"). Resolves the show by best match, same as `play-latest`.

- `--season N` restricts to one season; `--unwatched` drops watched episodes.
- Render as Format 1 (Title table). Detail column = `S0xE0y — Episode Title`.
- The result echoes `show` + `showRatingKey` — the series the fuzzy query bound to. If it isn't the show the user named, flag it rather than rendering the wrong series.
- Empty result → `ok:true` with `count:0` and a note; render `No episodes found for "<show>".` (or `All caught up on "<show>".` when `--unwatched` returned nothing).
- Debug: append `ratingKey` column.

### Audio Audit

```
plexctl audit-audio "SHOW" [--language eng] [--season N]
```

Reports, per episode, the **default** audio track's language and the **selected** (this-user) track — they can differ — plus whether the default is the preferred `--language` and whether a preferred-language alternate exists. Use it to answer "what audio is this show set to?" / "is anything not in English?".

- `--language` is the preferred code (default `eng`); the *user's* standing preference lives as a lesson — pass `--language` per request, adapting per show (anime → `jpn`, a German film → `deu`).
- Row fields: `defaultAudioCode`, `selectedAudioCode`, `isPreferredDefault`, `hasEnglishAlt`. Surface episodes where `isPreferredDefault` is false.
- The result echoes `show` + `showRatingKey` — the series the fuzzy query actually bound to. If it doesn't match what the user named, say so instead of presenting the wrong show's audit.
- This is a read-only audit — it changes nothing. (Setting tracks is a separate write command.)

### Bulk Sweeps (library-wide audits)

Looping a command over many shows (e.g. auditing audio across the whole library) is supported and fast — one `audit-audio` call per show, ~0.5s each. Rules:

- Enumerate shows once via `library list`, then loop `audit-audio "<show>"` (or better, by ratingKey-resolved title). Never loop per-episode `metadata` — `audit-audio` already returns every episode.
- Every call has a built-in 10s HTTP timeout — a stalled call errors out, it does not hang. Exit code 2 means "timed out": retry just those items once, then report them as skipped. No external watchdog, no `pkill` — plexctl runs one process per call and leaves nothing behind.
- Write intermediate results to the session scratchpad directory, not ad-hoc paths.
- For kill-safe partial progress on big sweeps, use `--ndjson` (on `episodes` and `audit-audio`): one JSON line per episode as produced, then a summary line — a killed run keeps everything already printed.

### Set Audio / Subtitle Track (write)

**Single item:**
```
plexctl set-audio RATING_KEY [--language eng | --stream-id N]
plexctl set-subtitle RATING_KEY [--language eng | --stream-id N | --off]
```

Selects the track *for this user* (not the file's `default`). `--language` and `--stream-id` are mutually exclusive; `--off` disables subtitles. Single-item runs immediately — no confirm. After success, `ok:true` with the `partId` / stream id set.

**Intent mapping:** treat "default" / "automatic" / "regular" / "normal" audio (or subtitle) requests as the **selected** track — that's the only knob plexctl can set; the file's embedded `default` disposition isn't writable. Just say you're setting the track that plays; don't explain the selected-vs-embedded-default distinction unless the user is specifically auditing the default flag (`audit-audio`). Still read intent for a specific language (anime → `jpn`, a German film → `deu`) and pass `--language`.

**Bulk (whole show):**
```
plexctl set-audio (--show "SHOW" | --show-rating-key KEY) --language eng \
        [--season N | --all-seasons] [--only-non-eng] [--dry-run]
```

- **Always `--dry-run` first.** The dry-run returns the resolved show + ratingKey, a per-season episode breakdown (`seasons: {"4": 15, "5": 16}`), and per-episode `from → to` codes — and writes nothing.
- **Confirm before the real write.** Bulk flips selected audio across many episodes of real account state. Show the user the dry-run plan (show title, season breakdown, how many will change) and get an explicit go-ahead before running without `--dry-run`. This confirm is the skill's job — the CLI itself does not prompt.
- **Ambiguous show → it refuses** (`ambiguous show … — N series match`). Disambiguate with `--show-rating-key KEY` (get the key from `search`).
- **Multi-season → needs `--all-seasons`** (or narrow with `--season N`). A bare multi-season `--show` is refused with `spans N seasons …` so the blast radius is never silent.
- `--only-non-eng` skips episodes already on the preferred language — safe to re-run (idempotent).
- Per-episode result carries `status` (`ok` / `skipped` / `error`); a single episode's failure doesn't abort the rest. Report applied / skipped / failed counts.

Preferred language is per-request, not a stored default — pass `--language` to match the content (English for most, `jpn` for anime, `deu` for a German film). The user's standing preference lives as a lesson, not a config key.

### Play Latest / Next Episode (default = strict unwatched)

```
plexctl play-latest --unwatched "SHOW" [--client "Name"]
```

Always pass `--unwatched`. If caught up: show `All caught up on "<show>".` Don't fall back to last-aired.

**"First" means first *unwatched*.** "First episode", "the first X", "first show" → treat identically to "next" — run `play-latest --unwatched`. Never interpret as S01E01 unless the user explicitly says "season 1 episode 1" or "the pilot". If a specific episode isn't found and the series name is confirmed, assume it's already watched — don't report it as missing from the server.

Show fallback to movie still happens (plexctl handles internally — no show match = movie search).

After success, fetch `plexctl metadata <ratingKey>` once for `summary` + `duration`.

**Output (episode)**:
```
Now playing: Stranger Things — S05E01 — Chapter One: The Crawl  (1h 12m)
Description: First sentence of summary.
```

**Output (movie)**:
```
Now playing: Rocky (2021)  (2h 35m)
Description: First sentence of summary.
```

Debug: same + `ratingKey:` line + echoed commands.

### Resolve Without Playing (`--key-only`)

```
plexctl play-latest --unwatched --key-only "SHOW"
```

Used internally to gather keys for queueing. Don't surface to user.

### Play by ratingKey

```
plexctl play-media RATING_KEY [--client "Name"]
```

Used when user picks `#N` from a list. Resolve `N` against most recent row-map. After play, fetch `metadata` for summary + duration. Output same shape as `play-latest`.

### Queue Create

```
plexctl queue KEY1 KEY2 ... [--client "Name"]
```

Drop `--shuffle` (not supported on PMS 1.43). `--repeat` allowed if user asks.

To queue multiple episodes: `play-latest --unwatched --key-only` per show, or pick `#N` rows from prior search/list.

Output: `Queue created — N items.`
On `partialQueueID` / `rollbackAttempted`: `Couldn't queue everything. Nothing added.`
Debug: + `playQueueID` + `selectedItemID` + echoed command.

**`queue` auto-starts playback.** Creating a queue begins playing item 1 on the client. Per the queue-then-pause rule, immediately follow with `plexctl pause` unless the user asked to start watching now. The resulting steady state is `paused` on item 1 at a low viewOffset — this is *your own doing*, expected, and must NOT be reported as "something started it" on a later status check. External state changes are normal. The user may make changes using the Plex UI directly, which you may see reflected as external changes. Accept that quietly and move on.

**Two failure shapes — resolve vs bind.** `queue` now resolves the client *before* creating the server-side playQueue. An inactive/unresolvable client errors before anything is created — no orphan; use the "registered but not active" translation. Only a *bind* failure (create succeeded, the playMedia to the client failed) leaves a server-side queue — and plexctl now records it so it's recoverable without rebuilding.

**`queue` bind failure ≠ nothing happened — read the result to pick recovery.** The result carries `playQueueID`/`selectedItemID`, and:
- **`staged: true`** → the queue was recorded and is recoverable. Report: `Made the queue, but the Apple TV didn't respond. Once it's awake, say "start the queue" and I'll bind it — no need to rebuild it.` Recovery is `queue-start`, NOT re-running `queue` (the queue is already staged — a retry just makes a second server-side queue for no gain).
- **bind failed with NO `staged` key** → a queue was already recorded for this client (the active one is preserved), so the new one couldn't be staged and `queue-start` would start the OLD queue. Report: `Made the queue, but one's already active and the Apple TV isn't responding — I couldn't stage the new one. Ask me to queue it again once it's back.` Recovery is re-running `queue`.
- **`clientUnreachable: true`** means the device didn't answer (asleep/unreachable), as opposed to an HTTP-status bind error (reachable but rejected). Prefer this flag over URL-sniffing to choose the wording. Retry only after the user says the device is back.

Distinct from a `partialQueueID` / `rollbackAttempted` failure (creation itself failed mid-add) — that keeps its `Couldn't queue everything. Nothing added.` line.

### Queue Recovery (`queue-start`)

```
plexctl queue-start [--client "Name"]
```

Binds the last saved/staged play queue to the client and starts it — the recovery step after a bind failure that reported `staged: true`, once the device is back. It does NOT rebuild the queue (no new server-side queue); it re-binds the existing one.

| Intent | Command |
|---|---|
| start the queue / start it now / try the queue again / bind the queue | `plexctl queue-start` |

Output:
- success → `Started — playing on Apple TV.` then apply the queue-then-pause rule (immediately `plexctl pause` unless the user asked to watch now), same as `queue`.
- `clientUnreachable: true` → `Still not responding. Try again once Plex is open on the Apple TV.` Retry only when the user says it's back.
- `no active queue on <client>` → `Nothing staged to start — queue something first.`

### Queue Inspect / Manage

| Intent | Command |
|---|---|
| show queue / what's queued | `plexctl queue-show` |
| clear queue | `plexctl queue-clear` |
| remove item N | `plexctl queue-remove ITEM_ID` (resolved from row-map) |
| add #N to queue / queue #N too / also queue #N | `plexctl queue-add RATING_KEY` (resolved from row-map) |
| add Rocky to the queue | search → resolve → `plexctl queue-add RATING_KEY` |
| add these to the queue | `plexctl queue-add K1 K2 K3` |
| shuffle queue | **refused upfront** |
| unshuffle queue | **refused upfront** |

`queue-add` appends to the active queue without creating a new one and
without disturbing playback. Output: `Added N to the queue.` On
`"no active queue on <client>"` error → `Nothing's queued yet — queue
something first.` On partial-success (`ok: false` with `added: N > 0`):
`Added the first N, then ran into an issue. Try again with the rest.`

**queue-show table** (Format 1; runtime on every row, Total line at end):

| # | Title | Detail | Runtime | Selected |
|---|---|---|---|---|
| 1 | Stranger Things | S05E01 — Chapter One | 1h 12m | ✓ |

Total: 1h 12m

Empty queue → result is `{"ok": true, "state": "empty", "client": "...", "items": []}` (exit 0). Render `No queue right now.`

`queue-show` reads plexctl's local record of the queue, then fetches it from the server. Caveats. "Empty" means plexctl has no record. A bind failure that reported `staged: true` DOES leave a record, so `queue-show` will display that staged queue (recoverable via `queue-start`) — only the dead-zone bind failure (no `staged`) leaves a new server queue with no record. An `HTTP 404` from `queue-show` means the remembered queue no longer resolves — render `No queue right now.` exactly like empty, not "Plex couldn't find that"; likewise `queue-add` 404 → `Nothing's queued yet — queue something first.` plexctl no longer deletes the record on a 404, so a genuinely pruned queue keeps reading empty until the next `queue` create replaces it — a lingering-empty `queue-show` is expected, not a bug.

`queue-show` returns `duration` inline — use it directly; no per-row `metadata` fetch.

Row `#` maps to `playQueueItemID`. Debug: append column.

`queue-clear` / `queue-remove` output: `Cleared.` / `Removed.` No confirmation prompt — run immediately.

### Now Playing

```
plexctl now-playing [--client "Name"]
```

Fetch `metadata <ratingKey>` for `summary` (duration already in result).

**Output (episode)**:
```
State:       Playing
Title:       Chapter One: The Crawl
Show:        Stranger Things — S05E01
Progress:    2:36 / 1:11:28
Description: First sentence of summary.
```

**Output (movie)**: omit `Show:` line. Title becomes `Rocky (2021)`.

Idle → result is `{"ok": true, "state": "idle", "client": "..."}` (exit 0). Render `Nothing playing.`

Debug: + `ratingKey:` line + echoed commands.

### Watch Status & Rating

`RATING_KEY` optional — auto-targets currently playing.

| Intent | Command |
|---|---|
| mark watched | `plexctl watched [RATING_KEY]` |
| mark unwatched / unmark | `plexctl unwatched [RATING_KEY]` |
| rate this N | `plexctl rate N [RATING_KEY]` |
| unrate | `plexctl rate 0 [RATING_KEY]` |

Scale: 0–10. Output: `Marked watched.` / `Marked unwatched.` / `Rated N/10.` / `Unrated.`
No confirmation — run immediately.
Idle + no key: `Nothing playing — give me a row number.`

**Watched target in the active queue → offer to remove it.** When a `watched` call resolves to an item currently in the queue, the user almost always also wants it gone — leaving it forces a second request. Check `queue-show` (fresh); if the marked ratingKey maps to a queue row, mark it watched, then *offer* removal (`Want it out of the queue too?`). Don't auto-remove silently. If the watched target isn't in the queue, no offer needed.

### History

```
plexctl history --limit 10
```

Default limit 10. Bigger if user asks.

**Default table** (Format 1; `duration` returned inline — use it; Total line at end):

| # | Title | Detail | Runtime | Viewed |
|---|---|---|---|---|
| 1 | Stranger Things | S05E01 — Chapter One | 1h 12m | 2026-04-18 |
| 2 | Rocky (2021) | movie | 2h 35m | 2026-04-17 |

Total: 3h 47m

Row `#` maps to `ratingKey`. Debug: append column.

### Context (Startup Bundle)

```
plexctl context [--client "Name"] [--history-limit N] [--no-history]
```

Single parallel-fetched JSON bundle of `nowPlaying` + `queue` + `history`. Use at turn start per the Startup Recall policy. Do NOT render directly to the user — it's internal state for answering questions and shaping next commands. If the user explicitly asks for a summary, render each section using the existing formatters (`now-playing` / `queue-show` / `history`).

`--history-limit` caps at 10 (default 5). `--no-history` skips the history fan-out (~30% faster).

**A "status" / "what's going on" request is a fresh three-query render, NOT the cached startup bundle.** Re-run `now-playing` + `queue-show` + `history --limit 5` fresh every time (the turn-start `context` snapshot drifts — don't reuse it). Render now-playing and the queue table as usual, and **cross-reference history against the queue to mark watched rows**: any queue item whose title appears in recent `history` gets a ✓ in an added `Watched` column (queue-show rows carry no ratingKey — match by title against `history[].title`). A played-but-still-queued item then reads as watched at a glance, not just in prose. Ground every watched/not-watched statement in the history result (per the Execution Policy watched rule).

### Continue Watching

```
plexctl continue-watching
```

`duration` already in result — runtime free. Show it on every row + Total line.

| # | Title | Detail | Runtime | Progress |
|---|---|---|---|---|
| 1 | Stranger Things | S05E04 — Chapter Four | 48m | 32:10 |

Total: 48m

Row `#` maps to `ratingKey`. Debug: append column.

### Clients

```
plexctl clients
```

| Name | Product | Active | Last Seen |
|---|---|---|---|
| Apple TV | Plex for Apple TV | Yes | 2026-04-18 |

Debug: + `machineIdentifier`, `baseurl`.

### Library Browse

```
plexctl library sections
plexctl library list --section ID [--type show|movie] [--unwatched] [--sort FIELD:dir]
```

**Sections table**:

| # | Title | Type |
|---|---|---|
| 1 | Movies | movie |

Row `#` → section `key`. Debug: append `key`.

**List table** (Format 1; runtime on every row from inline `duration` — Total line at end):

| # | Title | Detail | Runtime | Watched |
|---|---|---|---|---|
| 1 | Rocky (2021) | movie | 2h 35m | Yes |
| 2 | The Simpsons | show | 45m | 4 left |

Total: 3h 20m

Row `#` → `ratingKey`. Debug: append column.

**Watched column semantics differ by row type.** Movies: `viewCount > 0` → Yes/No. Shows: use `unwatchedLeaves` (returned inline) — `0` renders `Yes`, otherwise `N left`. Never derive a show's watched state from its `viewCount`: that field is a play-history counter, not watch state (a show can have viewCount 38 with nothing watched, or viewCount 0 fully watched).

`--unwatched` on show listings is filtered by plexctl on episode leaf counts (a show appears iff it has unwatched episodes) — it does NOT use PMS's server-side filter, which is wrong in both directions. Trust this list; per-episode truth is `episodes "<show>" --unwatched`.

### Collections

```
plexctl collection list [--section ID]
plexctl collection show RATING_KEY
plexctl collection create TITLE SECTION_ID KEY1 [KEY2 ...]
plexctl collection delete RATING_KEY
plexctl collection rename RATING_KEY NEW_TITLE
plexctl collection add COLLECTION_KEY KEY1 [KEY2 ...]
plexctl collection remove COLLECTION_KEY ITEM_RATING_KEY
```

`collection list` walks every video section by default; pass `--section` to scope.

**Collection list table** (no runtime — collections don't carry duration):

| # | Title | Items | Section | Smart |
|---|---|---|---|---|
| 1 | Psycho | 12 | TV | — |
| 2 | Comfort Movies | 24 | Movies | ✓ |

- Row `#` → collection `ratingKey`. Debug: append `ratingKey` and `sectionID` columns.
- `Section` is the section title from `library sections`; resolve `sectionID` → title with a single `library sections` call before rendering. If only one section is involved, omit the column.
- `Smart` ✓ when `smart: true`, else blank.

**Collection show table** (Format 1; `duration` returned inline — use it; Total line at end):

Same shape as the `library list` Format 1 table.

**"Play my X collection" flow**:

1. `plexctl collection list` once.
2. Match by title (case-insensitive substring). If 0 matches: `No collection matching "<x>".` If >1: render the numbered list and ask which.
3. `plexctl collection show <ratingKey>` to get child rating keys.
4. `plexctl queue K1 K2 ...` to play. Output: `Playing <Collection> — N items.`

Empty collection → `That collection has no items.` Don't shell out to `queue`.

**Edit a collection — DO NOT rebuild.** When the user says *"add X to my Y collection"* or *"remove #3 from the Y collection"*, mutate in place via `collection add` / `collection remove`. **Never** delete-and-recreate a collection to make an edit — that loses ordering, locked title state, artwork, and the ratingKey other tools may reference. Rebuild is justified only when the user explicitly says *"start over"* / *"recreate"*.

**Smart-collection guard.** The CLI itself refuses content-modifying mutations on smart collections — it pre-checks the `smart` flag and returns `{"ok": false, "error": "smart collection: contents are query-driven ..."}`. This is necessary because PMS 1.43 silently 2xx-no-ops these mutations.

The skill should still check `smart` from the most-recent `collection list` row *before* shelling out, and render this user-facing line directly:

> That's a smart collection — its contents come from a search rule. Edit the rule in the Plex app.

Doing the check skill-side saves a wasted HTTP round trip and gives a tighter message than the raw CLI error. `delete` is allowed on smart collections — deletion is unambiguous, so don't refuse it.

**Intent grammar:**

| Intent | Command |
|---|---|
| *"create a Psycho collection with #1 #2 #3"* | resolve row-map → `collection create "Psycho" <sectionID> K1 K2 K3` |
| *"add Rocky to my Psycho collection"* | `collection list` to find ID → `collection add <collectionKey> <rocky-ratingKey>` |
| *"add #3 to my Psycho collection"* | resolve `#3` → `collection add <collectionKey> <ratingKey>` |
| *"remove Rocky from my Psycho collection"* | `collection show` to find item ratingKey → `collection remove <collectionKey> <ratingKey>` |
| *"rename Psycho to Spooky Season"* | `collection rename <collectionKey> "Spooky Season"` |
| *"delete the Psycho collection"* | `collection delete <collectionKey>` |

`create` needs a section ID (which library the collection lives in). If unspecified and the user's row-map has a single section, use it. Otherwise: run `library sections`, render the list, and ask which.

`delete` is destructive — run immediately, no confirmation. The collection is gone; its children remain in the library.

Output for mutation commands:

| Intent | Output |
|---|---|
| `create` | `Created "<Title>" — N items.` |
| `add` | `Added N items to "<Title>".` (resolve title from prior list row-map) |
| `remove` | `Removed from "<Title>".` |
| `rename` | `Renamed to "<New Title>".` |
| `delete` | `Deleted "<Title>".` |

On error containing `not a movie or show section` → `Can't make a collection there — only movies and TV.`

### Playlists

```
plexctl playlist list [--type video|audio|photo]
plexctl playlist show RATING_KEY
plexctl playlist create TITLE KEY1 [KEY2 ...] [--type video|audio|photo]
plexctl playlist delete RATING_KEY
plexctl playlist rename RATING_KEY NEW_TITLE
plexctl playlist add PLAYLIST_KEY KEY1 [KEY2 ...]
plexctl playlist remove PLAYLIST_KEY ITEM_ID
plexctl playlist clear PLAYLIST_KEY
```

**Playlist list table**:

| # | Title | Type | Items | Runtime | Smart |
|---|---|---|---|---|---|
| 1 | Comfort Movies | video | 7 | 14h 22m | — |

- Runtime from `duration` (free in result, ms). Format same as `Mm` / `Nh Mm`.
- Row `#` → playlist `ratingKey`. Debug: append.
- `Smart` ✓ when smart-playlist, else blank.

**Playlist show**: same shape as `collection show`. Row `#` → `ratingKey` (NOT `playlistItemID` — we don't expose playlist edits yet).

**"Play my X playlist" flow**:

1. `plexctl playlist list` once.
2. Match by title (case-insensitive substring). 0 / >1 → same behavior as collections.
3. **Check `playlistType` on the matched row.** If `audio` (or `photo`), refuse here:
   `Audio playback isn't supported — Apple TV only plays video.` Don't shell out to `show`.
4. `plexctl playlist show <ratingKey>` to get child rating keys.
5. `plexctl queue K1 K2 ...`. Output: `Playing <Playlist> — N items.`

**Edit a playlist — DO NOT rebuild.** Same rule as collections: use `playlist add` / `playlist remove` / `playlist clear` / `playlist rename` to mutate. **Never** delete-and-recreate to make an edit — `ratingKey` and `playlistItemID` references are lost. Rebuild only on explicit *"start over"* / *"recreate"*.

For removing items, the second argument is `playlistItemID` (NOT `ratingKey`) — get it from `playlist show`. Note that the row-# protocol for `playlist show` maps to the item's `ratingKey` for playback intent (`play #2`), but for *removal* intent (`remove #2`) you must resolve to `playlistItemID`. Hold both columns in conversation context when you render `playlist show` so follow-ups can branch.

**Smart-playlist guard.** Same shape as the collection guard. The CLI refuses content-modifying mutations on smart playlists (`add`, `remove`, `clear`, `rename`); the skill should pre-check `smart` from the list row and render:

> That's a smart playlist — its contents come from a search rule. Edit the rule in the Plex app.

`delete` is allowed on smart playlists.

**Intent grammar:**

| Intent | Command |
|---|---|
| *"create a Comfort Movies playlist with #1 #2 #3"* | resolve row-map → `playlist create "Comfort Movies" K1 K2 K3` |
| *"create an audio playlist Workout with #1 #2"* | `playlist create "Workout" K1 K2 --type audio` |
| *"add Rocky to Comfort Movies"* | `playlist list` to find ID → `playlist add <playlistKey> <ratingKey>` |
| *"remove #3 from Comfort Movies"* | `playlist show` → resolve `#3` to `playlistItemID` → `playlist remove <playlistKey> <playlistItemID>` |
| *"rename Comfort Movies to Cozy Movies"* | `playlist rename <playlistKey> "Cozy Movies"` |
| *"clear Comfort Movies"* | `playlist clear <playlistKey>` |
| *"delete Comfort Movies"* | `playlist delete <playlistKey>` |

`clear` empties the playlist but keeps it; `delete` removes the playlist itself. If the user says *"empty Comfort Movies"* prefer `clear`; *"throw away Comfort Movies"* prefer `delete`. If ambiguous, ask.

Output for mutation commands:

| Intent | Output |
|---|---|
| `create` | `Created "<Title>" — N items.` |
| `add` | `Added N items to "<Title>".` |
| `remove` | `Removed from "<Title>".` |
| `rename` | `Renamed to "<New Title>".` |
| `clear` | `Emptied "<Title>".` |
| `delete` | `Deleted "<Title>".` |

Audio creation is allowed (the refusal is only for *playback* — creating and managing audio playlists from voice is fine).

### Metadata

```
plexctl metadata RATING_KEY
```

Used internally for summary + duration on play / queue / list rendering. Direct user request → render:
- Title, Year, Type, Runtime, Studio, Rating
- Audio streams (language, codec, channels)
- Subtitle streams
- Full `summary`

Hide raw GUIDs / `key` / `thumb` / `art` unless debug.

---

## On Deck List

The **On Deck list** is a user-curated staging list of episodes/movies — resolved and ordered, but deliberately NOT sent to the Plex play queue or playback until the user explicitly commits it. It lives only in conversation context (a row-map of ratingKeys). It is the workspace the user builds *before* deciding to actually watch.

**Not the same as Plex's native "On Deck" shelf** (Plex's auto-generated next-up list, surfaced here via `continue-watching`). When the user says "on deck" they mean *this* curated list. Only if they explicitly ask for Plex's own next-up / continue-watching shelf, use `continue-watching`.

### Lifecycle

1. **Build** — lookups resolve items and append them to the list.
2. **Edit** — add / trim / reorder in place. The Plex queue is never touched during edits.
3. **Commit** — an explicit verb (`queue` / `play`) turns the list into a real Plex queue.
4. The list persists across turns until cleared or committed.

### Vocabulary → Action

| User intent | Action |
|---|---|
| "look up X", "find the next episode of X", "note X", "add X" — no queue/play verb | Resolve X (`play-latest --unwatched --key-only`, `search`, or `metadata`), **append** to On Deck list. Do NOT touch the Plex queue. |
| "add the next N episodes of X" | Resolve via one `episodes "X" --unwatched --json` call, take the first N in order, append all. No per-episode `metadata` loop. |
| "trim item N" / "remove item N" / "drop N" | Remove that row from the list, renumber. |
| "put X at the top" / "move N to top" / "move N to position M" | Reorder the list. |
| "show the list" / "what's on deck" / "on deck status" | Render the current On Deck list (table + Total). |
| "clear the list" / "start over" | Empty the On Deck list. |
| "queue that" / "queue the list" / "queue the on deck" | **Commit:** create a Plex queue from the list, in order. |
| "play that" / "play the list" | **Commit + start:** create the queue and begin playback. |

### Rules

- Every On Deck item must be fully resolved on add: ratingKey, title, S/E or movie type, runtime. Never store an unresolved title.
- Render the list with the standard List Formatting table (`# | Title | Detail | Runtime`) plus a `Total:` line, every time it changes.
- **Re-verify on every touch, not just commit.** Any time the On Deck list is rendered, added to, reordered, or committed, re-check the watched status of *every existing item* against a **single** `plexctl history` call (cross-reference its keys against the list) and drop anything now watched. Don't loop `plexctl metadata` per item to read `viewCount` — one `history` call covers the whole list. Items go stale between turns and between days — the user can watch them on a remote while the list sits frozen in conversation context. On `queue` / `play` also run the re-check-state queries (`now-playing`, `queue-show`) and warn before clobbering an actively playing item.
- Committing replaces the existing Plex queue. If that queue has an actively playing/paused item, warn first (see binding lesson); an idle queue is safe to replace.
- After a successful commit the On Deck list is consumed — clear it. A fresh lookup starts a new list.
- The On Deck row-map and the Plex queue row-map are separate lists. A bare `#N` reference resolves against whichever was rendered most recently.
- Debug mode: append a `ratingKey` column to the On Deck table.

---

## Ambiguity Rules

- **Search returns >1 match** → render numbered table, wait for `play #N` / `queue #N` / `more about #N`. Don't auto-pick.
- **`play-latest "x"` resolves to single show / single movie hit** → run directly. Only prompt when search returns multiple plausible matches.
- **`play it` / `resume` with no context** → ask what to play (or check `continue-watching` if user said "keep watching" / "resume").
- **`play #N` / `remove #N` / `more about #N`** → resolve `N` against most recent row-map. No row-map → ask user to search first.
- **Client unspecified** → Apple TV.

---

## Personalisation

Local to this server/user — not general plexctl behavior:

- **Ambiguous parent/spinoff dictation prefers the spinoff.** When a dictated title could match either a show and a same-named spinoff (e.g. a spinoff titled "After the [Parent]"), search the spinoff first, falling back to the parent only if no spinoff exists. Pair with `--min-score 0` — exact-match misses titles starting with "The".
- **Friday and Saturday nights are movie nights.** When building or suggesting an On Deck list / schedule for a Fri or Sat evening, lean toward movies over TV episodes. If the user adds episodes anyway, don't block. Weeknights have no preference.
- **Always favour unwatched.** Any time the user refers to shows or movies — search, listing, recommendation, disambiguation, "what should I watch," title lookup — default to unwatched. Lead with unwatched titles, sort/filter watched ones down, and when a query could resolve to either a watched or unwatched candidate, prefer the unwatched one. Not an absolute exclusion — still surface watched titles when the user explicitly asks, says "already seen"/"rewatch"/"including watched," or when unwatched yields nothing — but unwatched is the default lens for every show/movie reference.
- **Standing shortcuts (local to this user).** The verb **"queue"** and its shorthand **`q`** mean *make ready whatever is on the On Deck list, paused*: commit the current On Deck row-map to a Plex queue (`plexctl queue K1 K2 …`), then immediately `plexctl pause` and verify `now-playing` is `paused`. Ready, **not** playing. Empty On Deck → nothing to make ready; say so and stop. Consume (clear) the On Deck list after a successful commit. The shorthand **`p`** = **"play"** — start/resume playback: active queue → start/resume it (idle-with-loaded-queue → bootstrap via `play-media` on the selected item); else resume the current paused item; else say "Nothing playing." and stop. Trigger on a bare `q`, a bare `p`, or the bare word "queue" (case-insensitive, nothing else on the line). `q` no longer maps to `queue-show` — to *see* the queue the user says "show queue."
- **Output: runtime + Total on every list** (see List Formatting Rules) — a standing human-friendliness preference.

---

## Startup Recall

At the start of each `/plex` turn, run `plexctl context --history-limit 5` once and retain the JSON for the rest of the turn. The bundle gives `nowPlaying`, the active queue (with `ratingKey` + `duration` + `year` per item), and recent history (with `duration` + `year`) in a single parallel fetch (~100 ms cold).

Use the cached bundle to answer read-only state questions without extra round-trips:
- "What's playing?" / "what's on?" → `nowPlaying`
- "What's queued?" / "what's next?" → `queue.items`
- "Did I watch X?" / "what did I just watch?" → `history.items`

**Do NOT** treat the bundle as a substitute for re-querying before any state-mutating action. The Execution Policy fetch list below still applies — re-run `now-playing` (or the targeted command) before mutating, because the cached snapshot can drift the moment the user touches a remote.

**If the startup `context` call fails, degrade — don't abort the turn.** `context` resolves the client first, so it fails whenever the Apple TV isn't registered active or plex.tv is unreachable — even though most asks don't need the client. History, search, library, collection, playlist, and metadata requests proceed normally without the bundle: run the targeted commands, none of them touch the client. Surface the translated error only when the ask actually needs client state (now-playing / queue / transport). Never render a client-liveness error for a turn that didn't need the client.

Skip the startup fetch only when the turn is a pure command with no state dependency (e.g. `/plex search Rocky`, `/plex pause`).

---

## Execution Policy

Once intent is unambiguous, run immediately. No confirmation prompts — even for `stop`, `queue-clear`, `unwatched`, `rate 0`. User text input is reliable.

Pre-action `now-playing` / `continue-watching` fetches: skip by default (the Startup Recall bundle already has them). Re-fetch fresh when intent depends on current state AND the action mutates:
- Relative seek (`+30s` / `-10s`)
- `watched` / `unwatched` / `rate` with no `RATING_KEY`
- "Resume" / "keep watching" intent

Before any statement about what has or hasn't been watched — or any schedule/recommendation built on watch status — run `plexctl history` first. Never infer watch status from queue `selectedItemID` position, `continue-watching`, or conversation memory: queue position is not a watched indicator, and the selected item itself may already be watched.

---

## Self-Improvement

Plex grows by capturing real session corrections to `~/.claude/skills/plex/LESSONS.md`. The file is local to the skill, NOT in the global memory dir. It is read at every invocation and referenced before novel decisions.

### Startup Recall
On every `/plex` invocation, before parsing intent:
1. Read `~/.claude/skills/plex/LESSONS.md` if it exists. If absent, skip silently.
2. For lessons with `seen: 3` or higher, treat them as **binding constraints** that override default behavior (e.g., a new error translation, a known-broken command).
3. For lessons with `seen: 1` or `2`, treat as **soft guidance**.
4. Do not announce the recall.

### Reflection Triggers
After any of these events, append a lesson:

| Trigger | When to write |
|---|---|
| `resolution` | `play X` / `play-latest` resolves to wrong show, episode, or movie and user corrects |
| `new-error` | `plexctl` returns an error not in the Error Translation table |
| `output` | User corrects output format (too verbose, wrong column, missing runtime, etc.) |
| `client` | A client behavior changes (Apple TV stops responding, new client appears, etc.) |
| `refused` | A command currently refused upfront starts working — or a working command starts failing systematically |
| `correction` | General behavioral correction not covered by other triggers |

Do NOT write lessons for:
- Transient network errors (single failure, no pattern) — but if the same transient recurs within one session, that IS a pattern: record it (`new-error` trigger)
- One-off user mood ("not in the mood for that show")
- Things already documented in SKILL.md

### Lesson Format
Append to LESSONS.md:

```
---
trigger: <resolution|new-error|output|client|refused|correction>
date: YYYY-MM-DD
seen: 1
---
**Context:** <intent + command run>
**Mistake:** <what Plex did or returned>
**Correction:** <what user said to do, or correct translation/behavior>
**Apply when:** <future condition that should trigger this rule>
```

### Deduplication
Before appending, scan existing LESSONS.md. If a lesson with the same `trigger` and substantively the same `Apply when` exists, increment its `seen` counter and update `date` instead of duplicating.

### Synthesis Threshold
After writing or incrementing, count lessons sharing the same `trigger`:
- **< 3 lessons** → no further action
- **≥ 3 lessons** → surface a synthesis proposal before continuing:
  - "3+ lessons under `<type>`. Suggested SKILL.md edit: <concrete change, e.g., new row in Error Translation table>. Approve?"
  - Wait for explicit approval. Do NOT auto-edit SKILL.md.
  - On approval: edit SKILL.md, mark synthesized lessons with `synthesized: true`.

## Incident Logging

A raw, append-only log of specific failure signatures, kept separate from the curated `LESSONS.md`. Purpose: longitudinal data to rank post-1.0 hardening work — not behavioral guidance, so don't read it at startup and don't let it influence in-session decisions.

### Triggers
Append a line to `~/.claude/skills/plex/INCIDENTS.md` (create it on first use) whenever you observe:

| Trigger | Note |
|---|---|
| `stateSaved: false` in any envelope | This field ships with the v1.0.0 binary and is dormant until that deploy — still worth logging so the data exists once it's live |
| A request timeout during queue creation, bind, or `queue-start` | |
| `clientUnreachable: true` | |
| A "no active queue" answer that contradicts a queue created moments earlier | |

### Format
One dated line per incident: date, exact command run, exit code, key envelope fields (`stateSaved`, `clientUnreachable`, `staged`, etc. — whichever are present), and one line of context.

```
2026-07-10 | plexctl queue-start --client "Apple TV" | exit 2 | clientUnreachable: true | queue created ~30s earlier via `queue`, bind never completed
```

Never write these to `LESSONS.md` — that file stays curated for behavioral corrections, not raw failure telemetry.

---

## Invocation

User invoked `/plex $ARGUMENTS`

1. Execute SELF-IMPROVEMENT → Startup Recall (read LESSONS.md, surface binding constraints).
2. Detect `debug` leading token / `--debug` flag → set `debug_mode`, strip.
3. Empty `$ARGUMENTS` → run `plexctl now-playing`, render result.
4. Else → parse intent, map to command(s), run.
5. Render per specs. Translate errors per table. Show runtime + `Total:` on every list. Apply debug additions if `debug_mode`.
6. After action completes, evaluate against Reflection Triggers — write/increment lesson if applicable.
