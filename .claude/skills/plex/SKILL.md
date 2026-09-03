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

# Plex Skill (v2 — requires plexctl ≥ 2.0.0)

Goal: smooth find / watch / play UX. Hide plexctl noise. Never surface internal IDs, raw envelopes, or codes unless `debug_mode`.

**Version gate:** on the first plexctl call of a session, if behavior looks pre-2.0.0 (a flat `"error": "text"` string instead of an error object) run `plexctl --version`; below 2.0.0 → stop and say: "plexctl is below 2.0.0 — upgrade the binary to 2.0.0 or later before using this skill." Do not attempt workarounds for older binaries.

---

## Hard Rules

- **Never run `curl`** (or any direct HTTP) against Plex. Stay inside `plexctl`. A capability gap is a missing feature to report, not to work around.
- **Don't invent plexctl limitations.** An `ok:true` empty result isn't proof of a missing feature. Run the command, report plainly. A gap is real only if documented here or the binary refuses it (`PLEX_UNSUPPORTED`).
- **Confirm before any bulk write.** Bulk `set-audio` always gets `--dry-run` first; show the plan (show, season breakdown, change count) and get an explicit go-ahead. The CLI doesn't prompt — this gate is yours.
- **Watched status is two questions with two sources.**
  - *Ever watched this item?* → per-item `viewCount`, read fresh via `metadata RATING_KEY`. **`viewCount >= 1` IS watched — that is the authority.** An item can be flagged viewed with no history row at all (mark-watched and credit-skip paths). `queue-show` rows carry no ratingKey, so resolve each row first — `search "<title>" --type episode --json`, confirm the match by comparing the queue row's `duration` to the result's — then read that ratingKey's `viewCount`.
  - *What was watched recently, and when?* → `plexctl history`, run fresh. **History takes precedence for anything about recency** — favourites get rewatched, so a `viewCount` says nothing about *when*.
  - Neither source vetoes the other on its own question. `history` missing an item never overrides `viewCount >= 1`; `viewCount` never answers "recently".
  - **`skipCount` is not evidence either way.** An episode watched through and then credit-skipped to advance records `viewCount: 1` with `skipCount: 1` — that is a *completed* episode, the normal signature. Never raise it as doubt or ask for confirmation of that pattern. (`skipCount` is often absent from the payload entirely.)
  - Never infer per-item watch state from queue position, `continue-watching`, show-level `skipCount` / `viewedLeafCount` arithmetic, or memory. (Show-*row* Watched columns still use `unwatchedLeaves` per REFERENCE — that is a different question.)

## The Error Contract

Every failure is `{ok:false, error:{code, message, http_status?, hint?}, data?}` on stdout, exit codes: 1 bad invocation, 2 Plex refused, 3 transport, 4 plexctl bug, 5 not logged in, 6 accepted-but-nothing-happened.

Two rules:

1. **Route on `error.code`, phrase per the table below.** Never show raw envelopes or codes (except debug).
2. **`error.hint` is the recovery — follow it exactly.** When a hint names a command (`queue-start`, `play-media RATING_KEY`, `auth login`), that IS the next step; don't improvise alternatives or blind-retry.

