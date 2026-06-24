# UX backlog

Living, prioritized list of concrete UX problems. Seeded by the Mode-S parallel
audit (11 agents: one per view/flow + nav/IA, first-run, cross-view consistency).
Highest payoff / lowest effort first. Mark items done as they ship; mark
`~~rejected~~` with a one-line reason when the review panel kills one.

Format per item:
`- [ ] (payoff/effort) <view/flow> — <problem> → <proposed fix>`

## Open

> **🏁 CONVERGED (iter 27) — `stable_streak` 3/3. The loop is COMPLETE; see `COMPLETE.md`.**
> The final Mode-S pass (5 agents, same coverage, HIGH bar + full digest) again surfaced no high/med
> frontend-only item. Four agents hard-converged. The Dashboard/Query agent floated **one candidate that
> collapsed as a FALSE PREMISE** on code inspection (self-rejected, no panel — iter-7/14/26 precedent):
> **Dashboard — change the Disk StatCard danger threshold `diskPct > 90` → `>= 90` to "match the backend
> health verdict."** **FALSE:** the agent claimed the card "shows *neutral* color" at exactly 90% while the
> badge shows Needs-attention. At `diskPct === 90.0` the card is **`warn` (yellow)**, not neutral (`90 > 80`
> is true, `Dashboard.tsx:150`); the backend flags `pct >= 90` (`handlers_dashboard.go:127`) → red badge **+**
> a "disk nearly full" line in the "Things to look at" Callout — three concurrent signals, nothing hidden.
> Stripped of the false "neutral" framing it's "turn the card red at the single float value `90.0`" — a
> practical no-op that would also de-align the disk gauge from its three sibling gauges (CPU/Mem/Connections
> all use `>`, an independent gradient heuristic, not a mirror of the backend's binary verdict). Backlog
> actionable-empty AND fresh discovery surfaced no high/med item → **third `stable_streak` increment, 2 → 3**.
> Per the contract, the UX has converged — `COMPLETE.md` written, loop stops.
>
> ---
> _Prior status (iter 26):_ backlog actionable-empty (NEEDS-BACKEND backup-badge item + low/watch nits). Ran a fresh
> **Mode-S discovery/convergence pass** (5 agents, same coverage as iters 15–25, HIGH bar + full shipped/rejected
> digest). **All five views converged.** Three agents hard-converged (Dashboard+Query; Alerts+Migrate;
> Settings+Tuning+Pooler+Login — the last correctly self-rejected the low/watch lockout-duration copy nit on
> restraint, noting only that the backend *does* send `locked_until`, which keeps it a parked low item). Two
> agents floated **one candidate each, both FALSE on code inspection** — self-rejected, no panel (iter-7/13
> precedent):
> - **Backups RestoreModal — gate "Restore now" on a non-empty datetime in PITR mode** (Roles/Backups agent,
>   self-rated HIGH/S on a "silent restore-to-latest" framing). **FALSE PREMISE:** the datetime `Input`
>   (`Backups.tsx:885`) has `required` and the button (`:835`) is a real `type="submit" form="restore-form"`, so
>   HTML5 constraint validation **blocks** the submit on an empty rendered required field — it's neither silent
>   nor submittable. The agent found `required` and rationalized past it. Stripped of the false framing it's
>   "duplicate a native required-gate with a disabled button," a sweeping consistency preference restraint kills.
> - **Layout sidebar — add `tooltip={item.label}` to nav buttons for the collapsed state** (nav/IA agent,
>   self-rated High/S, "affects every mobile user"). **FALSE PREMISE:** `<Sidebar>` uses no `collapsible` prop →
>   default `"offcanvas"` (`ui/sidebar.tsx:154`), where a collapsed sidebar slides fully off-screen (no icon
>   rail), and mobile renders a Sheet with **full labels** (`:181`). The tooltip is gated `hidden={state !==
>   "collapsed" || isMobile}` (`:533`) → zero visible effect anywhere in this app's mode.
>
> **stable_streak increments 1 → 2.** One more clean pass → write COMPLETE.md and stop. Next iteration: the final
> Mode-S convergence check (don't manufacture low-value work to avoid converging — converging early is a win).
>
> ---
> _Prior status (iter 25): a fresh 5-agent Mode-S pass. **All five views converged.** Four hard-converged; the
> Roles/Backups agent floated one borderline candidate — gate the Restore…/Test-a-restore/Deep-restore-test
> buttons on `hasNoBackups` — self-rated medium-LOW and self-rejected on inspection (the "no backups" state is
> already shown twice: `BackupStatusSummary` danger Callout `Backups.tsx:488` + Backup-history `EmptyState`
> `:256`; `RestoreModal` is typed-confirm so an empty repo yields a clear server error; restore-tests are
> read-only). Other finds backend-coupled → out of scope. stable_streak 0 → 1._
>
> ---
> _Prior status (iter 23): a fresh 5-agent Mode-S pass. **All five views converged.** Three candidates
> surfaced and ALL collapsed on code-level inspection (self-rejected, no panel — iter-5/13/14 precedent):_
> - **Query — clear the results panel when the SQL changes** (Dashboard/Query agent). **FALSE PREMISE about
>   expected behavior.** Clicking a "Try:" sample (`Query.tsx:88`) or editing the textarea (`:99`) changes
>   `sql` but leaves the prior `result` (`:38`) visible. The proposed `useEffect(…, [sql])` fires on every
>   keystroke and would wipe the result table the moment you edit a query to refine it — hostile to the
>   run→read→refine→run loop. The persisted result is the universal SQL-console convention (psql, pgAdmin,
>   DataGrip, Jupyter, devtools all keep the last-run output while you type the next query); the panel means
>   "last run," not "current editor text," and carries its own row-count/duration. The agent itself ended on
>   CONVERGED. See learnings.md.
> - **Backups — "Back up now" destination awareness** (Roles/Backups agent). Agent self-rated **low payoff**:
>   the destination badge is already in the page header and the run is confirmed; surfacing the destination on
>   the button too is incremental polish, not structural. Not promoted.
> - **Backups — "redundant" empty-state messaging** (nav/IA agent). On inspection it's **intentional two-layer
>   design**: the danger-tone Callout is the *status warning* ("your data is not protected"), the EmptyState is
>   the *next step* ("run your first backup"). Different purposes, not duplication. Agent ended on CONVERGED.
>
> Backlog actionable-empty AND a fresh discovery pass surfaced no high/med item → **second `stable_streak`
> increment, 1 → 2/3**. One more clean convergence pass → write COMPLETE.md and stop. Next iteration: run a
> final Mode-S convergence check (don't manufacture low-value work to avoid converging — converging early is a
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
- [ ] (low/S, NEEDS BACKEND) Alerts — durable fix for the empty-credential channel hole. The iter-24 fix is a
  **client-side** guard in `ChannelModal`; the backend `handleSaveAlertChannel` still accepts an enabled channel
  with blank credentials if anything POSTs outside the modal. → Have the handler reject (or auto-disable) an
  enabled channel whose required credential is empty. Out of this frontend-only loop's scope; both technical
  reviewers (Priya + UX) flagged it as the eventual honest fix but agreed it shouldn't block the UI guard.
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

- [x] (high/S) Alerts — a **new notification channel could be saved enabled with no credentials**, producing
  a **silent dead alert pipeline**. The backend `handleSaveAlertChannel` (`internal/server/handlers_alerts.go`)
  validates **only the channel kind**, never credential presence — so an enabled Pushover/Webhook channel with
  a blank token/user/URL saves fine. The `ChannelCard` then shows a green **"Configured"** badge
  (`configured = Boolean(config?.enabled)`) and the existing `rulesWontFire` guard **misses it** (it only checks
  `c.enabled`, not whether creds exist), so the user gets two green lights over a channel that can never notify —
  the failure only surfaces when a real alert silently fails to reach them. **Shipped iter 24.** Fix in
  `ChannelModal`: `requiredMissing` (pushover: token|user blank; webhook: url blank) +
  `blockEmptyNew = !config && enabled && requiredMissing`; the **"Save channel"** button is
  `disabled={busy || blockEmptyNew}` with an explanatory `title`. **Scoped to NEW channels (`!config`) on
  purpose** — existing channels' secret fields (`PushoverToken`, `WebhookURL`) come back blank because
  `maskAlertChannels` strips them (write-only), so a blank token on **edit** means "keep the stored secret";
  gating that would break the edit flow. A **disabled** draft channel can still be saved with blanks. ~3 lines,
  no new component/visible copy. Added a test: a new enabled pushover channel can't be saved blank, then saves
  once token+user are filled. **4 SHIP, zero blockers** — UX heuristics ("textbook H#5 error-prevention; the
  `!config` scope is the critical detail — it spares the edit flow where blank = keep-secret"); Sam ("a silent
  safety net is worse than no safety net because I trust it; the greyed button + tooltip is the gentlest way to
  stop me"); Priya ("the form refusing to lie to me at zero workflow cost; 'Send test' is opt-in so it's not a
  guard; scoping to new-channel is correct, not a shortcut"); restraint critic ("the FIRST and ONLY guard on a
  currently-unguarded silent-failure state, NOT an Nth redundant warning — the existing Callout inspects the
  wrong field and literally can't catch this; disabling a submit on missing required fields is the most baseline
  form affordance there is"). Surfaced by the iter-24 final convergence panel (4 of 5 views converged; verified
  REAL against the backend before promoting — iter-11 lesson). Gates: typecheck ✓, 146 tests ✓ (145 + 1 new),
  build ✓ (dist regenerated + staged), go build ✓ (exit 0, outside sandbox). stable_streak resets 2 → 0.

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
