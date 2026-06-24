# UX backlog

Living, prioritized list of concrete UX problems. Seeded by the Mode-S parallel
audit (11 agents: one per view/flow + nav/IA, first-run, cross-view consistency).
Highest payoff / lowest effort first. Mark items done as they ship; mark
`~~rejected~~` with a one-line reason when the review panel kills one.

Format per item:
`- [ ] (payoff/effort) <view/flow> — <problem> → <proposed fix>`

## Open

### Quick wins (high/med payoff, S effort) — do these first
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
- [ ] (med/S) Query — the server returns `executed_sql` (possibly LIMIT-rewritten)
  but it's never shown, so a user can't tell what actually ran. → Show a compact,
  collapsed "Executed SQL" code block below results (render the value already returned).
- [ ] (med/S) Alerts — "Sustained" and "Cooldown" table column headers are bare
  jargon; a user can't tell what they do without opening the editor. → Add `Tooltip`
  info on the two headers with one-line plain-English definitions.
- [ ] (med/S) Pooler — the enable-confirmation copy ("Your apps then connect to
  …") can read as "the pooler reroutes them automatically"; users may not realize
  they must change their app's connection string, then debug phantom issues. →
  Reword to make it explicit that apps **must** be reconfigured to the pooler address.
- [ ] (med/S) Dashboard — "Connections" is shown twice (Postgres card + Server card)
  with slightly different formatting, adding cognitive load with no extra signal. →
  Keep it once (Server/host card); drop the duplicate from the Postgres card.

### Higher-effort, clear payoff
- [ ] (high/M) Backups + Settings — backup **config** (S3 destination, retention,
  encryption) lives under `/settings` while backup **operations** (run, history,
  restore) live at `/backups`; a user must bounce between routes to configure then
  run, and discovers the dependency mid-flow (e.g. restore with no S3 configured). →
  Co-locate: bring backup-destination config into `/backups` as a "Backup
  destination" card (and/or a quick-config dialog from the local-backup warning), so
  configure-then-run is one place. **The canonical seed item — 4 agents flagged it.**
- [ ] (med/S→M) Settings — the backup-config card gives no next step after saving
  ("is it actually working?"). → If the co-location item above isn't taken first,
  add a "Good to go — run a test backup" `<Link>` to `/backups` after the success
  state. (Subsumed by the co-location item if that ships.)
- [ ] (med/M) Migrate — completing a migration and clicking "Start another" leaves
  the previous source connection, target name, and overwrite checkbox pre-filled;
  batch-migrating users must clear each field. → Reset form state when returning
  from a terminal job.
- [ ] (med/M) Migrate — the overwrite gate is split across three intent-shifts
  (checkbox → button text flips to "Continue…" → modal asks to type the name) with no
  inline warning when overwrite is checked. → Surface a single visible destructive
  confirmation section when overwrite is on, instead of the silent button-label flip.
- [ ] (med/M) Settings — the page conflates three unrelated domains (backup config,
  read-only DatabaseTuning reference, Pooler add-on) with no grouping. → Group into
  clearly-labeled sections/cards (e.g. Backup destination, Database performance,
  Connection pooling). (Partly resolved if backup config moves to /backups.)
- [ ] (med/M) DatabaseTuning — parameter help text assumes DBA knowledge
  (shared_buffers, work_mem) and the page reads as prescriptive when it's really
  informational. → Add one reassuring intro line: defaults are already tuned for the
  hardware and the typical indie app needs no changes. (Intro-only change is S.)

### Lower / watch
- [ ] (med/M) Query — accidental write SQL (DELETE/UPDATE/DROP pasted in) is only
  rejected server-side after Run. → Optional: client-side keyword detector that warns
  before Run that the editor is read-only. (Keep restrained — copy hint, not a parser.)
- [ ] (low/S) Login — lockout message "Try again later" gives no sense of how long.
  → Frontend-only: soften to "Try again in a few minutes" (a precise duration needs a
  backend hint, which is out of scope).

## Done

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
