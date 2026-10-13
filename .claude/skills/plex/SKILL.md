---
name: plex
description: >
  Plex Media Server control via plexctl CLI. TRIGGER when: user wants to
  find, line up, schedule, play, queue, or rate movies and TV episodes,
  plan tonight's viewing, check what's on deck, control playback, or says
  "/plex". Parses voice/text intent into plexctl commands. Hides verbose
  plexctl internals — translates errors to plain English. On Deck is a
  server playlist ("Up Next Tonight"); play/playback requests are answered
  with "use the TV remote" while client control is unavailable.
argument-hint: "[voice phrase | command | query]"
allowed-tools:
  - "Bash(plexctl:*)"
  - "Bash(jq:*)"
---

# Plex Skill (v3 — requires plexctl ≥ 2.0.1)

Goal: smooth find / line-up / schedule UX. Hide plexctl noise. Never surface internal IDs, raw envelopes, or codes unless `debug_mode`.

**On Deck is a server playlist.** "Up Next Tonight" (its ratingKey is recorded in PERSONAL.md) is the single source of truth for what's lined up. The user plays it from the Plex app with the TV remote. Building, editing, reordering and checking status all happen on that playlist with server-only plexctl commands — fully supported, no client involvement. REFERENCE.md → "On Deck (Up Next Tonight)" has the lifecycle.

**Client control is unavailable.** Plex for Apple TV 2026.18.0 dropped Plex Companion; every client-driving command (`now-playing`, `play`, `pause`, `stop`, `seek`, `next`, `prev`, `play-media`, `play-latest` without `--key-only`, `queue`, `queue-start`, `queue-show`, all `queue-*`, and `context`, which bundles now-playing) returns PLEX_CLIENT_INACTIVE. Never run them, never retry, never tell the user to relaunch the app or find a setting. The old client-control rules are kept in the appendix at the end, for if Companion returns.

**Version gate:** on the first plexctl call of a session, if behavior looks pre-2.0.0 (a flat `"error": "text"` string instead of an error object) run `plexctl --version`; below 2.0.0 → stop and say: "plexctl is below 2.0.0 — upgrade the binary to 2.0.0 or later before using this skill." Do not attempt workarounds for older binaries.

---

## Hard Rules

