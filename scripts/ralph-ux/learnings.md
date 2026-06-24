# Learnings

Accumulated rules-of-thumb and — importantly — **ideas already rejected**, so the
loop doesn't re-propose them. Newest at top.

## Rejected ideas (do not re-propose)

These surfaced in the Mode-S audit (iter 1) but were dropped before reaching the
backlog — they violate the loop's anti-over-design / one-view-per-iteration rules.

- **Sweeping loading-state refactor (Skeleton everywhere).** Converging every view
  from `Spinner` to a shared `PageSkeleton` is a multi-page redesign for marginal
  polish — explicitly forbidden ("never a sweeping multi-page refactor"). If a single
  view's loading state is genuinely confusing, fix that one view in its own iteration.
- **Sweeping destructive-confirm consolidation.** Auditing all 8–10 destructive
  flows to unify TypedConfirmDialog/ConfirmDialog/Modal at once is multi-view churn.
  Capability parity already covers safety; only touch a confirm if one specific flow
  is unsafe, one view at a time.
- **Top-bar breadcrumbs inside modals / per-tab context.** The flat single-label top
  bar is fine for this small app; threading breadcrumb text through modals and tabs
  is added surface area without a clear task payoff (med/L). Over-design.
- **Restore: replace typed-name confirm with dual checkboxes.** The typed-name gate
  is a deliberate blast-radius match for an overwrite-live-DB action; swapping it for
  checkboxes weakens the gate. (Real-time "name matches ✓" feedback on the existing
  input would be fine if ever pursued — but not a checkbox swap.)
- **Login: stop clearing the password field on auth failure.** Clearing on failure
  is a defensible default; the "friction" is minor and reversing it trades a small
  convenience for a small security signal. Not worth an iteration.

## Rules of thumb

- The audit strongly corroborated the seed item: backup **config** (/settings) and
  backup **operations** (/backups) being split is the single most-cited UX problem
  (4 of 11 agents). Co-location is the anchor improvement for this loop.
- Empty states are the weakest spot found: prefer the shadcn `Empty`/`EmptyState`
  pattern with BOTH a title and an actionable `hint` everywhere a list can be empty.
- "Honest state" wins are cheap and high-value here: surfacing data the backend
  already returns (executed_sql), removing always-blank fields (Version), and warning
  on silent failures (alert rules with no channel). Prefer these over new UI.
