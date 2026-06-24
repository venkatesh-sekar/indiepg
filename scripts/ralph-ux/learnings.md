# Learnings

Accumulated rules-of-thumb and — importantly — **ideas already rejected**, so the
loop doesn't re-propose them. Newest at top.

## Rejected ideas (do not re-propose)

These surfaced in the Mode-S audit (iter 1) but were dropped before reaching the
backlog — they violate the loop's anti-over-design / one-view-per-iteration rules.

- **Migrate: make the overwrite typed-confirm `Modal` non-dismissible (`dismissible={false}`).**
  Rejected iter 22 on a **false premise**, self-rejected with decisive code evidence (no panel).
  A nav/IA audit agent argued that letting Escape/click-outside dismiss the typed-confirm dialog
  "undermines the safety gate — users could proceed without typing the confirmation." **This is
  backwards.** Verified in `Migrate.tsx` (lines 336–361 single-db, 507–530 cluster): the destructive
  action fires *only* via the `<Button onClick={start}>` whose `disabled={busy || !overwriteMatches}`
  requires the typed name to match; **dismissing the modal (Escape / backdrop / Back) just calls
  `setConfirmOpen(false)` and executes nothing.** So dismissing is the **safe escape**, not a bypass —
  it cancels a destructive action. This is the **mirror opposite of iter-20's SecretsModal**: there,
  dismissing destroyed a once-shown credential (irreversible *loss*), so `dismissible={false}` was the
  fix; here, dismissing *avoids* a destructive action, so non-dismissibility would *trap a user inside a
  destructive dialog* and make backing out harder — negative payoff. **Lesson:** before locking down a
  dialog's dismiss paths, ask **which direction is the dangerous one.** `dismissible={false}` is correct
  only when *dismissing itself* causes irreversible loss (SecretsModal). When dismissing *cancels* a
  destructive action (every type-to-confirm execute-gate), the casual escape is a feature — keep it. Don't
  copy iter-20's pattern onto a confirm dialog whose gate is on the execute button, not on the dismiss.

- **BackupStorageForm: add a "clear saved credential" affordance.** Rejected iter 22 — speculative and
  backend-dependent (out of scope for this frontend-only loop). The audit agent couldn't confirm the API
  accepts an empty/clear signal ("the backend must support empty string as 'clear', which it may already
  do"). *Changing* a credential already works (the field placeholder reads "Leave blank to keep current";
  type a new key to replace). *Clearing to empty* isn't a coherent indie-hacker goal (you don't blank an
  S3 secret while keeping the bucket; you reconfigure or stop using S3 — a different, larger question), and
  it requires an unconfirmed backend behavior. **Lesson:** a "you can't UNSET X" gap is only a real item
  when unsetting is a goal a user actually has AND the backend already supports it; otherwise it's a
  NEEDS-BACKEND/speculative item, not a frontend fix.

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

