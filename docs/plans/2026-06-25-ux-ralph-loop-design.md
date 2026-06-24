# UX Ralph Loop — Design

**Date:** 2026-06-25
**Status:** Approved (brainstorm complete)
**Owner:** venkatesh

## Goal

A self-improving Ralph loop that continuously improves the **UX** of the indiepg
web panel — information architecture, task flows, onboarding/first-run, and
visual polish — for an indie-hacker audience. Simple, minimal, rock stable.
It uses the best-fit shadcn/ui components and reviews every change with a mix
of end-user personas and expert reviewers.

This is a **separate, new loop** from the completed `scripts/ralph/` shadcn
migration loop. That loop is done (COMPLETE.md written, 2026-06-24) and is not
reopened.

## Core tension and how it's resolved

"Improve the UX" is open-ended and has no natural finish line. An open-ended
loop with freedom to change anything is exactly what drifts into over-built UI.

The **anti-over-design guardrail is the spine of this loop**, not an afterthought:
- A dedicated **restraint critic** in the review panel rejects changes that add
  complexity without clear user payoff. Default to REJECT when unsure.
- "Make no change this iteration" is an allowed, healthy outcome.
- The loop stops early (soft "stable" completion) when it runs out of genuinely
  high-value work, rather than inventing busywork.

## Where it lives

A sibling loop at `scripts/ralph-ux/` that reuses the proven `ralph.sh` engine
almost verbatim (self-healing, atomic-commit, no-progress HALT, caps). Only the
*contract* changes.

```
scripts/ralph-ux/
├── ralph.sh              # copy of the proven runner, points at this dir; 6h cap default
├── PROMPT.md             # the UX iteration contract (new)
├── UX-PRINCIPLES.md      # what "good UX" means here + anti-over-design rules
├── PERSONAS.md           # end-user + expert reviewer personas
├── UI-RULES.md           # carried over: shadcn-only, semantic tokens, gap-* etc.
├── backlog.md            # living prioritized UX issue list (seeded by in-loop audit)
├── state.json            # iteration counter + current focus + stable-streak counter
├── learnings.md          # accumulated rules-of-thumb (incl. rejected ideas + why)
├── progress-current.md   # rolling narrative
└── run-logs/
```

## Stability guarantee — "rock stable"

**Capability parity, layout/flow free.** Every existing capability and API call
must still work; the loop is free to change layout, navigation, grouping, copy,
and flow. Enforced exactly like the old loop enforced behavior-parity:

- The existing Vitest suite (130 tests) must stay green every iteration.
- `npm run typecheck`, `npm run build`, and `go build ./...` must all pass.
- One view/flow per iteration, one atomic commit, never push, never leave the
  tree dirty, auto-revert on any failure.

Tests are the contract that says "the feature still works"; the loop rewrites
everything around them.

## How one iteration works

Each iteration does exactly one of two things, never both:

- **Mode A — Fix a queued item.** If `backlog.md` has reviewer-approved items,
  take the top one and implement it.
- **Mode B — Discover.** If the backlog is empty/low-value, pick ONE view or flow,
  find the single highest-value UX problem, add it to the backlog, then implement it.

Steps:

1. Read context: `state.json`, `backlog.md`, `learnings.md`, `UX-PRINCIPLES.md`, `UI-RULES.md`.
2. Pick ONE thing (Mode A or B). One view/flow per iteration — never a sweeping
   multi-page refactor in a single commit.
3. Implement the shadcn way. Capability parity preserved.
4. Update/extend tests so they still assert the capability and stay green.
5. **Review gate** (below) — experts + personas + restraint critic. Can reject.
6. **If rejected** → revert the change, record *why* in `learnings.md` (so the loop
   doesn't re-propose it). The learnings update is the commit for that iteration.
7. **If approved** → hard gates: typecheck, test, build, `go build`. All must pass.
8. Commit atomically; update `progress-current.md` and `state.json`.

A rejected change is a normal, frequent outcome. Capturing the reasoning is how
the loop learns restraint rather than thrashing.

## The review gate and parallelism

**Rule of thumb:** parallelize read-only thinking (audit, review); keep writing
(code edits + commits) strictly sequential. Parallel implementation would cause
git conflicts and lose clean rollback — that fights "rock stable." Implementation
stays one-at-a-time; everything else fans out.

**1. Review panel — all reviewers run concurrently** (parallel subagents in one
message) after a change:

- **Expert — UX/IA & heuristics:** `ui-heuristics-reviewer`. Clarity, error/empty/
  loading states, affordances, consistency, IA.
- **Persona — non-technical indie hacker:** "I just want my DB backed up and safe.
  Can I do the thing without docs?"
- **Persona — technical solo founder:** "I know Postgres. Don't hide power, don't
  make me click 5 times."
- **Restraint critic (the spine):** "Does this add UI/complexity without clear
  payoff? Would removing it be better? Default to REJECT if unsure."

Each returns a structured verdict (ship / reject + reason). A change merges only
on consensus-pass (no blocker from any critic).

**2. Upfront audit fans out — and is part of the loop.** When the loop starts and
`backlog.md` is empty, iteration 1's unit of work *is* the audit: spawn one agent
per view/flow in parallel, merge findings into a prioritized `backlog.md`, commit
it, stop. No code changes that iteration. Driven via the Workflow tool for
deterministic fan-out + structured verdicts. There is no manual pre-step — you
just run the loop and it audits itself first.

## Stopping conditions (first to fire wins)

1. **6-hour wall-clock cap (hard limit).** `RUNTIME_CAP_HOURS=6` default. Checked
   before each iteration; stops cleanly *between* iterations (never mid-commit),
   writes a summary to `progress-current.md`, exits 0. Primary stop.
2. **No-progress HALT** (carried over): 10 consecutive no-commit iterations → stop.
3. **Soft "stable" completion**: if N consecutive discovery iterations find no
   worthwhile improvement (all candidates rejected by restraint critic), write
   `COMPLETE.md` and stop early. Protects "minimal / don't build what's not needed."

In practice: audit → work the backlog for up to 6h → bail early if high-value work
runs out.

## Known seed items for the backlog

- **Backup config split.** Backup *operations* live at `/backups`; backup *config*
  (S3 destination, retention, encryption) lives under `/settings`. Co-locate or
  cross-link so a user can configure-then-run without hunting across routes.
- First-run / onboarding empty states for indie hackers (where do I start?).
- (Remaining items discovered by the in-loop audit.)

## Out of scope / non-goals

- No parallel code-editing iterations (stability over speed).
- No new backend capabilities — UX only; capability parity is a hard constraint.
- No dark-mode or large net-new features unless the audit proves clear user payoff.
