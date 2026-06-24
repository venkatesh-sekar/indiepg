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
- **Roles & Databases: scope `dropBusy` per-row so unrelated Delete buttons stay
  enabled during a drop.** Rejected iter 5 — the premise ("unrelated rows look frozen")
  is false. `dropBusy` is true *only* while `doDrop` runs, and `doDrop` is reachable
  only from the `TypedConfirmDialog` (open iff `dropTarget !== null`). So a modal
  AlertDialog is always open during the entire busy window — Radix marks the
  background inert/`aria-hidden` (proven: a background Delete button is in the DOM but
  unreachable via `getByRole` while the dialog is open), and on success the dialog
  closes *and* `reloadAll()` swaps the tables for a Spinner before busy clears. The
  user can never see or click an "unrelated frozen row," so per-row scoping adds
  conditional logic for zero observable payoff. **Lesson:** a global busy flag that
  only flips while a modal is up is already effectively scoped — the modal does the
  gating. Don't "fix" disabled-state breadth on rows a modal already covers.

- **Dashboard: make the "no backup yet" callout's "Backups page" an actual `<Link>`.**
  Rejected iter 6 on restraint. The change was genuinely minimal and clean (3 of 4
  reviewers shipped — UX heuristics, Sam, Priya — citing recognition-over-recall and
  consistency with the existing inline `<Link>` in Backups.tsx). But the **restraint
  critic blocked**, and that blocker is never overruled: the copy *already names* the
  destination ("Backups page") and the persistent left-nav is one click from every
  view, so the link only shaves one click off an empty state most users see at most
  once. No task was blocked or meaningfully harder before; the payoff is decorative.
  **Lesson:** "matches an existing pattern" justifies a link you're adding for a real
  need — it does not, by itself, manufacture the need. An inline `<Link>` earns its
  place only when the target is *not* otherwise one obvious click away (e.g. a
  cross-domain jump like Backups→Settings for S3, which the nav doesn't make obvious).
  A same-name link to a top-level nav destination is the weak case. Don't re-propose.

## Rules of thumb

- The audit strongly corroborated the seed item: backup **config** (/settings) and
  backup **operations** (/backups) being split is the single most-cited UX problem
  (4 of 11 agents). Co-location is the anchor improvement for this loop.
- Empty states are the weakest spot found: prefer the shadcn `Empty`/`EmptyState`
  pattern with BOTH a title and an actionable `hint` everywhere a list can be empty.
- "Honest state" wins are cheap and high-value here: surfacing data the backend
  already returns (executed_sql), removing always-blank fields (Version), and warning
  on silent failures (alert rules with no channel). Prefer these over new UI.
