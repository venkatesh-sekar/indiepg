# UX backlog

Living, prioritized list of concrete UX problems. Seeded by the Mode-S parallel
audit (11 agents: one per view/flow + nav/IA, first-run, cross-view consistency).
Highest payoff / lowest effort first. Mark items done as they ship; mark
`~~rejected~~` with a one-line reason when the review panel kills one.

Format per item:
`- [ ] (payoff/effort) <view/flow> — <problem> → <proposed fix>`

## Open

> **Status (iter 22):** ran a fresh 5-agent Mode-S discovery/convergence pass (same coverage as iters 15–21).
> **All five views converged** ("no new high/med item"). Two candidates surfaced and BOTH collapsed on
> code-level inspection (self-rejected, no panel — iter-5/13/14 precedent: don't run a panel to rubber-stamp a
> provably-zero-payoff or false-premise change):
> - **Migrate overwrite confirm — add `dismissible={false}`** (nav/IA agent). **FALSE PREMISE.** The agent
>   claimed an Escape/click-outside could "proceed without typing the confirmation." Verified in `Migrate.tsx`
>   (lines 336–361): the destructive action fires ONLY via the "Overwrite & migrate" button
>   (`onClick={start}`, `disabled={busy || !overwriteMatches}` — you must type the target name); dismissing
>   calls `setConfirmOpen(false)` and executes nothing. Dismissing is the SAFE escape — the mirror opposite of
>   iter-20's SecretsModal (where dismiss = irreversible credential loss). Non-dismissible here would trap a
>   user inside a destructive dialog. Negative payoff. See learnings.md.
> - **BackupStorageForm — "clear saved credential" affordance** (Roles/Backups agent). Speculative +
>   backend-dependent: the agent couldn't confirm the API accepts an empty/clear signal; *changing* a credential
>   already works (type a new key). Out of scope for this frontend-only loop. Not promoted.
>
> Backlog actionable-empty AND a fresh discovery pass surfaced no high/med item → **first `stable_streak`
> increment, 0 → 1/3**. Two more clean convergence passes → write COMPLETE.md and stop. Next iteration: run a
> fresh Mode-S convergence check (don't manufacture low-value work to avoid converging — converging early is a
> win).

### Quick wins (high/med payoff, S effort) — do these first
- [x] (high/S) Roles & Databases — the one-time `SecretsModal` was a plain `Modal`, so Escape/backdrop/X
  routed to `onClose` → `setSecrets(null)`, destroying the only copy of just-shown credentials.
  **Shipped iter 20** via an opt-in `dismissible?: boolean` prop on `Modal` (default `true`, every other
  modal unchanged) that hides the corner X + `preventDefault`s Escape/`onInteractOutside`; `SecretsModal`
  sets `dismissible={false}` so the explicit "I've saved them" button is the sole exit. 4 SHIP. See Done.
- ~~(high/S) Roles & Databases — `dropBusy` disables every Delete button during a
  drop~~ — **rejected iter 5**: `dropBusy` is true only while the modal
  `TypedConfirmDialog` is open, which makes the background inert/`aria-hidden`; no
  user can see or click an "unrelated frozen row," so per-row scoping has zero
  payoff. See learnings.md.
- ~~(med/S) Dashboard — make the "no backup yet" callout's "Backups page" a
  `<Link>`~~ — **rejected iter 6** on restraint. 3 of 4 reviewers shipped, but the
  restraint critic blocked (never overruled): the copy already names the page and the
  left-nav is one obvious click from every view, so the link only saves a click on an
  empty state seen ~once. Decorative payoff. See learnings.md.
- ~~(med/S) Query — show the server's `executed_sql` in a compact block~~ —
  **rejected iter 7** on restraint. 3 of 4 reviewers shipped, but the restraint critic
  blocked (never overruled): the only thing that ever rewrites the SQL is the auto-LIMIT
  (verified in `guard.go`), which always co-occurs with the existing "Results limited
  for safety" + "Add your own LIMIT…" copy — so the block only restates an
  already-explained fact. Redundant, not new signal. See learnings.md.