- **Migrate (single-db): inline danger Callout when the overwrite checkbox is armed.**
  Rejected iter 13 on restraint. Implemented cleanly (a conditional `Callout tone="danger"`
  below the checkbox, shown only while `overwrite` is true, stating the target gets
  dropped/recreated + "you'll type its name to confirm next"). **3 of 4 shipped** (UX
  heuristics — "consequence at arm-time, not deferred to the modal"; Sam — "the safety net
  I want the moment I tick the box"; Priya — "zero added clicks, purely informational").
  But the **restraint critic blocked** and is never overruled, with decisive code-level
  reasoning: the single-db overwrite already has a **three-stage escalating gate** — (1) the
  checkbox label literally says "Replace `<target>` if it already exists **(destructive)**",
  (2) the submit button flips to **"Continue…"** signalling a confirm step, (3) the modal's
  own danger Callout ("…will be dropped and recreated… This cannot be undone") + a
  **type-the-name** input that is the *actual* execute-time safety gate. The new Callout just
  moves the modal's text up by one click, restating a label three inches away, and even
  pre-narrates the confirm step the user is about to see — so the user reads "dropped and
  recreated, can't be undone" **twice within seconds**. The **consistency-with-cluster
  steelman is false on inspection**: `ClusterForm`'s inline Callout is *always-on* (not
  conditional) and carries info its checkbox label omits ("can drop **every matching
  database**") — a categorically scarier, standing warning — so it isn't the pattern "warn
  inline when overwrite is checked"; copying its shape misses its reason. Also: the destructive
  action **cannot fire from this screen at all** (the button only opens the modal; the modal's
  type-to-confirm is the real error-prevention surface, and it's untouched), so visibility
  "at arm-time" buys nothing executable. **Lesson:** before adding a warning, count the
  warnings that already fire on the same interaction — a destructive control that already has
  a label-flag + a button-label change + a modal-with-typed-confirm is *already* a graduated
  gate; a fourth restatement is warning-on-top-of-warning, not error-prevention. And don't
  justify a copy from a sibling view by *shape* — verify the sibling's version carries
  information the local label/modal doesn't (the cluster warning does; this one wouldn't).
  Refines iter 7's rule: "honest/safety surface only wins when the fact is otherwise hidden."

- **DatabaseTuning: add a "reassuring intro line" (defaults tuned for the hardware; the
  typical indie app needs no changes).** Rejected iter 14 on restraint — **the premise is
  false on inspection** (the iter-5 pattern, not the iter-7/13 "plausible-but-redundant"
  pattern), so self-rejected with decisive code evidence and no panel. The page **already
  opens with exactly this reassurance, threefold**: (1) the card title is `Database tuning
  (host-sized)` — "host-sized" signals automatic; (2) the first element is an info `Callout`
  titled **"Sized to this server automatically"** whose body reads *"Postgres is tuned to this
  machine on safe best defaults — you don't need to tune anything by hand"* — that IS the
  proposed intro line, verbatim in spirit; (3) the active **Mixed** profile's own description
  says *"the best default for an indie-hacker box"*, telling the indie user their profile is
  already right; and the profile switcher's preview is explicitly framed *"This is a preview —
  nothing changes here … applied at install/provision time — not from this screen."* The item's
  second sub-concern ("help text assumes DBA knowledge: shared_buffers, work_mem") is also
  already handled — each setting carries a plain-English one-liner (e.g. "Memory Postgres uses
  to cache data pages. Sized to your RAM."), and the proposed fix didn't address it anyway.
  There is no edit to make that wouldn't restate a reassurance the page already gives. Running a
  4-agent panel to rubber-stamp a provably-zero-payoff change is the churn the loop guards
  against (iter-5 precedent). **Lesson:** when a backlog item says "add a reassuring/intro line,"
  *read the view's existing intro first* — an audit agent that skims a parameter table can miss
  the Callout sitting right above it. If the page already opens with that exact reassurance,
  the item is already done; mark it so and move on. This is the iter-7/13 rule ("surface a fact
  only when it's otherwise hidden") applied to *reassurance copy*, not just data.

## Rules of thumb

- **A disabled state with no path to re-enable itself is a dead-end, not a safeguard — and removing it is a
  subtractive fix the restraint critic waves through.** Iter 21: Login set `locked=true` (disabling the input)
  + cleared the password (disabling the button) on a server lockout, but `locked` only reset inside `onSubmit`
  — which could never fire because the controls it needed were disabled. The form froze with no exit but a
  full-page reload, and stayed frozen even after the server lock expired. The fix is to *remove* the disable
  (`disabled={busy || locked}` → `disabled={busy}`), not to add a timer/retry button: the **server was always
  the real enforcer** (it returns the lock again on resubmit), so the UI disable bought nothing but the trap,
  and the existing warn Callout already conveys the locked state. All four reviewers (incl. restraint) shipped
  it as code-gets-smaller. **The diagnostic:** when a boolean disables the only control that can clear that
  boolean, you have an unrecoverable state — trace every disabled/`readonly`/guard flag to "what re-enables
  this, and can the user reach it?" If the only answer is "reload the page," it's a defect. **Why it's the
  strong case (vs the rejected adds):** it's behavior-correctness at *negative* surface (like iter-17's scroll
  reset and iter-20's non-dismissible modal) — the rejected items all *added* UI/copy; this removes a
  restriction. Watch for tests that *encode* the bug: the existing Login test literally asserted
  `input.toBeDisabled()` after lockout — a green test over a dead-end. Updating it to assert the recovery path
  is part of the fix, not a regression.

- **Once-only content must close only by an explicit acknowledgement — strip the casual dismiss paths, don't
  just add a warning.** Iter 20: the `SecretsModal` showed credentials that "cannot be retrieved again," but
  it was a plain `Modal`, so Escape, a backdrop click, OR the corner X each routed to `onClose` →
  `setSecrets(null)` and destroyed the only copy. The fix is the mirror of iter-18's "add the first guard on
  an unguarded irreversible action," but here the action is *dismissal itself*: you don't add UI, you
  **remove the accidental exits** so the deliberate one ("I've saved them", already present) becomes the
  sole path out. All four reviewers (incl. the restraint critic) shipped because the change is *subtractive*
  — it removes the corner X and adds no control/copy/click. **Implementation pattern:** add an opt-in
  `dismissible?: boolean` to the shared `Modal` (default `true` → every other modal byte-for-byte
  unchanged), and when false wire all THREE Radix vectors together — `showCloseButton={false}` (the X),
  `onEscapeKeyDown` → `preventDefault` (Escape), `onInteractOutside` → `preventDefault` (backdrop/focus-out).
  Missing any one leaves a live data-loss path. **Why it clears restraint** (vs the rejected items): a
  safe-default boolean with one concrete caller is *not* premature configurability — a bespoke non-shared
  SecretsModal would be MORE divergence than one prop wired to capabilities Radix already exposes; and the
  payoff is specific and irreversible (a reflexive keypress permanently destroys a credential), not
  decoration. **Tell it's the strong case:** when `dismissible={false}` makes one control the only exit,
  confirm that control is unconditionally rendered/enabled (here the footer `<Button>` always renders) so
  the user can never be trapped — the heuristics reviewer and restraint critic both flagged this as the one
  thing to verify, and it held.

- **Reassurance copy that is conditionally FALSE is a defect, not decoration — gate it on the condition that
  makes it false, and verify the backend to know which condition that is.** Iter 19: Migrate's failed-job
  Callout always said "your existing data is intact," but for an *overwrite* job the orchestrator drops the
  target DB *before* the restore (verified in `orchestrator.go`: single-db `prepareTarget`→`DropDatabase`→
  `Restore`; cluster per-db `DropDatabase`→`Restore`), so a mid-restore failure can leave the old data gone.
  All four reviewers (incl. the restraint critic) shipped a `job.overwrite` ternary that swaps the line for an
  honest "it may already have been dropped — restore from a backup." **Lesson:** this is the mirror of the
  rejected "add a redundant warning" items — correcting a *false* statement at *zero net surface* (one string →
  a ternary on an existing field) clears restraint easily, because the true fact ("your old DB may be gone")
  appears NOWHERE else on the post-failure screen. Two tells it's the strong case: (a) the existing copy is
  *false in a real branch*, not merely redundant, and (b) the corrected fact is otherwise hidden. **Hedge
  honestly:** the failure can land *before* the destructive step too (e.g. during dump), so say "may already
  have been dropped," not "is gone" — don't over-claim doom any more than you'd under-claim safety. And per the
  iter-11 rule: *verify the backend before writing reassurance/failure copy* — the whole fix hinges on the
  drop-before-restore ordering being real, which had to be confirmed in the Go orchestrator, not assumed.

- **Adding the FIRST guard to an unguarded destructive-by-effect action is genuine error-prevention, not
  the decoration the restraint critic kills — and it's the mirror image of the rejected items.** Iter 18:
  Roles "Rotate password" rotated on a single unguarded click; rotation invalidates the old password
  instantly, so any live app on it loses DB access until reconfigured — the SAME blast radius as the row's
  Delete, which was already gated by a typed confirm, and the page even promises "Every action here is
  guided and confirmed." All four reviewers (incl. the restraint critic) shipped a plain `ConfirmDialog`
  on it. **Lesson:** the rejected items (iters 6/7/13/14) all ADDED a warning/copy that RESTATED something
  already on screen, or piled an Nth gate onto an already-gated flow. The opposite case — there is NO
  confirmation at all on a production-breaking, irreversible action — is the strong case for adding one.
  Two tells that it clears restraint: (a) a *sibling action of equal blast radius* is already gated (the
  asymmetry is itself a Consistency failure), and (b) the page makes a "guided/confirmed" promise this
  action breaks. When both hold, add the missing first guard. **Altitude matters:** match the gate to the
  consequence — rotation isn't data loss, so use the *plain* `ConfirmDialog` (one "are you sure?"), NOT the
  type-the-name `TypedConfirmDialog` (reserved for irreversible data destruction like drops). A typed gate
  here would be the modal wall Priya rejects; one confirm click is proportionate. Also: when you gate an
  action behind a dialog, the dialog's own Confirm button carries the busy state — drop any now-redundant
  inline button spinner so the change isn't purely additive.

- **Convergence is provisional — a late discovery pass can still find a real bug; don't rubber-stamp
  the streak.** Iter 17 was the "final" check at stable_streak 2/3; four of five agents converged, but
  the nav/IA agent found a genuine defect (the `<main overflow-y-auto>` scroll container persists
  `scrollTop` across `<Outlet/>` route swaps, so you land mid-page in the next view). It shipped 4–0,
  resetting the streak to 0. **Lesson:** keep each convergence pass honest — a high bar is for filtering
  *decoration*, not for refusing to look. When an agent surfaces something, verify it against the code
  (here: confirmed `<main>` is the scroll container and is never remounted) rather than dismissing it
  because the prior two passes were clean. Converging early is a win; converging *blind* isn't.
- **Behavior-correctness fixes with zero added surface are NOT the decoration the restraint critic
  kills — and the critic agrees.** The rejected items (iters 6/7/13/14) all *added* UI/copy that
  restated something already present. The scroll-reset fix added no UI, no control, no copy — it made
  navigation match the universal "start at the top" expectation (what React Router's `<ScrollRestoration>`
  provides). The restraint critic shipped it outright ("zero added surface area; the bug is real and
  routine"). **Lesson:** "restraint" is about surface-vs-payoff, not about never touching anything. A real
  defect fixed in ~5 lines with no new surface clears the restraint bar easily; don't pre-reject it as
  "churn." Conversely, scope it tightly: key the reset on the route *path* (not on every render) so an
  in-place data refresh doesn't yank a reading user to the top — that scoping is what makes it friction-free.


- **Co-locate config with the operation it configures by *moving* it (one home), not by
  adding a second copy.** Iter 11: backup config lived on /settings, operations on /backups
  — the canonical "configure-then-run bounce." The restraint-defensible fix was to MOVE the
  form into a shared component rendered on /backups (in a `Sheet`) and REMOVE it from
  Settings — net-neutral surface, single home, no duplication, and Settings got *simpler*.
  All three of {heuristics expert, technical persona, restraint critic} shipped it straight
  up; the critic explicitly preferred "moved, not duplicated." **Lesson:** when an item says
  "co-locate," resist the tempting additive version (keep it on page A *and* add it to page B)
  — two homes for one setting is the consistency/over-surface trap a restraint critic kills.
  Move it. A `Sheet` (side panel) is the right disclosure when the destination page is already
  dense and the config is an occasional task — it's progressive disclosure, not new machinery,
  and keeps the page's primary (operational) content unburied.
- **When you move a setting off its old home, leave a one-line pointer so muscle-memory users
  aren't stranded.** Iter 11: removed backup config from Settings → added a single info Callout
  ("Looking for backup storage? It's on the Backups page →"). The restraint critic called it
  the weakest element but kept it as "minimum-clutter migration breadcrumb" — flagging it as
  time-boxable debt to remove once users relearn the location. Recognition-over-recall beats a
  silent disappearance.
- **An honest failed-state message beats a comforting false one — and beats no message.** Iter 11:
  Sam (REJECT→SHIP) froze on the failed "Save & connect" state — "am I still backed up right now?"
  The tempting reassurance ("backups still land locally") was FALSE: verified in `handlers_config.go`
  that a failed save still persists the config and switches the live target, so new backups to the
  broken bucket *fail* until fixed. The shippable fix led with the *true* reassurance instead:
  "Your existing backups are untouched and still safe — but new backups to this bucket will fail
  until this is fixed." **Lesson:** find the precise truthful thing that answers the user's real
  question ("am I protected?") — don't paper over a failure with a falsehood, and don't dump a raw
  error without first answering the human question. Verify the backend behavior before writing
  reassurance copy.
- **A "make it fully honest" ask that needs a backend signal is out of scope for this frontend
  loop — defer it as a NEEDS-BACKEND backlog item, don't fake it.** Iter 11: Sam also wanted the
  green "Stored in S3" badge to not claim S3 until a connect succeeds. But `GET /config` returns
  no target-health signal, so on a cold reload the frontend genuinely *cannot* know — the badge
  has always meant "a bucket is configured," panel-wide, and this change neither created nor
  worsened that. Faking per-session health state would be hacky and inconsistent. Filed it as a
  backend item and shipped, because the in-form failed-save copy already prevents the gap from
  biting *during configuration* (only a cold reload over a never-initialized stanza is left).
  **Lesson:** a reviewer's blocker can be real *and* correctly deferred when the honest fix
  requires backend work this loop can't touch and the change didn't introduce the gap — document
  it precisely rather than overrule it or hack around it.

- **For an action whose effect requires manual follow-up, the confirm copy must name
  the follow-up explicitly — passive "X then happens" framing implies automation.**
  Iter 9: the pooler-enable confirm said "Your apps then connect to <addr> instead of
  Postgres directly." Enabling PgBouncer does *not* reroute anything; the user must edit
  each app's connection string. The passive "your apps then connect…" reads as the system
  doing it for you → user enables, sees no change, debugs a phantom. Fix: lead with the
  negation of the misconception ("Enabling won't move any app over by itself") then state
  the concrete manual step. **Lesson:** when a feature configures a *capability* the user
  must then *opt into* (repoint a connection string, flip an env var), say so in the
  imperative; don't describe the end-state as if it arrives automatically.
- **Review the whole dialog/section for internal consistency, not just the sentence you
  changed.** Iter 9: my paragraph fix was clean and 3 reviewers shipped it outright, but
  Sam (the non-technical persona) caught that an *adjacent, unchanged* bullet — "Route N
  roles through it" — still implied auto-routing and now contradicted my new "you must
  repoint" paragraph. Fixing one misleading sentence can expose/sharpen a neighboring one.
  Reword was cheap, clearly right, and strengthened the same fix, so I took it in-iteration
  (bullet → "Allow N roles to connect through the pooler", which is also more accurate:
  enabling just adds the role to the userlist). **Lesson:** when you correct a mental-model
  bug in copy, scan the surrounding copy in the same view for other phrasings that assert
  the old (wrong) model — they'll now clash. Reconcile them together.

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

- **When resetting a form for "do it again", keep the reusable inputs and clear only the
  per-run ones — and always reset destructive flags.** Iter 12: Migrate's "Start another"
  used to leave the whole form pre-filled. The naive fix (full wipe) was REJECTED by the
  restraint critic — the source connection (host/user/password) is genuinely reusable for
  the next database off the same host, and blanking it adds friction to the likely-next
  task. The shippable fix kept the connection but cleared the per-run identifiers
  (database, target, exclude) AND force-reset the destructive `overwrite` flag + its typed
  confirm. **Lesson:** "reset the form" is not one decision — split inputs into *reusable
  infrastructure* (keep) vs *per-run intent* (clear), and treat any *destructive/armed
  toggle* as must-clear regardless (a checked "replace/drop" silently surviving onto a new
  target is a real footgun — all four reviewers, including the personas, flagged this as
  THE value of the change). The "keep what's reusable, clear what's per-run" split reads as
  natural, not inconsistent, even when it means different flows keep different fields.
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
