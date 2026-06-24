# Progress — UX loop

Rolling narrative, newest at top. One short entry per iteration: date, mode, what
changed, why.

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
