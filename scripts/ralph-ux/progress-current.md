# Progress — UX loop

Rolling narrative, newest at top. One short entry per iteration: date, mode, what
changed, why.

## 2026-06-25 — iter 24 — Mode F (Alerts: block creating an empty enabled channel) — stable_streak 2 → 0
Ran the FINAL Mode-S convergence panel (5 agents, same coverage as iters 15–23, HIGH bar + full
shipped/rejected digest). **4 of 5 views converged.** The Alerts/Migrate agent floated two Alerts candidates;
I verified both against the code (iter-11 lesson: never act on an audit claim without checking the source):
- **Candidate A — rule Switch lacks loading/optimistic-revert.** **Self-rejected (false premise).** The
  shadcn/Radix `Switch` is fully *controlled* by `checked={rule.enabled}`, which is bound to server data and
  only changes after `cfg.reload()`. On a failed `toggleRule` the thumb never moves, so there is no misleading
  optimistic state to revert. The proposed spinner/revert machinery would guard a state that can't occur.
- **Candidate B — a NEW notification channel can be saved enabled with no credentials.** **Verified REAL and
  SHIPPED.** `handleSaveAlertChannel` validates only the channel *kind*, never credential presence, so an
  enabled Pushover/Webhook channel with blank token/user/URL saves; the card then shows green "Configured" and
  the `rulesWontFire` guard misses it (it checks `c.enabled`, not creds) — a *silent* dead alert pipeline.
  Fix in `ChannelModal`: disable "Save channel" when creating a new channel (`!config`) that's enabled but
  missing required creds, plus an explanatory `title`. Scoped to new channels because existing channels'
  secret fields are masked/write-only (blank on edit = "keep stored secret"), so gating edit would break it.
  Disabled drafts can still be saved blank. Ran the **full review panel → 4 SHIP, zero blockers** (restraint
  critic affirmed: first/only guard on an unguarded silent-failure state, not a redundant warning; "Send test"
  is optional so not sufficient; disabling a submit on missing required fields is the most baseline affordance).
  Added 1 test (146 total). Filed the durable handler-side validation as a NEEDS-BACKEND follow-up.
  **stable_streak RESETS 2 → 0** — a genuine improvement shipped, so the UX has NOT converged. Gates: typecheck
  ✓, 146 tests ✓, build ✓ (dist staged), go build ✓ (exit 0, outside sandbox — snap-confine blocks in-sandbox).
  LESSON: an audit's "add a loading/optimistic-revert state" claim is only valid if the control is *uncontrolled*
  or *optimistically* updated — a fully-controlled component bound to server state already can't show a stale lie.

## 2026-06-25 — iter 23 — Mode S (convergence check) — stable_streak 1 → 2
Backlog still actionable-empty after iter 22 (only the NEEDS-BACKEND backup-badge item + low/watch nits:
Query write-detector, Login lockout-duration copy, Settings grouping). Per the contract, ran a fresh **Mode-S
discovery/convergence pass** rather than chew the low-value items: a 5-agent parallel panel, each given its
view(s) + the full already-shipped/already-rejected digest + a deliberately HIGH bar. Coverage identical to
iters 15–22: (1) Dashboard + Query, (2) Roles & Databases + Backups + BackupStorageForm, (3) Alerts + Migrate,
(4) Settings + DatabaseTuning + Pooler + Login, (5) nav/IA + first-run + cross-view consistency.
**All five views converged.** Three candidates surfaced and **all three collapsed on code-level inspection**,
self-rejected with decisive evidence and no panel (iter-5/13/14 precedent):
- **Query — clear the results panel when the SQL changes** (Dashboard/Query agent). **FALSE PREMISE about the
  expected behavior.** Clicking a "Try:" sample (`Query.tsx:88` `setSql`) or editing the textarea (`:99`)
  changes `sql` but leaves the prior `result` (`:38`) visible. The proposed `useEffect(() => { setResult(null);
  setError(null); }, [sql])` fires on **every keystroke** and would wipe the result table the instant you edit a
  query to refine it (add a WHERE, fix a typo) — hostile to the run→read→refine→run loop. The persisted result
  is the **universal SQL-console convention** (psql, pgAdmin, DataGrip, Jupyter, devtools all keep last-run
  output while you compose the next query); the panel means "last run," not "current editor text," and carries
  its own row-count/duration. The agent itself ended on CONVERGED.
- **Backups — "Back up now" destination awareness** (Roles/Backups agent). Agent self-rated **low payoff** —
  the destination badge is already in the header and the run is confirmed; not promoted.
- **Backups — "redundant" empty-state messaging** (nav/IA agent). On inspection it's **intentional two-layer
  design** (danger Callout = status warning; EmptyState = next step) — not duplication. Agent ended on CONVERGED.