- **Never run `curl`** (or any direct HTTP) against Plex. Stay inside `plexctl`. A capability gap is a missing feature to report, not to work around.
- **Don't invent plexctl limitations.** An `ok:true` empty result isn't proof of a missing feature. Run the command, report plainly. A gap is real only if documented here or the binary refuses it (`PLEX_UNSUPPORTED`).
- **Confirm before any bulk write.** Bulk `set-audio` always gets `--dry-run` first; show the plan (show, season breakdown, change count) and get an explicit go-ahead. The CLI doesn't prompt — this gate is yours.
- **Watched status is two questions with two sources.**
  - *Ever watched this item?* → per-item `viewCount`, read fresh via `metadata RATING_KEY` (or the row's own `viewCount` in `playlist show`). **`viewCount >= 1` IS watched — that is the authority.** An item can be flagged viewed with no history row at all (mark-watched and credit-skip paths).
  - *What was watched recently, and when?* → `plexctl history`, run fresh. **History takes precedence for anything about recency** — favourites get rewatched, so a `viewCount` says nothing about *when*.
  - Neither source vetoes the other on its own question. `history` missing an item never overrides `viewCount >= 1`; `viewCount` never answers "recently".
  - **`skipCount` is not evidence either way.** An episode watched through and then credit-skipped to advance records `viewCount: 1` with `skipCount: 1` — that is a *completed* episode, the normal signature. Never raise it as doubt. (`skipCount` is often absent from the payload entirely.)
  - Never infer per-item watch state from playlist position, `continue-watching`, show-level `skipCount` / `viewedLeafCount` arithmetic, or memory. (Show-*row* Watched columns still use `unwatchedLeaves` per REFERENCE — that is a different question.)
- **Always pass a RATING_KEY** to `watched`, `unwatched` and `rate`. With no key they target "the currently playing item", which needs a controllable client and now always fails.

## The Error Contract

Every failure is `{ok:false, error:{code, message, http_status?, hint?}, data?}` on stdout, exit codes: 1 bad invocation, 2 Plex refused, 3 transport, 4 plexctl bug or a response it could not decode, 5 not logged in, 6 accepted-but-nothing-happened.

Two rules:

1. **Route on `error.code`, phrase per the table below.** Never show raw envelopes or codes (except debug).
2. **`error.hint` is the recovery — follow it exactly** (e.g. `auth login`); don't improvise alternatives or blind-retry. Exception: the PLEX_CLIENT_INACTIVE relaunch hint is stale — ignore it.

| Code | Say (adapt naturally) |
|---|---|
| BAD_REQUEST | (your command was malformed — fix and re-run silently; only surface if you can't) |
| PLEX_AUTH_REQUIRED | plexctl isn't logged in. Run `plexctl auth login`. |
| PLEX_AUTH_FAILED | Login didn't work — check the credentials. |
| PLEX_NOT_FOUND | Nothing found for "<q>". / Plex couldn't find that. |
| PLEX_ALL_WATCHED | All caught up on "<show>". |
| PLEX_SHOW_AMBIGUOUS | Several shows match — which one? (pick via `data.matches`, disambiguate per hint) |
| PLEX_SCOPE_REQUIRED | That's N seasons. All of them, or just one? |
| PLEX_TRACK_NOT_FOUND | No <lang> track on that one. |
| PLEX_CLIENT_INACTIVE | "The Apple TV can't be remote-controlled since the Plex update — use the Up Next list." Don't retry. (You shouldn't hit this: client commands are off the menu.) |
| CLOUD_UNREACHABLE | plex.tv is unreachable — your server is fine; try shortly. |
| TRANSPORT_TIMEOUT | Plex was slow to answer. The request may already have been applied — say so before retrying anything that changes state. (Batches: retry only TIMEOUT items.) |
| TRANSPORT_FAILED / PLEX_SERVER_ERROR / PLEX_HTTP_ERROR | Can't reach Plex right now. / Plex errored. |
| PLEX_SMART_CONTAINER | That's a smart collection/playlist — edit its rule in the Plex app. |
| PLEX_UNSUPPORTED | Not supported — use the Plex app UI / TV remote. |
| NOT_APPLIED (exit 6) | Plex accepted it but nothing actually changed — say so; follow the hint. |
| DECODE_ERROR | Plex sent something that was not the JSON it promised, or a response over the size bound. Report it — do not retry. |
| INTERNAL | plexctl bug — surface the message, suggest `debug`. |

Client-only codes (PLEX_NOTHING_PLAYING, PLEX_SEEK_FAILED, PLEX_CLIENT_UNKNOWN/_AMBIGUOUS/_UNREACHABLE, PLEX_QUEUE_*, PLEX_PLAYBACK_NOT_STARTED, PLEX_NO_QUEUE) are in the appendix.

Success envelopes may carry `warnings` (e.g. PLEX_STATE_SAVE_FAILED). Mention a warning only if it affects what the user does next.

## Core Behaviors

- **Row numbers, not IDs.** Any list with `ratingKey`/`playlistItemID` gets a `#` column; keep the #→ID map in context for "drop #2", "one and four". Debug mode appends ID columns.
- **Compound titles split first.** A dictated phrase that joins titles with "and" ("Fallout and the Virtues, and After the First 48") is split into candidate titles *before* any lookup, and each one is resolved independently with its own `play-latest --unwatched --key-only "<clean title>"`. Never reuse a resolution obtained from the unsplit string, and never fall back to show-level `skipCount`/`viewedLeafCount` arithmetic for an unwatched count — a direct `--unwatched` query or per-episode `viewCount` is the only authority.
- **Dictated show names.** When `--key-only` resolves to a show whose real name differs only cosmetically from what was said ("body in the basement" → "A Body in the Basement"), render the real name and carry on — no question. If it's a different series, flag it (see show-identity echo).
- **The answer stands alone.** A confirmation is one line, or the table — nothing after it. A fully successful check-and-fix ("make sure X is set") is confirmed in one word or a short line — "Done." / "Done — 3 switched." Detail only on a failure or when asked. Never append what a rebuild displaced, that an episode advanced or got watched since last time, or that media has gone missing (a 404 on a previously-watched key and a null-runtime history row are expected downstream of the user deleting watched films — render the row blank and say nothing; investigate absence only if asked outright). Never state what's on deck unless it was asked *this turn* and queried *this turn* — a bare 👍 / "ok" / "thanks" gets no state report at all.
- **`search` caps ~10 results** — for whole-show episode lists use `episodes "<show>" [--unwatched]`, not search. `"loose": true` hits are unconfirmed: "Closest I get is <Title> (<year>) — that it?"
- **An empty search result is final.** Report "Nothing found" and STOP — never enumerate the library (`library list` scans) to double-check absence. One corrected-spelling retry is fine; expeditions are not.
- **`durationNominal: true`** rows (shows): render `~43m typical` or blank, never a bare runtime, and no `Total:` line on tables containing them. Movie and episode durations are real.
- **Show-identity echo:** `episodes`/`audit-audio`/bulk results echo `show`+`showRatingKey` — if it isn't the show the user named, flag it instead of rendering the wrong series.
- **Edits mutate in place** (`collection add`/`playlist remove`/…) — never delete-and-recreate a playlist or collection; rebuild only on explicit "start over". Playlist removal takes `playlistItemID` (from `playlist show`), not ratingKey.
- **No confirmation prompts** for unambiguous single commands (playlist clear/remove, rate 0, delete) — run immediately. The one gate is bulk set-audio (above).
- **State recall:** for "status" / "what's on deck" / a bare `/plex`, run `playlist show <Up Next key>` fresh and render it with each row's own `viewCount`. Use `history --limit N` only when the question is about recency or rotation. Re-query fresh before any state-mutating action — never trust a previous turn's list. (Don't call `context`; it fails whole when the client is inactive.)
- **Chain with `&&`, no banners.** Multi-step plexctl sequences go in ONE bash call joined by `&&` (never `;`, never `echo "=== X ==="` separators). Each envelope is self-identifying; `&&` stops at the first failure so the last line is always the failing step.
- **Post-import check.** After a film or episode arrives from a download (arrctl hand-off), find it in Plex (`search --json`), then report in one short block: runtime, each audio track (language, codec, channels, which is selected), and the subtitle tracks. Unlabelled tracks are reported as unlabelled — never guess the language. Never switch a track unless asked.
- **Discovery:** unsure of a flag or command? `plexctl commands` (JSON tree) or `plexctl <cmd> --help`. Never guess flags.

## Evening Planning

These are skill rules. The *values* they use (start time, evening length, slot habits, rotation staples, movie-night shape) are local customisations in PERSONAL.md — see "Skill rules vs local customisations" below.

- **Suggestions are proposals; picks are writes.** Recommended items are shown with a reason each and are NOT written. The moment the user picks ("one and four", "on deck X, Y, Z", "add 5 and 6"), write them to the playlist and render it. `q` / "lock it in" then only re-verifies (viewCount, order) and confirms "Saved to Up Next Tonight."
- **New night clears.** The first build on a new calendar day clears the playlist and refills it, unless the user says "add to". Later edits the same evening mutate in place. Never run `unwatched` on items being cleared.
- **Schedule table.** When building or reordering a night, render Format 1 with a `Starts` column: cumulative from the evening start time, summed in exact milliseconds (never by adding rounded per-row runtimes), plus the `Total:` line. The start time comes from PERSONAL.md; with nothing set, use 6:00 pm. The user can move it for one night ("starting at 8") — that night only, never written back. Status renders use `Watched` instead of `Starts`.
- **Evening length.** If PERSONAL.md sets a target length, aim suggestions at it and mention, in one clause, when a list runs well past it. Never trim a list on that basis.
- **Reorder by category.** "Action first, the rest after", "crime then drama": classify, keep relative order within each group, then clear + re-add in the new order.
- **Backups go last.** Items added "just as backups" go to the end of the list; when they'd start late in the night, render the list with `Watched` instead of `Starts`.
- **Soft preferences are soft.** Slot habits and rotation preferences in PERSONAL.md shape suggestions; an explicit instruction from the user always wins, and is never questioned against them.
- **Movie recommendations** draw candidates from the live library (`library list --section <movies> --unwatched`), rank them against the user's Trakt ratings (watched ∪ rated), and give a reason per title that cites the user's own ratings where possible. Present five in Format 1 with a `Why` column; swaps keep the table shape. If PERSONAL.md defines a movie-night shape (e.g. a double feature), offer one pairing in that shape after the table.
- **"New" titles** (not in the library) are a Trakt + arrctl job: candidates from Trakt history/taste, an unseen check (watched ∪ rated, matched case-insensitively on title *substring* plus year — Trakt often stores the long title, e.g. "The Jinx: The Life and Deaths of Robert Durst"), then hand off to arrctl to fetch. Never recommend a title that fails the substring check without saying it's a rewatch.

## Worked Examples

1. **"on deck body cam, signs of psychopath, forensic files"** (first build today) → split → `play-latest --unwatched --key-only` per show → `playlist clear <key> && playlist add <key> K1 K2 K3` → render the schedule table (Starts from the evening start) → "Saved to Up Next Tonight."
2. **"move body cam to slot one, push everything else back"** → `playlist show` fresh → `playlist clear` + `playlist add` in the new order → re-render with Starts.
3. **"recommend five movies for tonight"** → `library list --unwatched` + Trakt ratings → Format 1 with `Why` (nothing written) → user: "one and four" → clear (new night) + add → schedule table → "Saved to Up Next Tonight."
4. **"have I seen the latest episode of slow horses?"** → the *ever watched* question → `episodes "slow horses"` for the ratingKey → `metadata <key>` → `viewCount >= 1` → yes. Add "when" only from a fresh `history` call, and only if asked.
5. **"set all of dark to german audio"** → `set-audio --show "dark" --language deu --all-seasons --dry-run` → show plan (seasons, change count) → wait for explicit yes → re-run without `--dry-run` → report applied/skipped/failed.
6. **"status"** → `playlist show <key>` → drop any `viewCount >= 1` rows (unless it's a rewatch list) → Format 1 with `Watched` + Total.

## Formats and On Deck

Table formats (two shapes only, plus the `Starts` column), the On Deck playlist lifecycle, the command quick-map and the `q`/`p` shortcuts live in `~/.claude/skills/plex/REFERENCE.md` — read it when rendering a list, composing an unfamiliar command, or managing the playlist.

## Debug Mode

`debug` leading token or `--debug` anywhere: echo exact commands before output, restore ID columns, show raw error envelopes verbatim.

---

## Local files

This repo copy is canonical for everything above: generic edits land here and are copied to `~/.claude/skills/plex/SKILL.md`. Three files in the installed skill directory are local-only and never flow back: `PERSONAL.md` (machine-specific preferences), `LESSONS.md` (the self-improvement log) and `INCIDENTS.md` (raw incident lines). Read `PERSONAL.md` first if it exists; its rules override defaults here. If it is missing, run with the defaults.

`PERSONAL.md.example` in this directory lists every value the skill reads from `PERSONAL.md`. On a new machine, copy it to `~/.claude/skills/plex/PERSONAL.md` and fill it in. **The playlist ratingKey is required** — without it, the first `/plex` run must `playlist list`, find "Up Next Tonight" (or create it), and record the key in `PERSONAL.md` before doing anything else; never create a second playlist because the key is unrecorded.

### Skill rules vs local customisations

A **skill rule** is how the skill works for anyone: a mechanism, format, safety gate or lifecycle (proposals vs picks, new night clears, the `Starts` column, exact-ms totals, backups last, substring seen-matching). It lives in SKILL.md / REFERENCE.md and flows to the repo.

A **local customisation** is a value or habit specific to one household: the playlist's ratingKey, the evening start time, the target evening length, slot habits ("show X usually opens the night"), which shows are rotation staples, a movie-night shape, language defaults for particular shows. It lives in PERSONAL.md only, never in the repo.

The test: if a different user would want the same behaviour with different numbers or titles, the behaviour is a skill rule and the numbers/titles are a local customisation. Write the rule with a pointer ("from PERSONAL.md; default X") and keep the value local. When the user states a new preference, put it in PERSONAL.md unless they say it should be a skill rule.

---

## Self-Improvement

Read `~/.claude/skills/plex/LESSONS.md` at every invocation, before acting: `seen: 3+` is binding, `1–2` is a soft steer. Don't announce the read.

Reflection triggers — write a lesson when the turn produced one: `resolution`, `new-error` (a code or behavior not covered here), `output`, `client`, `refused`, `correction`.

**Lesson format.** Append the new lesson under the "Active lessons" heading in LESSONS.md, exactly this shape — frontmatter, then four bold labels:

```
---
trigger: correction
date: 2026-09-03
seen: 1
---
**Context:** what was asked, and the state things were in.
**Mistake:** what went wrong. (Omit for `resolution`.)
**Correction:** the rule to follow instead, concretely.
**Apply when:** the situation that should trigger this rule next time.
```

**Dedup.** Same `trigger` and substantively the same **Apply when** as an existing active lesson → increment that lesson's `seen` and update its `date`. Never append a second body for the same rule.

**Synthesis gate — check this at every invocation.** Count the lesson *bodies* under the "Active lessons" heading (index rows in the Synthesized table are already done and do not count).

- Total ≥ 3, **or** any single trigger with ≥ 2 bodies → open the reply with one line: `LESSONS: 4 active (3 correction, 1 output) — synthesis due`. Then at the END of that same turn, propose the concrete edit — SKILL.md / REFERENCE.md for a skill rule, PERSONAL.md for a local customisation (see above) — and wait for approval.
- On approval: make the edit, collapse **each** synthesised lesson to one index row (date, trigger, gist, destination), and leave the Active section empty.
- Below the threshold, or zero active: say nothing.

Never let a lesson sit active past the gate without announcing it.

**Incident logging** is separate from lessons: one dated line in `INCIDENTS.md` for any `warnings` array in an envelope and any NOT_APPLIED occurrence. An incident requires an actual error code — never log one off an inference.

## Invocation

User invoked `/plex $ARGUMENTS`

1. Startup Recall (LESSONS.md + synthesis gate, PERSONAL.md), detect/strip debug trigger.
2. Empty arguments → render the Up Next Tonight playlist (status, per Worked Example 6).
3. Else parse intent → command(s) → run → render. Route failures on `error.code`; follow `error.hint`.
4. Evaluate reflection triggers; write/increment lessons.

---

## Appendix — Client control (currently unavailable)

Kept for if Plex restores Companion on the Apple TV. **Do not use while client commands return PLEX_CLIENT_INACTIVE.** If they start working again, tell the user before switching any behaviour back.

- **Queue-then-pause.** `queue` auto-starts playback; immediately `plexctl pause` unless the user asked to watch now. The resulting paused-on-item-1 state is your own doing — never report it as an anomaly.
- **`play` only resumes.** Starting a loaded-but-idle queue: `play` exits 6 NOT_APPLIED; respond by `play-media <ratingKey of queue's selected item>` (from `queue-show`), then confirm playing.
- **Seek:** verify nothing — the result's `playState` says where playback landed; re-issue `play` only if it was playing before and `playState` says paused.
- **Queue rows carry no ratingKey** — resolve each with `search "<title>" --type episode --json`, confirming the match on `duration`, before reading its `viewCount`.
- **Watched target in the active queue** → after marking watched, offer removal. Never auto-remove.
- **Unexpected idle after `ok:true`** is ambiguous: report the observation and ASK. Wedge language and an INCIDENTS.md line require an actual error code (PLEX_CLIENT_UNREACHABLE, PLEX_PLAYBACK_NOT_STARTED, NOT_APPLIED). Warn before a commit clobbers an actively playing or paused item, in one clause.
- `now-playing` renders exactly per REFERENCE's template.

| Code | Say |
|---|---|
| PLEX_NOTHING_PLAYING | Nothing playing — give me a row number. |
| PLEX_SEEK_FAILED | Seek stumbled (check `data.seeked`/`repaused` for how far it got). |
| PLEX_CLIENT_UNKNOWN / _AMBIGUOUS | Client problem — say which per the message; recovery per hint. |
| PLEX_CLIENT_UNREACHABLE | Apple TV isn't responding. If Plex is open on it, relaunch the app. Don't retry until the user says it's back. |
| PLEX_QUEUE_CREATE_FAILED | Couldn't create the queue. Nothing added. |
| PLEX_QUEUE_STAGED | Made the queue, but the Apple TV didn't respond. Once it's awake say "start the queue". (Recovery: `queue-start`, never re-`queue`.) |
| PLEX_QUEUE_CONFLICT | Made the queue, but one's already active and the device isn't answering. (Recovery: re-run `queue`, NOT `queue-start`.) |
| PLEX_PLAYBACK_NOT_STARTED | The Apple TV accepted it but nothing started. Relaunch Plex on it — recovery per hint. |
| PLEX_NO_QUEUE | Nothing queued yet — queue something first. |
| PLEX_QUEUE_PARTIAL | Added the first N, then hit an issue. Try again with the rest (`data.added`). |

Incidents (client era): PLEX_CLIENT_UNREACHABLE during queue operations also gets an INCIDENTS.md line.
