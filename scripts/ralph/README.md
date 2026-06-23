# indiepg Ralph loop

A self-improving loop that hardens indiepg one solid, reviewed, tested commit at
a time. It picks one item, implements it, has it reviewed, verifies it, commits
atomically, and repeats — biased toward **stability, security, durability, and
usability** over new features.

## Run it

From the repo root, with a clean tracked tree:

```bash
./scripts/ralph/ralph.sh                        # opus, 100 iters, 24h cap
./scripts/ralph/ralph.sh --model sonnet 200     # custom model + max iters
./scripts/ralph/ralph.sh --runtime-cap-hours 12 # custom wall-clock cap
```

It **never pushes**. Review the commits and push yourself when happy.

## What one iteration does

Read state → pick the single highest-priority item (see bands in `PROMPT.md`) →
implement test-first → **review pass** (code-reviewer subagent on the diff) →
verify gates (gofmt/vet/test/build; web typecheck/build/test) → atomic commit →
update progress + state. If it can't finish cleanly, it reverts and picks a
smaller item. It never parks work for a human.

## Stop conditions

- **Caps** (max iterations / wall-clock): normal stop, just restart.
- **`COMPLETE.md`**: the loop judged the panel rock-solid and good-enough.
  Delete it to let the loop keep improving.
- **`HALT.md`**: only the genuinely-stuck case (10 iterations with no commit).
  Read it, resolve, delete it, restart.

The runner self-heals: if any iteration leaves the tracked tree dirty, the next
run reverts to the last good commit and redoes the work — no flailing, no parked
queue.

## Files

- `ralph.sh` — the runner (guardrails, caps, atomicity, auto-revert)
- `PROMPT.md` — orchestrator instructions (the iteration contract + priorities)
- `DEFAULTS.md` — trusted Postgres/PgBouncer/pgBackRest defaults (from `sm`)
- `backlog.md` — living, prioritized task list
- `state.json` — counters + current focus
- `learnings.md` — rules-of-thumb accumulated across iterations
- `progress-current.md` — rolling history of what was done
- `run-logs/` — per-iteration claude output (created at runtime)

## When you come back

1. `git log --oneline | grep '^.* ralph'` — what got done.
2. `scripts/ralph/progress-current.md` — the narrative.
3. `scripts/ralph/backlog.md` — what's left.
4. Review the diffs, then push.