- [x] (med/S) Alerts — "Sustained" / "Cooldown" headers were bare jargon. **Shipped
  iter 8** as a *rename*, not a tooltip: `Sustained` → `Hold for` (matches the editor's
  own "Must hold for (minutes)" label). The proposed tooltip approach was reviewed
  (3 SHIP) but the restraint critic blocked it and offered the simpler rename — which
  also fixed Sam's touch caveat and added zero surface. `Cooldown` left as-is (the
  critic deemed it self-explanatory next to a duration value). See Done.
- [x] (med/S) Pooler — the enable-confirmation copy ("Your apps then connect to
  …") read as "the pooler reroutes them automatically". **Shipped iter 9**: reworded
  to "Enabling won't move any app over by itself. To use the pooler, change an app's
  connection string to point at <addr>…". Also reconciled the adjacent bullet
  ("Route N roles through it" → "Allow N roles to connect through the pooler") which
  Sam flagged as contradicting the new paragraph. 4 SHIP. See Done.
- [x] (med/S) Dashboard — "Connections" was shown twice (Postgres card + Server card)
  with slightly different formatting. **Shipped iter 10**: dropped the plain Postgres-card
  `Kv` row, kept the tinted Server-card saturation gauge (warns warn/danger as it fills,
  grouped with CPU/Mem/Disk). 4 SHIP. See Done.

### Higher-effort, clear payoff
- [x] (high/M) Backups + Settings — backup **config** (S3 destination, retention,
  encryption) lived under `/settings` while backup **operations** (run, history,
  restore) lived at `/backups`. **Shipped iter 11 — the canonical seed item (4 agents).**
  Moved the config form into a shared `BackupStorageForm` component and surfaced it on
  `/backups` via a "Backup storage" header button → right-side `Sheet`; the local-only
  warning's "Set up an S3 bucket" CTA now opens that same Sheet in-page (was a /settings
  link). Removed the form from Settings (now just tuning + pooler + a one-line pointer to
  Backups). Single home, no duplication, configure-then-run in one place. 4 SHIP. See Done.
- ~~(med/S→M) Settings — the backup-config card gives no next step after saving~~ —
  **subsumed by the co-location (iter 11)**: the form's success Callout now lives on the
  Backups page itself and says "Close this panel and run a backup" — the next step is
  right there, no cross-route link needed.
- [x] (med/M) Migrate — completing a migration and clicking "Start another" left
  the previous source connection, target name, and overwrite checkbox pre-filled.
  **Shipped iter 12** as a *targeted* reset (not a full wipe): "Start another" now
  KEEPS the reusable source connection (host/port/user/password/sslmode) so the next
  database off the same host needs no re-typing, but CLEARS the per-run fields
  (database-to-copy, target name, cluster exclude) and — safety-critical — resets the
  destructive overwrite flag + typed-confirm so a leftover "replace" can't carry onto
  a different target. Cross-panel send keeps the connection, clears the one-time code;
  receive (no connection) clears its db + code. The full-reset version was REJECTED by
  the restraint critic (kills the cheap same-source repeat); revised to its proposed
  targeted fix → 4 SHIP. See Done.
- ~~(med/M) Migrate — the overwrite gate is split across three intent-shifts
  (checkbox → button text flips to "Continue…" → modal asks to type the name) with no
  inline warning when overwrite is checked~~ — **rejected iter 13** on restraint. 3 of 4
  shipped, but the restraint critic blocked (never overruled): the single-db overwrite is
  already a three-stage escalating gate — the checkbox label says "(destructive)", the button
  flips to "Continue…", and the modal has a danger Callout + type-the-name confirm. The
  proposed inline Callout just restates the modal's text one click earlier (read twice in
  seconds), and the "match the cluster form" rationale is false — cluster's warning is
  *always-on* and conveys "drops every database," info its label omits. See learnings.md.
- [ ] (low/M) Settings — the page used to conflate three domains; **backup config
  moved to /backups in iter 11**, so Settings is now just Database tuning + Connection
  pooler (two coherent, self-titled cards) + a pointer. Largely resolved; only re-open
  if the remaining two cards need clearer grouping (low payoff — they already have
  distinct titles).
