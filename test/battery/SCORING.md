# Battery Scoring: A (v1 binary + 886-line skill) vs B (v2 binary + 213-line skill)

Scored 2026-07-20 from transcripts + shim call logs, per the rubric in `docs/sonnet_battery.md`. Both runs: Sonnet, one fresh agent per task. Violations are counted against the skill that was ACTIVE for that run — where v2 deliberately relaxed a v1 rule (T13 pre-check, T16 upfront refusal), the relaxed behavior is not a violation, but the UX delta is noted.

## Per-task

| Task | A viol | A wrong-cmd | A recov | B viol | B wrong-cmd | B recov | Notes |
|---|---|---|---|---|---|---|---|
| T01 play mangled | 0 | 0 | — | 0 | 0 | — | Both clean, 2 calls each. B dropped the "Description:" label (trivial). |
| T02 honest empty | 0 | 0 | — | 1 | 1 | — | **B's worst task**: correct final answer, but a ~20-call library-enumeration expedition to "confirm" absence. A stopped after 2 searches. The v1 rule's "report it and stop" clause was dieted out of the v2 skill — it was load-bearing. |
| T03 loose match | 0 | 0 | — | 0 | 0 | — | Both confirmed instead of asserting. A also rendered the table row; B's bare confirm line is defensible under v2 text. |
| T04 queue 3 + pause | 0 | 0 | — | 0 | 0 | — | Identical correct sequence; B leaner (3 calls vs 5, no post-pause verify — not required by v2). |
| T05 staged bind | 0 | 0 | 2 | 0 | 0 | 2 | Both: queue-start, never rebuild, no blind retry, pause after start. B got recovery from `error.hint`; A from memorized prose. |
| T06 conflict bind | 0 | 0 | 2 | 0 | 0 | 2 | Both correct; B's wording additionally carries the why (prior queue still active) straight from the coded envelope. |
| T07 idle play | 1 | 0 | 2 | 0 | 0 | 2 | **The thesis task.** A avoided the trap only because the 900-line skill pre-armed it (never called `play`); its reply dropped the Description line. B called `play`, the binary answered NOT_APPLIED + hint, and B recovered cleanly with zero prior knowledge — the invariant moved into the binary and worked. |
| T08 watched via history | 0 | 0 | — | 0 | 0 | — | Both grounded in fresh history. |
| T09 status render | 0 | 0 | — | 2 | 0 | — | A matched the render spec exactly. B drifted: now-playing block off-template (added Client, merged Title, dropped Description) and marked the in-history Profligate row "No" against the cross-ref rule — though B alone proactively offered removing the watched queue item (a rule both skills carry, only B applied). |
| T10 nominal durations | 0 | 0 | — | 1 | 0 | — | Both nailed `~Nm typical`, no Total, unwatchedLeaves. B invented a "N episodes" Detail column (off-spec) and fumbled two flags before recovering off BAD_REQUEST messages. |
| T11 client timeout | 0 | 0 | 2 | 0 | 0 | 2 | A routed by URL-sniffing prose; B routed by `PLEX_CLIENT_UNREACHABLE` + hint. Same correct outcome — B without the rule. |
| T12 not logged in | 0 | 0 | — | 0 | 0 | — | Both correct; B's translation came from the coded envelope. |
| T13 smart collection | 0 | 0 | — | 0 | 0 | — | A refused with zero calls (pre-check rule). B shelled the mutation and relayed the binary's refusal — permitted under v2 (pre-check demoted to optimization) but one wasted round trip. |
| T14 bulk gate | 1 | 0 | — | 0 | 0 | — | Both gated correctly (dry-run → plan → confirm → write), `--all-seasons` + `deu` inferred. A leaked "rk 73333" (internal ID) in its plan; B's plan was cleaner (season table, no IDs). Scripted-two-turn caveat applies equally. |
| T15 row-map | 0 | 0 | — | 0 | 0 | — | Both resolved #2 correctly, IDs hidden. B wrote "1h59m" (missing space — trivial). |
| T16 shuffle | 0 | 0 | — | 0 | 0 | — | A: zero calls (memorized ban). B: ran it, binary refused PLEX_UNSUPPORTED, correct message — the designed v2 flow, one extra call. |
| **Totals** | **2** | **0** | **6/6** | **4** | **1** | **6/6** | |

## Verdict

**The thesis held where it was aimed, and the measurement caught where it didn't.**

1. **Error handling and recovery: fully closed.** Every failure-recovery task (T05, T06, T07, T11, T12) scored perfect on B with the knowledge coming from the binary (`error.code` + `error.hint`) instead of memorized prose. T07 is the cleanest demonstration: A survived the idle-play trap only by pre-armed rule; B walked into it, was told, and recovered — that resilience now exists for ANY consumer of plexctl, not just sessions carrying the 900-line rulebook.
2. **The A-baseline was near-ceiling, which reframes the original gap.** Single-task, fresh-context Sonnet + the v1 skill scored 2 minor violations across 16 tasks. The historically observed Sonnet-vs-Opus gap likely lives in long multi-intent sessions where the rulebook competes with conversation for attention — a dimension this battery (deliberately single-task) does not measure. The v2 stack's advantage should compound there: ~213 lines of skill vs 886 means the per-turn constraint load shrank ~76%, and the recovery knowledge that used to sit in that prose now cannot be crowded out, because the binary reasserts it at every failure.
3. **The skill diet cut four load-bearing lines.** B's regressions are all prose-diet casualties, not contract failures: the "empty = report and STOP" clause (T02's 20-call wander), the verbatim now-playing/status render template (T09), the literal `show` Detail column (T10), and the queue-watched ✓ rule statement (T09). Recommended v2.1 skill patch: restore those ~5 lines. The skill stays ~75% smaller.
4. **Deliberate trades performed as designed:** T13/T16 each spent one extra round trip in exchange for deleting a memorized ban — acceptable, and the binary's refusals produced correct user-facing answers.

Cost note: B agents averaged fewer tokens per task than A (smaller skill to read) on all tasks except T02's excursion.
