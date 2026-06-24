# UX Ralph loop

An autonomous loop that **continuously improves the UX** of the indiepg web panel for
an indie-hacker audience — information architecture, task flows, onboarding, and
visual polish. Simple, minimal, rock stable.

This is the sibling of `scripts/ralph/`, which was a one-time shadcn **migration** loop
(now COMPLETE). That one rebuilt every view on shadcn; this one improves how the panel
*works and feels*, on top of that foundation.

## Run it

```bash
./scripts/ralph-ux/ralph.sh                    # opus, ~6h wall-clock cap
./scripts/ralph-ux/ralph.sh --runtime-cap-hours 3
./scripts/ralph-ux/ralph.sh --model sonnet 200 # custom model + max iterations
```

The tree must be clean at cold start (it protects your uncommitted work). Each
iteration ends in exactly one atomic commit; the runner auto-reverts any mess and
never pushes.

## How it works

- **Iteration 1 (Mode S):** a parallel audit fans out one agent per view/flow, merges
  findings into `backlog.md`. No code changes — seeding the backlog *is* the work.
- **Every iteration after (Mode F):** take the top backlog item, implement it
  preserving capability parity, then run a **parallel review panel** — a UX/IA expert
  (`ui-heuristics-reviewer`), two end-user personas, and a **restraint critic** that
  can reject over-design. Ships only on unanimous no-blocker; otherwise reverts and
  records why in `learnings.md`.
- **Stability guarantee:** the Vitest suite + `typecheck`/`build`/`go build` must stay
  green every iteration. Tests are the "the feature still works" contract; everything
  around them is free to change.

## Stopping

First to fire wins:
1. **6-hour wall-clock cap** (the primary stop; `--runtime-cap-hours` to change).
2. **No-progress HALT** — 10 consecutive no-commit iterations.
3. **Soft completion** — when `stable_streak >= 3` (discovery keeps finding no
   high-value work), the loop writes `COMPLETE.md` and stops. Converging early is a
   win, not a failure.

Delete `COMPLETE.md` (or `HALT.md`) to let it run again.

## Files

| File | Role |
|---|---|
| `ralph.sh` | the runner (guardrails, caps, auto-revert) |
| `PROMPT.md` | the per-iteration contract |
| `UX-PRINCIPLES.md` | what "good UX" means here + anti-over-design rules |
| `PERSONAS.md` | the four-reviewer panel |
| `UI-RULES.md` | shadcn-only / semantic-token rules (carried over) |
| `backlog.md` | living prioritized UX issue list |
| `learnings.md` | rules-of-thumb + rejected ideas |
| `progress-current.md` | rolling narrative |
| `state.json` | iteration counter, focus, stable streak |
| `run-logs/` | per-iteration claude output |
