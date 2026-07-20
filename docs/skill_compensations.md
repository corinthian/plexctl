# Skill Compensations Inventory (P0.2)

Generated 2026-07-19. Source: `~/.claude/skills/plex/SKILL.md` (886 lines), `~/.claude/skills/plex/LESSONS.md` (85 lines). Read-only extraction — no skill or binary files touched. Purpose: catalogue every rule the skill carries to compensate for plexctl's current error contract (free-text errors, no codes, exit 0/1/2/64 only), as input to the error-model-v2 hardening plan.

## A. Error Translation Table (skill's `## Error Translation`, 33 rows)

| Skill rule | Binary gap it compensates | Candidate fix |
|---|---|---|
| 1. `nothing playing — provide a ratingKey` | free-text, no code | `PLEX_NOTHING_PLAYING` |
| 2. `nothing found for: '<q>'` | free-text | `PLEX_NOT_FOUND` |
| 3. `no unwatched episodes for: '<q>'` | free-text, conflated with #2 | new `PLEX_ALL_WATCHED` |
| 4. `client not found: <x>` | free-text | `PLEX_CLIENT_UNKNOWN` |
| 5. `is registered but not active` | free-text | `PLEX_CLIENT_INACTIVE` |
| 6. `ambiguous client name` | free-text | `PLEX_CLIENT_AMBIGUOUS` |
| 7. transport error on `/player/`/`:32500` URL | no code at all — skill must string-sniff the URL to know it's the client, not the server | `PLEX_CLIENT_UNREACHABLE` (binary should classify target itself, not leave URL-parsing to caller) |
| 8. error starts `plex.tv ` | same URL-sniffing gap for the cloud path | `CLOUD_UNREACHABLE` |
| 9. `query cannot be empty` | free-text arg validation | `BAD_REQUEST` |
| 10. `show cannot be empty` | free-text | `BAD_REQUEST` |
| 11. `ambiguous show` | free-text, no equivalent of client-ambiguous code | new `PLEX_SHOW_AMBIGUOUS` |
| 12. `spans N seasons` | free-text refusal, not "ambiguous", not "not found" — a third shape | new `PLEX_SCOPE_REQUIRED` |
| 13. `no <lang> audio/subtitle track` | free-text | new `PLEX_TRACK_NOT_FOUND` |
| 14. `--stream-id is single-item only` / mutually exclusive / `provide RATING_KEY` | free-text arg errors already exit 64 | `BAD_REQUEST` (formalize existing exit-64 convention) |
| 15. `could not determine current playback position` | free-text | `PLEX_NOTHING_PLAYING` (reuse) |
| 16. `could not resume before seek:` | free-text, compound seek failure | new `PLEX_SEEK_FAILED` |
| 17. `seeked but failed to restore pause state:` | free-text, partial-success not structured | `PLEX_SEEK_FAILED` + `repaused:false` field |
| 18. `unrecognised position format:` | free-text | `BAD_REQUEST` |
| 19. `invalid seek position:` | free-text | `BAD_REQUEST` |
| 20. `playQueue creation returned no playQueueID` | free-text | new `PLEX_QUEUE_CREATE_FAILED` |
| 21. dict has `partialQueueID`/`rollbackAttempted` | ad hoc keys, no code | `PLEX_QUEUE_CREATE_FAILED` + structured rollback fields |
| 22. result has `staged: true` | meaning inferred from key presence, not a code | `PLEX_QUEUE_STAGED` |
| 23. result has `clientUnreachable: true` | ad hoc flag | `PLEX_CLIENT_UNREACHABLE` |
| 24. `playback never started` (`clientEngaged: false`) | free-text + ad hoc flag | `PLEX_PLAYBACK_NOT_STARTED` |
| 25. `no active queue on <client>` (queue-start) | free-text | `PLEX_NO_QUEUE` |
| 26. `HTTP 404` (raw body included) | raw HTTP leaked into error string, meaning varies by command | `PLEX_NOT_FOUND` (or `PLEX_NO_QUEUE` for queue-show — see §C) |
| 27. `connection failed:` / `request failed:` | free-text | `TRANSPORT_FAILED` |
| 28. `request timed out:` | free-text | `TRANSPORT_TIMEOUT` |
| 29. `missing config key:` | free-text | `PLEX_AUTH_REQUIRED` |
| 30. `smart collection: contents are query-driven` | free-text | `PLEX_SMART_CONTAINER` |
| 31. `smart playlist: contents are query-driven` | free-text, duplicate of #30 | `PLEX_SMART_CONTAINER` (+ `kind:"playlist"`) |
| 32. `section <ID> is not a movie or show section` | free-text | `BAD_REQUEST` |
| 33. anything else `ok:false` | true catch-all — no bound on free-text surface | invariant: every `ok:false` MUST carry one of the above codes; catch-all message stays in skill only for genuinely unclassified errors |

## B. Refused Upfront (3 rows) — skill hardcodes a ban list because the binary's failure signal is misleading

| Skill rule | Binary gap | Candidate fix |
|---|---|---|
| shuffle/`queue-shuffle` refused, real call returns raw 404 | 404 reads as "not found" when it means "not implemented" | new `PLEX_UNSUPPORTED` |
| unshuffle refused, same 404 | same | `PLEX_UNSUPPORTED` |
| volume commands refused, real call returns `ok:true` no-op | silent no-op looks like success | `NOT_APPLIED` |

