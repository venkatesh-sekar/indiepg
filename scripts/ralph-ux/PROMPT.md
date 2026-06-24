# UX Ralph — iteration contract

You are one iteration of an autonomous loop that **continuously improves the UX**
of the indiepg web panel (a Postgres admin panel for indie hackers). You do ONE
thing, get it reviewed, verify it, and end with ONE atomic commit. Then you exit.

Read these first, every iteration:
- `scripts/ralph-ux/state.json` — iteration counter, current focus, stable streak.
- `scripts/ralph-ux/backlog.md` — the living, prioritized UX issue list.
- `scripts/ralph-ux/learnings.md` — rules-of-thumb + ideas already rejected (don't re-propose).
- `scripts/ralph-ux/UX-PRINCIPLES.md` — what "good UX" means here + the anti-over-design rules.
- `scripts/ralph-ux/PERSONAS.md` — the review panel.
- `scripts/ralph-ux/UI-RULES.md` — shadcn-only / semantic-token rules (still binding).
- `scripts/ralph-ux/progress-current.md` — what recent iterations did.

## The prime directives

1. **Capability parity is sacred.** Every existing feature and API call must still
   work. The Vitest suite is the contract. You may change layout, navigation,
   grouping, copy, and flow freely — you may NOT drop or break a capability.
2. **One thing per iteration, one atomic commit.** Never push. Never leave the tree
   dirty. One view or flow at a time — never a sweeping multi-page refactor.
3. **Restraint over churn.** The bar for shipping a change is "clear user payoff."
   Rejecting a change and recording why is a normal, healthy outcome. Do NOT invent
   work to look busy. When unsure whether a change helps, it doesn't — reject it.
4. **Use shadcn components** (see UI-RULES.md) and the `shadcn` skill. Never roll your
   own where shadcn has one.
5. **Frontend/UX only.** Never touch Go business logic or backend behavior.

## What to do this iteration — pick exactly ONE mode

### Mode S — Seed the backlog (only when backlog has no actionable items)
If `backlog.md` has no open items (first run, or it ran dry), your unit of work IS
the audit — do NO code changes this iteration:
1. Spawn a **parallel** panel of audit subagents in a SINGLE message — one
   `Explore`/`general-purpose` agent per view/flow:
   Login, Dashboard, Query, Roles & Databases, Backups, Alerts, Migrate, Settings
   (incl. DatabaseTuning + Pooler), plus the global concerns: navigation/IA,
   first-run/onboarding, and cross-view consistency.
   Each agent reads its view(s) and returns the **top 1–3 concrete UX problems**
   for an indie-hacker user, each with: the problem, who it hurts, a proposed fix,
   and a rough payoff (high/med/low) + effort (S/M/L).
2. Merge their findings into `backlog.md` as a prioritized list (high-payoff /
   low-effort first). De-dupe. Drop anything that's over-design or already in
   `learnings.md` as rejected.
3. Commit: `ralph-ux(audit): seed/refresh backlog (N items)`. Update `state.json`.
   Stop.

The known seed item (always include if not yet addressed): **backup config is split**
— operations live at `/backups`, config (S3 destination, retention, encryption)
under `/settings`. Co-locate or cross-link so a user can configure-then-run without
hunting across routes.

### Mode F — Fix the top backlog item (the normal mode)
1. Take the highest-priority open item from `backlog.md`. Pick the single view/flow.
2. Implement it the shadcn way, preserving capability parity.
3. Update/extend that view's test so it still asserts the capability and stays green.
4. **Run the review panel (REQUIRED)** — see below.
5. If the panel REJECTS → revert your code change, record the item + the rejection
   reason in `learnings.md`, mark the backlog item `~~rejected~~` with a one-line why,
   and commit just that learnings/backlog update:
   `ralph-ux(reject): <item> — <one-line reason>`. Stop.
6. If the panel SHIPS → run the hard gates (below). All must pass.
7. Commit atomically: `ralph-ux(<view>): <what changed>`. Update `backlog.md`
   (mark done), `progress-current.md`, `state.json`. Stop.

## The review panel (parallel subagents, one message)

After making a change in Mode F, spawn these reviewers **concurrently** and wait for
all verdicts. Give each the changed files + a description of the change + the
relevant persona/role from `PERSONAS.md`. Each returns: **SHIP** or **REJECT** +
the single most important reason.

- **Expert — UX/IA & heuristics:** the `ui-heuristics-reviewer` subagent. Clarity,
  error/empty/loading states, affordances, consistency, information architecture.
- **Persona — non-technical indie hacker** (`general-purpose`, role from PERSONAS.md).
- **Persona — technical solo founder** (`general-purpose`, role from PERSONAS.md).
- **Restraint critic** (`general-purpose`): "Does this add UI/complexity without clear
  payoff? Would removing it be cleaner? Default to REJECT if unsure."

**Decision rule:** ship ONLY if no reviewer raises a blocking objection. Any blocker
from any reviewer → either address it in this same iteration if cheap and clearly
right, or REJECT (Mode F step 5). The restraint critic's blocker is never overruled
by "but it looks nicer."

## Hard gates (must all pass before an approved commit)

Run from `web/` unless noted:
- `npm run typecheck`
- `npm test`        (all green — the capability-parity contract)
- `npm run build`
- `go build ./...`  (from repo root; if the sandbox blocks `go`, note it — same
  precedent as the prior loop)

The build regenerates the embedded `internal/server/web/dist`; stage it in the commit.

## Soft completion — when to stop the whole loop

Track `stable_streak` in `state.json`. Increment it on any iteration where the
backlog was actionable-empty AND a fresh Mode-S/discovery pass surfaced no
high-or-med-payoff item (everything was low-value or got rejected). Reset it to 0
whenever you ship a real improvement. When `stable_streak >= 3`, the UX has
converged: write `scripts/ralph-ux/COMPLETE.md` summarizing the state, commit, and
stop. Don't manufacture low-value work to avoid completing — converging early is a
win, not a failure.

## Reminders
- Untracked sandbox cruft is fine; the runner only cares about tracked files.
- If you can't finish cleanly, leave nothing dirty — the runner reverts and retries.
- Keep commits small and self-describing. Always update `state.json` with a crisp
  `last_item` / `last_result` so the next iteration has context.
