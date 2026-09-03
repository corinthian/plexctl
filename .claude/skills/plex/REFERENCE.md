# Plex Skill Reference (v2 companion — loaded on demand)

Read when rendering lists, managing the On Deck list, or handling `q`/`p` shortcuts. Error handling and behavioral rules live in SKILL.md.

## List Formatting

Exactly two table formats.

### Format 1 — Title table (movies + episodes)

| # | Title | Detail | Runtime | <Context> |
|---|---|---|---|---|
| 1 | Rocky (2021) | movie | 2h 35m | … |
| 2 | Stranger Things | S05E01 — Chapter One | 1h 12m | … |

Total: 3h 47m

- Title: movie name + year, or show name alone (no S/E). Detail: `movie`, `S0xE0y — Episode Title`, or the literal word `show` on show-level rows — never invented variants like "N episodes".
- **Never emit a half-filled template.** `history` rows carry no `season`/`episode`/`index` field, so on any table sourced from `history` the Detail column is the episode title **alone** — not `S— Title`. Same rule everywhere: a field the payload doesn't contain is omitted, never rendered as an empty placeholder. S/E numbers, if genuinely needed, require a per-ratingKey `metadata` call.
- Runtime on every row from inline `duration` (ms → `Nh Mm`; hours = ms // 3600000, minutes = (ms // 60000) % 60). Deleted-item rows (`duration: null` in history/collection show) render blank.
- Context column swaps per command: Description (search, first sentence of summary ≤120 chars), Selected (queue-show ✓), Viewed (history, date), Progress (continue-watching), Watched (library/collection/playlist).
- `Total:` line sums raw ms — omit entirely when any show row is present.
- Show rows (`durationNominal: true`): `~43m typical` or blank, never bare. Watched column for shows uses `unwatchedLeaves` (`0` → Yes, else `N left`), never `viewCount`.
- Movie Watched: `viewCount > 0` → Yes/No.
- Debug: append `ratingKey` / `playQueueItemID` column.

### Format 2 — Container table (collection list, playlist list)

Own columns (`Items`, `Section`, `Smart`, playlist adds `Type` + `Runtime`), no per-row episode runtime, no Total. `Smart` ✓ from `smart: true`. Library `sections` is a trivial `# | Title | Type` list.

## Output one-liners

Transport confirmations: `Paused.` `Playing.` `Stopped.` `Skipped.` `Back.` Seek: `Seeked to mm:ss.` Mutations: `Created "<T>" — N items.` / `Added N to "<T>".` / `Removed.` / `Renamed to "<T>".` / `Emptied "<T>".` / `Deleted "<T>".` / `Marked watched.` / `Rated N/10.`

Now playing (play-latest / play-media success):
```
Now playing: Stranger Things — S05E01 — Chapter One: The Crawl  (1h 12m)
Description: First sentence of summary.
```
now-playing command — render exactly these lines, nothing added (no Client line), nothing merged:
```
State:       Paused
Title:       Chapter One: The Crawl
Show:        Stranger Things — S05E01
Progress:    2:36 / 1:11:28
Description: First sentence of summary.
```
(movies: omit Show, Title becomes `Rocky (2021)`). Idle → `Nothing playing.`

Empty states: `No queue right now.` (queue-show empty), `Nothing found for "<q>".`, `All caught up on "<show>".`

## Command quick-map

| Intent | Command |
|---|---|
| pause / play / stop / next / back | `plexctl pause|play|stop|next|prev` |
| seek to 1:30 / ahead 30s / back 10s | `plexctl seek 1:30|+30s|-10s` |
| find X | `plexctl search --json "X" [--type show|movie|episode]` |
| list a show's episodes | `plexctl episodes "SHOW" [--season N] [--unwatched] [--json]` |
| next unwatched of X | `plexctl play-latest --unwatched "X"` (resolve-only: add `--key-only`) |
| play #N | `plexctl play-media RATING_KEY` |
| queue items | `plexctl queue K1 K2 …` then `pause` (queue-then-pause) |
| start staged queue | `plexctl queue-start` |
| show/clear/edit queue | `plexctl queue-show|queue-clear|queue-remove ITEM_ID|queue-add K…` |
| what's on | `plexctl now-playing`; bundle: `plexctl context --history-limit 5` |
| history / continue | `plexctl history --limit 10` / `plexctl continue-watching` |
| watch state | `plexctl watched|unwatched [KEY]`, `plexctl rate N [KEY]` (0–10; 0 = unrate) |
| item detail / watched check | `plexctl metadata RATING_KEY` (per-item `viewCount`, `lastViewedAt`, `duration`, `summary`) |
| audio audit | `plexctl audit-audio "SHOW" [--language eng] [--season N]` |
| set track (single) | `plexctl set-audio KEY [--language X | --stream-id N]`, `set-subtitle … [--off]` |
| set track (bulk) | `plexctl set-audio --show "S" --language X [--season N|--all-seasons] [--only-non-eng] --dry-run` first, confirm, then real run |
| collections / playlists | `plexctl collection|playlist list/show/create/add/remove/rename/delete[/clear]` |
| clients | `plexctl clients` |
| discovery | `plexctl commands` (JSON tree) |

Two flag facts that are easy to get wrong:

- **`play-media` takes a `ratingKey`.** `queue-show`'s top-level `selectedItemID` and each row's `playQueueItemID` are play-queue-item IDs — a different ID space; passing one returns HTTP 400. Bootstrapping an idle client: take the selected row's own `ratingKey` if the payload carries one, otherwise resolve it (`search "<title>" --type episode --json`, confirm on `duration`) or reuse the ratingKey already known from building the On Deck list.
- **`--json` exists only on `search` and `episodes`**, where it switches off the human table. Every other plexctl command is JSON by default — adding `--json` gets BAD_REQUEST "unknown flag". Unsure → `plexctl commands` or `--help`.

Language intent: anime → `jpn`, German film → `deu`, default `eng`; per-request, not stored config. "Default/normal audio" = the selected track (the only writable knob) — set it without explaining selected-vs-embedded.

Bulk sweeps (library-wide audits): enumerate via `library list` once, loop `audit-audio` per show in ONE bash loop; `--ndjson` for kill-safe progress; TRANSPORT_TIMEOUT items retried once then reported skipped.

## On Deck List (conversation-local staging)

User-curated list of resolved items, NOT sent to Plex until committed. Distinct from Plex's continue-watching shelf — "on deck" means this list unless they explicitly ask for Plex's next-up shelf.

- Build: lookup verbs with no queue/play verb ("look up X", "add X", "note X") resolve (`play-latest --unwatched --key-only` / `search` / `episodes` for "next N episodes") and append. Plex queue untouched.
- Edit: "trim/remove item N", "move N to top" — reorder/renumber in place. **"undeck X" / "take X off the deck" / "drop N" edits THIS list**, never Plex: remove the entry and re-render Format 1 + Total. If nothing is staged but a shelf or list was rendered in a recent turn, adopt that rendered set as the working list and edit it — don't reply "the list is empty". Read it as a server-side write only when the user explicitly names Plex's Continue Watching row (and plexctl has no verb that writes that shelf — `queue-remove`, `collection remove`, `playlist remove` are the only removal verbs; that's a footnote, never the answer).
- Show: render Format 1 + Total every time it changes.
- Commit: "queue that/the list" → `plexctl queue K1 K2 …` in order (+ pause); "play that" → commit and let it play. Committing replaces the Plex queue — warn first if the existing queue has an actively playing/paused item; consume (clear) the list after a successful commit.
- Every item fully resolved on add (ratingKey, title, S/E or movie, runtime). On every touch, re-check each item's watched status by reading its own `viewCount` — `plexctl metadata <ratingKey>`, one call per item, the list is short — and drop anything with `viewCount >= 1`. History is not the source here; it can be missing a genuinely watched item (SKILL.md → Hard Rules).
- On Deck row-map and Plex queue row-map are separate; bare `#N` resolves against whichever was rendered most recently.

## Standing Shortcuts

Trigger only on a bare `q`, bare `p`, or the bare word "queue" — nothing else on the line.

- **`q`** / **"queue"**: commit the On Deck list to a Plex queue, `pause`, verify `now-playing` says paused — ready, not playing. Empty list → say so, stop. Clear the list after commit. (`q` ≠ queue-show; "show queue" does that.)
- **`p`**: play — active queue → start/resume (idle → `play` exits 6; follow its hint with `play-media` on the selected item); else resume paused item; else `Nothing playing.`

## Clients

| Client | Controllable |
|---|---|
| Apple TV | Yes (default — omit `--client`) |
| iPad / Plex for Mac / Plex Web | No — offer Apple TV instead |
