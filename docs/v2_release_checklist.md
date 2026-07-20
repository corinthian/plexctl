# plexctl v2.0.0 Release Checklist

Everything on `feature/error-model-v2` is done and green; these are the ship-day steps. Order matters — the skill and binary must never drift (a new-skill/old-binary session is worse than either pair).

1. **A-run the battery first** (if not already done): run `docs/sonnet_battery.md` on a Sonnet agent against the **v1 binary + current live skill**. Record transcripts. This must happen BEFORE the binary deploys — after that the v1 pairing is gone.
2. **Merge** `feature/error-model-v2` → main (PR per repo convention). Tag v2.0.0; `build.sh` stamps the version via ldflags.
3. **Deploy the binary**: `./build.sh` → install `dist/plexctl` to `/usr/local/bin` (or wherever `$PATH` resolves it). Verify: `plexctl --version` reports 2.0.0 and `plexctl commands | jq .ok` is true.
4. **Swap the plex skill** (staged beside the live one):
   - `mv ~/.claude/skills/plex/SKILL.md ~/.claude/skills/plex/SKILL.v1.md.bak`
   - `mv ~/.claude/skills/plex/SKILL.v2.md ~/.claude/skills/plex/SKILL.md`
   - `REFERENCE.md` is already in place (the v2 skill reads it on demand). LESSONS.md, INCIDENTS.md untouched.
5. **Patch the subtrakt skill** (`~/.claude/skills/subtrakt/SKILL.md`), two edits:
   - Hard Rules: "The plex skill's never-`--min-score` rule" → drop that clause (flag no longer exists; inherited rules phrasing otherwise unchanged).
   - Error Handling: replace the bullet "plexctl has no error codes — free-text on stdout. Read it like a human; auth-shaped → suggest `plexctl auth status`." with: "plexctl v2 emits codes — classify like traktctl. `PLEX_AUTH_REQUIRED` (exit 5) is auth-family; `NOT_APPLIED` exit 6 = nothing changed; follow `error.hint` verbatim."
   - Also in `next` step 3: delete "never retry with `--min-score`".
6. **B-run the battery**: same tasks, same wording, Sonnet + v2 binary + new skill. Score both runs per the battery's rubric (violations / wrong-command / recovery). The A-vs-B delta is the answer the whole plan exists to measure.
7. The Subtrakt spec's conformance section (vault) already describes v2 with an "unreleased" note — remove that note once deployed.

Deferred, tracked but not blocking: P3.6 GUID search (`--id-type imdb|tmdb|tvdb`); authoritative per-item `watched:bool` (P0.2 §D candidate); search `truncated` flag (no capping signal in the hubs response today). traktctl `user ratings --rating N` bug is TaskWarrior task 7, different repo.
