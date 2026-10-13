# Plex Skill Reference (v3 companion — loaded on demand)

Read when rendering lists, managing the On Deck playlist, or handling `q`/`p` shortcuts. Error handling, behavioral rules and evening planning live in SKILL.md. Machine-specific values (the playlist's ratingKey, evening start time, slot habits) live in PERSONAL.md.

## List Formatting

Exactly two table formats.

### Format 1 — Title table (movies + episodes)

| # | Title | Detail | Runtime | <Context> |
|---|---|---|---|---|
| 1 | Rocky (2021) | movie | 2h 35m | … |
| 2 | Stranger Things | S05E01 — Chapter One | 1h 12m | … |

Total: 3h 47m

- Title: movie name + year, or show name alone (no S/E). Detail: `movie`, `S0xE0y — Episode Title`, or the literal word `show` on show-level rows — never invented variants like "N episodes".
- **Never emit a half-filled template.** `history` and `playlist show` rows carry no `season`/`episode` field, so on a table sourced only from them the Detail column is the episode title **alone** — not `S— Title`. Same rule everywhere: a field the payload doesn't contain is omitted, never rendered as an empty placeholder. S/E numbers come from a per-ratingKey `metadata` call (`parentIndex`/`index`/`grandparentTitle`) or from the `play-latest --key-only` that resolved the item.
- Runtime on every row from inline `duration` (ms → `Nh Mm`; hours = ms // 3600000, minutes = (ms // 60000) % 60). Deleted-item rows (`duration: null` in history/collection show) render blank.
- Context column swaps per command: `Why` (recommendations — one line each), `Starts` (a night's schedule), `Watched` (status, playlist, library, collection), `Description` (search, first sentence of summary ≤120 chars), `Viewed` (history, date), `Progress` (continue-watching).
- **`Starts`** column: clock time each item begins, cumulative from the evening start time (PERSONAL.md; default 6:00 pm; a per-night override from the user wins for that night). Accumulate exact ms and floor to the minute for display — never add rounded per-row runtimes. Format `6:00 pm`.
- `Total:` line sums raw ms — omit entirely when any show row is present.
- Show rows (`durationNominal: true`): `~43m typical` or blank, never bare. Watched column for shows uses `unwatchedLeaves` (`0` → Yes, else `N left`), never `viewCount`.
- Movie Watched: `viewCount > 0` → Yes/No.
- Debug: append `ratingKey` / `playlistItemID` column.

### Format 2 — Container table (collection list, playlist list)

Own columns (`Items`, `Section`, `Smart`, playlist adds `Type` + `Runtime`), no per-row episode runtime, no Total. `Smart` ✓ from `smart: true`. Library `sections` is a trivial `# | Title | Type` list.

## Output one-liners

Mutations: `Saved to Up Next Tonight.` / `Created "<T>" — N items.` / `Added N to "<T>".` / `Removed.` / `Renamed to "<T>".` / `Emptied "<T>".` / `Deleted "<T>".` / `Marked watched.` / `Rated N/10.` Check-and-fix: `Done.` / `Done — N switched.`

Empty states: `Nothing on deck yet.` (playlist empty), `Nothing found for "<q>".`, `All caught up on "<show>".`

## Command quick-map

### Server (works)

| Intent | Command |
|---|---|
| find X | `plexctl search --json "X" [--type show|movie|episode]` |
| list a show's episodes | `plexctl episodes "SHOW" [--season N] [--unwatched] [--json]` |
| next unwatched of X (resolve) | `plexctl play-latest --unwatched --key-only "X"` → `{ratingKey, season, episode, title}` at top level (no show name, no duration — `metadata` for those) |
| item detail / watched check | `plexctl metadata RATING_KEY` (`viewCount`, `lastViewedAt`, `duration`, `summary`, `grandparentTitle`, `parentIndex`, `index`, `Media[].Part[].Stream[]`) |
| unwatched films / library scan | `plexctl library list --section N [--unwatched] [--type T] [--sort addedAt:desc]` → `.items[]` (ratingKey, title, year, duration, viewCount, addedAt) |
| On Deck playlist | `plexctl playlist show|add|remove|clear KEY …` (see below) |
| history / continue | `plexctl history --limit N` (`.history[]`) / `plexctl continue-watching` |
| watch state / rating | `plexctl watched|unwatched KEY`, `plexctl rate N KEY` (0–10; 0 = unrate) — always pass KEY |
| audio audit | `plexctl audit-audio "SHOW" [--language eng] [--season N]` |
| set track (single) | `plexctl set-audio KEY [--language X | --stream-id N]`, `set-subtitle … [--off]` |
| set track (bulk) | `plexctl set-audio --show "S" --language X [--season N|--all-seasons] [--only-non-eng] --dry-run` first, confirm, then real run |
| collections / playlists | `plexctl collection|playlist list/show/create/add/remove/rename/delete[/clear]` |
| discovery | `plexctl commands` (JSON tree) |

### Client (unavailable — PLEX_CLIENT_INACTIVE)

`now-playing`, `play`, `pause`, `stop`, `next`, `prev`, `seek`, `play-media`, `play-latest` (without `--key-only`), `queue`, `queue-*`, `context`, `volume`. Don't run them. `clients` still answers (read-only) but shows the Apple TV as not active.

Flag facts that are easy to get wrong:

- **`--json` exists only on `search` and `episodes`**, where it switches off the human table. Every other plexctl command is JSON by default — adding `--json` gets BAD_REQUEST "unknown flag". Unsure → `plexctl commands` or `--help`.
- **Payload keys differ by command**: `search` → `.results`, `library list` / `playlist show` → `.items`, `history` → `.history`, `metadata` → `.metadata`, `library sections` → `.sections`, `play-latest --key-only` → flat. Read the right one rather than assuming `.data`.

Language intent: anime → `jpn`, German film → `deu`, default `eng`; per-request, not stored config. "Default/normal audio" = the selected track (the only writable knob) — set it without explaining selected-vs-embedded.

Bulk sweeps (library-wide audits): enumerate via `library list` once, loop `audit-audio` per show in ONE bash loop; `--ndjson` for kill-safe progress; TRANSPORT_TIMEOUT items retried once then reported skipped.

## On Deck (Up Next Tonight)

The server playlist "Up Next Tonight" IS On Deck; its ratingKey is in PERSONAL.md. There is no conversation-local list. One playlist, reused: never delete it or create a second one. If it's missing, `playlist create "Up Next Tonight" K…` and record the new key in PERSONAL.md.

- **Build:** resolve each title (`play-latest --unwatched --key-only` for shows, `search`/`library list` for films, `episodes` for "next N episodes"), then `playlist add KEY K…` in order. First build of a new day: `playlist clear KEY` first (SKILL.md → Evening Planning).
- **Proposals aren't written.** Recommendations are rendered with `Why` and nothing touches the playlist until the user picks.
- **Remove** ("undeck X", "drop N", "take X off"): `playlist remove KEY <playlistItemID>` (from `playlist show`, not ratingKey). Never touch the item's watch state or resume point.
- **Reorder** (move N to slot 1, push the rest back, category ordering): `playlist clear KEY` then `playlist add KEY` in the new order. There is no move verb yet.
- **Insert** ("put X before Y"): resolve X, then clear + re-add the full order.
- **Show / status:** `playlist show KEY` → Format 1 + Total (`Watched` column; `Starts` when presenting a schedule).
- **Every touch:** re-check each row's `viewCount` (from `playlist show`, or `metadata`) and `playlist remove` anything with `viewCount >= 1`.
- **Rewatch lists:** when the user deliberately asks for items already watched (rewatches, favourites, a shuffle of a watched series), keep them despite `viewCount >= 1` — exempt from the every-touch removal — and never run `unwatched` on them. Shuffle if asked; size to the evenings named, using the evening length from PERSONAL.md (default ~3h each). The user plays them from the playlist and skips what they don't want.
- **New night:** `playlist clear KEY` and refill. Never run `unwatched` on old items — their watch state and resume points stay as they are.

## Standing Shortcuts

Trigger only on a bare `q`, bare `p`, or the bare words "queue" / "lock it in".

- **`q` / "queue" / "lock it in":** make sure the playlist holds exactly the agreed list in order (re-check viewCount, drop watched items), re-render it, reply "Saved to Up Next Tonight." Nothing else.
- **`p`:** same as `q` — nothing can be started remotely; the user plays the playlist with the TV remote.

## Clients

| Client | Controllable |
|---|---|
| Apple TV | No — Companion removed in Plex for Apple TV 2026.18.0 (still absent in 2026.19.1) |
| iPad / Plex for Mac / Plex Web | No |

## Appendix — client-era templates (unavailable)

Now playing (play-latest / play-media success):
```
Now playing: Stranger Things — S05E01 — Chapter One: The Crawl  (1h 12m)
Description: First sentence of summary.
```
now-playing command — render exactly these lines:
```
State:       Paused
Title:       Chapter One: The Crawl
Show:        Stranger Things — S05E01
Progress:    2:36 / 1:11:28
Description: First sentence of summary.
```
(movies: omit Show, Title becomes `Rocky (2021)`). Idle → `Nothing playing.` Transport confirmations: `Paused.` `Playing.` `Stopped.` `Skipped.` `Back.` `Seeked to mm:ss.`

Flag fact: `play-media` takes a `ratingKey`, not a `playQueueItemID` (different ID space; passing one returns HTTP 400).