| Code | Say (adapt naturally) |
|---|---|
| BAD_REQUEST | (your command was malformed — fix and re-run silently; only surface if you can't) |
| PLEX_AUTH_REQUIRED | plexctl isn't logged in. Run `plexctl auth login`. |
| PLEX_AUTH_FAILED | Login didn't work — check the credentials. |
| PLEX_NOTHING_PLAYING | Nothing playing — give me a row number. |
| PLEX_NOT_FOUND | Nothing found for "<q>". / Plex couldn't find that. |
| PLEX_ALL_WATCHED | All caught up on "<show>". |
| PLEX_SHOW_AMBIGUOUS | Several shows match — which one? (pick via `data.matches`, disambiguate per hint) |
| PLEX_SCOPE_REQUIRED | That's N seasons. All of them, or just one? |
| PLEX_TRACK_NOT_FOUND | No <lang> track on that one. |
| PLEX_SEEK_FAILED | Seek stumbled (check `data.seeked`/`repaused` for how far it got). |
| PLEX_CLIENT_UNKNOWN / _INACTIVE / _AMBIGUOUS | Client problem — say which per the message; recovery per hint. |
| PLEX_CLIENT_UNREACHABLE | Apple TV isn't responding. If Plex is open on it, relaunch the app. Don't retry until the user says it's back. |
| CLOUD_UNREACHABLE | plex.tv is unreachable — your server is fine; try shortly. |
| TRANSPORT_TIMEOUT | Plex was slow to answer — retrying may work. (Batches: retry only TIMEOUT items.) |
| TRANSPORT_FAILED / PLEX_SERVER_ERROR / PLEX_HTTP_ERROR | Can't reach Plex right now. / Plex errored. |
| PLEX_QUEUE_CREATE_FAILED | Couldn't create the queue. Nothing added. |
| PLEX_QUEUE_STAGED | Made the queue, but the Apple TV didn't respond. Once it's awake say "start the queue" — no need to rebuild. (Recovery: `queue-start`, never re-`queue`.) |
| PLEX_QUEUE_CONFLICT | Made the queue, but one's already active and the device isn't answering — couldn't stage the new one. Ask me to queue it again when it's back. (Recovery: re-run `queue`, NOT `queue-start`.) |
| PLEX_PLAYBACK_NOT_STARTED | The Apple TV accepted it but nothing started. Relaunch Plex on it and tell me — recovery per hint. |
| PLEX_NO_QUEUE | Nothing queued yet — queue something first. |
| PLEX_QUEUE_PARTIAL | Added the first N, then hit an issue. Try again with the rest (`data.added`). |
| PLEX_SMART_CONTAINER | That's a smart collection/playlist — edit its rule in the Plex app. |
| PLEX_UNSUPPORTED | Not supported (shuffle/volume) — use the Plex app UI / TV remote. |
| NOT_APPLIED (exit 6) | Plex accepted it but nothing actually changed — say so; follow the hint (e.g. idle `play` → `play-media` the queue's selected item's ratingKey from `queue-show`). |
| INTERNAL | plexctl bug — surface the message, suggest `debug`. |

Success envelopes may carry `warnings` (e.g. PLEX_STATE_SAVE_FAILED). Mention a warning only if it affects what the user does next.

## Core Behaviors

- **Row numbers, not IDs.** Any list with `ratingKey`/`playQueueItemID` gets a `#` column; keep the #→ID map in context for "play #2". Debug mode appends ID columns.
- **Queue-then-pause.** `queue` auto-starts playback; immediately `plexctl pause` unless the user asked to watch now. The resulting paused-on-item-1 state is your own doing — never report it as an anomaly.
- **`play` only resumes.** Starting a loaded-but-idle queue: `play` exits 6 NOT_APPLIED; respond by `play-media <ratingKey of queue's selected item>` (from `queue-show`), then confirm playing.
- **Seek:** verify nothing — the result's `playState` says where playback landed; re-issue `play` only if it was playing before and `playState` says paused.
- **Compound titles split first.** A dictated phrase that joins titles with "and" ("Fallout and the Virtues, and After the First 48") is split into candidate titles *before* any lookup, and each one is resolved independently with its own `play-latest --unwatched --key-only "<clean title>"`. Never reuse a resolution obtained from the unsplit string, and never fall back to show-level `skipCount`/`viewedLeafCount` arithmetic for an unwatched count — a direct `--unwatched` query or per-episode `viewCount` is the only authority.
- **The answer stands alone.** A confirmation is one line, or the table — nothing after it. Never append what a commit will displace, what is still sitting in the previous queue, that an episode advanced or got watched since last time, or that media has gone missing (a 404 on a previously-watched key and a null-runtime history row are expected downstream of the user deleting watched films — render the row blank and say nothing; investigate absence only if asked outright). Never state what is queued, playing or paused unless it was asked *this turn* and queried *this turn* — a bare 👍 / "ok" / "thanks" gets no state report at all. An unexpected idle or stop after an `ok:true` command is ambiguous, not diagnostic: report the observation ("nothing is playing; queue still selected on item 1") and ASK. Wedge language and an INCIDENTS.md line require an actual error code (`PLEX_CLIENT_UNREACHABLE`, `PLEX_PLAYBACK_NOT_STARTED`, exit 6 `NOT_APPLIED`) — never an inference. The existing-queue warning survives only for its documented case (an actively playing or paused item is about to be clobbered), and then as one clause.
- **`search` caps ~10 results** — for whole-show episode lists use `episodes "<show>" [--unwatched]`, not search. `"loose": true` hits are unconfirmed: "Closest I get is <Title> (<year>) — that it?"
- **An empty search result is final.** Report "Nothing found" and STOP — never enumerate the library (`library list` scans) to double-check absence. One corrected-spelling retry is fine; expeditions are not.
- **`durationNominal: true`** rows (shows): render `~43m typical` or blank, never a bare runtime, and no `Total:` line on tables containing them. Movie durations are real.
- **Show-identity echo:** `episodes`/`audit-audio`/bulk results echo `show`+`showRatingKey` — if it isn't the show the user named, flag it instead of rendering the wrong series.
- **Watched target in the active queue** → after marking watched, offer removal ("Want it out of the queue too?"). Never auto-remove.
- **Edits mutate in place** (`collection add`/`playlist remove`/…) — never delete-and-recreate; rebuild only on explicit "start over". Playlist removal takes `playlistItemID` (from `playlist show`), not ratingKey.
- **No confirmation prompts** for unambiguous single commands (stop, queue-clear, rate 0, delete) — run immediately. The one gate is bulk set-audio (above).
- **Startup recall:** run `plexctl context --history-limit 5` once per turn for read-only state questions; degrade gracefully if it fails (most asks don't need the client). Re-query fresh before any state-mutating action, and always re-render "status" asks fresh (`now-playing` + `queue-show` + `history --limit 5`). Render now-playing EXACTLY per REFERENCE's template. The Watched column on queue rows comes from each row's own `viewCount` (resolve the row to a ratingKey first, per the Hard Rule) — never from the title turning up in `history`, and this includes the currently playing/paused item.
- **Chain with `&&`, no banners.** Multi-step plexctl sequences go in ONE bash call joined by `&&` (never `;`, never `echo "=== X ==="` separators). Each envelope is self-identifying; `&&` stops at the first failure so the last line is always the failing step.
- **Discovery:** unsure of a flag or command? `plexctl commands` (JSON tree) or `plexctl <cmd> --help`. Never guess flags.

## Worked Examples

1. **"play the next episode of strange of things"** → mangled dictation → `plexctl play-latest --unwatched "stranger things"` → success: `Now playing: Stranger Things — S05E01 — … (1h 12m)` + one-line description (fetch `metadata` once). "First/next" always means first *unwatched*; never S01E01 unless they say pilot.
2. **"queue the next three episodes of black books"** → `episodes "black books" --unwatched --json` → take first 3 ratingKeys → `queue K1 K2 K3` → immediately `pause` → "Queue created — 3 items, ready when you are."
3. **`queue` fails with PLEX_QUEUE_STAGED** → say the queue is made and recoverable; when user says the device is awake → `queue-start` (the hint said so — never rebuild).
4. **"have I seen the latest episode of slow horses?"** → that's the *ever watched* question → `search "slow horses" --type episode --json` (or `episodes "slow horses"`) to get the episode's ratingKey → `metadata <key>` → `viewCount >= 1` → yes. Add "when" only from a fresh `history` call, and only if asked.
5. **"set all of dark to german audio"** → `set-audio --show "dark" --language deu --all-seasons --dry-run` → show plan (seasons, change count) → wait for explicit yes → re-run without `--dry-run` → report applied/skipped/failed.
6. **"shuffle the queue"** → run it; binary answers PLEX_UNSUPPORTED → "Shuffle isn't supported — use the Plex app UI."

## Formats, On Deck, Shortcuts

Table formats (two shapes only), the On Deck curated-list lifecycle, and the `q`/`p` standing shortcuts live in `~/.claude/skills/plex/REFERENCE.md` — read it when rendering a list, composing an unfamiliar command, managing the On Deck list, or on a bare `q`/`p`/"queue" input.

## Debug Mode

`debug` leading token or `--debug` anywhere: echo exact commands before output, restore ID columns, show raw error envelopes verbatim.

---

## Personalisation (local-only)

<!-- fenced:start -->
> Local-only; ships empty. Record machine-specific preferences (viewing schedule, dictation habits, default watched/unwatched lens) between these markers in the installed copy at `~/.claude/skills/plex/SKILL.md`. The installed (NUC) copy is canonical: generic edits land there first and flow here with this section emptied; personal content never comes back here.
<!-- fenced:end -->

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

- Total ≥ 3, **or** any single trigger with ≥ 2 bodies → open the reply with one line: `LESSONS: 4 active (3 correction, 1 output) — synthesis due`. Then at the END of that same turn, propose the concrete SKILL.md / REFERENCE.md edit and wait for approval.
- On approval: make the edit, collapse **each** synthesised lesson to one index row (date, trigger, gist, destination), and leave the Active section empty.
- Below the threshold, or zero active: say nothing.

Never let a lesson sit active past the gate without announcing it.

**Incident logging** is separate from lessons: one dated line in `INCIDENTS.md` for any `warnings` array in an envelope, PLEX_CLIENT_UNREACHABLE during queue operations, and any NOT_APPLIED occurrence. An incident requires an actual error code — never log one off an inference.

## Invocation

User invoked `/plex $ARGUMENTS`

1. Startup Recall (LESSONS.md + synthesis gate), detect/strip debug trigger.
2. Empty arguments → `plexctl now-playing`, render.
3. Else parse intent → command(s) → run → render. Route failures on `error.code`; follow `error.hint`.
4. Evaluate reflection triggers; write/increment lessons.
