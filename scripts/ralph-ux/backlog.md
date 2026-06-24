# UX backlog

Living, prioritized list of concrete UX problems. The first iteration (Mode S) runs a
parallel audit to seed this. Highest payoff / lowest effort first. Mark items done as
they ship; mark `~~rejected~~` with a one-line reason when the review panel kills one.

Format per item:
`- [ ] (payoff/effort) <view/flow> — <problem> → <proposed fix>`

## Open

_(empty — to be seeded by the Mode-S audit on the first iteration)_

### Always-include seed (until addressed)
- [ ] (high/M) Backups + Settings — backup **config** (S3 destination, retention,
  encryption) lives under `/settings` while backup **operations** (run, history,
  restore) live at `/backups`; a user must bounce between routes to configure then
  run. → Co-locate or cross-link so configure-then-run is one place / one flow.

## Done

_(none yet)_

## Rejected

_(none yet — restraint critic verdicts land here with a one-line why)_
