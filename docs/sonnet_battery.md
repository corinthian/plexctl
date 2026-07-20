# Sonnet Task Battery (frozen 2026-07-19)

Purpose: measure whether moving invariants from the plex skill into the plexctl binary closes the Sonnet-vs-Opus gap on `/plex` tool use. This battery is FROZEN before any v2 code merges. Do not edit tasks after this commit — a changed battery invalidates the A/B comparison.

Protocol: run every task on a Sonnet-class agent twice — (A) v1 binary + old skill (commit this file lands on, before Phase 1), (B) v2 binary + rewritten skill (after Phase 4). Same wording, same preconditions. Tasks needing an injected failure run against a replay/mock harness or manual injection; the injection mechanism is the runner's choice but must be identical across A and B.

Scoring per task: `violations` = count of binding skill rules broken (rules listed per task); `wrong-command` = 0/1 primary command choice wrong; `recovery` = 0–2 (0 = wrong/no recovery, 1 = recovers with detours, 2 = clean). Report totals per run plus per-task notes.

## Tasks

### T1 — mangled dictation, play next
Utterance: "play the next episode of startek progidy"
Precondition: Star Trek: Prodigy in library, unwatched episodes exist, Apple TV active.
Rules in play: resolve mangled title; `play-latest --unwatched` (never bare `play`); "first/next means first unwatched"; output format (Now playing + description + runtime).

### T2 — search miss stays honest
Utterance: "find the god father"
Precondition: The Godfather NOT in library.
Rules in play: never pass `--min-score`, never retry with it; empty = honest "nothing found"; no invented availability.

### T3 — loose match confirmation
Utterance: "search for blak buks"
Precondition: Black Books in library; result carries `"loose": true`.
Rules in play: loose hit is named as unconfirmed ("Closest I get is…"), not presented as a clean find; Format 1 table if multiple rows.

### T4 — queue three, then pause
Utterance: "queue up the next three episodes of black books"
Precondition: ≥3 unwatched episodes; client active.
Rules in play: `episodes "<show>" --unwatched --json` (not search, no per-episode metadata loop); one `queue K1 K2 K3`; queue auto-starts → immediate `pause` (queue-then-pause); report "Queue created — 3 items", paused state not narrated as anomaly.

### T5 — bind failure, staged
Utterance: "queue up the next episode of slow horses" (then, after failure report: "okay try now")
Precondition: injected — `queue` bind fails with `staged: true`, `clientUnreachable: true`.
Rules in play: recovery is `queue-start`, NOT re-running `queue`; wording says queue is made and recoverable; no blind retry before user says device is back.

### T6 — bind failure, not staged
Utterance: "queue rocky"
Precondition: injected — bind fails, NO `staged` key (an active queue already recorded).
Rules in play: recovery is re-running `queue` later, NOT `queue-start` (which would start the OLD queue); wording distinguishes this from the staged case.

### T7 — play from idle
Utterance: "play"
Precondition: queue loaded, `now-playing` reports `idle`.
Rules in play: skip bare `plexctl play` (silent no-op on idle); bootstrap via `play-media` on the selected item's ratingKey; verify state flipped to `playing` before reporting success.

### T8 — watched status grounded in history
Utterance: "have I seen the latest episode of slow horses?"
Precondition: episode present in history; also sitting in queue.
Rules in play: run `plexctl history` before any watched claim; never infer watched from queue position, continue-watching, or memory.

### T9 — status render
Utterance: "what's going on?"
Precondition: something paused, 2-item queue, one queue item already in recent history.
Rules in play: fresh `now-playing` + `queue-show` + `history --limit 5` (not the cached startup bundle); queue table cross-referenced with history → Watched ✓ column; Format 1 shapes; Total line.

### T10 — show rows and nominal durations
Utterance: "list my TV shows with how long they run"
Precondition: library has shows; show rows carry nominal `duration`.
Rules in play: show-row duration rendered `~Nm typical` or blank, never bare; no `Total:` line when show rows present; watched column from `unwatchedLeaves`, never `viewCount`.

### T11 — client transport failure routing
Utterance: "pause it"
Precondition: injected — transport timeout on a `/player/`:32500 URL.
Rules in play: route on URL → Apple TV wording ("isn't responding… relaunch the app"), not generic "can't reach Plex"; no blind retry of device commands; raw error never shown.

### T12 — not set up
Utterance: "what's playing?"
Precondition: injected — `missing config key:` error (no auth).
Rules in play: translate to "plexctl not set up. Run `plexctl auth login`."; no raw error; no curl workaround.

### T13 — smart collection edit refusal
Utterance: "add rocky to my comfort movies collection"
Precondition: Comfort Movies is a smart collection (visible in prior `collection list` row).
Rules in play: skill-side pre-check of `smart` flag — refuse BEFORE shelling out the mutation; exact app-edit wording; no delete-and-recreate.

### T14 — bulk write gate
Utterance: "set all of dark to german audio"
Precondition: Dark spans multiple seasons.
Rules in play: `--dry-run` first; show plan (show, season breakdown, change count); explicit user go-ahead before real write; multi-season needs `--all-seasons`; `--language deu` inferred.

### T15 — row-number follow-up
Utterance: "search for rocky" then "play #2"
Precondition: search returns ≥2 matches.
Rules in play: numbered table, no auto-pick; `#2` resolved against row-map → `play-media <ratingKey>`; internal IDs hidden in default mode.

### T16 — refused upfront
Utterance: "shuffle the queue"
Precondition: queue exists.
Rules in play: refuse without invoking plexctl; exact "Shuffle isn't supported" wording.

## Notes

- T5, T6, T11, T12 require injection; all others run live-read-only or with reversible writes (T4/T14 write real state — run against a test-safe show or undo after).
- The A run must happen on the v1 binary BEFORE Phase 1 merges. Record raw transcripts; score after both runs to avoid drift in the rubric.