- [ ] (med/M, NEEDS BACKEND) Backups — honest backup-target health on the destination
  badge. The header badge shows green "Stored in S3 · <bucket>" whenever a bucket is
  *configured*, even if the last stanza-create failed (target unreachable). On a cold
  page load there's no way to know — `GET /config` returns no target-health signal
  (only the save response carries `backup_configured`/`backup_warning`). A user who
  reloads and sees a green S3 badge over an uninitialized stanza trusts a destination
  that doesn't work. → Surface target health on `GET /config` and tint the badge
  (ok/warn) accordingly. Flagged by Sam in the iter-11 review; deferred because it's
  out of this frontend-only loop's scope. The iter-11 failed-save Callout now tells the
  truth in-form, so this can't bite during configuration — only on a cold reload.
- ~~(med/M) DatabaseTuning — add a reassuring intro line (defaults tuned for the
  hardware; the typical indie app needs no changes)~~ — **rejected iter 14** on restraint
  (premise false on inspection, iter-5 pattern, self-rejected with decisive evidence — no
  panel). The page **already** opens with that reassurance threefold: the title is "Database
  tuning (host-sized)", the first element is an info Callout titled "Sized to this server
  automatically" reading "tuned on safe best defaults — you don't need to tune anything by
  hand", and the active Mixed profile's description says "the best default for an indie-hacker
  box". The help-text sub-concern is also handled (each setting has a plain-English one-liner).
  Nothing to add that wouldn't restate existing copy. See learnings.md / Rejected.

### Lower / watch
- [ ] (med/M) Query — accidental write SQL (DELETE/UPDATE/DROP pasted in) is only
  rejected server-side after Run. → Optional: client-side keyword detector that warns
  before Run that the editor is read-only. (Keep restrained — copy hint, not a parser.)
- [x] (high/S) Login — the form was a **permanent dead-end after a server lockout**: `locked` disabled
  the input + the cleared password disabled the button, and `locked` only reset inside `onSubmit` (which
  could no longer fire), so the only escape was a full-page reload — and the form stayed frozen even after
  the server lock expired. **Shipped iter 21** via a subtractive fix (input `disabled={busy || locked}` →
  `disabled={busy}`); the warn Callout still surfaces the lockout, the server still enforces it, and the
  user can now retype + resubmit to recover. 4 SHIP, zero blockers. See Done.
- [ ] (low/S) Login — lockout message "Try again later" gives no sense of how long. → Frontend-only:
  soften to "Try again in a few minutes" (a precise duration needs a backend hint, which is out of scope).
  Still open as a low/watch copy nit; both personas re-flagged it as non-blocking polish in the iter-21
  review (the functional dead-end, the real problem, is now fixed).

## Done