No code shipped, docs-only commit, no hard gates needed. Per the contract (backlog actionable-empty AND a fresh
discovery pass surfaced no high/med item) this is the **second `stable_streak` increment → 2/3**. One more clean
pass → write COMPLETE.md and stop the loop. **LESSON:** before "fixing" stale UI state, ask whether the
staleness is the *expected convention* for that control — a read-only results/output panel that persists across
edits is a feature (the edit-and-rerun loop depends on it), not a bug; clearing it on input change is right only
when the stale value could be acted on as if current. Next iteration: run the final Mode-S convergence check
(don't manufacture low-value work to avoid converging — converging early is a win).

## 2026-06-25 — iter 22 — Mode S (convergence check) — stable_streak 0 → 1
Backlog was actionable-empty after iter 21 (only the NEEDS-BACKEND backup-badge item + two low/watch nits —
Query write-detector, Login lockout-duration copy). Per the contract, ran a fresh **Mode-S discovery /
convergence pass** rather than chew the low/watch items: a 5-agent parallel panel, each given its view(s) + the
full already-shipped/already-rejected digest + a deliberately HIGH bar. Coverage identical to iters 15–21 for
comparability: (1) Dashboard + Query, (2) Roles & Databases + Backups + BackupStorageForm, (3) Alerts + Migrate,
(4) Settings + DatabaseTuning + Pooler + Login, (5) nav/IA + first-run + cross-view consistency.
**All five views converged.** Three returned clean "no new high/med item" (Dashboard+Query — StaleBanner/honest
state mature; Alerts+Migrate — verified the 3-stage overwrite gate + that `reset()` clears `confirm`; Settings/
Tuning/Pooler/Login — verified the lockout recovery, the DatabaseTuning reassurance Callouts, the Pooler
repoint copy). Two candidates surfaced and **both collapsed on code-level inspection**, self-rejected with
decisive evidence and no panel (iter-5/13/14 precedent):
- **Migrate overwrite confirm — add `dismissible={false}`** (nav/IA agent). **FALSE PREMISE** — the agent said
  an Escape/click-outside could "proceed without typing the confirmation." Verified in `Migrate.tsx`
  (336–361): the destructive action fires ONLY via the "Overwrite & migrate" button
  (`disabled={busy || !overwriteMatches}` — must type the target name); dismissing calls
  `setConfirmOpen(false)` and executes nothing. Dismissing is the **safe escape** — the mirror opposite of
  iter-20's SecretsModal (dismiss = irreversible loss). Non-dismissible here would TRAP a user inside a
  destructive dialog — negative payoff.
- **BackupStorageForm — "clear saved credential"** (Roles/Backups agent). Speculative + backend-dependent (the
  agent couldn't confirm the API accepts a clear signal); *changing* a credential already works. Out of scope.
No code shipped, docs-only commit, no hard gates needed. Per the contract (backlog actionable-empty AND a fresh
discovery pass surfaced no high/med item) this is the **first `stable_streak` increment → 1/3**. Two more clean
passes → write COMPLETE.md and stop. **LESSON:** before locking down a dialog's dismiss paths, ask *which
direction is dangerous* — `dismissible={false}` is right only when dismissing itself causes irreversible loss
(SecretsModal); when dismissing *cancels* a destructive action (every type-to-confirm execute-gate), the casual
escape is a feature, keep it. Next iteration: run another Mode-S convergence check (don't manufacture low-value
work to avoid converging — converging early is a win).

## 2026-06-25 — iter 21 — Mode F (SHIP) (Login: lockout no longer dead-ends the form) — stable_streak 0 → 0
Ran a fresh discovery/convergence pass (5-agent Mode-S panel, same coverage as iters 15–19). **Four of five
agents converged** ("no new high/med item" — Dashboard+Query, Roles+Backups, Alerts+Migrate, nav/IA+first-run+
consistency, each citing the mature state: graduated destructive gates, honest state, co-located backup config,
paired empty states + hints). The Settings/Login agent surfaced a **genuine functional defect**: the Login form
was a **permanent dead-end after a server lockout**. On repeated wrong admin passwords the server returns
`CodeLocked` (HTTP 429); the handler set `locked=true` (disabling the input) and cleared the password (disabling
the Sign-in button), and `locked` only resets inside `onSubmit` — which could no longer fire, so the only escape
was a full-page reload, and the form stayed frozen **even after the server lock expired**. **Verified against the
code** before promoting: `Login.tsx` input `disabled={busy || locked}` (line 91), button `disabled={busy ||
!password}` (line 98), `setLocked(false)` only at line 38; and the existing test literally *encoded* the dead-end
(`"…disables the input"` → `toBeDisabled()`). Distinct from the parked "Try again later" copy nit — this is a
state-machine trap, not wording. Promoted to Mode F.
Fix (subtractive, zero net surface): input `disabled={busy || locked}` → **`disabled={busy}`** (removed the
lockout-disable; added a 5-line comment). The lockout is still surfaced by the existing warn-tone `Callout`, and
the **server remains the sole enforcer** — the UI disable was never the enforcement, only the trap. A locked-out
user can now retype + resubmit; the resubmit clears `locked` and the server re-decides. Updated the test that
encoded the dead-end to assert the lockout is still surfaced (warn alert + cleared field) AND the input stays
editable AND a retype+resubmit fires a second auth attempt (recovery without reload).
Review panel: **4 SHIP, zero blockers** — UX heuristics ("absolute User-Control-&-Freedom violation: a
permanently unrecoverable state with no visible escape; one-char removal, no weakened security boundary; H#4
consistency win"); Sam ("I'd have sat there reloading like an idiot; now the box stays alive, I retype and I'm
in — no docs"); Priya ("removes a wall, keeps the gate where it belongs — on the server; nothing buried,
nothing slowed"); restraint critic ("the rare change a simplicity critic should wave through — makes the code
smaller and removes a trap; do-nothing leaves every locked-out admin frozen until they guess 'reload'"). Both
personas re-flagged the lockout-duration copy as non-blocking polish (the parked low/watch item). Gates:
typecheck ✓, 145 tests ✓ (updated 1 existing case, no net new), build ✓ (dist regenerated + staged), go build ✓
(exit 0, outside sandbox). **stable_streak stays 0** (shipped a real improvement — convergence clock restarts).
**LESSON:** a disabled state with no path to re-enable itself is a dead-end, not a safeguard — trace every
guard flag to "what re-enables this, and can the user reach it?"; if the only answer is "reload the page," it's
a defect, and removing the disable is a subtractive fix the restraint critic waves through. Next iteration: run
a fresh Mode-S discovery/convergence pass.

## 2026-06-25 — iter 20 — Mode F (SHIP) (Roles: secrets modal closes only by explicit ack) — stable_streak 0 → 0
Took the top backlog quick-win filed by the iter-19 Roles/Backups convergence agent: the one-time
`SecretsModal` ("Save these now", shown after New App / rotate) displays passwords + connection strings
that **"cannot be retrieved again,"** yet it was a plain `Modal` — so **Escape, a backdrop click, or the
corner X** each routed to `onClose` → `setSecrets(null)`, destroying the only copy before the user had
copied it (recovery = rotate again). A reflexive dismiss = irreversible credential loss.
Fix (subtractive — *removes* a control, adds none): added an opt-in **`dismissible?: boolean`** prop to the
shared `Modal` (default `true`, so all other modals are byte-for-byte unchanged). When `false` it sets
`showCloseButton={false}` (drops the corner X) and `preventDefault`s `onEscapeKeyDown` + `onInteractOutside`
(swallows Escape + backdrop/focus-out) — all three Radix dismiss vectors closed in concert; miss any one and
a live data-loss path remains. `SecretsModal` now passes `dismissible={false}`, making the **existing**
"I've saved them" footer button the sole exit (it always renders, so the user is never trapped). Verified
the `SecretValue` Reveal/Copy controls are real `Button`s, fully keyboard-reachable inside Radix's focus
trap (the heuristics reviewer's one non-blocking note — already satisfied, no change needed). Extended the
rotate test: after confirming, the secrets dialog has no Close (X) button, an Escape keydown does NOT
dismiss it, and only the "I've saved them" click closes it.
Review panel: **4 SHIP, zero blockers** — UX heuristics ("textbook H#5 error-prevention; all three dismiss
vectors closed together; works by subtraction, not addition"); Sam ("the safe direction to fail in — one
stray Escape used to mean rotate-again; the footer button always renders so I'm never trapped"); Priya ("the
one place a reflexive dismiss is irreversible loss; scoped to `SecretsModal`, every other dialog keeps
Escape/click-away; a guardrail, not hand-holding"); restraint critic ("removes a control, adds none;
`dismissible` is a safe-default opt-in with a concrete caller — NOT premature config; a bespoke SecretsModal
would be MORE divergence; do-nothing accepts guaranteed credential loss"). Gates: typecheck ✓, 145 tests ✓
(extended the existing rotate test rather than adding a case), build ✓ (dist regenerated + staged), go build
✓ (exit 0, outside sandbox). **stable_streak stays 0** (shipped a real improvement). **LESSON:** once-only
content must close only by an explicit acknowledgement — strip the casual dismiss paths (X + Escape +
backdrop, all three) rather than adding a warning; a safe-default opt-in `dismissible` boolean with one
concrete caller clears restraint because it's subtractive and the loss it prevents is irreversible. Backlog
is now actionable-empty again. Next iteration: run a fresh Mode-S discovery/convergence pass.

## 2026-06-25 — iter 19 — Mode F (SHIP) (Migrate: honest failed-job copy on an overwrite) — stable_streak 0 → 0
Ran a fresh discovery/convergence pass (5-agent Mode-S panel, same coverage as iters 15–18). **Three of
five agents converged** (Dashboard+Query, Settings/Tuning/Pooler/Login, nav/IA — all "no new high/med
item"). The other two each surfaced a **genuine new item**: (1) Alerts/Migrate agent — the failed-job
Callout in `DirectJobProgress` (Migrate.tsx) unconditionally claims *"Your existing data is intact"*, which
is **false for an overwrite job**; (2) Roles/Backups agent — the one-time `SecretsModal` can be dismissed by
an accidental Escape/backdrop click, destroying credentials that "cannot be retrieved again." Promoted the
Migrate item (the stronger of the two — a verified-false statement on a destructive op); filed the
SecretsModal item as the next high/S quick win.
**Verified the backend before writing copy** (iter-11 lesson): `internal/migrate/orchestrator.go` drops the
existing target DB *before* the restore when overwrite is set — single-db `prepareTarget`→`DropDatabase`→
`Restore` (lines 156/159/541-542), cluster per-db `DropDatabase`→`Restore` (lines 250-261). So a mid-restore
failure can leave the old data gone, yet the UI told the user it was intact.
Fix (zero net surface): gated the one line on the already-present `job.overwrite` field. Additive failure
keeps "Your existing data is intact — the import only writes a freshly created database." Overwrite failure
now reads *"Because you chose to replace the existing &lt;target_database&gt; (or 'existing databases' for a
cluster), it may already have been dropped before the failure — restore from a backup if you need the old
data back."* The **"may" hedge** is deliberate — the failure can land before or after the drop (e.g. during
dump), so the copy doesn't over-claim doom. Added 2 tests (additive→"intact", overwrite→"may already have
been dropped"/"restore from a backup", neither bleeds into the other).
Review panel: **4 SHIP, zero blockers** — UX heuristics ("textbook H#9; the old copy made a false safety
claim to users who just lost a database"); Sam ("the one lie a panel like this can never tell — and it tells
me my next move"); Priya ("honest correction, accurately hedged, right altitude, no nannying; 'may' is
correct not weasel-wording"); restraint critic ("corrects a verified-false reassurance at zero net surface;
the true state appears nowhere else — NOT the Nth-warning-on-a-gated-flow pattern the loop rejects"). Gates:
typecheck ✓, 145 tests ✓ (143 + 2 new), build ✓ (dist regenerated + staged), go build ✓ (exit 0, outside
sandbox). **stable_streak stays 0** (shipped a real improvement). **LESSON:** reassurance copy that is
conditionally FALSE is a defect, not decoration — gate it on the condition that makes it false, hedge
honestly, and verify the backend ordering before you trust the premise. Next iteration: take the SecretsModal
accidental-dismiss item (Mode F).

## 2026-06-25 — iter 18 — Mode F (SHIP) (Roles: confirm before rotating a password) — stable_streak 0 → 0
Ran a fresh discovery/convergence pass (5-agent Mode-S panel, same coverage as iters 15–17). **Four of
five agents converged** ("no new high/med item") — but the Roles & Databases + Backups agent surfaced a
**genuine new defect**: the per-row **"Rotate password"** button rotated immediately on a single unguarded
click (`RolesDatabases.tsx:230` called `rotate()` directly). Rotating invalidates the old password
server-side **instantly**, so any live app still using the old credential loses DB access until its
connection string is updated — the **same blast radius** as the row's Delete button, which IS gated through
a `TypedConfirmDialog` ("Any application using this user will immediately lose access"). The page header
even promises *"Every action here is guided and confirmed,"* yet Rotate was the lone unguarded mutation,
with zero warning anywhere about the breakage. **Verified against the code** before promoting (confirmed the
direct onClick→rotate path and the asymmetry with the gated Delete). Promoted to Mode F as a real
defect/asymmetry, not decoration.
Fix (~20 lines, reuses the existing component): gated rotate behind the **plain `ConfirmDialog`** — NOT the
typed-name gate, because rotation isn't data loss, so a proportionate "are you sure?" (tone danger) is the
right altitude. Added a `rotateTarget` state mirroring `dropTarget`; the button onClick now
`setRotateTarget(name)`; the dialog reads *"The current password for <name> stops working immediately. Any
app connecting as this user will lose access until you update its connection string with the new password —
shown once, right after."* Removed the now-redundant inline "Rotating…" button spinner (busy shows on the
dialog Confirm). Added a test: clicking the row button does NOT call `rotatePassword`; only the dialog
confirm does → then the existing one-time secrets modal appears.
Review panel: **4 SHIP, zero blockers** — UX heuristics ("fixes a simultaneous Consistency + Error-Prevention
failure on the most accident-prone action; plain confirm is the right altitude vs the typed gate reserved
for data loss"); Sam ("I clicked Rotate half-curious and that one sentence stops me cold in exactly the right
way — saves me from nuking my live app's DB connection"); Priya ("proportionate gate on an irreversible,
connection-breaking op; plain confirm not type-the-name is the right call; one click, buries nothing, dumbs
down nothing"); restraint critic ("creates the absent FIRST gate on an unguarded production-breaking action
whose sibling of equal blast radius is already gated — categorically different from the prior
Nth-redundant-warning rejects; genuine error-prevention, not hand-holding"). Gates: typecheck ✓, 143 tests ✓
(142 + 1 new), build ✓ (dist regenerated + staged), go build ✓ (outside sandbox). **stable_streak stays 0**
(shipped a real improvement). **LESSON:** convergence is provisional *again* — a high bar filters decoration,
not looking; an *unguarded* destructive-by-effect action is the mirror image of the rejected "restating an
existing warning" pattern, and adding the FIRST guard clears restraint easily (all four, incl. the critic,
shipped it). Next iteration: run a fresh discovery/convergence pass.

## 2026-06-25 — iter 17 — Mode F (SHIP) (Navigation: reset scroll to top on route change) — stable_streak 2 → 0
Ran what was meant to be the FINAL convergence check (a 5-agent Mode-S pass, identical coverage to
iters 15/16). Four agents converged ("NO new high/med item") — but the nav/IA + first-run +
cross-view-consistency agent surfaced a **genuine new defect**: the routed view renders inside
`<main className="… overflow-y-auto …">` (Layout.tsx), which is its OWN scroll container. With
client-side routing only the `<Outlet/>` children swap on navigation; the `<main>` element is never
remounted, so its `scrollTop` carried over between views. Concretely: scroll down in a long view
(Backups history, a long Roles/Alerts table), click another sidebar item, and you land in the new view
still scrolled down with its header off-screen — the "stuck page" feel, contradicting the universal web
expectation (React Router ships `<ScrollRestoration>` for exactly this). **Verified against the code**
before promoting (confirmed `<main>` is the scroll container and persists across route swaps; no
`ScrollRestoration` anywhere). Promoted to Mode F rather than converging — a discovery pass surfacing a
real high/med item means it is NOT a convergence increment, regardless of the other four agents.
Fix (~5 lines, zero added UI): a `useRef` on `<main>` + a `useEffect` keyed on `location.pathname`
setting `scrollTop = 0` on each route change — keyed on the *path* so an in-place re-render of the same
view (e.g. backup history auto-refreshing while you read it) does NOT yank you to the top; only actual
navigation does. Added `data-testid="main-content"` and a test (scroll to 400 → navigate → assert 0).
Review panel: **4 SHIP, zero blockers** — UX heuristics ("Consistency & Standards fix; no anchor-nav or
back/forward scroll-restoration contract being broken"); Sam ("the only thing that'd trip me up is being
yanked off data I'm mid-read on — it isn't, it's keyed on path"); Priya ("deep-links land at the top like
a fresh load; filters/selections live in state/URL not scrollTop, nothing I care about resets");
restraint critic ("behavior-correctness fix with zero added surface area; the bug is real and routine").
Gates: typecheck ✓, 142 tests ✓ (was 141 + 1 new), build ✓ (dist regenerated + staged), go build ✓
(outside sandbox). **stable_streak reset 2 → 0** (shipped a real improvement — the convergence clock
restarts). Next iteration: run a fresh discovery/convergence pass again.

## 2026-06-25 — iter 16 — Mode S (convergence check) — stable_streak 1 → 2
Backlog still actionable-empty (same NEEDS-BACKEND backup-badge item + two low/watch items as iter 15;
none promoted). Per the contract, ran a **second Mode-S convergence pass** rather than chew the
low-value items. A fresh 5-agent parallel panel, each given its view(s) + the full already-shipped /
already-rejected digest + a deliberately HIGH bar (genuinely new, high-or-med-payoff, non-over-design,
frontend-only, fixable in one view). Coverage identical to iter 15 for comparability: (1) Dashboard +
Query, (2) Roles & Databases + Backups + BackupStorageForm, (3) Alerts + Migrate, (4) Settings +
DatabaseTuning + Pooler + Login, (5) nav/IA + first-run + cross-view consistency. **All five again
independently returned "NO new high/med item — converged."** Agent 1 re-examined the Query zero-rows
state and the speculative write-SQL detector and judged neither high-payoff; agent 3 re-probed the two
nits the iter-15 pass had flagged (cluster "Exclude" FieldDescription — confirmed it already HAS one at
Migrate.tsx; Alerts "Send test" disabled-state native `title` — a standard disabled affordance, not the
iter-8 decorator-tooltip pattern) and confirmed both zero-payoff; agents 2/4/5 found the views mature and
internally consistent (graduated destructive gates, honest state, co-located backup config, paired empty
states + hints, plain-English copy throughout). No code shipped, docs-only commit, no hard gates needed.
Per the contract (backlog actionable-empty AND a fresh discovery pass surfaced no high/med item) this is
the **second stable_streak increment → 2/3**. One more clean convergence pass → write COMPLETE.md and stop
the loop. Next iteration: run the final Mode-S convergence check; do NOT manufacture low-value work from
the low/watch items to avoid converging (converging early is a win).

## 2026-06-25 — iter 15 — Mode S (convergence check) — stable_streak 0 → 1
Backlog was actionable-empty for this frontend loop (only a NEEDS-BACKEND backup-badge item, out of
scope, and two low/watch items — Query client-side write-detector, speculative/restraint-risky; Login
lockout copy, risks a false duration promise). Per the iter-14 plan, ran a **Mode-S discovery /
convergence check** rather than chew the low/watch items: a 5-agent parallel panel, each given its
view(s) + the full already-shipped/already-rejected digest, tasked with finding any *genuinely new,
high-or-med-payoff, non-over-design* problem against a deliberately HIGH bar. Coverage: (1)
Dashboard + Query, (2) Roles & Databases + Backups + BackupStorageForm, (3) Alerts + Migrate,
(4) Settings + DatabaseTuning + Pooler + Login, (5) nav/IA + first-run + cross-view consistency.
**All five independently returned "NO new high/med item; converged."** Each cited the mature state:
graduated destructive gates (not warning-on-warning), honest state surfacing (StaleBanner,
"limited for safety"), consistent shadcn primitives + terminology across views, empty states paired
with actionable hints, backup config co-located (iter 11). Agent 3 (Alerts+Migrate) probed six
candidate nits (e.g. cluster "Exclude" field lacks a FieldDescription the single-db target has;
"Send test" disabled-state relies on a hover `title`) and judged every one restraint-negative or
zero-payoff — correctly. No code shipped, no hard gates needed (docs-only). Per the contract
(backlog actionable-empty AND a fresh discovery pass surfaced no high/med item) this is the **first
stable_streak increment → 1/3**. Two more clean passes → write COMPLETE.md. Next iteration: another
Mode-S convergence check (don't manufacture low-value work to avoid converging).

## 2026-06-25 — iter 14 — Mode F (REJECT) (DatabaseTuning: add a "reassuring intro line")
Took the top actionable item (the NEEDS-BACKEND backup-badge item above it is out of scope for
this frontend-only loop): DatabaseTuning "reads as prescriptive when it's really informational →
add one reassuring intro line." On reading `DatabaseTuning.tsx` the item's **premise is false** —
the page *already* opens with exactly that reassurance, threefold: (1) the card title is
**"Database tuning (host-sized)"** ("host-sized" = automatic); (2) the very first element is an
info `Callout` titled **"Sized to this server automatically"** whose body reads *"Postgres is
tuned to this machine on safe best defaults — you don't need to tune anything by hand"* — that IS
the proposed intro line; (3) the active **Mixed** profile's own description says *"the best
default for an indie-hacker box"*, telling the indie user their profile is already right (and the
profile preview is explicitly framed "This is a preview — nothing changes here … applied at
install/provision time — not from this screen"). The item's second sub-concern ("help text
assumes DBA knowledge: shared_buffers, work_mem") is also already handled — each setting carries a
plain-English one-liner (e.g. "Memory Postgres uses to cache data pages. Sized to your RAM.") —
and the proposed fix didn't address it anyway. There is no edit to make that wouldn't restate a
reassurance the page already gives. **Self-rejected on restraint with decisive code evidence and
no panel** (the iter-5 pattern: premise false on inspection → running 4 agents to rubber-stamp a
provably-zero-payoff change is the churn the loop guards against; unlike iters 7/13, this isn't a
plausible change 3 reviewers would ship — there's literally nothing to add). No code shipped, no
gates needed (docs-only). Recorded the lesson ("when an item says 'add a reassuring/intro line,'
read the view's existing intro first — an audit agent skimming the parameter table can miss the
Callout right above it"). stable_streak stays 0 (a Mode-F reject, not an actionable-empty
discovery pass). **Backlog is now near-dry**: only a NEEDS-BACKEND item (out of scope) and two
low/watch items (Query client-side write-detector — speculative/restraint-risky; Login lockout
copy — risks a false duration promise) remain. Next iteration should run a **Mode-S discovery /
convergence check** rather than chew the low/watch items — if it surfaces nothing high/med, that's
the first stable_streak increment toward convergence.

## 2026-06-25 — iter 13 — Mode F (REJECT) (Migrate: inline danger warning when overwrite is armed)
Took the top open item: the single-db overwrite "gate is split across three intent-shifts with
no inline warning when overwrite is checked." Implemented the restraint-aware version — a
conditional `Callout tone="danger"` rendered below the overwrite checkbox **only while it's
checked**, stating the target gets dropped/recreated, can't be undone, and "you'll type its name
to confirm before it runs" (mirroring how the cluster form warns inline). Added a test asserting
the warning appears on arm and disappears on disarm; 14 Migrate tests green. Ran the full panel.
**3 SHIP** (UX heuristics — "consequence at arm-time, not deferred to the modal"; Sam — "the
safety net I want the moment I tick the box"; Priya — "zero added clicks, purely informational").
**Restraint critic REJECTED** with decisive code-level reasoning, and the blocker is never
overruled: the single-db overwrite is *already* a three-stage escalating gate — the checkbox
label says "Replace `<target>` … **(destructive)**", the submit button flips to **"Continue…"**,
and the modal carries its own danger Callout ("…dropped and recreated… This cannot be undone")
plus a **type-the-name** input that is the real execute-time gate. The new Callout just moves the
modal's text up by one click — the user reads "dropped and recreated, can't be undone" twice
within seconds — and it even pre-narrates the confirm step. The **consistency-with-cluster
steelman is false on inspection**: `ClusterForm`'s inline Callout is *always-on* and carries info
its label omits ("can drop **every matching database**"), a categorically scarier standing
warning — so copying its shape misses its reason. And the destructive action **cannot fire from
this screen at all** (the button only opens the modal), so visibility "at arm-time" buys nothing
executable. Reverted both files clean, recorded the lesson ("count the warnings that already fire
on the same interaction before adding another; don't justify a copy from a sibling view by shape —
verify it carries info the local label/modal doesn't"), marked the backlog item rejected. No code
shipped. stable_streak stays 0 (a Mode-F reject, not an actionable-empty discovery pass). Next top
item: DatabaseTuning — add one reassuring intro line (defaults already tuned; the typical indie app
needs no changes); intro-only change is S.

## 2026-06-25 — iter 12 — Mode F (SHIP) (Migrate: "Start another" — targeted form reset)
The Migrate page's "Start another" button (shown once a migration job hits a terminal state)
returned the user to the form with every prior value still filled in — source connection,
database/target names, and a checked destructive "Replace if it already exists (overwrite)"
flag. The footgun: pulling a second DB off the same host, you could skim past a still-armed
"replace" and silently drop a database you never meant to touch. Fixed across all four flows
(one-db, whole-cluster, cross-panel send/receive). I first wrote a **full reset** — and the
**restraint critic REJECTED** it: the source connection is genuinely reusable for the next DB
off the same host, so blanking it adds friction to the likely-next task. It proposed resetting
only the destructive flag while keeping the connection. I revised to exactly that **targeted
reset**: keep host/port/user/password/sslmode; clear the per-run fields (database-to-copy,
target, cluster exclude) and reset `overwrite`+`confirm`+`error`. Send keeps the connection,
clears the one-time session code; receive (no connection) clears its db + the generated code.
Exported `SingleDBForm` and added a test asserting the source host *persists* through "Start
another" while database/target blank out and overwrite disarms (141 tests). Re-ran the panel on
the revised behavior → **4 SHIP** (UX heuristics: "keep infrastructure, clear intent" reads
natural + a real error-prevention win on the overwrite flag; Sam: "worst case is a harmless
re-type, never an accidental overwrite"; Priya: same-source repeat friction gone, destructive
flag safely cleared; restraint critic: "minimal correct fix, nothing left to drop"). Priya's
one non-blocking nit — the cluster exclude list is arguably source-stable too — left as-is
(re-pulling the same whole cluster is rare; keeping it would be speculative). Gates: typecheck
OK, 141 tests OK, build OK (dist regenerated + staged), go build OK (outside sandbox).

## 2026-06-25 — iter 11 — Mode F (SHIP) (Backups+Settings: co-locate backup config — the canonical seed item)
The anchor improvement of this loop, flagged by 4 of 11 audit agents. Backup **config**
(S3 destination, retention, encryption) lived on `/settings`; backup **operations** (run,
history, restore, restore-tests) lived on `/backups` — so a user bounced between routes to
configure-then-run and could discover the dependency mid-flow. Fix (a *move*, not an add):
extracted the config form into a shared `web/src/components/BackupStorageForm.tsx` (identical
fields + the save-doubles-as-connection-test behavior), and surfaced it ON the Backups page
via a "Backup storage" header button → right-side `Sheet`. The existing `LocalBackupWarning`
CTA ("Set up an S3 bucket") now opens that same Sheet **in-page** instead of `<Link to="/settings">`,
so the user never leaves the backups workflow; the Sheet stays open after save so the form's
"ready / not ready" connection-test feedback persists. Removed the form from Settings, which is
now Database tuning + Connection pooler + a one-line info Callout pointing to /backups (so
old-muscle-memory users aren't stranded). Single home, no duplication, Settings got simpler
(28 lines). Moved the 4 form tests to a new `BackupStorageForm.test.tsx`; rewrote Settings.test
to assert the form is gone + the pointer link; updated the Backups `LocalBackupWarning` test
(link → button calling `onConfigure`). 140 tests. Review panel: **Sam REJECTED first** — on a
*failed* Save&connect, a non-expert can't tell if they're still protected, and the green "Stored
in S3" badge could imply working off-host backups over a broken target. Verified the real
behavior in `internal/server/handlers_config.go` (config saves and the live target switches even
when stanza-create fails → new backups to it fail until fixed; past/local backups untouched), then
addressed in-iteration by leading the failed-save Callout with an **honest** reassurance ("Your
existing backups are untouched and still safe — but new backups to this bucket will fail until this
is fixed…") rather than the false "still lands locally". Sam's second point (badge claims S3 over a
broken target on a *cold reload*) is backend-limited — `GET /config` returns no target-health signal —
so it's out of this frontend-only loop's scope; filed as a NEEDS-BACKEND backlog item. Re-ran Sam → SHIP.
Also fixed two cheap nits the panel raised: stale success copy "run a backup from the Backups page" →
"Close this panel and run a backup" (you're already there), and a stale `backupDestination` doc comment
("The Settings form" → "The backup-storage form"). Panel: **4 SHIP, zero blockers** (UX heuristics —
"directly fixes the panel's own most-cited UX problem"; Sam — after the honest-reassurance fix; Priya —
"one click from where I run backups, fewer route hops than before, nothing buried or dumbed down";
restraint critic — "kills a top-cited route bounce at net-neutral surface; form moved not duplicated;
Settings got simpler"). Gates: typecheck ✓, 140 tests ✓, build ✓ (dist regenerated + staged), go build ✓
(outside sandbox — snap-confine blocks it in-sandbox). stable_streak stays 0 (shipped a real improvement).
Next top item: Migrate — reset the form state when returning from a terminal job ("Start another" leaves
the previous source/target/overwrite pre-filled).

## 2026-06-25 — iter 10 — Mode F (SHIP) (Dashboard: drop the duplicate "Connections" row)
Top quick-win, consistency/minimalism fix. The Dashboard rendered the connections metric
(active / max_connections) **twice on one screen**: a plain `Kv` row in the **Postgres**
card (`3 / 100 (3%)`, no colour) and a tinted `StatCard` saturation gauge in the **Server**
card (`3/100`, sub `3%`, warn/danger tint as connPct crosses 75/90, sitting beside the
CPU/Memory/Disk gauges). Same number in two slightly different formats added cognitive load
and a "wait, do these disagree / is one more correct?" hesitation, with no extra signal.
Removed the Postgres-card row (−4 rendered lines + an intent comment so it isn't re-added)
and kept the single tinted Server-card gauge — strictly more informative (it warns *before*
you hit the connection limit) and grouped with the other "% of capacity" gauges. The Postgres
card stays coherent and distinct: Status + cache hit ratio, TPS, deadlocks, replication lag —
all DB-internal health. Added a `getAllByText("Connections").toHaveLength(1)` invariant to the
Dashboard test. Considered the purist objection (max_connections is a Postgres *setting*, so
connections "belongs" in the Postgres card) but both the technical persona (Priya) and the
heuristics reviewer read connection saturation as a resource-exhaustion symptom (same family
as CPU/disk), not a config fact — the tinted gauge is the right single home. Review panel:
**4 SHIP, zero blockers** (UX heuristics — kills a simultaneous Consistency + Minimalism
violation, kept copy strictly dominates; Sam — "two identical numbers made me second-guess
whether they meant the same thing; keep the coloured one that actually warns me"; Priya —
"one metric, one home; the plain row was a dimmer colourless copy"; restraint critic — "genuine
duplicate removal, the opposite of over-design; the kept gauge strictly dominates the deleted
row"). Gates: typecheck ✓, 138 tests ✓, build ✓ (dist regenerated + staged), go build ✓
(outside sandbox — snap-confine blocks it in-sandbox). stable_streak stays 0 (shipped a real
improvement). Next top item: Backups + Settings co-location (the canonical seed item, high/M).

## 2026-06-25 — iter 9 — Mode F (SHIP) (Pooler: enable-confirm copy says you must repoint apps)
Top quick-win, honest-state copy fix. The "Enable the connection pooler?" confirm dialog
closed with "Your apps then connect to <addr> instead of Postgres directly" — which reads
as if enabling PgBouncer auto-reroutes apps. It doesn't: enabling only installs/starts the
service and adds chosen roles to the userlist; the user must manually edit each app's
connection string. A misread leaves the user enabling it, seeing no change, and debugging a
phantom. Reworded to lead with **"Enabling won't move any app over by itself"** and spell out
the manual step ("change an app's connection string to point at <addr> in place of the direct
Postgres host. Apps you don't repoint keep connecting to Postgres directly, unchanged"),
preserving the "does not restart Postgres / does not touch your data" honesty line. Pure copy
reword in an existing dialog — no new UI/controls. Added two assertions to the enable test
(the "won't move any app" + "change an app's connection string" copy). Review panel: **3 SHIP**
straight up (UX heuristics — "classic Error-Prevention failure, fixed at the moment it
matters"; Priya — "corrects the one mechanical fact I'd otherwise get wrong, respects that I
know what a pooler is"; restraint critic — "a word swap that fixes a genuine 'why did nothing
happen' trap, earns itself"). **Sam REJECTED** — not on my paragraph but on an adjacent
contradiction: the bullet "Route N roles through it" implied the panel auto-routes, clashing
with the new "you must repoint" paragraph. Cheap + clearly-right + it strengthens this very
fix, so per the contract I addressed it in-iteration rather than rejecting: reworded the
bullet to **"Allow N roles to connect through the pooler"** (also more accurate — enabling
just adds the role to the userlist) and updated its test assertion. Re-ran Sam → SHIP. Panel
now 4 SHIP, no blockers. Gates: typecheck ✓, 138 tests ✓, build ✓ (dist regenerated +
staged), go build ✓ (outside sandbox — snap-confine blocks it in-sandbox). stable_streak
stays 0 (shipped a real improvement). Next top item: Dashboard — "Connections" is shown
twice (Postgres card + Server card); drop the duplicate.

## 2026-06-25 — iter 8 — Mode F (SHIP) (Alerts: rename "Sustained" header → "Hold for")
Took the top quick-win: the rules-table column header "Sustained" is bare jargon — a user
scanning a value like "instant"/"5m" can't tell whether it means "wait then check" or "the
condition must hold this long," and the plain wording only lives inside the rule editor.
First implemented the backlog's literal proposal — a shadcn `Tooltip` (with an `InfoIcon`
trigger + `TooltipProvider`, definition mirrored into the trigger's `aria-label` for a11y +
testability) on BOTH "Sustained" and "Cooldown". Ran the full review panel: **3 SHIP** (UX
heuristics — recognition-over-recall; Sam — "'Sustained' next to 'instant' genuinely puzzled
me," ship with a touch caveat that hover-only help is invisible on mobile; Priya — muted,
ignorable, out of the way). **Restraint critic REJECTED** and is never overruled: the
"Cooldown" tooltip is decoration (cooldown + a duration is self-explanatory), and the one
genuine gap ("Sustained") doesn't need tooltip machinery + a hover-only affordance — just
**rename the header**. The critic's alternative was clearly right and cheaper, and it also
resolved Sam's touch caveat, so I addressed the blocker in-iteration instead of rejecting:
reverted the tooltip change entirely and renamed `Sustained` → **`Hold for`**, matching the
editor's own "Must hold for (minutes)" label (self-documenting, consistent, zero new
surface). Left "Cooldown" unchanged. Replaced the test with a `columnheader` assertion for
"Hold for". Gates: typecheck ✓, 138 tests ✓, build ✓ (dist regenerated + staged), go build ✓
(outside sandbox — snap-confine blocks it in-sandbox). stable_streak reset to 0 (shipped a
real improvement). Next top item: Pooler — reword the enable-confirmation copy to make
explicit that apps must be reconfigured to the pooler address.

## 2026-06-25 — iter 7 — Mode F (REJECT) (Query: surface the server's executed_sql)
Took the top quick-win: the API returns `executed_sql` (the statement the server
actually ran, possibly auto-LIMIT-rewritten) but it was never shown. Implemented it the
restraint-aware way — captured the submitted SQL into state and rendered a compact
"Executed SQL" `<pre>` block (mirroring the existing idiom in Settings.tsx) **only when**
`normalize(executed_sql) !== normalize(submitted)`, i.e. the server actually rewrote the
query; verbatim runs add zero surface. Updated the test fixture to a realistic
`executed_sql` matching the submitted query, asserted the block is hidden on verbatim
runs, and added a test asserting it appears on an auto-LIMIT rewrite. 6 Query tests green.
Ran the full review panel. **3 SHIP** (UX heuristics — visibility of system status; Sam —
"turns a vague 'we capped you' into 'here's exactly what ran', a trust win"; Priya —
"exact LIMIT value made visible, zero friction"). **Restraint critic REJECTED**: redundant
decoration — the *only* rewrite path is the auto-LIMIT injection, which always co-occurs
with the existing "Results limited for safety" badge + "Add your own LIMIT…" copy that
already explain a LIMIT was applied; the block restates that fact in technical form.
**Verified the critic's load-bearing assumption** in `internal/pg/guard/guard.go`:
`Check()` mutates the statement solely via `injectLimit`, which sets
`cls.HasLimit → limited=true` — there is NO rewrite path that changes the SQL without also
flipping `limited`. So the assumption holds and the REJECT stands firm; per the contract
the restraint blocker is never overruled by the three ships. Reverted both files clean,
recorded the lesson ("honest-state only wins when the data is otherwise hidden; here the
`limited` copy already conveys it") in learnings.md, marked the backlog item rejected. No
code shipped. Next top item: Alerts — add `Tooltip` definitions to the bare "Sustained" /
"Cooldown" column headers.

## 2026-06-25 — iter 6 — Mode F (REJECT) (Dashboard: link "Backups page" in the no-backup callout)
Took the top quick-win: the "no backup yet" warn Callout names "Backups page" as plain
text, so the user hunts the sidebar — proposed wrapping it in `<Link to="/backups">`,
matching the existing inline `<Link to="/settings">` in Backups.tsx. Implemented it
(added the import, wrapped the words, wrapped the test renders in `MemoryRouter`, asserted
`href="/backups"`); test passed. Ran the full review panel. **3 SHIP** (UX heuristics —
recognition-over-recall + consistency; Sam — one-click trail that doesn't dead-end; Priya
— "gets out of my way, no new control"). **Restraint critic REJECTED**: decorative payoff —
the copy already names the destination and the persistent left-nav is one obvious click
from every view, so the link only shaves a click off an empty state most users see at
most once; no task was blocked or harder before. Per the contract the restraint blocker is
never overruled by the other ships, so the change does NOT ship. Reverted both files clean,
recorded the lesson ("'matches an existing pattern' justifies a link you're adding for a
real need — it doesn't manufacture the need; a same-name link to a top-level nav
destination is the weak case") in learnings.md, marked the backlog item rejected. No code
shipped. Next top item: Query — surface the server's `executed_sql` (possibly
LIMIT-rewritten) so a user can see what actually ran.

## 2026-06-25 — iter 5 — Mode F (REJECT) (Roles & DBs: scope `dropBusy` per-row)
Took the top quick-win: the audit claimed a single `dropBusy` boolean disables *every*
Delete button during any one drop, "freezing" unrelated rows. Implemented the scoped fix
(`dropBusy && dropTarget?.kind === … && dropTarget.name === row.name` on both tables) and
wrote a test for it — and the test exposed the flaw: while the drop runs, the modal
`TypedConfirmDialog` is open, so Radix marks the background table inert/`aria-hidden` (the
unrelated row's Delete button is in the DOM but unreachable via `getByRole`). Traced the
lifecycle to confirm: `dropBusy` is true **iff** `doDrop` is running, and `doDrop` is only
reachable from the dialog (`open={dropTarget !== null}`); on success the dialog closes *and*
`reloadAll()` swaps the tables for a Spinner before busy clears. So no user can ever see or
click an "unrelated frozen row" — the scoping changes nothing observable and only adds
conditional logic. **Self-rejected on restraint** with decisive evidence (no review panel
needed — running 4 agents to rubber-stamp a proven no-payoff change is the churn the loop
guards against). Reverted code + test; recorded the lesson ("a global busy flag that only
flips under a modal is already effectively scoped — the modal does the gating") in
learnings.md and marked the backlog item rejected. No code shipped. Next top item:
Dashboard — make the "no backup yet" callout's "Backups page" an actual `<Link>`.

## 2026-06-25 — iter 4 — Mode F (Alerts: warn when rules can't fire — no channel)
Top quick-win, silent-failure honest-state fix. A user could enable alert rules while
having no enabled notification channel — the rules then fired into the void with nothing
warning them (`toggleRule`/RuleModal default `enabled: true`, channels independent). Added
a conditional warning `Callout` ("Your rules won't fire — No notification channel is
enabled… Set up and enable Pushover or a Webhook above first") placed between the channels
card and the rules table. Gated on `hasEnabledRule && !anyChannelEnabled` (computed from
`cfg.data`), so it's invisible in the healthy state and self-clears the instant a channel
is enabled or no rule is enabled. Reuses the existing `Callout` — no new component, no new
control, no clicks. Added 3 tests (warns in the broken state; no warning once a channel is
enabled; no warning when no rule is enabled) → 137 tests. Review panel: 3 SHIP (UX
heuristics called it "the most significant usability gap on this page"; Sam + Priya both
ship, Priya: "a guardrail, not a nag"). Restraint critic conditionally REJECTED, preferring
a per-row inline indicator or an enable-time guard — but explicitly conceded the banner is
"shippable as the least-bad option." Resolved to SHIP: per-row would duplicate the same
message on every enabled row (noisier), and an enable-time guard adds a modal wall to a
deliberate one-click toggle (friction Priya rejects); there is no per-rule channel routing,
so the page-level banner is the correct altitude. Not a "looks nicer" overrule — the banner
is genuinely the simplest honest fix. Gates: typecheck ✓, 137 tests ✓, build ✓, go build ✓
(outside sandbox — snap-confine blocks it in-sandbox).

## 2026-06-25 — iter 3 — Mode F (Dashboard: remove always-blank Version row)
Top quick-win honest-state fix. The Postgres card's "Version" row always rendered an
em-dash "—": confirmed in `internal/server/handlers_dashboard.go` the field is
`omitempty` with the comment "the foundation does not expose a server version yet", so
`pg.version` is never sent and the row was permanently blank. A blank field next to a
green "Running" badge reads as missing/partial data and erodes trust in the rest of the
card. Removed the `<Kv label="Version">` row (~4 lines); the card still shows Status,
Connections, Cache hit ratio, TPS, Deadlocks, Replication lag — all live. Dropped the
misleading `version: "16.2"` fixture value (backend never sends it) and added a
`queryByText("Version")` → null assertion. Review panel: 4 SHIP, zero blockers (UX
heuristics + Sam + Priya + restraint critic). Both personas independently noted PG
version is genuinely useful and the right next move is to surface a *real* version on a
details/settings page later — not to keep a placeholder. Gates: typecheck ✓, 134 tests
✓, build ✓, go build ✓ (outside sandbox — snap-confine blocks it in-sandbox).

## 2026-06-25 — iter 2 — Mode F (Roles & Databases empty state)
Top quick-win: the "Users & roles" card's empty state showed a bare "No roles yet"
while the sibling Databases card already had an actionable hint. Added a `hint` to the
`EmptyState` pointing to the card's user buttons and the page-header "New app
(one-click)" path. Text-only, reuses the existing `hint` prop — no new component or
control, vanishes the moment any role exists. Extended the empty-states test to assert
the guidance. Review panel: 3 SHIP (UX heuristics, Priya, restraint critic). Sam
(non-technical persona) flagged that the first draft said "use New app above" while
that button sits in the page header, not the card's action row — fixed in-iteration by
rewording to "buttons above … or 'New app (one-click)' at the top of the page" rather
than moving any control. Gates: typecheck ✓, 134 tests ✓, build ✓, go build ✓ (ran
outside sandbox — snap-confine blocks it in-sandbox, same precedent as prior loop).

## 2026-06-25 — iter 1 — Mode S (seed)
Ran the parallel audit panel (11 agents: Login, Dashboard, Query, Roles&DBs, Backups,
Alerts, Migrate, Settings+Tuning+Pooler, nav/IA, first-run, cross-view consistency).
Merged + de-duped findings into `backlog.md`: **17 open items**, ordered quick-wins
(high/med payoff, S effort) first. Dropped 5 over-design / sweeping-refactor ideas
into `learnings.md` so they don't resurface. The seed item (backup config split
across /settings ↔ /backups) was independently flagged by 4 of 11 agents — the
anchor improvement. No code changed this iteration. Next: Mode F on the top quick win
(Roles & Databases empty-state hint).
