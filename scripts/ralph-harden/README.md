# ralph-harden — the never-ending hardening loop

A self-improving loop that makes indiepg **rock-solid and dumb-proof**, one
solid, reviewed, tested commit at a time. It is biased toward **correctness,
preflight checks, fail-fast error signals, durability, and self-healing** — not
new features. Because much of the panel was written fast, its first job is to
**prove the code actually does what it claims** and to guard every risky path.

Sibling of `scripts/ralph/` (hardening → shadcn migration, done) and
`scripts/ralph-ux/` (UX, done). Same harness, sharper mandate.

## Run

```sh
./scripts/ralph-harden/ralph.sh                     # opus, 500 iters, 24h cap
./scripts/ralph-harden/ralph.sh --forever           # unbounded — truly never-ending
./scripts/ralph-harden/ralph.sh --model sonnet 200  # custom model + max iterations
./scripts/ralph-harden/ralph.sh --runtime-cap-hours 12
```

Requires a clean tracked tree at cold start and the `claude` CLI on PATH.

## What one iteration does

1. Reads state (`state.json`, `backlog.md`, `learnings.md`, `DEFAULTS.md`, `AGENTS.md`).
2. Picks **one** item + mode:
   - **A — prove behavior & test** the riskiest, least-tested paths (fix bugs it finds);
   - **P — preflight & fail-fast** a risky/destructive/external op;
   - **D — defaults & self-heal** so a bad config can't wedge Postgres;
   - **S — re-seed** the backlog via a parallel audit panel when it runs thin.
3. Implements test-first, then a **review pass** (`feature-dev:code-reviewer` +
   a mode-specific critic — e.g. a *test-skeptic* that checks the test would
   actually fail if the behavior broke).
4. **Verify gate:** `make verify` (and `make verify-web` if `web/` changed; a
   relevant `make e2e SCENARIO=…` when feasible). Never commits red.
5. Records progress + updates state, then **one atomic commit**. Reverts any
   mess instead of leaving the tree dirty. Never pushes.

## Never-ending

This loop does **not** declare the panel "done". When the backlog empties it
runs Mode S and finds more to harden. It stops only on:

- a **cap** (iterations or wall-clock) — unless `--forever`;
- a **no-progress stall** — 10 iterations with zero commits writes `HALT.md`;
- **Ctrl-C**, or an operator-authorized `COMPLETE.md`.

The shell owns the mechanical guardrails: clean-tree cold start, per-iteration
atomicity (auto-revert of anything uncommitted), and the caps above.

## Files

- `ralph.sh` — the runner (guardrails + `claude --print < PROMPT.md` per iteration).
- `PROMPT.md` — the orchestrator prompt (the brain; the 4 modes + priority ladder).
- `state.json` — machine-readable cursor + last-iteration handoff.
- `backlog.md` — prioritized work queue (starter seed; Mode S grounds/expands it).
- `learnings.md` — durable rules + "Rejected ideas — do not re-propose".
- `progress-current.md` — human-readable per-iteration history.
- `run-logs/iter-NNNN.log` — raw claude output per iteration (gitignored).
- `HALT.md` / `COMPLETE.md` — stop sentinels (gitignored).

Shared defaults live in `scripts/ralph/DEFAULTS.md` (single source of truth).

## When you come back

```sh
git log --oneline --grep '^ralph-harden'      # what it did
sed -n '1,40p' scripts/ralph-harden/progress-current.md
cat scripts/ralph-harden/HALT.md 2>/dev/null  # if it stopped stuck
git diff <since>..HEAD                          # review the work, then push if happy
```
