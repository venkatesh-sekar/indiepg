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

- **Query: show the server's `executed_sql` in a compact block when it was rewritten.**
  Rejected iter 7 on restraint. Implemented cleanly (block appears *only* when
  `normalize(executed_sql) !== normalize(submitted)`, i.e. the server actually rewrote
  the query; zero added surface on verbatim runs). 3 of 4 reviewers shipped (UX
  heuristics — visibility of system status; Sam + Priya — "exact LIMIT value, trust
  win, no friction"). But the **restraint critic blocked** and is never overruled:
  the rewrite path is *only ever* the auto-LIMIT injection — **verified in
  `internal/pg/guard/guard.go`**: `Check()` mutates the statement solely via
  `injectLimit`, which sets `cls.HasLimit → limited=true`. There is no rewrite that
  changes the SQL without also flipping `limited`. So the block can only appear when
  the existing **"Results limited for safety"** badge + **"Add your own LIMIT…"** copy
  are *already* present and already explain that a LIMIT was applied. The block merely
  restates that fact in technical form. The one genuinely-new bit (the literal cap
  value, e.g. `1000`) doesn't justify a whole code block. **Lesson:** "honest state —
  surface data the backend already returns" is only a win when that data is *otherwise
  hidden*. Here it isn't — the `limited` messaging already tells the story, so the
  `executed_sql` block is redundant with existing copy, not new signal. Before
  proposing an honest-state surface, check whether existing UI already conveys the same
  fact; if it does, the surface is decoration. Don't re-propose unless the guard gains
  a rewrite path that does NOT set `limited` (then "show exactly what ran" becomes
  real). This refines the prior rule-of-thumb that listed `executed_sql` as a cheap win.

## Rules of thumb

- **Prefer a clearer label over a tooltip when the column header itself is the jargon.**
  Iter 8: the "Sustained" header was ambiguous, so the backlog proposed a `Tooltip`
  definition on it (and on "Cooldown"). The panel shipped 3–1, but the restraint critic
  blocked and was right: a tooltip adds an info icon, a `TooltipProvider`, a hover-only
  affordance (dead on touch — Sam flagged this), and a helper component — to explain ONE
  word. The simpler, strictly-better fix was to **rename the header to plain language
  that already exists in the flow**: `Sustained` → `Hold for`, matching the editor's
  "Must hold for (minutes)" label. Self-documenting next to its values, consistent with
  the edit modal, zero new surface, works everywhere. **Lesson:** when the header word
  is the problem, fix the word — don't annotate a bad label with hover text. Reserve
  tooltips for genuinely terse-by-necessity columns whose value can't be self-explained
  by a better name. Also: only ONE of the two columns was actually ambiguous
  ("Cooldown" + a duration is self-explanatory) — don't batch a decorative second
  tooltip in just because you're touching the row of headers; fix only what's unclear.
- When the restraint critic blocks but hands you a cheaper, clearly-right alternative,
  *take the alternative in-iteration* rather than rejecting outright — that ships the
  real improvement (which even the critic agreed existed) without the over-design. The
  blocker isn't always "do nothing"; sometimes it's "do the smaller thing."

- The audit strongly corroborated the seed item: backup **config** (/settings) and
  backup **operations** (/backups) being split is the single most-cited UX problem
  (4 of 11 agents). Co-location is the anchor improvement for this loop.
- Empty states are the weakest spot found: prefer the shadcn `Empty`/`EmptyState`
  pattern with BOTH a title and an actionable `hint` everywhere a list can be empty.
- "Honest state" wins are cheap and high-value here: removing always-blank fields
  (Version) and warning on silent failures (alert rules with no channel). Prefer these
  over new UI. **Caveat (iter 7):** "surface data the backend already returns" only
  wins when that data is *otherwise hidden*. `executed_sql` failed this test — it can
  only differ via the auto-LIMIT, which the existing "limited for safety" copy already
  explains, so surfacing it was redundant, not honest-state signal. Check existing UI
  first.
