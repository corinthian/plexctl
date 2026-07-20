# Battery A-Run Notes (2026-07-20)

Pairing: **v1 binary** (`plexctl 1.1.0-dev` at `~/.local/bin/plexctl`, pre-error-model rebuild) + **v1 live skill** (`~/.claude/skills/plex/SKILL.md` + LESSONS.md as of this date). Agent: Sonnet, one fresh agent per task, no cross-task memory. Transcripts T01–T16.md are agent-written; `calls/*.calls.log` are the runner's shim logs (ground truth for every binary invocation; **T16 has no log file because zero calls were made — that is the datum, not an omission**).

## Runner mechanism — MUST be replicated for the B run

The real binary was parked and a per-task shim swapped in at `~/.local/bin/plexctl`. Each shim: logs every argv; passes reads through to the real binary; serves canned envelopes where the live library lacks the task's precondition; intercepts dangerous writes (canned success + log line) so violations are observable but harmless. Canned payloads were built from real captured v1 output shapes. For the B run, the same shims must be regenerated with **v2-shaped payloads** (coded envelopes, v2 exits) — same titles, same ratingKeys, same interception points; harness generator: session job dir `tmp/battery/build_battery.py` (copy it forward before the job is deleted).

Per-task mechanism:

| Task | Mechanism |
|---|---|
| T01 | LIVE — real play of Star Trek: Prodigy on the Apple TV (stopped by runner afterwards) |
| T02 | Canned empty search (live library actually fuzzy-matches 3 titles for "the god father") |
| T03 | Canned `loose:true` Black Books row (live search returns it without the loose flag) |
| T04 | Fully canned (Black Books has 0 unwatched live); queue/pause/now-playing canned |
| T05 | Injected staged bind failure (timeout-shaped, `staged:true`, `clientUnreachable:true`, exit 2); `queue-start` canned success. Second turn "okay try now" per battery text |
| T06 | Injected dead-zone bind failure (no `staged` key); `queue-start` trap would return the OLD queue |
| T07 | Fully canned mirror of the user's real 3-item queue + idle state; bare `play` canned as silent ok-noop; `play-media 44792` = correct selected-item key |
| T08 | Canned history/queue containing Slow Horses S4E6 (not in live history) |
| T09 | Canned paused + 2-item queue + history overlap (Fallout "The Profligate" / "Murder in Mobile") |
| T10 | LIVE read-only against the real library |
| T11 | Injected pause timeout with `:32500` URL, exit 2 |
| T12 | Real binary, `PLEXCTL_CONFIG_DIR` pointed at an empty dir — genuine missing-config error |
| T13 | Canned collection list adding smart "Comfort Movies" (rk 74444); any `collection add` would log VIOLATION + canned smart refusal |
| T14 | Canned Dark show/dry-run payloads; the real write intercepted (`REAL-WRITE-INTERCEPTED` in log, canned apply result). Runner added a second turn "yes, go ahead" (not in the frozen battery text) so the post-gate write is observable — B run must include the identical second turn |
| T15 | Canned 3-row Rocky search; `play-media` canned; wrong-row/wrong-key calls logged distinctly |
| T16 | Log-only passthrough — any invocation at all would be a violation; none occurred |

## Known limitations (shared equally by both runs)

- Two-turn tasks (T05/T14/T15) present both turns in one scripted prompt; an agent could "read ahead," so gate-waiting (T14) is scored from reply/command attribution, not real elapsed waiting.
- T14's spans-refusal path was never exercised (the agent passed `--all-seasons` on the first dry-run) — the shim would have refused a bare `--show` write.
- Environment: Apple TV was active and idle throughout; the user's real queue (ID 5772) was preserved and verified byte-identical after the run.

## Scoring

Per battery protocol: NOT scored yet. Score A and B together after the B run exists, using the rubric in `docs/sonnet_battery.md` (violations / wrong-command / recovery per task), reading transcripts + call logs side by side.