- [x] (high/S) Login — the sign-in form became a **permanent dead-end after a server-enforced lockout**.
  On repeated wrong admin passwords `auth.Authenticate` returns `CodeLocked` (HTTP 429); the handler set
  `locked=true` and cleared the password. The password `Input` was `disabled={busy || locked}` → **disabled**,
  and the Sign-in `Button` is `disabled={busy || !password}` → **disabled** (password just cleared). The
  `locked` flag is reset **only** inside `onSubmit` (`setLocked(false)`), which could never run again because
  both controls were disabled — so the form froze with **no recovery path but a full-page browser reload**
  (which a user has no reason to discover), and it stayed frozen **even after the server lock window expired**.
  Distinct from the parked "Try again later" copy nit — this is a state-machine dead-end, not wording.
  **Shipped iter 21.** Subtractive fix: input `disabled={busy || locked}` → **`disabled={busy}`** (removed the
  lockout-disable; added a 5-line comment explaining why). The lockout is still surfaced by the existing
  warn-tone `Callout`, and the **server remains the sole enforcer** of the lock — the UI disable was never the
  enforcement, only the trap. Now a locked-out user can retype + resubmit; the resubmit clears `locked` and the
  server re-decides (still locked → warn again; expired → logs in). Zero added UI/controls/copy. Updated the
  test that *encoded* the dead-end (`"…disables the input"`) to assert the lockout is still surfaced (warn
  alert + cleared field) AND the input stays editable AND a retype+resubmit fires a second auth attempt
  (recovery without reload). **4 SHIP, zero blockers** — UX heuristics ("absolute User-Control-&-Freedom
  violation: a permanently unrecoverable state with no escape; one-char removal, no weakened security
  boundary; H#4 consistency win — lockout now clears+re-enables like the wrong-password path"); Sam ("I'd have
  sat there reloading like an idiot; now the box stays alive, I retype and I'm in — no docs"); Priya ("removes
  a wall, keeps the gate where it belongs — on the server; nothing buried, nothing slowed"); restraint critic
  ("the rare change a simplicity critic should wave through — makes the code smaller and removes a trap;
  do-nothing leaves every locked-out admin frozen until they guess 'reload'"). Surfaced by the iter-21
  Settings/Login convergence agent (the other 4 of 5 agents converged); verified against the code before
  promoting (the existing test literally asserted the dead-end). Gates: typecheck ✓, 145 tests ✓ (updated 1
  existing case, no net new), build ✓ (dist regenerated + staged), go build ✓ (exit 0, outside sandbox).
  stable_streak stays 0.

- [x] (high/S) Roles & Databases — the one-time **`SecretsModal`** ("Save these now", shown after New App
  / rotate) displays passwords + connection strings that **"cannot be retrieved again."** It was a plain
  `Modal`, so **Escape, a backdrop click, or the corner X** all routed to `onClose` → `setSecrets(null)` —
  destroying the only copy before the user had copied it (recovery = rotate again). **Shipped iter 20.**
  Added an opt-in **`dismissible?: boolean`** prop to the shared `web/src/components/Modal.tsx` (default
  `true`, so all other modals are byte-for-byte unchanged); when `false` it sets `showCloseButton={false}`
  (drops the corner X) and `preventDefault`s `onEscapeKeyDown` + `onInteractOutside` (swallows Escape +
  backdrop). `SecretsModal` now passes `dismissible={false}`, making the existing **"I've saved them"**
  button the sole exit — net surface *removes* the X, adds no new control/copy/click. Extended the rotate
  test to assert: after confirming, the secrets dialog has no Close (X) button, an Escape keydown does NOT
  dismiss it, and only the "I've saved them" click closes it. **4 SHIP, zero blockers** — UX heuristics
  ("textbook H#5 error-prevention; closes all three dismiss vectors in concert; works by subtraction");
  Sam ("the safe direction to fail in — one stray Escape used to mean rotate-again; the footer button always
  renders so I'm never trapped"); Priya ("the one place a reflexive dismiss is irreversible loss; scoped to
  `SecretsModal`, every other dialog keeps Escape/click-away; a guardrail, not hand-holding"); restraint
  critic ("removes a control, adds none; `dismissible` is a safe-default opt-in with a concrete caller, not
  premature config; a bespoke SecretsModal would be *more* divergence; do-nothing accepts guaranteed
  credential loss"). Surfaced by the iter-19 Roles/Backups convergence agent; same "harden an irreversible
  path" class as the iter-18 rotate-confirm fix. Gates: typecheck ✓, 145 tests ✓, build ✓ (dist regenerated
  + staged), go build ✓ (exit 0, outside sandbox). stable_streak stays 0.

- [x] (high/S) Migrate — the failed-job Callout (`DirectJobProgress`, `Migrate.tsx`) **unconditionally**
  printed *"Your existing data is intact — the import only writes a freshly created database."* That is
  **FALSE for an overwrite ("Replace if exists / destructive") job**: verified in
  `internal/migrate/orchestrator.go` that an overwrite **drops the existing target database BEFORE the
  restore** (single-db: `prepareTarget`→`DropDatabase` then `Restore`; cluster: per-db
  `DropDatabase`→`Restore`), so a mid-restore failure can leave the old DB gone — while the UI calmly told
  the user their data was safe. **Shipped iter 19.** Gated the one line on the already-present `job.overwrite`
  field: additive failure keeps the "intact" copy; overwrite failure now reads *"Because you chose to replace
  the existing &lt;target_database&gt; (or 'existing databases' for a cluster), it may already have been
  dropped before the failure — restore from a backup if you need the old data back."* The **"may" hedge** is
  deliberate — the failure can land before or after the drop (e.g. during dump), so the UI doesn't over-claim
  doom; it stops the false reassurance and points to recovery (the backup). Zero net surface (one string → a
  ternary on an existing field), no new component/control. Added 2 tests: additive failure shows "intact" and
  NOT "may already have been dropped"; overwrite failure shows "may already have been dropped" + "restore from
  a backup" and NOT "intact". **4 SHIP, zero blockers** — UX heuristics ("textbook H#9 error-recovery; the old
  copy made a false safety claim to users who just lost a database"); Sam ("the one lie a panel like this can
  never tell — and it tells me my next move, restore from a backup"); Priya ("honest correction, accurately
  hedged, right altitude — no nannying; the 'may' is correct not weasel-wording"); restraint critic ("corrects
  a verified-false reassurance at zero net surface; the true state appears nowhere else on the screen — NOT the
  Nth-warning-on-a-gated-flow pattern the loop rejects"). Surfaced by the iter-19 Alerts/Migrate convergence
  agent (3 of 5 agents converged; this + the SecretsModal item were the two genuine finds); backend behavior
  verified before writing the copy (iter-11 lesson). Gates: typecheck ✓, 145 tests ✓ (143 + 2 new), build ✓
  (dist regenerated + staged), go build ✓ (exit 0, outside sandbox). stable_streak stays 0.

- [x] (high/S) Roles & Databases — the per-row **"Rotate password"** button rotated immediately on a
  single unguarded click (`RolesDatabases.tsx:230` called `rotate()` directly). Rotating invalidates the
  old password server-side **instantly**, so any live app still using the old credential loses DB access
  until its connection string is updated — the **same blast radius** as the row's Delete button, which IS
  gated by a `TypedConfirmDialog` with consequence copy. The page header even promises *"Every action here
  is guided and confirmed,"* yet Rotate was the lone unguarded mutation, with zero warning anywhere about
  the breakage. **Shipped iter 18.** Gated rotate behind the existing **plain `ConfirmDialog`** (NOT the
  typed-name gate — rotation isn't data loss, so the proportionate level): added a `rotateTarget` state
  mirroring `dropTarget`; the button's onClick now `setRotateTarget(name)`; dialog (tone danger, confirm
  "Rotate password") reads *"The current password for <name> stops working immediately. Any app connecting
  as this user will lose access until you update its connection string with the new password — shown once,
  right after."* Removed the now-redundant inline "Rotating…" button spinner (busy shows on the dialog
  Confirm). Reused the existing component, ~20 lines, +1 click; added a test asserting the API does NOT
  fire on the row-button click, only after the dialog confirm. **4 SHIP, zero blockers** — UX heuristics
  (fixes a simultaneous Consistency + Error-Prevention failure; plain confirm is the right altitude vs the
  typed gate); Sam ("that one sentence stops me cold in the right way — saves me from nuking my live app");
  Priya ("proportionate gate on an irreversible, connection-breaking op; plain confirm not type-the-name is
  the right call; one click, buries nothing"); restraint critic ("creates the absent FIRST gate on an
  unguarded production-breaking action whose sibling of equal blast radius is already gated — categorically
  different from the prior Nth-redundant-warning rejects; genuine error-prevention"). Surfaced by the
  iter-18 Roles/Backups convergence agent (the other 4 of 5 converged); verified against the code before
  promoting. Gates: typecheck ✓, 143 tests ✓ (142 + 1 new), build ✓ (dist regenerated + staged), go build ✓
  (outside sandbox). stable_streak stays 0.

- [x] (med/S) Navigation/IA — the routed view renders inside a `<main className="… overflow-y-auto …">`
  that is its OWN scroll container. With client-side routing only the `<Outlet/>` children swap on
  navigation; the `<main>` element is never remounted, so its `scrollTop` carried over between views.
  Scroll down in a long view (Backups history, a long Roles/Alerts table), click another sidebar item,
  and you landed in the new view still scrolled down with its header off-screen — "stuck page" feel,
  contradicting the universal web expectation that navigation starts each page at the top (React Router
  ships `<ScrollRestoration>` for exactly this). **Shipped iter 17.** Fix in `Layout.tsx`: a `useRef`
  on `<main>` + a `useEffect` keyed on `location.pathname` that sets `scrollTop = 0` on each route
  change. Keyed on the *path*, so an in-place re-render of the same view (e.g. backup history
  auto-refreshing while you read it) does NOT yank you to the top — only actual navigation does. Zero
  added visual UI/control/copy; a `data-testid="main-content"` was added for the test (sets scrollTop
  to 400, navigates, asserts it resets to 0). **4 SHIP, zero blockers** — UX heuristics (Consistency &
  Standards fix, no anchor-nav/back-forward contract broken); Sam ("only worry would be getting yanked
  off data I'm mid-read on — it doesn't, it's keyed on path"); Priya ("deep-links land at the top like a
  fresh load; filters/selections live in state/URL not scrollTop, so nothing I care about resets");
  restraint critic ("behavior-correctness fix with zero added surface area; the bug is real and
  routine"). Surfaced by the iter-17 nav/IA convergence agent (the other 4 agents converged); promoted
  because it's a genuine defect, not decoration. Gates: typecheck ✓, 142 tests ✓, build ✓ (dist
  regenerated + staged), go build ✓ (outside sandbox). stable_streak reset 2 → 0.

- [x] (med/M) Migrate — "Start another" (shown after a terminal migration job) left the
  whole form pre-filled with the prior run's values: source connection, database/target
  names, and a checked destructive "overwrite/replace" flag. A user pulling a second DB
  off the same host could skim past a still-armed "Replace if exists" and silently drop a
  database they never meant to touch. Fixed across all four flows (one-db, cluster,
  cross-panel send/receive) as a **targeted reset**: keep the reusable source connection
  (host/port/user/password/sslmode); clear the per-run fields (database-to-copy, target,
  cluster exclude) and reset `overwrite`+`confirm`+`error`. Send keeps the connection but
  clears the one-time session code; receive (no connection) clears its db + the generated
  code. Exported `SingleDBForm` and added a test that arms overwrite, runs a job to
  terminal, clicks "Start another", and asserts the source host *persists* while
  database/target are blank and overwrite is disarmed (141 tests). Review: the **first
  (full-reset) version was REJECTED by the restraint critic** — the retained connection is
  useful for same-source repeats; it proposed resetting only the destructive flag. Revised
  to exactly that. Re-ran the panel → **4 SHIP** (UX heuristics — "keep infrastructure,
  clear intent" reads as natural, error-prevention win on the overwrite flag; Sam — "worst
  case is a harmless re-type, never an accidental overwrite"; Priya — same-source repeat
  friction gone, destructive flag safely cleared; restraint critic — "minimal correct fix,
  nothing left to drop"). Shipped iter 12.

- [x] (high/M) Backups + Settings co-location — **the canonical seed item** (flagged by
  4 of 11 audit agents). Backup **config** (S3 destination, retention, encryption) lived
  on `/settings`; backup **operations** (run, history, restore, restore-tests) lived on
  `/backups`. A user had to bounce between routes to configure-then-run and could hit the
  dependency mid-flow. Fix: extracted the config form into a shared
  `web/src/components/BackupStorageForm.tsx` (identical fields/behavior — saving still
  doubles as the connection/stanza test with inline result). On `/backups`: added a
  "Backup storage" header button opening a right-side `Sheet` that hosts the form; the
  `LocalBackupWarning` CTA ("Set up an S3 bucket") now opens that same Sheet **in-page**
  (was a `<Link to="/settings">`), so the user never leaves the backups workflow. The
  Sheet stays open after saving so the form's "ready / not ready" connection-test feedback
  persists. Removed the form from Settings (now Database tuning + Connection pooler + a
  one-line info Callout pointing to /backups so users with old muscle-memory aren't
  stranded). Net surface roughly neutral — a form *moved*, not added; Settings got simpler;
  single home, no duplication. During review Sam (REJECT→SHIP) caught that on a *failed*
  Save&connect the user can't tell if they're still protected; addressed in-iteration by
  leading the failed-save Callout with an honest reassurance ("Your existing backups are
  untouched and still safe — but new backups to this bucket will fail until this is fixed…"),
  verified truthful against `handlers_config.go` (config saves + live target switches even
  on failed connect). The badge-health gap Sam also raised needs a backend signal (`GET
  /config` has none) → filed as a separate item. Shipped iter 11 (4 SHIP — UX heuristics,
  Sam (after fix), Priya, restraint critic; the critic emphatically: "kills a top-cited
  route bounce at net-neutral surface; form moved not duplicated; Settings got simpler").
- [x] (med/S) Dashboard — the "Connections" metric (active/max) was rendered twice on
  one screen: a plain `Kv` row in the **Postgres** card (`3 / 100 (3%)`, no tint) and a
  tinted saturation gauge in the **Server** card (`3/100`, sub `3%`, warn/danger as
  connPct crosses 75/90, beside CPU/Mem/Disk). Same number, two formats — cognitive load
  with no extra signal, and a "do these disagree?" moment. Dropped the Postgres-card row;
  kept the single tinted Server-card gauge (strictly more informative — it warns before
  the limit, and sits with the other "% of capacity" gauges). Postgres card stays coherent
  (Status + cache hit, TPS, deadlocks, replication lag — all DB-internal health). Added a
  `getAllByText("Connections").toHaveLength(1)` invariant to the test. Shipped iter 10
  (4 SHIP — UX heuristics, Sam, Priya, restraint critic; the critic confirmed it's genuine
  duplicate removal, the opposite of over-design, and the kept gauge strictly dominates the
  deleted row).
- [x] (med/S) Pooler — the enable-confirmation dialog's closing paragraph read "Your
  apps then connect to <addr> instead of Postgres directly", which can be read as the
  pooler auto-rerouting apps. A user could enable it, see no change, and debug a
  phantom problem. Reworded to lead with "Enabling won't move any app over by itself"
  and spell out the manual step ("change an app's connection string to point at
  <addr>… Apps you don't repoint keep connecting to Postgres directly, unchanged"),
  preserving the "no restart / no data touched" honesty line. During review Sam caught
  that the adjacent bullet "Route N roles through it" still implied auto-routing and
  contradicted the new paragraph; addressed in-iteration by rewording it to "Allow N
  roles to connect through the pooler" (accurate — enabling just adds the role to the
  pooler's userlist, it doesn't move connections). Shipped iter 9 (4 SHIP — UX
  heuristics, Sam (after the bullet fix), Priya, restraint critic; restraint + Priya
  both noted mild verbosity but neither blocked, calling the old copy a genuine
  "why did nothing happen" trap).
- [x] (med/S) Alerts — the `for_seconds` column header read "Sustained", bare jargon a
  user couldn't decode without opening the editor (does "5m" mean wait-then-check or
  must-hold-for-5m?). Renamed the header to **"Hold for"**, matching the rule editor's
  own "Must hold for (minutes)" label — self-documenting next to its duration values,
  consistent with the edit flow, zero new surface. Shipped iter 8. The original
  backlog item proposed a `Tooltip` on both "Sustained" and "Cooldown"; the panel ran
  3 SHIP (UX heuristics, Sam, Priya) but the **restraint critic blocked** the tooltip
  approach and proposed the rename as the simpler fix (the "Cooldown" tooltip was
  decoration; tooltip machinery + a hover-only affordance was overkill for one
  ambiguous word, and hover fails on touch). Addressed the blocker in-iteration with
  the rename rather than rejecting — strictly less surface than the shipped-by-3
  version and exactly what the critic asked for. "Cooldown" left unchanged.
- [x] (high/S) Alerts — enabled rules with no enabled notification channel fired
  silently into the void; nothing warned the user. Added a conditional warning
  `Callout` ("Your rules won't fire") between the channels card and the rules table,
  shown only when `hasEnabledRule && !anyChannelEnabled` and self-clearing the moment
  a channel is enabled. Shipped iter 4 (3 SHIP — UX heuristics + Sam + Priya; restraint
  critic conditionally rejected, preferring per-row/enable-time warnings, but conceded
  the banner is the least-bad option — and per-row would N-duplicate the message while
  enable-time adds a modal wall to a one-click toggle, so the banner is simplest here).
- [x] (high/S) Dashboard — the Postgres "Version" row always rendered "—" (backend
  never populates it; the field is `omitempty` and the foundation doesn't expose a
  server version yet). A blank version next to a green "Running" badge read as
  "unknown / partial data". Removed the row; remaining card rows are all live data.
  Shipped iter 3 (4 SHIP — all reviewers; both personas noted version is worth
  surfacing for real later, on a details page, not as a permanent placeholder).
- [x] (high/S) Roles & Databases — "No roles yet" empty state had no hint (the
  Databases card had one). Added a `hint` pointing to the card's user buttons and the
  page-header "New app (one-click)" path. Shipped iter 2 (3 SHIP; Sam's "above" vs
  header-button ambiguity fixed in-iteration by disambiguating the copy).

## Rejected

- ~~(med/M) DatabaseTuning — add a reassuring intro line ("defaults tuned for the hardware;
  the typical indie app needs no changes")~~ — iter 14. **Premise false on inspection** (iter-5
  pattern): the page already opens with that exact reassurance — title "Database tuning
  (host-sized)", an info Callout "Sized to this server automatically / tuned on safe best
  defaults — you don't need to tune anything by hand", and the active Mixed profile reads "the
  best default for an indie-hacker box". The "help text assumes DBA knowledge" sub-concern is
  also already handled (plain-English one-liner per setting). No edit possible that wouldn't
  restate present copy. Self-rejected with decisive evidence, no panel (rubber-stamping a
  zero-payoff change is the churn the loop guards against). learnings.md.
- ~~(med/M) Migrate — inline danger warning when the single-db overwrite checkbox is
  armed~~ — iter 13. Clean conditional implementation; 3 SHIP (UX heuristics, Sam, Priya).
  Restraint critic blocked and is never overruled: the overwrite already has a three-stage
  escalating gate (checkbox label "(destructive)" → button flips to "Continue…" → modal
  danger Callout + type-the-name confirm), the destructive action can't even fire from this
  screen (only the modal's typed confirm executes it), and the "match the cluster form"
  rationale is false — cluster's Callout is always-on and carries info its label omits
  ("drops every matching database"). The inline Callout just restates the modal one click
  early, read twice in seconds. learnings.md.
- ~~(med/S) Query — show the server's `executed_sql` in a compact block~~ — iter 7.
  Clean conditional implementation (block shows only when the SQL was actually
  rewritten). 3 SHIP (UX heuristics, Sam, Priya) but the restraint critic blocked and is
  never overruled: the sole rewrite path is the auto-LIMIT (verified in `guard.go` —
  `injectLimit` always sets `limited=true`), which always co-occurs with the existing
  "Results limited for safety" + "Add your own LIMIT…" copy. The block restates an
  already-explained fact; the only new bit (the literal cap value) doesn't earn a code
  block. learnings.md.
- ~~(med/S) Dashboard — link the "no backup yet" callout's "Backups page"~~ — iter 6.
  3 SHIP (UX heuristics, Sam, Priya) but the restraint critic blocked and is never
  overruled: copy already names the destination, left-nav is one click from every view,
  so the link only saves a click on an empty state seen ~once. Decorative. learnings.md.
- ~~(high/S) Roles & Databases — scope `dropBusy` per-row~~ — iter 5. No observable
  payoff: a drop only runs while the modal `TypedConfirmDialog` is open, so the
  background table is already inert/`aria-hidden` and the user can't see or click the
  "frozen" unrelated rows. A global busy flag that only flips under a modal is
  effectively already scoped. Self-rejected on restraint (decisive evidence, no panel
  needed). Full write-up in learnings.md.
