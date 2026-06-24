# Progress — UX loop

Rolling narrative, newest at top. One short entry per iteration: date, mode, what
changed, why.

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
