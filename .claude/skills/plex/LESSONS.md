# Plex Skill — Lessons (Index)

The skill's self-improvement log. Every lesson below has been **synthesized into SKILL.md**; the verbose bodies were collapsed to this index on 2026-06-18 to keep the file lean (it is read on every `/plex` invocation). Each row records the date, trigger, the gist, and the SKILL.md section where the rule now lives. This collapse supersedes the per-lesson `synthesized: true` marking for everything already promoted.

New corrections still get appended **in full** under "Active lessons" per the Self-Improvement protocol (SKILL.md → Lesson Format / Synthesis Threshold). Once a lesson is synthesized into SKILL.md, collapse it into the index below.

## Synthesized lessons

| Date | Trigger | Lesson | Now in SKILL.md |
|---|---|---|---|
| 2026-05-07 | resolution | "After the X" dictation = the spinoff, not the parent | Personalisation |
| 2026-05-07 | correction | Never curl the Plex API — surface gaps as bugs, don't work around | Hard Rules |
| 2026-05-10 | resolution | "First episode" = first *unwatched*, never S01E01 | Play Latest / Next Episode |
| 2026-05-13 | client | Idle client won't `play` — use `play-media` on the selected key | Transport (idle bootstrap) |
| 2026-05-14 | correction | Watched target in the active queue → offer to remove it | Watch Status & Rating |
| 2026-05-14 | correction | `queue` auto-starts then pauses — that state is expected, not anomalous | Queue Create |
| 2026-05-17 | correction | Don't refuse a user command on an inferred plexctl gap — run it | Hard Rules |
| 2026-05-17 | output | `search` caps ~10 — use `episodes` for whole-show lists | Library Search / Episodes |
| 2026-05-20 | client | `seek` leaves the client paused → follow with `play` if it was playing | Seek |
| 2026-05-22 | correction | Fri/Sat nights are movie nights | Personalisation |
| 2026-05-23 | output | Every list shows runtime per row + a `Total:` line | List Formatting Rules |
| 2026-05-25 | correction | Re-verify every On Deck item vs history on every touch | On Deck Rules |
| 2026-05-31 | resolution | Bare title → lead with `play-latest`, not `search` | Ambiguity Rules / Episodes |
| 2026-06-06 | correction | Don't editorialize a user-initiated playback change as "off-list" | Queue Create |
| 2026-06-06 | correction | Watched check = `.metadata.viewCount` (absent = unplayed) — jq/grep-ban now obsolete (guard hook deleted) | Watch Status (viewCount) |
| 2026-06-08 | correction | Per-show episode enumeration works — retry `--min-score 0` before calling it a gap | Library Search / Episodes |
| 2026-06-10 | correction | **Always re-check state before acting — never trust prior-turn data** (seen: 5, synthesized) | Execution Policy + On Deck Rules + Startup Recall |
| 2026-06-12 | refused | Audio-track writes are a real command now (`set-audio`) — was curl-only | Set Audio / Subtitle Track |
| 2026-06-14 | correction | "default / automatic / regular" audio = the *selected* track (only writable knob) | Set Audio / Subtitle Track |
| 2026-06-14 | new-error | `--min-score` is a `search` flag only — never on `play-latest` / `episodes` | Library Search |
| 2026-06-14 | output | "Status" = 3 fresh queries (now-playing + queue-show + history) + Watched column | Context (Startup Bundle) |
| 2026-07-06 | new-error | Client timeouts surface as `request timed out:` on a `/player/` URL — the `transport error contacting` row never existed in plexctl; route transport errors by URL | Error Translation |
| 2026-07-06 | client | 2026-07-05 companion wedge: ATV port 32500 stopped answering with device on and app foregrounded; blind retries orphaned server queues; report observations, never assert device state | Error Translation / Queue Create |
| 2026-07-06 | correction | Bind-failed `queue` leaves a live server queue with `queue-show` empty; degrade `context` failures | Queue Create / Queue Inspect / Startup Recall |
| 2026-07-06 | correction | Always favour unwatched when the user refers to shows/movies (search/list/recommend/disambiguate) unless they say rewatch/already-seen | Personalisation |
| 2026-07-06 | correction | Shortcuts: "queue"/`q` = commit On Deck to a queue then pause (ready, not playing); `p` = play/resume. `q` no longer = queue-show | Personalisation |

## Active lessons (not yet synthesized)

_None — all captured lessons are in SKILL.md. New full-body lessons get appended here until synthesized._

---
trigger: correction
date: 2026-06-25
seen: 1
---
**Context:** Suggested a weeknight TV schedule anchored at 7:00pm.
**Mistake:** Wrong start time — user said "TV time starts at 6pm."
**Correction:** Anchor evening viewing schedules at 6:00pm, not 7:00pm.
**Apply when:** Building or proposing any On Deck / evening TV schedule with clock times — start the first slot at 6:00pm unless the user says otherwise.

---
trigger: correction
date: 2026-07-09
seen: 2
---
**Context:** User asked for "Conversations with a Killer: The Ted Bundy Tapes." `plexctl search "Ted Bundy"` (and several alternate-title queries) returned only unrelated fuzzy junk, so I told the user it wasn't on the server.
**Mistake:** The show WAS in the library (rk 45692). `search`'s fuzzy ranking buried the exact title under low-relevance matches; I wrongly declared a gap and disregarded the user's correct insistence.
**Correction:** When `search` returns junk for a title the user is confident exists, do NOT conclude it's absent. Enumerate the real catalog and grep it: `plexctl library list --section 3 --type show | jq -r '.items[].title'` (and section 1 for movies), grep -i for distinctive words. The catalog is authoritative; fuzzy search is not.
**Apply when:** Any title lookup where `search` comes back empty/junk but the user asserts the title exists — grep the library catalog before reporting it missing.

