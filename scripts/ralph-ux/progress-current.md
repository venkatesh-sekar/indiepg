# Progress — UX loop

Rolling narrative, newest at top. One short entry per iteration: date, mode, what
changed, why.

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