## C. Queue Failure State Machine — prose beyond the table

| Skill rule | Binary gap | Candidate fix |
|---|---|---|
| "Two failure shapes — resolve vs bind": pre-create client failures are safe, post-create bind failures leave server state | not signaled — skill infers from key presence and command ordering | invariant: return `PLEX_CLIENT_INACTIVE`/`PLEX_CLIENT_UNREACHABLE` pre-create, `PLEX_QUEUE_STAGED` only post-create |
| "dead-zone" bind failure — bind fails, no `staged` key, because another queue is already active client-side | undocumented third outcome, distinguished only by an *absent* key | new `PLEX_QUEUE_CONFLICT` (explicit code instead of key-absence inference) |
| `queue-show` 404 vs empty: both render "No queue right now" but mean different things (pruned vs legitimately empty) internally | overloaded 404 | `PLEX_NO_QUEUE` (unify both under one code, drop raw 404 leak) |
| `queue-add` "no active queue" and `queue-add` partial success (`added: N > 0`) | free-text + ad hoc partial-success field | `PLEX_NO_QUEUE`; new `PLEX_QUEUE_PARTIAL` for partial adds |
| `stateSaved: false` in envelope (Incident Logging trigger) — persisted-state write failed, currently has zero user-facing translation, only logged | pure gap — not even in the error table yet | new `PLEX_STATE_SAVE_FAILED` |

## D. Per-command landmines

| Skill rule | Binary gap | Candidate fix |
|---|---|---|
| `play` silently no-ops from idle (Apple TV accepts, returns `ok:true`); skill must detect idle state and swap to `play-media` | `ok:true` on a command that did nothing | invariant: `play` on idle client returns an error/code (e.g. `PLEX_PLAYBACK_NOT_STARTED`) instead of a false-positive success |
| Show-row `duration` is nominal (typical episode length), not a real runtime — skill must never render it as one, never sum it | field is silently overloaded, no flag distinguishing nominal vs actual | invariant: binary adds `nominal:true`/omits `duration` on show-level rows |
| `--min-score` banned outright, including retry-on-empty (a former skill rule now flagged actively harmful) | flag exists and bypasses the relevance guard, producing wrong-but-plausible results | invariant: remove/deprecate `--min-score`; it should not exist as a footgun |
| `search` caps at ~10 results, skill must know to switch to `episodes` for full-show listings | silent truncation, no signal of how many were dropped | invariant: return `truncated:true` + total count |
| LESSONS 2026-07-18: `play-media` requires `ratingKey`, not `selectedItemID`/`playQueueItemID` — passing the wrong ID space returns raw `HTTP 400` | undifferentiated 400, two ID spaces look interchangeable | `BAD_REQUEST` with a structured `expected: "ratingKey"` field |
| Smart collection/playlist guard — CLI already refuses with a coded-shaped message; skill duplicates the check pre-flight purely to save a round trip | none really — mostly resolved already | `PLEX_SMART_CONTAINER` (formalize the existing message into the real code so skill's pre-check becomes an optimization, not a necessity) |
| `viewCount`/`skipCount`/`viewedLeafCount` are not authoritative watch-state — skill bans inferring "watched" from them (Library Browse line 539, Execution Policy line 794, LESSONS 2026-07-19 double-failure) and mandates a fresh `history` call before any watched/unwatched statement | binary exposes several watch-adjacent counters, none of which reliably answers "is this watched" per item; caller must cross-reference a second endpoint | invariant: binary returns an authoritative per-item `watched:bool` (and per-show `unwatchedCount`) so no cross-referencing is needed |
| Post-seek play-state drift: on a playing client, seek can silently leave it paused; skill must remember pre-seek state, re-check `now-playing`, re-issue `play` if needed (Seek section, line 195) | seek's result doesn't report/guarantee resulting play state | invariant: `seek` response includes resulting `playState`, or guarantees pre-seek state is preserved |

## E. Auth failure handling

Covered by row 29 above (`missing config key:` → `PLEX_AUTH_REQUIRED`). No separate auth-specific state machine beyond this single translation.

## F. Stays in skill (presentation / personalization / UX policy — not error-contract gaps)

Row-number ID hiding, debug-mode toggling, the two list-table formats (Format 1/2), runtime math and `Total:` line rules, `loose:true` fuzzy-match phrasing, Standing Shortcuts (`q`/`p`), On Deck list lifecycle, "confirm before bulk write" gate (CLI-doesn't-prompt is a deliberate design choice, not a contract gap), "never curl the API", "don't invent plexctl limitations", "back-to-back device commands don't collide" (already solved binary-side, not a compensation), client-side `--unwatched` filtering (data-correctness choice, no error signal involved), personalization section, all Self-Improvement/Incident-Logging meta-process.

Fuzzy-resolution verification (skill must check echoed `show`/`showRatingKey` against the user's intended title, lines 235/249) is defensibly left here too: the binary already echoes the resolved identity for comparison, and the underlying search-floor bug is fixed (LESSONS 2026-07-09 root-cause note) — this is a caller-side sanity check on a working resolver, not a contract gap.

---

**Acceptance:** Error Translation table row count: **33**. Mapped (code or "stays in skill"): **33/33** — all 33 rows in §A assigned a candidate code or explicit invariant change; none deferred to "stays in skill" (every row is error-contract-shaped by definition of being in that table).
