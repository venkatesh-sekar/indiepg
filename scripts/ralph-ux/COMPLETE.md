# UX Ralph — CONVERGED

**Date:** 2026-06-25 · **Final iteration:** 27 · **stable_streak:** 3/3

The UX-improvement loop has **converged**. Three consecutive Mode-S discovery passes
(iters 23, 25/26, 27) — each a 5-agent parallel panel against a deliberately HIGH bar,
with the full shipped/rejected digest as context — surfaced **no new high-or-medium-payoff,
non-over-design, frontend-only UX problem**. Per the iteration contract (`stable_streak >= 3`),
the loop stops here.

## North star (met)

> The indiepg panel is the easiest, clearest, safest way for an indie hacker to run their
> Postgres — minimal surface, co-located workflows, honest state, no over-design.

## What shipped (12 real improvements over 26 iterations)

Anchor:
- **iter 11 — Backups + Settings co-location** (the canonical seed item, flagged by 4 of 11
  audit agents). Backup *config* (S3 destination, retention, encryption) moved off `/settings`
  into a shared `BackupStorageForm` surfaced on `/backups` via a `Sheet`; the local-only
  warning's CTA opens it in-page. Configure-then-run in one place; Settings got simpler.

Safety / honest-state (the highest-value class here):
- **iter 24 — Alerts:** can't save a NEW enabled notification channel with blank credentials
  (was a silent dead alert pipeline behind a green "Configured" badge).
- **iter 21 — Login:** server lockout no longer dead-ends the form (`disabled={busy||locked}`
  → `disabled={busy}`); a locked-out user can retype + resubmit instead of reloading.
- **iter 20 — Roles:** one-time `SecretsModal` made non-dismissible (`dismissible={false}`);
  only "I've saved them" closes it — a reflexive Escape/backdrop no longer destroys credentials.
- **iter 19 — Migrate:** failed-job copy is honest for an overwrite job ("may already have been
  dropped — restore from a backup") instead of the false "your data is intact."
- **iter 18 — Roles:** "Rotate password" gated behind a plain `ConfirmDialog` (was a single
  unguarded click that instantly breaks any live app on the old credential).
- **iter 4 — Alerts:** warn when enabled rules have no enabled channel (silent fire-into-void).
- **iter 3 — Dashboard:** removed the always-blank "Version" row (honest state).

Clarity / consistency / friction:
- **iter 17 — Navigation:** reset scroll to top on route change (was carrying `scrollTop`
  across `<Outlet/>` swaps — "stuck page" feel).
- **iter 12 — Migrate:** "Start another" targeted reset — keeps the reusable source connection,
  clears per-run fields, force-resets the destructive overwrite flag.
- **iter 10 — Dashboard:** dropped the duplicate "Connections" row, kept the tinted gauge.
- **iter 9 — Pooler:** enable-confirm copy now says you must repoint apps manually.
- **iter 8 — Alerts:** "Sustained" header renamed to "Hold for" (plain language over a tooltip).
- **iter 2 — Roles:** actionable empty-state hint for "No roles yet."

## What was rejected (restraint held — this is healthy, not failure)

8 changes were rejected, each recorded in `learnings.md` with decisive reasoning:
- **On restraint** (3 reviewers shipped, restraint critic blocked, never overruled): Dashboard
  "Backups page" link (iter 6), Query `executed_sql` block (iter 7), Migrate inline overwrite
  Callout (iter 13).
- **On a FALSE premise** (self-rejected with code evidence, no panel): `dropBusy` per-row
  (iter 5), DatabaseTuning "reassuring intro" — already present threefold (iter 14), Migrate
  confirm `dismissible={false}` — dismiss is the safe escape (iter 22), Query clear-results-on-edit
  — breaks the run→refine loop (iter 23), Backups zero-backup button gate — already shown twice
  (iter 25), Backups PITR datetime gate — native `required` already blocks it (iter 26), sidebar
  tooltip — dead in offcanvas mode (iter 26), Dashboard disk-threshold `>`→`>=` — card is warn
  not neutral, no-op at the boundary (iter 27).

The recurring lesson: a finding's HIGH rating evaporates when its load-bearing word ("silent",
"broken", "every user", "neutral") is false on code inspection. Verify the premise against the
source before promoting — and check whether existing UI already conveys the fact.

## Capability parity

Sacred throughout. The Vitest suite (146 tests) was the contract; every shipped change kept it
green and extended it to assert the new capability. No feature or API call was dropped.

## Out of scope (parked, not defects in this loop)

- **NEEDS-BACKEND:** honest backup-target health on the destination badge (`GET /config` carries
  no target-health signal); durable handler-side credential validation for alert channels (the
  iter-24 fix is the client-side guard).
- **Low/watch copy nits:** Login "Try again later" lockout duration (a precise countdown needs a
  backend hint and borders on over-design); optional Query client-side write-keyword hint.

These are documented in `backlog.md` for a future backend-capable pass; none is a frontend-only
high/med item, so none blocks convergence.

## Restarting the loop

If the panel gains new views/flows or backend capabilities land (e.g. target-health on
`GET /config`), delete this file and run a fresh Mode-S seed pass to repopulate `backlog.md`.
