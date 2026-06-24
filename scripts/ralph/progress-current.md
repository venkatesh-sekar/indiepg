# Progress (rolling)

Newest first. One short entry per iteration: date, band, what changed, why.
Keep ~20 entries; archive older ones if this grows large.

<!-- iterations will be prepended here -->

## 2026-06-24 · band C · Modal → shadcn Dialog
Recomposed the hand-rolled `Modal` (`src/components/Modal.tsx` — a `.modal-backdrop`
div with a manual `keydown`/Escape handler, manual first-focus + focus-restore, and
a custom `.modal*` layout) over the shadcn `Dialog`. The wrapper keeps its exact
public API (`open, title, onClose, children, footer?, tone?, width?`) and composes
`Dialog`/`DialogContent`/`DialogHeader`/`DialogTitle`/`DialogFooter`, so all ~10
callsites (Alerts ×2, Migrate ×3, Backups ×1, RolesDatabases ×4, ConfirmDialog ×2)
stay untouched and behaviour is identical — Radix now owns the focus trap, Escape,
backdrop dismiss and focus restore. `width` maps to `sm:max-w-md|xl|3xl`; `tone="danger"`
adds a semantic `ring-destructive/30` accent (replacing the old danger header border)
plus a `data-tone` hook. The busy case (`onClose` is a no-op) still keeps the dialog
open: the controlled `open` prop overrides Radix's `onOpenChange(false)`. Reviewer
(ui-heuristics) caught two regressions, both fixed: (1) `overflow-y-auto` on
`DialogContent` would scroll the absolute-positioned close button out of view → moved
the scroll to an inner `max-h-[65vh] overflow-y-auto` wrapper around `{children}` only,
leaving the close button pinned; (2) Radix auto-generates an `aria-describedby` with no
matching `DialogDescription` → opted out with `aria-describedby={undefined}`. Deleted
the dead `.modal*` CSS and the now-orphaned `@keyframes fade`/`pop` (kept `.confirm-message`,
still used by ConfirmDialog). 95 web tests green (no test referenced modal classes;
ConfirmDialog/Pooler dialog tests query by text/role and stay green through the portal).
Modal.tsx stays as a thin Dialog shell — full per-callsite decomposition + `.btn`→`Button`
rides along with each view migration in band D.

## 2026-06-24 · band C · Spinner → shadcn Spinner
Recomposed the hand-rolled loading `Spinner` (`ui.tsx`, a `<div className="loading">`
with a CSS-`border`-spun `<span className="spinner">`) over the shadcn `Spinner`
primitive (`ui/spinner.tsx` — a `Loader2Icon` with `animate-spin`). The wrapper keeps
its `{ label }` public API and the muted status row, now styled with Tailwind semantic
tokens (`flex items-center gap-2.5 px-1 py-6 text-muted-foreground`); all 14 callsites
across App + 9 views untouched. Deleted the dead `.loading`/`.spinner`/`@keyframes spin`
CSS from `styles.css` (the `spin` keyframe had no other user). Accessibility: kept the
row's `role="status"` and made it an explicit live region (`aria-live="polite"`,
`aria-atomic="true"`); the inner icon is suppressed to decorative (`role="presentation"`
`aria-hidden="true"`) so there's no nested `role="status"` collision — props spread last
in the primitive, so the wrapper wins without editing the shared primitive. Added a
`Spinner` test to `ui.test.tsx` (status role + label + composed-over-`data-slot="spinner"`,
default-label fallback). 95 web tests green (was 93).

## 2026-06-24 · band C · Alert family → shadcn Alert
Migrated the hand-rolled callouts (`Callout`/`ErrorNotice`/`StaleBanner` in `ui.tsx`,
rendered as `<div className="callout callout-*">`) onto the shadcn `Alert`. Extended
`alert.tsx` cva with soft-tinted `success`/`warning`/`info` variants (reusing the same
`--color-*` theme tokens the Badge migration added) and set the destructive base to a
soft bg, mirroring the legacy callout look. Added a `data-variant` attr to `<Alert>`
(parity with Badge) so tests can assert tone without class strings. `Callout` keeps its
tone-based public API and maps tone→variant (info→info, warn→warning, danger→destructive,
ok→success) over `<Alert>` + `<AlertTitle>`/`<AlertDescription>`; all 40+ callsites
untouched. `ErrorNotice`/`StaleBanner` recomposed the same way (kept `labelForCode`
labels, the hint, role="alert" via Alert). Also switched two raw hand-rolled callout
divs to the shared `Callout`: `ConfirmDialog.tsx` (consequence) and `Login.tsx` (error).
Deleted the dead `.callout` container/color CSS (`.callout`, `.callout-title`, adjacency
margins, `.callout-info/ok/warn/danger`); kept `.callout-detail`/`.callout-hint` content
classes still used inside children. Migrated test asserts off `.callout-*` classes onto
`data-variant`/`data-slot` (Backups ×9, ConfirmDialog ×1, ui StaleBanner ×1).
ui-heuristics-reviewer: ACCEPTED the urgency/colorblind finding (added `border-l-4
border-l-<tone>` accent to all variants) and the hint-contrast finding (scoped
`[data-slot=alert] .callout-hint` to inherit the variant color, dimmed). DECLINED
removing `text-muted-foreground` from `AlertDescription` — that's the stock shadcn
pattern and the claim was hedged/unverified; the accent border already covers urgency.
Gates green: typecheck, 93 web tests, build, go build.

## 2026-06-24 · band C · Badge family → shadcn Badge
Migrated the hand-rolled badges (`Badge`/`ReadOnlyBadge`/`ResultBadge` in `ui.tsx`,
rendered as `<span className="badge badge-*">`) onto the shadcn `Badge`. shadcn's
base Badge lacks semantic status colors, so extended its cva with `success`/
`warning`/`info` variants (mirroring the built-in `destructive` soft-bg pattern),
backed by new `@theme inline` tokens `--color-success/-soft`, `--color-warning/-soft`,
`--color-info/-soft` that alias the existing legacy `--ok/--warn/--info` vars (already
carry light+dark values). `ui.tsx` keeps the same tone-based public API but maps
tone→variant over `<ShadcnBadge>`: neutral→secondary, ok→success, warn→warning,
danger→destructive, info→info, readonly→info. Callsites untouched (Badge ×81,
ResultBadge ×12, ReadOnlyBadge ×3). Deleted the dead `.badge*` CSS block. Migrated
`ui.test.tsx` ResultBadge tests from `.badge-*` class asserts to `data-variant`.
ui-heuristics-reviewer: ACCEPTED its ReadOnlyBadge regression finding (outline blended
with metadata labels → switched to the `info` variant, restoring a prominent colored
read-only affordance that matches the Query page's info Callout); DECLINED its
info/primary-soft token-collision finding as out-of-scope (pre-existing in the legacy
tokens, parity preserved) — logged it as a band-E item instead. Gates green: typecheck,
93 web tests, build, go build.

## 2026-06-24 · band B · Layout.tsx → shadcn Sidebar shell
Rebuilt the hand-rolled sidebar (`.app-shell`/`.sidebar`/`.nav-item` markup) as a
shadcn `Sidebar` shell: `SidebarProvider` + `Sidebar` (Header brand, Content nav
via `SidebarMenu`/`SidebarMenuButton asChild` over `NavLink`, Footer with signed-in
subject + sign-out) + `SidebarInset` with a top bar (`SidebarTrigger`, `Separator`,
current-view label). Nav items use lucide icons; active route via `isActive` →
`data-active`. Behavior identical: same 7 routes, same sign-out (logout→toast→/login).
Deleted the now-dead Layout-only CSS in styles.css (app-shell/sidebar/brand/nav-*/
sidebar-foot/content + their responsive rules); kept `.brand-mark` (Login still uses
it) and `.view`. Added `window.matchMedia` stub to test/setup.ts (jsdom lacks it; the
Sidebar's use-mobile hook needs it — benefits all future shadcn components). New
`Layout.test.tsx` (5 tests: all nav items + outlet, active state, navigation,
sign-out→/login, header title). ui-heuristics-reviewer pass: added the top-bar page
label (mobile system-status gap, finding 2); rejected its remove-isActive fix (that
shadcn pattern is canonical and drives `data-active` — NavLink alone wouldn't style
active) and its add-a-sign-out-confirmation fix (behavior change / scope creep — old
shell signed out on one click). Gates green: typecheck, 93 web tests, build, go build.

## 2026-06-24 · NEW TRACK: pure UI/UX (shadcn) · band A scaffold
Reconfigured the loop into a pure UI/UX track to rebuild the panel on shadcn/ui
(operator explicitly authorized the dep tree, overriding the old rule-4/5 refusal
that left band 4 untouched across 57 hardening iterations). Set up the scaffold
manually because installs need network/cache the loop's sandbox lacks: shadcn init
(radix base, Nova preset, Tailwind v4), added 24 ui components to src/components/ui/,
fixed the CLI's literal-`@/`-dir alias bug (relocated into src/, added paths to root
tsconfig.json), wired NPM_CONFIG_CACHE=web/.npm-cache into ralph.sh. Scaffold smoke
test proves the `@/` alias resolves + components render. Gates green: typecheck,
88 web tests (85+3), build, go build. New PROMPT.md/UI-RULES.md/backlog drive bands
B (shell) → C (primitives) → D (views, one per iteration) → E (cleanup).

## 2026-06-24 · COMPLETE · final north-star audit of the last two uncovered angles came back clean → loop done
North-star audit over the two not-yet-deep-audited angles the iter-56 note named, run as two parallel
read-only subagents: (1) the pg_dump/pg_restore/pgBackRest/psql/pg_ctl/systemctl **argv construction**
traced end-to-end as a UNIT (not the exec LAYER, which was cleared iter-53), and (2) the scheduler
**cron-spec parsing + next-fire + DST/clock-edge math**. BOTH returned a clean bill on correctness —
no CRITICAL/HIGH/MEDIUM present-day defect. Argv: no shell anywhere, every user value sits in
value-position after its own flag (a leading `-` is consumed as the flag's argument, never re-parsed)
or is `=`-joined only after whitelisting (stanza `[a-z0-9-]`, type `{full,diff,incr}`, validated
recovery targets); the recovery-target/direct-source validators do cover the argv tokens they feed.
Scheduler: robfig/cron v3 handles spring-forward (no skip), fall-back (no double-fire — `Next` advances
monotonically), `@every` (DST-immune), empty-spec opt-out, and never-firing specs correctly; overlapping
backups are backstopped by the Manager single-flight TryLock. The only findings were latent /
out-of-topology / cosmetic least-surprise notes (e.g. scheduler evaluating in implicit `time.Local` —
the least-surprising default, behaviorally harmless on a fixed box; argv values already
value-position-safe) — NONE load-bearing: none can lose data, wedge the panel, or confuse the operator.
With bands 0/1/1.5/2/2.5/3 all complete and every subsystem deep-audited across iters 44–57, the only
open backlog items are the YAGNI-gated SQLite schema-versioning LOW (wait for a real column-add) and
band-4 cosmetic shadcn redo (mechanically blocked + rule-4/5 conflict) — neither load-bearing. This is
exactly the completion condition the iter-53/55/56 notes set (a fresh audit over the last uncovered
angles coming back empty), so wrote `scripts/ralph/COMPLETE.md` and stopped. Gates green: Go
(gofmt/vet/test/build all pass), web (85 tests). Tree clean.

## 2026-06-24 · band 2 (stability) · expired session mid-use now routes to /login instead of getting stuck
North-star audit of the SPA routing/session-context lifecycle (one of the three not-yet-deep-audited
areas the iter-55 note named). FOUND + FIXED the one concrete "never get stuck" gap the prior web
audits had flagged: `RequireSession` (App.tsx) gates purely on the SessionProvider's `authenticated`
flag, which was only ever set at mount/login/logout. When the server-side session expires (cookie
TTL) or is revoked (operator logs out in another tab → the HMAC signing secret rotates, killing this
tab's token too), every subsequent API call 401s — but the hooks only set `error`, never told the
session context, so the flag stayed `true`, the guard kept rendering the dead view, and the operator
was stuck on a generic error forever. FIX (KISS, decoupled): new `web/src/auth/expiry.ts` — a tiny
Set-based pub/sub bridge so the generic `useAsync`/`usePolling` hooks can signal a 401 WITHOUT
importing React context (keeps the hooks tiny + the bundle small). Both hooks now call
`notifySessionExpired()` ONLY when the caught error is an `ApiError` with `isAuth` (code==="auth" ||
status===401); `usePolling` also sets `active=false` on that branch so it halts (the session is dead —
re-polling just 401s again, and the finally's guard then skips re-scheduling). `SessionProvider`
subscribes in a useEffect and flips `authenticated=false` + clears subject → `RequireSession`
Navigates to /login. Verified the bridge can't misfire: the login page uses `api.login` directly
(wrong-password 401 never goes through the hooks), and the boot `refresh()` uses raw `api.session`/
`api.whoami` in try/catch (a startup 401 is silently handled, not signalled). Test-first
(RED→GREEN): `hooks.test.tsx` (4) — both hooks trip expiry on an auth error, NEITHER on a non-auth
error (proven RED before the hooks change); `expiry.test.ts` (3) — pub/sub deliver/unsubscribe/multi,
with an afterEach drain so a throwing test can't leak a listener; `SessionContext.test.tsx` (1) — a
live authed session flips to anon on `notifySessionExpired()`. Reviewed (feature-dev:code-reviewer):
NO blocking findings; applied both non-blocking suggestions — corrected the reviewer's incomplete
"clearTimeout in catch" idea (the finally re-schedules regardless) to `active=false` which actually
halts the poller, and added the test-leak afterEach. Web gate green (typecheck/build/85 tests, was
77); Go gate green (gofmt/vet/test/build); committed regenerated dist; tree clean.

## 2026-06-24 · band 2 (stability) · Migrate.tsx pollers no longer show an error AND an infinite spinner together
North-star audit iteration over three NOT-YET-COVERED areas (owner-claim/instance-bootstrap,
alert/metrics evaluation math, web SPA/api-client error-handling) — the three candidates the
iter-53 note named. The first two came back with a CLEAN bill (no present-day CRITICAL/HIGH/MEDIUM;
only latent/out-of-topology findings — e.g. replication-lag false-positive needs a replica
topology indiepg never provisions, so on the supported single-primary box that metric is always 0
and never fires). The web audit found ONE genuine present-day defect (LOW but real): all three
polling sub-components in `web/src/views/Migrate.tsx` (`MigrationHistory`, `DirectJobProgress`,
`SessionProgress`) rendered a first-load error notice AND a perpetual loading `Spinner`
simultaneously on first-load failure — `{error && !X ? <ErrorNotice/> : null}` followed by
`{!X ? <Spinner/> : …}`, both true when `X` is null and `error` is set — exactly the
"infinite-spinner-beside-an-error" confusion the band-2 stability work exists to kill, but this
file's three pollers were missed when StaleBanner shipped. Fixed by gating the spinner to show
ONLY while genuinely loading: `error ? null : <Spinner/>` (the error notice above already speaks).
Also closed a sibling-inconsistency: `MigrationHistory` had NO `StaleBanner`, so a poll that failed
AFTER first success silently kept a possibly-stale log on screen — added
`{error && data ? <StaleBanner error={error}/> : null}` mirroring `DirectJobProgress`/`SessionProgress`.
Exported the three previously-internal components as the test seam (consistent with the codebase's
"export helper for tests" convention). Test-first `web/src/views/Migrate.test.tsx` (10 tests): the
3 first-load cases proven RED on the old double-render (asserts the error text shows AND
`queryByRole("status")` — Spinner's role, nothing else uses it — is absent), the post-success
stale cases (cached row stays + "Live updates paused" banner shows; clean poll shows neither
error nor banner nor any `role="alert"`), and 3 positive "still spins while genuinely loading"
cases. Reviewed (feature-dev:code-reviewer): NO blocking findings — confirmed no blank-stuck state,
StaleBanner split symmetric with siblings, exports safe, tests non-vacuous; applied both
non-blocking suggestions (positive spinner tests for the other two pollers + a zero-state poll
default to remove a future-contributor footgun). Web gate green (typecheck/build/77 tests, was 67);
Go gate green (gofmt/vet/test/build); committed dist. Tree clean.

## 2026-06-24 · band 2 (stability) · direct-migration source Port/User/SSLMode validated fail-fast
`validateDirectSource` (internal/server/handlers_migrate.go) checked only `Host != ""` +
`ValidateIdentifier(Database)`, leaving the source connection's `Port`/`User`/`SSLMode`
unchecked — they flow into `-p`/`-U` libpq argv and `PGSSLMODE` env in `migrate.connArgs`
(os/exec value-position, NO shell — never an injection), so a bogus value made libpq fail
the connect with an OPAQUE error on a user-facing migration path. Mirroring the accepted
PITR recovery-target fix, added three fail-fast validators (single-db, cluster, and ssh-less
export all share this one function): `validateSourcePort` (empty-or-numeric-in-`[1,65535]`;
the `strconv.Atoi` error arm catches the integer-overflow case); `validateSourceUser`
(empty-allowed, ≤63 BYTES = PG NAMEDATALEN, rune-iterated to reject ONLY control chars so a
legit mixed-case/symbol role like `App_Reader-1` still passes — no false-reject); and
`validateSourceSSLMode` (empty-allowed → libpq default `prefer`, else exact membership in
{disable,allow,prefer,require,verify-ca,verify-full}, case-sensitive). Each rejects with
`core.ValidationError`, turning a confusing mid-connect failure into a clear up-front message.
Password is deliberately left unvalidated (opaque; a wrong one yields a clear libpq auth error).
Test-first `TestValidateDirectSource` (12 valid / 12 invalid, each invalid asserting
`core.CodeValidation`) proven RED→GREEN. Reviewed (feature-dev:code-reviewer): no blocking
findings — no false-reject, overflow handled, byte-vs-rune length correct. Go gates green
(gofmt/vet/test/build); web untouched. Tree clean. Only the YAGNI-gated SQLite schema-versioning
LOW item now remains open in band 2 (deferred until a real additive column change needs it).

## 2026-06-24 · band 2 (stability) · PITR recovery target validated up front before a restore
North-star audit of the TWO subsystems prior audits skipped — the exec/runner layer
(`internal/exec`, `internal/pg/adminexec.go`) and the web handler input-validation surface
(`internal/server/handlers_*.go`) — i.e. the command-injection / untrusted-input attack surface.
Both came back fundamentally SOLID (no CRITICAL/HIGH): the exec layer uses `os/exec` argv with
NO shell anywhere, `AsUser` is a fixed `sudo -u <user>` triple (never interpolated), `Sensitive`
keeps secrets out of argv/logs; every operator identifier reaching SQL goes through
`ValidateIdentifier`/`QuoteIdent`/`QuoteLiteral`, request bodies are bounded by `MaxBytesReader`
+ `DisallowUnknownFields`, and every state-changing route sits inside `requireAuth` (CSRF-gated).
FIXED the one real load-bearing gap (LOW): the PITR recovery target's `XID`/`LSN`/`Name` came
straight from the restore JSON body and reached `pgbackrest --target=<value>` with NO content
validation — `RecoveryTarget.Validate()` checked only the COUNT of set targets and the Action
enum. Not an injection (argv value-position, no shell), but a malformed value produced an opaque
pgBackRest failure PARTWAY through a restore — the worst moment (data recovery) for a confusing
error, and after the safety-backup work already ran. Added `validateContent` at the tail of
`Validate()` (which `Manager.Restore` calls BEFORE `takeSafetyBackup`, so a bad target now fails
fast with NO side effects): XID ≤20 digits all-numeric; LSN two hex runs (≤8 digits each — a PG
LSN is two 32-bit halves) joined by exactly one slash; Name rejects control chars / DEL / U+2028
/ U+2029 and caps at 128 bytes (spaces + printable Unicode still allowed, matching real
restore-point names). Test-first (proven RED): extended `TestRecoveryTargetValidate` with valid
(only-lsn, max-width-lsn, name-with-space) and invalid (non-numeric/negative/spaced xid,
slash-less/non-hex/trailing-slash/two-slash/overlong-segment lsn, newline/null name) cases.
Reviewed (feature-dev:code-reviewer): applied the one Important finding — LSN segment cap
tightened 16→8 + an overlong-segment test added; the no-false-reject invariant (no legitimate
LSN/xid/name rejected) and no-regression-to-the-restore-path confirmed. The audit's other finding
(direct-migration source Port/User/SSLMode unvalidated, also LOW) was added to the backlog, not
fixed (rule 1 = one item). Go gates green (gofmt/vet/test/build); web untouched. Tree clean.

## 2026-06-24 · band 2 (stability) · SQLite connection pragmas now apply to EVERY connection
North-star audit (store/config/notifier — areas prior audits skipped) found a MEDIUM "never
get stuck" defect: the four connection pragmas (`busy_timeout=5000`, `foreign_keys=on`,
`synchronous=NORMAL`, `journal_mode=WAL`) were applied ONCE via `db.Exec` on the pooled
`*sql.DB` (`store.go applyPragmas`). With `SetMaxOpenConns(1)` the single connection normally
persists, but `database/sql` can discard and reopen it (driver error, idle eviction) — and a
fresh connection then silently reverts the PER-CONNECTION pragmas. The load-bearing one is
`busy_timeout`: reverting to 0 turns a transient lock into an immediate "database is locked"
surfaced to the operator instead of the intended 5s wait. (`foreign_keys` is currently inert —
the schema declares no FKs — but encoding it costs nothing and is correct for future columns;
`journal_mode=WAL` is file-header-persistent so it survived regardless.)
FIX: encode the pragmas into the DSN as `_pragma=...` query params (`buildDSN`), which the
modernc.org/sqlite v1.52.0 driver re-runs as `PRAGMA ...` on every connection open (verified:
`applyQueryParams` is called inside the per-connection open path in the driver's `conn.go`).
Removed `applyPragmas`; `sql.Open` is lazy so a bad-pragma error now surfaces at first use,
with the `db.Close` on the `migrate()` error path unchanged. Empty-path guard returns the DSN
unchanged (driver only parses query params when `?` is not the first char, and there's no file
to tune). Tests (test-first, non-vacuous — proven RED with `buildDSN` neutered to return the
bare path: busy_timeout read back as 0): `TestConnectionPragmasApplyToEveryConnection` forces
`SetMaxIdleConns(0)` so each query opens a brand-new connection, then asserts busy_timeout=5000
+ foreign_keys=1 + journal_mode=wal on that fresh conn; `TestBuildDSNEncodesPragmas` pins the
path-preserved + each pragma encoded + empty-path passthrough. Reviewed
(feature-dev:code-reviewer): no high-confidence findings — DSN parsing, `:memory:`/space-path
safety, non-regression on WAL, secret-handling unchanged (no secret ever in the DSN), and
test non-vacuousness all confirmed sound. Go gates green (gofmt/vet/test/build); web untouched.

## 2026-06-24 · band 3 (usability) · operator can now DISABLE the pooler from the UI
The opt-in PgBouncer pooler had no off switch: `internal/pgbouncer/enable.go` had only
`Enable`/`IsEnabled` and the handlers only status+enable, so an operator who turned it on
was stuck shelling in to undo it — "safe optional overrides" implies reversibility. Added
`Manager.DisableNow` (`systemctl disable --now pgbouncer`, idempotent inverse of EnableNow)
and `Manager.Disable(ctx, state)`, which stops the service FIRST then persists
`pooler.enabled=false`. Ordering is the load-bearing safety choice: a stop failure returns
the error WITHOUT clearing the flag, so the panel never reports the pooler off while it is
actually still running (the inverse of Enable's "persist last" rule — recording off then
failing to stop would be the worse lie). New CSRF-gated `POST /api/pooler/disable`
(`handlePoolerDisable`, no body) under requireAuth, auto-covered by the band-1 route-walk
CSRF test, audited by code. UI: a red "Disable connection pooler" button on the enabled
view behind a `tone="danger"` confirm that states exactly what stops (service down, no
restart on reboot, apps pointed at the pooler fail to connect until repointed at Postgres)
+ reassurance (no PG restart, no data touched, re-enable anytime); plain confirm because
disabling is reversible. `onEnabled`→`onChanged` prop rename; new `api.disablePooler()`.
Tests: manager (stops-then-persists, stop-failure-doesn't-persist [RED on a persist-first
impl], idempotent, persist-failure-surfaces, requires-runner+state); handler (requires-auth,
flag untouched on reject); web (disable button only when on, confirm copy, failed-disable
keeps dialog open). Reviewed (feature-dev:code-reviewer): both Important findings applied.
Go + web gates green. Band 3 now fully complete (pooler round-trips on↔off).

## 2026-06-24 · band 2 (stability) · ssh-less S3 migration no longer OOMs on a multi-GB dump
The ssh-less (shared-bucket) migration path buffers the WHOLE pg_dump in memory —
`ExportToSession` does `os.ReadFile`→`PutObject([]byte)` and `ImportFromSession` does
`GetObject`→`[]byte`→`WriteFile` — so a multi-GB DB could OOM-kill the single binary
mid-migration (direct-pull streams to a file and was unaffected). Took the minimal
interim per the backlog: a 1 GiB cap (`MaxSessionDumpBytes`, internal/migrate/orchestrator.go)
checked on EXPORT against `info.SizeBytes` BEFORE `os.ReadFile`, and on IMPORT against
the exporter-recorded `sess.DumpSize` BEFORE `GetObject` — over-cap fails the (cross-panel)
session and returns a CodeValidation error pointing the operator at direct-pull. Honestly
documented residual: the import guard trusts the recorded size, so a lying peer recording
a small size could still OOM — acceptable interim (trusted builds, short-lived bucket);
full fix needs streaming GetObject. Test-first (RED→GREEN): export/import each refuse before
the upload/download phase, fail the session, and surface the direct-pull pointer. Reviewed
(feature-dev:code-reviewer): fixed an inverted-threat comment; guard placement + failSession
coverage + tests confirmed sound. Go gate green.

## 2026-06-24 · band 2 (stability) · migration workers can no longer hang forever on a stalled source
North-star audit HIGH defect. The three detached migration workers
(`runDirectJob`/`runExportJob`/`runImportWorker`, internal/server/migrate_worker.go)
ran on a bare `context.Background()` with no deadline, and the engine's `connArgs`
(internal/migrate/engine.go) set no connect timeout. A source that accepts the TCP
connection then stalls (firewall black-hole, overloaded host) made validateSource /
pg_dump block indefinitely → the migration sat in `importing` forever, work dir never
cleaned until the next restart sweep. Direct-pull is the primary path and the source is
arbitrary user input, so this is a real "get stuck" hazard. FIX (two parts): (1) added
`PGCONNECT_TIMEOUT=10` to the REMOTE branch of connArgs — bounds libpq's connect/auth
phase only (never a running dump, so it can't abort a legit slow migration) so a dead
source fails fast on connect; (2) every worker now runs on `workerContext()` =
`context.WithTimeout(context.Background(), migrationJobTimeout=6h)` — a generous
backstop for a stall that begins after connect; the orchestrator already threads ctx
into every engine call so the deadline propagates and the work dir is cleaned on return.
Reviewer (feature-dev:code-reviewer) caught a CRITICAL: `rec.Fail`/`rec.Succeed` wrote
through the SAME now-expired worker ctx, so on a timeout the SQLite write would no-op
and leave the job wedged in `importing` — defeating the fix. Fixed at the persistence
boundary (one place, covers the worker timeout arm AND the orchestrator's
fail/failSession-from-a-stalled-dump): `storeRecorder.Fail`/`Succeed` persist on
`context.WithTimeout(context.WithoutCancel(ctx), 10s)`, matching the existing
backup-manager detached-ctx convention. Tests (test-first + the reviewer regression):
connArgs remote-has / local-lacks the timeout; orchestrator stalled-source (blocking
Version) fails at the deadline with `errors.Is(err, DeadlineExceeded)` and never dumps;
workerContext is bounded + its shrunk timeout fires; and `Fail` persists `failed`
status despite an already-cancelled ctx (non-vacuous: RED on the old direct-ctx code).
Go gate green incl. `-race`; web untouched.

## 2026-06-24 · band 1.5 (data durability) · cluster migration no longer aborts on the `postgres` maintenance DB
North-star audit (areas iter-44 didn't cover: migration, scheduler, leaks, pooler
atomicity) found a HIGH-severity migration defect. A whole-cluster migration
(`directCluster`, orchestrator.go) iterates `engine.ListDatabases`, which excluded
only `template0`/`template1` — so the source's `postgres` maintenance DB entered the
per-database loop. In overwrite mode `DropDatabase(target,"postgres")` runs
`DROP DATABASE IF EXISTS postgres` while connected to `-d postgres` → "cannot drop the
currently open database" → the cluster move aborts AFTER earlier DBs were already
dropped+restored (half-migrated box). In non-overwrite mode the restore's
`CREATE DATABASE postgres` collides with the target's own maintenance DB. Every real
cluster has a `postgres` DB, so this fired on essentially every cluster move — and the
existing tests never included `postgres`, so it was invisible. FIX (test-first): added
`'postgres'` to the SQL `NOT IN` exclusion in `engine.ListDatabases` (engine.go) — its
SOLE caller is the cluster loop (the panel's `GET /databases` uses a different
`pg.ListDatabases`), so `total`, the dump/restore loop, and the verify loop all
consistently exclude it with one change. Roles/grants are carried by globals, so the
maintenance DB holds no user data to move. Test `TestListDatabases_excludesPostgresMaintenanceDB`
asserts the query carries the exclusion (filtering is SQL-side; the quoted `'postgres'`
literal is unambiguous vs the unquoted `-d postgres` arg); proven RED→GREEN. Reviewed
(feature-dev:code-reviewer): no blocking issues — sole-caller, count-consistency, and
non-vacuous-test all confirmed; the now-silent skip of a `postgres` DB holding user
tables is a net improvement over crashing. The other audit findings (migration worker
has no timeout/connect_timeout → can hang forever; ssh-less S3 path holds whole dump in
RAM → OOM; no pooler-disable inverse) are appended to the backlog.

## 2026-06-24 · band 2.5 (config safety) · IsRunning judges PG liveness by a real probe, not the lying systemd wrapper
Closed the last open band-2.5 follow-up (the audit-found LOW item). `Manager.IsRunning`
(internal/pg/manager.go) trusted `systemctl is-active postgresql`, but on the
apt-provisioned Debian/Ubuntu platform that unit is a oneshot wrapper that reports
"active" even when the real `postgresql@<ver>-main.service` failed to start — so a
down cluster could masquerade as running on the dashboard health badge (its only
caller, handlers_dashboard.go:63).

Fix: IsRunning now reuses the existing `confirmAcceptingConnections` helper — a real
`SELECT 1` over the local socket as the postgres OS user (bounded 30s) — as the
authoritative "is PG up?" signal, the same probe restartWithRollback uses. A probe
failure (down or unreachable) stays a clean `(false, nil)`, preserving the
documented "not running is a normal queryable state" contract; the caller already
discards the error. This is the third and final code path (after restartWithRollback's
initial + post-rollback restarts) hardened against the wrapper lie.

Test-first: new is_running_test.go `TestIsRunning_ProbesPostmasterNotSystemdWrapper`
proves a down postmaster reports not-running even when an `is-active` responder would
say "active", asserts the SELECT 1 probe runs as the postgres user, and (reviewer-
hardened) asserts IsRunning NEVER calls `systemctl is-active`. Proven RED→GREEN.
Removed the obsolete table-driven `TestIsRunning` (tested the deleted is-active
path); `TestIsRunning_NoRunner` kept for the error-code path. Reviewed
(feature-dev:code-reviewer): the one finding — add the negative `is-active`
assertion — applied. Go gate green incl. `-race`; no web changes.

## 2026-06-24 · band 2 (stability) · one rule's persist failure no longer drops sibling alert events
Audit-surfaced gap (state.json notes, iter 44): `alert/engine.go` `Evaluate`
appended each rule's event AFTER its store write and did `return nil, err` on a
persist failure — so a single rule's failed write discarded every firing/recovery
already computed for OTHER rules that cycle, and `runTelemetryCycle` then returned
early without dispatching anything. Under store pressure a real critical page
(e.g. `disk-almost-full`) could be silently lost because an unrelated rule could
not be saved — defeating the alert subsystem at the worst moment.

Fix: `Evaluate` now appends each event BEFORE the best-effort persist; a persist
failure logs the per-rule error, accumulates it, and presses on rather than
aborting, returning all computed events + `errors.Join(persistErrs...)`. The
caller treats that error as non-fatal and dispatches the events anyway (only a
sampling error still aborts the cycle). Unpersisted state self-heals next tick (at
worst a sustained breach re-notifies — strictly safer than a dropped alert).
Added a narrow `alertStore` interface (ListAlerts/UpsertAlert; `*store.Store`
satisfies it, no caller change) so a persist failure can be injected in tests.

Test-first: `TestEvaluateOneRulePersistFailureKeepsSiblingEvents` — the failing
rule sorts FIRST so it's processed before the good one, proving the loop doesn't
abort on the first error. Proven RED against the old code (sibling event dropped
to `""`), GREEN after. Reviewed (feature-dev:code-reviewer): no blocking issues.
Go gate green incl. `-race` on alert + server.

## 2026-06-24 · band 1.5 (data durability) · backup-failed alert can no longer go silent when the failure-row insert also fails
Audit-surfaced gap (state.json notes, iter 44): the immediate critical
`backup-failed` alert is derived by the telemetry collector SOLELY from the
newest `backup_history` row (`enrichBackup`), but `recordBackup`
(backup/manager.go) is best-effort and swallows insert errors. So if a scheduled
backup FAILS (e.g. S3 outage) AND its `fail` row insert ALSO fails (SQLite
contention / disk pressure on the panel volume), the newest row stays the prior
SUCCESS → the alert never fires, degrading to the 26h `backup-stale` warning.
That defeats the north-star "loud alert on backup failure" guarantee.

FIX (test-first): the backup `Manager` now remembers its last backup outcome IN
MEMORY (`lastOutcome{at, failed, valid}`, guarded by an `outcomeMu` RWMutex),
recorded in `Backup` right after the run completes — BEFORE and independent of
the best-effort `recordBackup` store write. New `Manager.LastOutcome()` exposes
it. The collector gained an optional `BackupOutcomeSource` (one method, satisfied
structurally by `*backup.Manager`, wired via `collector.UseBackupOutcome(s.backups)`
in `newServer`). `enrichBackup` now merges the newest stored row with the
in-memory outcome and takes whichever is MORE RECENT to drive `LastBackupFailed`
— so a failed-but-unpersisted backup still fires, a recovery still clears, and
the persisted row stays authoritative on a tie (and across restarts, where memory
is empty). No-backups-yet still leaves the signal at 0 (fresh box not "failed").

Tests: manager — failure records in-memory outcome with NO store at all, success
clears it, never-ran leaves it unset. collector — stale-store-success +
newer-in-memory-fail still fires (the regression), in-memory recovery clears a
stale stored fail, store-wins-when-newer, in-memory-only (no rows) fires. All
non-vacuous (the regression test reads 0 against old code). Reviewed
(feature-dev:code-reviewer): both Important findings applied — `outcomeMu` is an
RWMutex with an RLock read path (matches the `mu` convention; readers don't block
the writer), and `UseBackupOutcome` documents the construct-before-goroutines
ordering. Go gate green incl. `-race` on backup/telemetry/server; web untouched.

## 2026-06-24 · band 2.5 (resource & config safety) · self-healing rollback: judge PG health by a real liveness probe, not the systemd exit code
North-star audit (bands 0–3 complete; only band-4 cosmetic shadcn redo remained,
which conflicts with the non-negotiable YAGNI/KISS + security-supply-chain rules,
so I audited instead of pulling in a Tailwind/Radix/lucide dep tree) surfaced a
CRITICAL load-bearing defect in `internal/pg/safeconfig.go`. `restartWithRollback`
— the self-heal that snapshots `postgresql.auto.conf` before a restart-requiring
change and reverts to last-known-good if PG won't come back up (used by
`ApplyTuning` host-sized tuning + `EnsureArchiving` WAL config) — judged "did PG
come back up?" SOLELY by the exit code of `systemctl restart postgresql`. But the
cluster is apt-installed on Debian/Ubuntu (manager.go:53), where the `postgresql`
unit is a oneshot wrapper that pulls in the real `postgresql@<ver>-main.service`
via a NON-binding `Wants` → `systemctl restart postgresql` exits 0 even when the
cluster fails to start. So a bad tuning/archiving value could leave Postgres DOWN
while the panel reported success and the rollback NEVER fired — defeating the one
guarantee the whole primitive exists for ("Postgres never left down"). FIX: health
is now judged by a real liveness probe — `confirmAcceptingConnections` runs
`SELECT 1` over the local socket as the postgres OS user (peer auth, bounded 30s
timeout), bypassing the lying wrapper. New `restartAndConfirm` (restart + confirm)
is used for BOTH the initial restart AND the post-rollback restart, so we also
never falsely claim "Postgres is running" after a rollback that didn't actually
bring it up (that path now honestly returns CodeInternal "down"). Reused the
existing bare-`psql` path so the `//go:build integration` test
(`tuning_apply_integration_test.go`, which injects PGHOST/PGPORT env into every
command + translates `systemctl restart postgresql`→`pg_ctl restart -w`) keeps
passing unchanged. Test-first: 2 new regression tests (systemd-lies→still-rolls-
back→CodeSafety; stays-down-after-rollback→CodeInternal) + augmented the happy
path to assert the liveness probe actually runs — all 3 RED against the old code,
GREEN after. Reviewed (feature-dev:code-reviewer): no blocking issues (confirmed
SELECT 1 is the stricter/correct signal vs pg_isready, integration test unbroken,
tests non-vacuous). Full Go gate green. NOTE follow-up added to backlog: `IsRunning`
(manager.go:251) is fooled by the same wrapper but only feeds a best-effort
dashboard badge (telemetry's real PG connection is the authoritative signal there).

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — React UI toggle (UI slice 3 of 3, FEATURE COMPLETE)
The final slice: the operator-facing toggle on Settings, wiring the two backend
endpoints into one card (`web/src/views/Pooler.tsx`, rendered after the tuning
card in `Settings.tsx`). Off by default. When OFF it explains in plain language
what a pooler is and when you don't need one, previews the loopback address apps
would connect to (`host:listen_port`) and the host-sized pool sizing (each row
labeled by effect via `PoolSettingsTable`), and lets the operator tick which app
roles to route — filtered to non-superuser login roles (superusers connect
directly; the pool reserves connections for admin). Enabling is gated behind a
`ConfirmDialog` that states EXACTLY what will happen first — installs the
PgBouncer package, starts the service on `host:listen_port`, routes the N named
roles — and reassures it does NOT restart Postgres or touch data. The enable
button is disabled until ≥1 role is picked AND Postgres is reachable (no
`pool` ⇒ can't size ⇒ refuse, matching the server's 409). Client sends `{roles}`
only — never `max_connections` (server sizes the pool; a forged value can't widen
it). New api client `poolerStatus()`/`enablePooler()` + 4 TS types mirroring the
Go JSON. Tests `Pooler.test.tsx` (9): enabled-view address+sizing, enabled with
PG unreachable, disabled role filtering (superuser/non-login excluded), button
gated on selection, gated when PG unreachable, roles-still-loading shows a
spinner not the misleading empty state, empty-state when no app roles, confirm
copy asserts each material claim + sends `{roles}` + calls onEnabled, failed
enable surfaces the error and does NOT signal success. Reviewed
(feature-dev:code-reviewer): fixed the Important finding (roles-loading race that
flashed "No app roles to route yet" before the list arrived — now threads
`rolesLoading` and shows a spinner) + the minor state-order note. **Pooler
feature COMPLETE (all 11 sub-slices done) → band 3 (usability) COMPLETE → next is
band 4 (UI redo, shadcn).**

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — enable endpoint (UI slice 2 of 3)
The status endpoint shipped last iteration gave the UI something to read; this
adds the action it will call. `POST /api/pooler/enable` (`handlePoolerEnable`)
wires the already-built, already-tested `pgbouncer.Manager.Enable` orchestrator
into the HTTP surface: a new `pooler *pgbouncer.Manager` field is constructed in
`newServer` over the shared OS runner (`s.pg` as VerifierSource, `s.store` as
PoolerState), and the route sits under `requireAuth` so it inherits the CSRF gate
for cookie flows — system-mutating (apt install + systemctl), so a deliberate
POST. Input is `{roles, profile}` only: `max_connections` is NOT client-supplied;
the pool is sized server-side from the live `s.pg.CurrentTuning().Applied`, so a
forged value can't widen the pool past what Postgres can serve (security
tie-break). Guards run cheap→costly: empty roles → 400 (an empty auth_file would
lock every app out), unknown profile → 400 (no silent mis-size), then
PG-unreachable → 409 with NO side effect rather than guess-then-install. Audited
by code only (never role names/verifiers). Tests cover the four guard paths
(auth/empty-roles/bad-profile/PG-unreachable, each asserting the flag stays off);
the full side-effecting happy path stays in `pgbouncer/enable_test.go` (fake
runner) by the same convention backups/migrate handlers follow. Reviewed
(feature-dev:code-reviewer): no findings. REMAINING: the React UI toggle (slice 3).

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — read-only status endpoint (UI slice 1 of 3)
The remaining pooler item was "the UI toggle", but the panel had no backend for
it to read — `Manager.Enable` exists but is unexposed. Built the safe, no-side-
effect half first: `GET /api/pooler` (`handlers_pooler.go`), behind `requireAuth`,
returning `{enabled, host, listen_port, pool}`. `enabled` comes from
`pgbouncer.IsEnabled` (config key `pooler.enabled`; absent = default-off);
`host`/`listen_port` are the loopback target same-box apps use (new exported
`pgbouncer.LoopbackHost` / `DefaultListenPort`, aliasing the existing unexported
constants — one source of truth); `pool` is the host-sized
`RecommendPool(max_connections, Mixed)` sizing that enabling would apply, so the
UI can label each pool setting by its effect. Pool sizing is best-effort via
`s.pg.CurrentTuning` — if Postgres is unreachable (`Applied==nil`) Pool stays nil
and the UI explains it's computed at enable time, never a guess. The endpoint
NEVER mutates host state (no install, no service touch); a tuning read error is
non-fatal so the enabled flag still renders. Split the old single "UI toggle"
backlog item into three slices (this status endpoint ✓, a future POST enable
endpoint, the React UI). Tests `handlers_pooler_test.go`: default-off,
reflects-persisted-enable (proves it reads the same key `Enable` writes),
requires-auth (401, never an SPA leak). Dropped a premature unused `pooler
*pgbouncer.Manager` Server field (YAGNI — it belongs to the enable slice).
Reviewed (feature-dev:code-reviewer): no critical/blocking; declined the one
Important finding (test the `CurrentTuning`-error branch) — that branch is
currently unreachable and testing it needs an injection seam nothing else uses.
Why: the UI toggle's first dependency, shipped as the safe read-only half so the
system-mutating enable path lands as its own confirmed, separately-reviewed slice.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — enable-flow ORCHESTRATOR (slice 7)
Wired the six already-built primitives into one opt-in entry point:
`internal/pgbouncer/enable.go` `Manager.Enable(ctx, src VerifierSource, state
PoolerState, p EnableParams) (EnableResult, error)`. OFF by default; idempotent /
re-runnable. Steps: InstallPackage → RoleVerifiers (via `src`, satisfied by
`*pg.Manager`) → EnsureConfig → EnsureUserlist → EnableNow → Reload **only when
config/auth_file changed** → IsRunning verify → persist `enabled=true` LAST (only
once the unit is confirmed up, so the stored flag can never contradict a failed
bring-up). Added `IsEnabled(ctx, state)` (unset key = default-off, not an error)
for the upcoming UI toggle. `PoolerState` is a narrow GetConfig/SetConfig
interface `*store.Store` satisfies — no store coupling pulled into the package.
SECURITY ORDERING (reviewer-caught, fixed before commit): EnsureConfig runs
BEFORE EnsureUserlist. EnsureConfig's marker guard is the flow's only ownership
check; EnsureUserlist (secret-adjacent SCRAM verifiers, no marker possible) must
not write into /etc/pgbouncer until that guard confirms indiepg owns the dir — so
a foreign distro/operator pgbouncer.ini is a hard stop with NO auth_file left
behind. Logs only role COUNT, never names/verifiers. Tests (enable_test.go, fake
VerifierSource + in-memory PoolerState, files in a temp confdir): happy-path
step-ordering + 0640 + auth_file path, idempotent-no-bounce, reload-only-on-change
(adding a role reloads, unchanged doesn't), verifier-error-before-any-write,
foreign-config-stops-before-auth_file, non-SCRAM-refused, not-running-not-recorded,
enable-fails-not-persisted, persist-failure-surfaces, input validation, IsEnabled
default-off. Reviewed (feature-dev:code-reviewer): both findings applied (the
ordering swap + bring-up-failure test hardening). REMAINING: the UI toggle slice.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — service-lifecycle primitives (enable-flow slice 6)
Carved the mechanical service-control layer off the remaining enable-flow item
(the riskiest sub-piece — touching apt + the systemd unit), mirroring
`pg/manager.go`'s proven Provision/IsRunning patterns. Added
`internal/pgbouncer/service.go` with four methods on the existing `Manager`:
- `InstallPackage(ctx)` — `apt-get update` then `apt-get install -y pgbouncer`,
  both `DEBIAN_FRONTEND=noninteractive` so a prompt can't wedge it; idempotent
  (apt no-ops an already-installed pkg). Update failure stops before install.
- `EnableNow(ctx)` — `systemctl enable --now pgbouncer` (idempotent: already-up
  unit is a no-op → re-runnable enable flow).
- `Reload(ctx)` — least-disruptive apply: tries `systemctl reload` first (SIGHUP,
  PgBouncer re-reads config + reopens auth_file WITHOUT dropping client conns),
  and only on failure falls back to `systemctl restart`; both failing surfaces a
  CodeExec error. The enable flow calls this ONLY when config/auth_file changed
  (EnsureConfig/EnsureUserlist report it), so an unchanged pooler is never bounced.
- `IsRunning(ctx)` — `systemctl is-active`. A reported non-active state
  ("inactive"/"failed", which exits non-zero) is a clean false; but an EMPTY
  stdout + runner error (systemctl absent, cancelled ctx) is surfaced as the error
  it is — improves on pg.IsRunning so the orchestrator's verify-after-start can't
  mistake "couldn't ask" for "service down" and needlessly bounce it.
NOT yet wired into a live path — the orchestrator `Enable(...)` (RoleVerifiers →
EnsureUserlist + EnsureConfig + these, with "reload only when changed") is the
next slice, then the UI toggle. Tests `service_test.go` (16): update-then-install
ordering + noninteractive env, update-stops-install, install-hint, enable argv,
reload-prefers-SIGHUP (never restarts on success), reload→restart fallback,
both-fail error, is-active true/inactive-false/undeterminable-error, requires-runner
for each. Reviewed (feature-dev:code-reviewer): applied the IsRunning
error-surfacing finding (#1) + its companion test (#2); rejected #3 (suggested
`WarnCtx` does not exist on `core.Logger` — only `Warn`, which matches pg's
fallback-path convention). Gates: gofmt clean, go vet, go test ./..., CGO_ENABLED=0
build all green; no web/ touched.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — pg_authid SCRAM verifier query (enable-flow slice 5)
Carved the next enable-flow slice off: the one input the auth_file installer
(`EnsureUserlist`) still needed fed — the app roles' SCRAM verifiers, read from
`pg_authid`. Added `Manager.RoleVerifiers(ctx, roleNames []string)
([]RoleVerifier, error)` in `internal/pg/verifiers.go`:
- `pg_authid.rolpassword` is superuser-only, so it ALWAYS goes through the
  privileged psql path (`runPsql`, postgres OS user, peer auth) — never the
  panel's non-superuser read pool. Mirrors `settings.go` showSetting's fallback.
- Secret-adjacent: the function logs nothing; `runPsql` logs only argv (role
  names, not secret), never stdout (the verifiers). The query carries no PASSWORD
  literal so `Sensitive` stays correctly false.
- Strict (auth_file is a boundary): every requested name is `ValidateIdentifier`-
  gated AND `QuoteLiteral`'d into the IN-list (defense in depth — the name still
  travels through SQL); a missing role is a NotFound naming it (can't pool a
  role that isn't there); a role with NULL/empty rolpassword is a Validation
  error, never silently dropped (a missing entry would lock that app out);
  duplicate requests de-duped; deterministic sort by name.
- SCRAM-vs-md5/plaintext is deliberately NOT checked here — `RenderUserlist` is
  the single gate that refuses a non-SCRAM verifier (no downgrade). Verbatim
  pass-through proven by test (an md5 verifier is returned, refused downstream).
Tests verifiers_test.go: superuser-path (AsUser postgres, pg_authid + quoted
literals in argv), verbatim non-SCRAM pass-through, request de-dup, missing-role
NotFound, empty-verifier Validation, invalid-name rejected before any psql runs,
requires-runner, empty-request. Reviewed (feature-dev:code-reviewer): no
critical; applied the one Important finding — dropped a dead `ORDER BY rolname`
(rows are parsed into a map and re-sorted in Go, so the SQL ordering was unused).
Remaining for the enable flow: apt package install + service lifecycle
(`systemctl enable --now` + SIGHUP reload/restart-fallback only when changed),
then the UI toggle. Go gate green; web untouched.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — auth_file atomic installer (slice 4b)
Carved the next install slice off the enable-flow item: the atomic `userlist.txt`
installer, mirroring how the `.ini` installer (slice 3) split the atomic write out
from service lifecycle. Added `Manager.EnsureUserlist(ctx, []UserlistEntry)
(changed bool, err)` in `internal/pgbouncer/install.go`:
- Renders via the (already-built) `RenderUserlist` then installs atomically —
  temp+rename, 0640 owned by the pgbouncer user (the auth_file holds
  secret-adjacent SCRAM verifiers, so never world-readable), O_NOFOLLOW symlink
  guard → `CodeConflict`, deterministic no-op when content is byte-identical so
  the enable flow can skip a needless reload. Logs only the user COUNT.
- KEY DECISION: NO foreign-file marker guard (unlike the .ini). The userlist.txt
  format is pure `"user" "verifier"` lines and cannot carry an in-file marker
  (confirmed against the `sm` source). Ownership is gated upstream by
  EnsureConfig's marker guard on pgbouncer.ini — the enable flow only reaches the
  auth_file once indiepg owns the .ini, and the auth_file is a fully
  indiepg-derived satellite of that managed config. RenderUserlist's
  SCRAM-only/injection validation still hard-stops any bad entry before a write.
- DRYed the shared atomic-install logic out of `writeConfigFile` into a
  package-level `atomicInstall(path, data, mode)` used by both the .ini and the
  auth_file (EnsureConfig behavior unchanged — pure mechanical extraction).
Tests `userlist_install_test.go`: writes-0640 (exact RenderUserlist bytes),
idempotent-no-rewrite (mtime-stable; a two-user set re-submitted in reverse input
order proves the sort-stable no-op at the install level), rewrites-on-change,
refuses-symlink (target untouched), rejects-non-SCRAM + rejects-empty before any
write, requires-runner, default-path.
Reviewed (feature-dev:code-reviewer): no blocking findings; applied the one
Important test fix (idempotency test now actually reorders a two-user set rather
than re-submitting a single entry). Gates: gofmt clean, go vet, go test ./...,
CGO_ENABLED=0 build all green; no web/ touched.
REMAINING for the enable flow: query pg_authid for the app role(s)' verifiers,
package install (apt) + service lifecycle.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — SCRAM userlist render (slice 4a)
Carved the substantive sub-piece the backlog flagged out of the enable-flow
item: the pure SCRAM `userlist.txt` render, mirroring how every prior pgbouncer
slice was built pure-first (pool math → ini render → ini installer → this).
Added `internal/pgbouncer/userlist.go`:
- `RenderUserlist([]UserlistEntry) (string, error)` builds the auth_file text —
  `"username" "verifier"` lines, the `sm`/pgbouncer format. Pure render, no file
  I/O (atomic install + chown lands in the install/enable slice, like the .ini).
- **Security-strict** (the auth_file is a boundary): at least one entry required
  (an empty auth_file silently locks every app out of the pooler); EVERY verifier
  must be `SCRAM-SHA-256$…` and drawn only from the verifier's own alphabet
  (`[A-Za-z0-9+/=:$-]`) — md5/plaintext are **refused, never downgraded**, and the
  charset check doubles as the injection guard for the quoted token; usernames
  containing a quote/whitespace/control char/Unicode line separator are rejected
  (not escaped) so a crafted name can't inject a second auth entry; duplicate
  usernames refused (pgbouncer silently honours only the first → ambiguous).
- **Deterministic** (sorted by username) so an unchanged user set renders
  byte-identical and the future enable flow can skip a needless reload.

Tests (`userlist_test.go`): line-format, sorted-stable/order-independent,
empty-refused, non-SCRAM-refused (md5/plaintext/wrong-case/wrong-algo),
verifier-injection (quote/newline/space/`#`), username-injection
(quote/space/tab/newline/U+2028/U+2029/NUL/empty), duplicate-refused,
no-write-on-error. Reviewed (feature-dev:code-reviewer): two test-hardening
findings applied — explicit U+2028/U+2029 separator cases (was an opaque raw
byte) and a `CodeValidation` assertion on the empty-slice path. Gates: gofmt
clean, go vet, go test ./..., CGO_ENABLED=0 build all green; no web/ touched.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — config installer (slice 3)
Third PgBouncer slice, mirroring how the pgBackRest installer split the atomic
config write out from `stanza-create`. Added `internal/pgbouncer/install.go`:
- `Manager` (Runner/Logger/ConfDir, ConfDir defaults to `/etc/pgbouncer`) with
  `EnsureConfig(ConfigParams) (changed bool, err)` — renders the pgbouncer.ini
  (slice 2's `RenderConfig`) and installs it **atomically** (temp + rename),
  owner `pgbouncer` **0640** (DEFAULTS.md; the .ini holds no secret — SCRAM
  verifiers live in the separate auth_file), returning whether it changed.
- **Safety (mirrors pgBackRest installer):** marker-guarded — a config lacking
  indiepg's first-line marker (hand-written or distro pgbouncer.ini) is surfaced
  as `CodeConflict`, never clobbered; deterministic render → byte-compare no-op
  when unchanged (so the future enable flow can skip a needless reload);
  `O_NOFOLLOW` path-hijack guard refuses a symlink at the config path as a clear
  conflict; chown to the pgbouncer user is root-fatal / non-root-best-effort.
- **No package-install / service-touch yet** — that's the enable slice (apt +
  SCRAM userlist + `systemctl enable --now` + SIGHUP reload/restart-fallback +
  verify running). Split slice (iii) in the backlog accordingly.

Tests (`install_test.go`): writes-marked-0640, idempotent-no-rewrite
(mtime-stable), rewrites-on-change, refuses-foreign + marker-not-first-line,
refuses-symlink (target untouched), rejects-injection-before-any-write,
requires-runner, default-confdir. Reviewed (feature-dev:code-reviewer): one
finding applied — the symlink case now returns a clear `CodeConflict` instead of
an opaque `CodeInternal`, honoring the "errors loudly" contract on the guard.
Gates: gofmt clean, go vet, go test ./..., CGO_ENABLED=0 build all green; no
web/ touched.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — pgbouncer.ini render (slice 2)
Second PgBouncer slice, mirroring how `RecommendTuning` was built (pure render
before any install). Added `internal/pgbouncer/config.go`:
- `RenderConfig(ConfigParams)` renders the full pgbouncer.ini from a
  `PoolRecommendation` (slice 1) plus host bits (pg/listen ports, auth_file,
  pidfile, admin user), every field safe-defaulted so a zero-valued
  `ConfigParams` (only `Pool` set) still renders a valid config.
- **Security pins (non-configurable):** `listen_addr` and the `[databases]`
  upstream host are both hardcoded `127.0.0.1` — the pooler is loopback-only and
  never widened. `[databases] * = host=127.0.0.1 port=<pg>` catch-all. The
  hardened constants (auth_type scram-sha-256, pool_mode transaction,
  server_reset_query DISCARD ALL) come straight from pool.go — never weakened.
- **Deterministic** (fixed line order) so an unchanged config renders
  byte-identical and the future installer can skip a needless rewrite + reload.
- **Injection-hardened:** every interpolated value is validated against control
  chars, U+2028/U+2029 line separators, AND `#`/`;` (pgbouncer.ini comment
  starters — an embedded `;` would silently truncate a value to a comment).
  Bad/colliding ports rejected; a zero/negative pool refused (a `0`
  server_idle_timeout disables idle reclamation → server slots leak forever).
- `ConfigMarker` first-line ownership guard (mirrors pgBackRest) + `HasManagedMarker`
  so the install slice never clobbers an operator/distro-written file.
- **No install/file-write yet** — atomic write as owner `pgbouncer` perms 0640
  lands in the next (install/enable) slice.
- Tests (`config_test.go`): default structure + all hardened settings, custom
  params interpolated, never-widens/weakens (no 0.0.0.0/`::`/trust/any/plain),
  determinism, injection rejection (newline/CR/U+2028/`;`/`#`), port validation
  + collision, zero/negative pool rejection, marker recognition incl. mid-file
  false-positive.
Reviewed (feature-dev:code-reviewer): applied the one critical (validate the
`Pool` numbers) + all 3 important findings (reject `#`/`;`, add `::` to the
never-widen test, add a zero-pool rejection test). Gates: gofmt clean, go vet,
go test ./..., CGO_ENABLED=0 build — all green; no `web/` touched.

## 2026-06-24 · band 3 (usability) · PgBouncer pooler — pure pool-sizing math (slice 1) + drop the dead provision-flow item
Two band-3 items remained. First, **dropped** "(a) provision flow shows computed
best-defaults up front": provisioning is CLI-driven (`indiepg install`) and the
panel has NO provision route (App.tsx), so the item has no surface — its intent
is already served read-only by the iter-31 "Database tuning (host-sized)" card,
which shows the computed defaults per profile labeled by effect.

Then started the **largest remaining band-3 item — the opt-in PgBouncer pooler**
— with its smallest atomic, fully-testable slice, mirroring exactly how
`RecommendTuning` was built (pure math first, then apply, then UI over several
iterations). Added `internal/pgbouncer/pool.go`:
- `RecommendPool(pgMaxConnections, profile)` faithfully ports the `sm` CLI pool
  math (verified against /primary01/git/server-management/.../pgbouncer.py and
  DEFAULTS.md): available = max_conn − 5 reserved-for-admin; default_pool_size =
  int(available × util) floored at 20 (util oltp .80 / mixed .70 / olap .60);
  min = default/4 (floor 5); reserve = default/5 (floor 5); max_client_conn =
  default × multiplex (20/10/5); server_idle_timeout 300 (oltp/mixed) / 600 (olap).
- Pure & total: reuses `pg.WorkloadProfile` (unknown → Mixed, no silent mis-size);
  max_conn < 1 clamped; panic-free on degenerate input. Touches no host/PG.
- Fixed safe defaults are hardcoded constants — `auth_type=scram-sha-256`,
  `pool_mode=transaction`, `server_reset_query=DISCARD ALL` (never trust/plain,
  never weakened) — exposed via `SettingsMap()` for a future read-only preview.
- Tests (`pool_test.go`): hand-computed table (mixed/oltp/olap + floor box),
  invariants across small→large boxes (floors fire, sub-pools ≥5, max_client =
  default×multiplex, pool never exceeds PG capacity once above the floor, idle
  per profile), degenerate/negative + unknown-profile, and SettingsMap asserting
  the hardened constants + stringified numbers.

Why: an indie hacker whose app exhausts Postgres' connection slots gets "stuck"
(apps can't connect). Transaction pooling is the safe, opt-in fix from DEFAULTS.md.
This slice lays the trusted sizing foundation with zero runtime risk; config
render / install-enable / UI toggle are tracked as the next sub-items.

Reviewed (feature-dev:code-reviewer): no critical findings; applied both
important ones — corrected DEFAULTS.md `round`→`int(...)` truncation to match the
`sm` source, and widened the invariant sweep ({1,5,10,20,...}) so the floor paths
are self-covered rather than relying on RecommendTuning's max_conn≥30 coupling.
Gates: gofmt clean, go vet, `go test ./...`, CGO_ENABLED=0 build all green; no
web/ touched.

## 2026-06-24 · band 3 (usability) · destructive-confirm audit + lock it with tests
First band-3 item. Audited every confirmation site against "states exactly what
will happen and what is irreversible": Backups (run full/incr, deep restore test,
restore-overwrite), Alerts (delete rule), RolesDatabases (drop database/user),
Migrate (single-DB + whole-cluster overwrite). All already carry plain-language
consequence text, and the truly destructive paths (drop db/user, restore,
migrate-overwrite) gate behind typing the exact object name. **The copy was good
but completely untested** — a refactor could silently weaken the typed-name gate
that stands between the operator and irreversible data loss.

So this iteration makes the audit durable: added `web/src/components/
ConfirmDialog.test.tsx` (8 tests, the first coverage for these primitives):
- `ConfirmDialog`: renders title/message, confirm+cancel handlers fire, danger
  tone applies `btn-danger`, busy disables both buttons and shows "Working…"
  (and a disabled confirm is a no-op), closed renders nothing.
- `TypedConfirmDialog`: states what is destroyed + "cannot be undone", surfaces
  the caller's consequence inside a `.callout-danger`, keeps the delete button
  **disabled until the exact name is typed** (near-miss stays disabled + flags
  the input invalid; clicking while disabled never calls `onConfirm`), fires
  `onConfirm(typed)` only on an exact match, and stays gated while `busy` even
  with a matching name.
No component changes — pure test hardening, lowest-risk way to bank the audit.
Reviewed (feature-dev:code-reviewer): two robustness findings — assert the
danger callout by container class (not the accidental text node), and assert the
input is "not invalid" rather than the exact `aria-invalid="false"` serialisation
so a future spec-correct change stays green. Both applied. Gates: gofmt clean,
go build OK (no Go touched), web typecheck + build (deterministic, no dist diff) +
55 web tests all green.

## 2026-06-24 · band 2.5 (resource & config safety) · APPLY host-sized tuning — UI surface
Closed the **last open band-2.5 item**: the operator can now SEE how Postgres is
sized to their box. Provisioning is CLI-driven (`indiepg install`), so the
`recommended_tuning`/`tuning` fields the Provision result carries were only ever
logged — never surfaced. Added a **read-only** surface:
- Backend `Manager.CurrentTuning(ctx)` (`internal/pg/tuning_status.go`) returns
  detected RAM/CPU, the live applied settings (read from `pg_settings` via the
  existing `readTunableSettings`, normalised to whole MB), and the host-sized
  recommendation for each workload profile (oltp/mixed/olap, via the pure
  `RecommendTuning` through the `hostTuning` seam). **Best-effort**: if Postgres
  is unreachable it returns `Applied=nil` (no error) so the recommendations still
  load. `GET /api/tuning` (`handleGetTuning`); never mutates Postgres.
- Frontend "Database tuning (host-sized)" card on Settings
  (`web/src/views/DatabaseTuning.tsx`): shows the box's RAM/CPU + active profile,
  the applied settings (each with a plain-English meaning), and a profile selector
  that **previews** what each profile would size the box to, every profile labeled
  by its effect. A non-default selection clearly says it's a preview that changes
  nothing and that a real switch needs a brief Postgres restart — so it's an
  install/provision-time action, not a button here (security tie-break: no
  restart-trigger surface in the panel).
Why read-only/preview, not apply: a profile switch resizes
shared_buffers/max_connections and must funnel through ApplyTuning's
`restartWithRollback`; exposing that as a one-click panel button is a separate,
riskier feature. This iteration removes the most risk (operator can finally see +
understand their tuning) with zero new mutation surface. Tests:
`TestCurrentTuning_*` (applied + all profiles; degrades when PG unreachable) and
`DatabaseTuning.test.tsx` (mbLabel units; host/active render; applied values;
null-applied calm warn + recommendations still shown; profile preview describes
effect + says nothing changes). All gates green; reviewed (feature-dev:code-reviewer:
no blocking issues; tidied one redundant detection call). **Band 2.5 COMPLETE →
next is band 3 (usability).**

## 2026-06-24 · band 2.5 (resource & config safety) · APPLY host-sized tuning — real-PG integration test
Closed the first of the two ApplyTuning apply-follow-up items: proved
`Manager.ApplyTuning` works against REAL Postgres, the one thing the fake-Runner
unit tests cannot cover. Added `internal/pg/tuning_apply_integration_test.go`
(`//go:build integration`, env-gated on `INDIEPG_PG_BINDIR`; never runs in the
untagged loop gate). It stands up a throwaway PG14 cluster on a private socket and
asserts: (1) a sane recommendation lands in `pg_settings` (memory normalised to
bytes via the unit column, max_connections exact) — proving the restart-requiring
shared_buffers/max_connections actually took effect via a real restart, not just a
reload; (2) a second apply of the same rec is a no-op (changed=false, nothing
re-written, no restart); (3) **self-healing**: a restart-requiring value the
postmaster refuses to boot with is rolled back via `restartWithRollback` to
last-known-good, with PG still UP and a `CodeSafety` error surfaced.

KEY DECISIONS / GOTCHAS (for the next iteration):
- To force a DETERMINISTIC, instant startup failure I set `max_connections = 1`
  (in-range so ALTER SYSTEM accepts it, but the postmaster refuses: reserved(3) +
  max_wal_senders(10) >= max_connections). My first attempt used an oversized
  shared_buffers (~16TB) — it HUNG, because Linux memory overcommit lets the huge
  anonymous mmap succeed/stall instead of failing fast. Avoid memory-size-based
  boot failures in tests; use a cross-GUC constraint instead.
- `pg_ctl start/restart` MUST be given `-l <logfile>`. Without it the daemonized
  postmaster inherits the runner's captured stdout pipe and never closes it, so
  the OSRunner's `cmd.Run` blocks FOREVER (both early test runs timed out at
  600s/180s on exactly this). `-l` redirects server output to a file so the pipe
  closes and the command returns.
- The throwaway cluster has no systemd unit, so a `tuningTestRunner` wraps the
  real OSRunner: strips `AsUser="postgres"` (run as current user), injects
  PGHOST/PGPORT/PGUSER so psql hits the throwaway socket, and translates the exact
  `systemctl restart postgresql` into `pg_ctl ... restart` of the test cluster —
  so the real ApplyTuning→restartWithRollback path runs unmodified.
Proven green against real PG14 (~1.5s); normal gate (gofmt/vet/test/build) green;
web untouched. Reviewed (feature-dev:code-reviewer): tightened the systemctl
intercept to match the exact 2-arg invocation + documented the as-current-user
divergence for the auto.conf cat/tee. REMAINING band-2.5 item: the ApplyTuning UI
surface (show applied defaults + workload-profile override labeled by effect).

## 2026-06-24 · band 2.5 (resource & config safety) · APPLY host-sized tuning at provision
Made the host-sized recommendation actually take effect, completing the band-2.5
arc (iter 28 only *surfaced* it because applying needed the self-healing restart
primitive, which iter 26 built). Added `Manager.ApplyTuning(ctx, rec)`
(`internal/pg/tuning_apply.go`): persists the five core settings via ALTER SYSTEM,
idempotent by comparing against `pg_settings` normalised to bytes through the unit
column (`settingUnitBytes`). Restart-requiring settings (shared_buffers,
max_connections) funnel through `restartWithRollback` — snapshot auto.conf before
write, roll back to last-known-good on a failed restart so PG is never left down;
reloadable-only changes use pg_reload_conf. Integer GUCs written unquoted, memory
settings quoted. Wired into `Provision` (replacing "surfaced, not applied"): applies
Mixed-profile defaults and gracefully tolerates a CodeSafety rollback (warns +
records `tuning: rejected`, doesn't fail the whole provision over an oversized
default). Why: best-defaults-first — a left-alone box now runs sized to its RAM/CPU,
and a bad value can't strand Postgres down. Added a `detectTuning` test seam so
provision tests pin RAM/CPU deterministically. Fully unit-tested (no-op / reload-only
/ restart / restart+reload / rollback / missing-setting / unit math / quoting);
provision happy-path + idempotent tests updated to native pg_settings units. Reviewed
(feature-dev:code-reviewer): fixed integer-GUC quoting + the synthetic test-unit gap.
Remaining (split into two new backlog items): real-PG integration proof + UI surface.

## 2026-06-24 · band 2.5 (resource & config safety) · host-sized tuning: pure sizing function
Closed the last open band-2.5 item. Added `RecommendTuning(memoryMB, cpuCount,
profile)` in `internal/pg/tuning.go` — a pure, deterministic function that sizes
the five core Postgres settings (shared_buffers, effective_cache_size, work_mem,
maintenance_work_mem, max_connections) from detected RAM/CPU, faithfully porting
the `sm` CLI tuning math (DEFAULTS.md / tuning.py `_calculate_max_connections`).
Workload profile is selectable (`WorkloadProfile` + `ParseWorkloadProfile`, empty
→ Mixed default, unknown → ValidationError so a typo can't silently mis-size).
Per-profile shared_buffers pct (oltp .25 / mixed .30 / olap .40), work_mem
(oltp 64 / mixed 128 / olap clamp(RAM/32,128,1024)), maintenance_work_mem
(olap min(RAM/4,4096) / else min(RAM/8,2048)), and the RAM-derived clamped
max_connections all match the source. Total + panic-free on degenerate inputs
(neg/zero RAM → 0, cpu<1 → 1, unknown profile → Mixed fallback).

Why it's not dead code: wired into `Provision` (`detectHostTuning` via the
existing `readMemInfo` + `runtime.NumCPU`, 4GB fallback like `sm`) to SURFACE the
host-sized best-default in the result (`recommended_tuning` Data map + a step
note) — best-defaults transparency for the operator. Deliberately INFORMATIONAL
ONLY, labeled "not yet applied": computing it never touches Postgres so it can't
fail provisioning. APPLYING the restart-requiring settings (shared_buffers,
max_connections) is the separately-scoped next step and MUST funnel through the
`restartWithRollback` self-healing primitive (iter 26). Acceptance met: table
test of the sizing function against sample RAM values (1GB–128GB × all three
profiles) with hand-computed expected values, plus profile-ordering, floor/ceil
clamp, degenerate-input, ParseWorkloadProfile, and SettingsMap tests. Go gate
green; web untouched. Reviewed (feature-dev:code-reviewer): math faithful, no
blocking issues; addressed the one cosmetic comment-clarity nit.

## 2026-06-24 · band 2.5 (resource & config safety) · connection-saturation alert: add a CRITICAL escalation tier
Closed the last band-2.5 alert item. The WARNING tier `connections-near-max`
(`pg.connections_percent` >= 85%, For 2m) already shipped — the prior iteration's
notes correctly suspected the item was "likely already satisfied." The audit found
it satisfied at the warning level but missing an escalation: at ~max_connections
Postgres refuses new clients ("too many clients already") — a hard outage where
apps can no longer connect and the panel itself can be locked out once only
`superuser_reserved_connections` remain. Every other outage-class resource
(disk, pg-down) pages CRITICAL; connections only had the one warning.

Added the critical tier `connections-critical` (`pg.connections_percent` >= 95%,
For 1m, Cooldown 15m, Severity Critical) — a higher threshold but a shorter For
and the 15m cooldown of the other outage-class rules, with 95% leaving a sliver
of runway to kill runaway sessions before exhaustion. This mirrors the
disk-headroom-low(80% warn) → disk-almost-full(90% crit) two-tier escalation
established for disk. The security/durability tie-break wins: escalate louder and
sooner before a hard outage.

Rule-only, like the disk early-warning tier: the metric is already real
(`internal/pg/sampler.go` samples `count(*) FROM pg_stat_activity` /
`current_setting('max_connections')` → snapshot → `metricValue`'s
`pg.connections_percent`), and `seedDefaultAlertRules` is ID-based so the new
rule auto-seeds on upgrade without clobbering an operator's edits. Updated the
`TestDefaultRules` want-list and added `TestConnectionSaturationTiers` proving
tier ordering (warning strictly lower severity + threshold than critical, same
metric) AND non-vacuous firing of BOTH tiers — the warning fires at 90% (past its
2m For) while the critical stays OK below 95%, and the critical fires at 97% past
its 1m For. Also fixed the now-stale `seedDefaultAlertRules` doc comment to name
the new tier. Go gate green (gofmt/vet/test/build, sandbox-disabled per snap);
web untouched. Reviewed (feature-dev:code-reviewer): no blocking findings.
Remaining band-2.5: host-sized tuning at provision (pure sizing fn, no PG needed).

## 2026-06-24 · band 2.5 (resource & config safety) · self-healing config: a config change that stops Postgres auto-rolls-back to last-known-good
The marquee band-2.5 item, named in the north star: "a bad change (even a user
override) that stops Postgres must auto-rollback to last-known-good." Built the
core primitive in `internal/pg/safeconfig.go`: `snapshotAutoConf` captures
`<data_directory>/postgresql.auto.conf` (the file ALTER SYSTEM writes) BEFORE a
change and fails closed if it can't read it — a restart we can't undo must never
proceed. `restartWithRollback` restarts Postgres to activate the change; the
postgresql systemd unit is synchronous, so a non-zero `systemctl restart` exit is
the authoritative "did not come back up" signal. On that signal it restores the
snapshot via `tee` as the postgres OS user (keeps owner + 0600 mode; a file
restore works even while PG is down, unlike `ALTER SYSTEM RESET`) and restarts
again, so the cluster is never left down — returning a typed `CodeSafety` error
("rolled back to last-known-good; Postgres is running"). If the rollback restart
ALSO fails it returns `CodeInternal` ("Postgres is down") with a hint that
auto.conf still holds the rejected settings.

Made it load-bearing immediately rather than dead code: routed the one real
config-change-then-restart path — `EnsureArchiving` (archive_mode/wal_level) —
through it. It now snapshots before its ALTER SYSTEM loop and restarts via
`restartWithRollback`. Fixed the caller (`ensureBackupConfigured`) to stop
re-wrapping the typed error as a generic `CodeInternal`, which had buried the
`CodeSafety`/`CodeInternal` distinction the SPA branches on.

Tests (`safeconfig_test.go` + `archive_test.go`): the acceptance case — a bad
setting (first restart fails) → exact last-known-good content restored via `tee` →
second restart → `CodeSafety`; rollback-restart-also-fails → `CodeInternal`
"down"; clean restart never rewrites auto.conf; snapshot fails closed; and the
load-bearing ordering invariant — a snapshot failure aborts EnsureArchiving BEFORE
any ALTER SYSTEM write. Go gate green (gofmt/vet/test/build); web untouched.
Reviewed (feature-dev:code-reviewer): addressed all three findings (caller error
code preserved, rollback-failed hint, ordering test). NOTE: there is no
operator-facing override-apply UI yet (next band-2.5 items: host-sized tuning at
provision, connection-saturation alert) — this primitive is what those and any
future override flow must funnel their restart through.

## 2026-06-24 · band 2.5 (resource & config safety) · early-warning disk headroom alert (warn well before the volume fills)
First band-2.5 item. The panel already shipped a CRITICAL `disk-almost-full` rule
at 90% on `host.disk_percent` (a statfs of the Postgres data/WAL volume), but 90%
is the emergency, not an early warning — by then a slow fill is nearly out of
runway and can stop Postgres. Added a second, lower tier: `disk-headroom-low`
(WARNING, `>= 80%`, `For: 5m`, `Cooldown: 1h`) to `alert.DefaultRules()`, mirroring
the existing two-tier `backup-stale` + `backup-failed` pattern. Lower threshold +
calmer cadence than the critical tier: the 5-minute `For` window ignores a transient
bump (e.g. a deep restore-test's scratch copy landing on the same volume), and the
1h cooldown re-reminds without a firehose; above 90% both tiers are independently
active (intended escalation). No new metric or wiring needed — `MetricDiskPercent`
and the seed/evaluate loop already exist; the seed in `background.go` inserts only
missing rule IDs so an upgrading panel gets the new rule without clobbering operator
edits. Tests: updated `TestDefaultRules` want-list, plus a new
`TestDiskHeadroomEarlyWarningTier` proving the tier ordering (warning lower-severity
and lower-threshold than critical) AND non-vacuous firing — at 85% disk the warning
fires after its For window while the critical stays OK. Proven non-vacuous (bumping
the warning threshold to 95% fails the ordering assertion). Go gate green
(gofmt/vet/test/build, sandbox-disabled per snap); web/ untouched. Reviewed
(feature-dev:code-reviewer): no blocking issues; fixed one misleading inline comment
it flagged (said "both tiers fire once disk crosses 90%" — corrected to reflect the
80% warning threshold). Confirmed no other test asserts a hardcoded default-rule count.

## 2026-06-24 · band 2 (stability) · polling views: surface a failed background refresh instead of silently freezing
Closed part of the last open band-2 item "audit every web API call for explicit
error + loading + empty states (no silent failures)." Audited all data-fetch surfaces:
`useAsync`/`usePolling` consumers (Dashboard, RolesDatabases, Backups, Alerts, Settings,
Migrate) and local-state forms all had loading + error + empty states EXCEPT one real
gap in the `usePolling` consumers. `usePolling` retains the last good `data` and only
sets `error` on a failed poll, but the three consumers gated their error UI on `!data`
(`error && !data ? <ErrorNotice>`). So a poll that started FAILING after the first
successful load was silently swallowed — the Dashboard kept showing a frozen "● Healthy"
badge and "refreshes every 5s" while the box was actually unreachable, and the two
Migrate progress views (`DirectJobProgress`, `SessionProgress`) kept a spinner/stepper
that looked like progress. For a Postgres admin panel that is the "never be confused"
risk. Added a reusable `StaleBanner` (warn tone, `role="alert"`, keeps the cached data
visible but says "Live updates paused — the latest refresh failed: <message>" + hint),
and surfaced it whenever `error` is present alongside cached data: Dashboard always,
the two Migrate pollers only while `!terminal` (a post-completion poll blip must not
alarm). Tests: 2 unit tests for `StaleBanner` (warn tone, alert role, message, hint)
and a Dashboard view test (mocks `usePolling`) proving the banner shows on error+data,
hides on a clean poll, and that the first-load failure still renders the full
`ErrorNotice` (no banner). Proven non-vacuous (removing the Dashboard wiring fails the
banner test). Web gate green (typecheck/build/42 tests); Go gates untouched + green.
Reviewed (feature-dev:code-reviewer): caught that `SessionProgress` was a third polling
surface with the same defect — fixed it before commit (no other blocking findings).

## 2026-06-24 · band 2 (stability) · provisioning is idempotent — prove a re-run is a safe no-op + report "already done"
Closed the band-2 item "re-running setup on an already-provisioned box is safe and
reports already done." The provisioning flow was already idempotent (apt install /
`systemctl enable --now` are no-ops when done; `provisionSQL` guards every statement
with DO/IF NOT EXISTS), but two gaps remained: nothing PROVED a second run is a no-op,
and `Provision` always returned "Postgres provisioned" — giving an operator re-running
`indiepg install` no signal it was already set up (north star: never be confused).
The only step that mutates on-disk state is `EnsureSocketAuth` (pg_hba.conf), and it
already returns a `changed` bool. Surfaced that honestly: `Provision` now adds
`socket_auth: "configured"|"already-present"` to its result data, and install.go logs
it. Deliberately scoped the claim to the one step we can truthfully detect — the
message does NOT assert the whole provision was a no-op (apt could have upgraded a
package). Added `TestProvision_SecondRunIsIdempotentNoOp`: two `Provision` calls share
one FakeRunner + one persistent real pg_hba.conf; asserts the second run succeeds,
the managed block appears EXACTLY ONCE (a duplicate would corrupt auth), no extra
`pg_reload_conf` is issued, and `socket_auth` flips to `already-present`. Proven
non-vacuous (breaking `injectHBARules` idempotency fails the test). Reviewed
(feature-dev:code-reviewer): no blocking issues — confirmed the test is non-vacuous
and the "already-present" claim is honest. Band-2 remaining: audit every web API call
for explicit error+loading+empty states.

## 2026-06-24 · band 2 (stability) · prove read-pool statement_timeout is enforced by Postgres
First band-2 item. The query box's runaway-query guard has two halves: auto-LIMIT
(already thoroughly unit-tested in `internal/pg/guard`) and `statement_timeout` on
the read pool. The timeout was only proven at the DSN-*string* level (buildDSN's
unit test asserts `statement_timeout=30000` is present) — nothing proved Postgres
actually honors it at runtime on the pool the query box uses. If the param were
ever silently dropped (e.g. pgx ignoring an unknown key), a runaway SELECT could
pin a pooled connection forever. Added `TestReadOnlyPool_StatementTimeoutEnforced`
(`internal/pg/readonly_integration_test.go`, `//go:build integration`, env-gated on
`INDIEPG_TEST_SOCKET` like its sibling — never runs in the untagged loop gate). It
sets a 250ms read-pool cap with a generous 30s client context (so the cancellation
we observe is Postgres, not the Go deadline), then: (1) the real query-box path —
`ExecuteRead("SELECT pg_sleep(5)")` is cancelled with a "statement timeout" message
in <5s; (2) a raw read-pool conn proves the wire-level SQLSTATE is `57014`
(query_canceled), i.e. the cap is enforced at the DB level on that exact pool; and
(3) a positive control — `pg_sleep(0.5)` on the privileged pool COMPLETES, proving
the cap is scoped to the read pool (the priv pool deliberately carries no forced
timeout so guided maintenance like CREATE INDEX is never killed). Proven green
against a throwaway PG14 cluster provisioned with the indiepg roles (both
integration tests pass; my test runs in ~1.0s, not 5s+, confirming the cap fired).
Reviewed (feature-dev:code-reviewer): no blocking findings — confirmed the
wrong-reason guard (client ctx vs server timeout) is real, the 57014 assertion hits
the right path via the raw conn, and the positive control is correctly scoped.

## 2026-06-24 · band 1.5 (data durability) · restore-test DEEP — end-to-end integration test
Committed the one remaining band-1.5 item: the end-to-end integration proof for
`Manager.RestoreTestDeep` (`internal/backup/restore_deep_integration_test.go`).
The fake-Runner unit tests can prove orchestration but never that a real restore
boots and counts; this does. It's `//go:build integration` + env-gated (skips
unless `INDIEPG_PG_BINDIR` is set and `pgbackrest` is on PATH), so it NEVER runs
in the loop's untagged `go test ./...` gate — by design. It stands up a throwaway
PG cluster with a local pgBackRest stanza, takes a real full backup of 1234
seeded+ANALYZEd rows through `Manager.Backup`, then asserts `RestoreTestDeep`
restores into a scratch dir, boots it (full WAL replay) on a private socket,
records a `success` row with `verified_rows > 0`, and tears the scratch dir down.
A `stripUserRunner` drops the production `AsUser="postgres"` and injects
`PGBACKREST_CONFIG` so every binary runs as the current user — no sudo. Verified
it compiles under `-tags integration` (vet clean) and leaves the normal gate
untouched (gofmt/vet/test/build all green). Reviewed (feature-dev:code-reviewer):
its one "critical" finding (port 5499 collision) was rejected as factually wrong —
both clusters use unix sockets in distinct dirs with empty `listen_addresses`, so
the shared port is only a socket-filename suffix in non-overlapping directories
(restore_deep.go's own comment designs against exactly this); its second finding
was a self-described doc note already covered by the test's ANALYZE comment.
**This was the last load-bearing band-1.5 item → band 1.5 (data durability) is
complete. Next iteration starts band 2 (stability).** (The unregistered `digest`
job is deferred — no digest builder exists, not load-bearing for "never lose data".)

## 2026-06-24 · band 1.5 (data durability) · restore-test DEEP — UI opt-in button
The deep-restore proof (`Manager.RestoreTestDeep`, `POST /backups/restore-test?deep=true`)
had backend + scheduler but no way to trigger it from the panel — only the cheap
verify had a button. Closed that: added a clearly-labeled "Deep restore test"
button on the Backups page next to "Test a restore", gated behind a
`DeepRestoreTestConfirm` dialog that states up front exactly what it does and its
costs before it runs (actually restores the latest backup into a throwaway copy,
boots it, counts rows; runs longer; needs free disk ≈ DB size; live database
never touched; scratch copy deleted; refuses rather than fill the disk) — the
"say what will happen before it happens" invariant. The default "Test a restore"
stays the cheap read-only verify. API client `runRestoreTest({ deep })` appends
`?deep=true`; the existing `verified_rows > 0` "rows restored and verified"
result branch already renders the outcome. New vitest/RTL tests
(`DeepRestoreTestConfirm`): closed renders nothing, copy asserts each material
claim (does-what + both costs + safety), confirm/cancel callbacks, busy disables.
Full web gate green (typecheck/build/37 tests) + full Go gate green. Reviewed
(feature-dev:code-reviewer): no blocking issues. Band 1.5 now has only the
env-gated DEEP end-to-end integration test left (won't run in the loop gate).

## 2026-06-24 · band 1.5 (data durability) · restore-test DEEP proof — non-destructive scratch restore + boot + real row count
The cheap `pgbackrest verify` (shipped earlier) checksums the repo but never
restores, so it cannot catch recovery-time failures: a WAL gap that only
manifests at replay, a corrupt `pg_control`, an unbootable catalog. Added
`Manager.RestoreTestDeep` (internal/backup/restore_deep.go) — the gold-standard
durability drill: it restores the newest backup into a fresh `os.MkdirTemp`
scratch dir (`--pg1-path` override, NEVER the live data dir), boots it with
`pg_ctl` for a full WAL replay on a PRIVATE unix socket (`listen_addresses=`
empty so no TCP, `unix_socket_directories=<scratch>`, `archive_mode=off` in BOTH
the restore config and the boot opts so the scratch cluster can never push WAL
into the live repo), counts user-table rows to prove the heap is queryable,
records `verified_rows`, then ALWAYS tears down (deferred stop + RemoveAll on a
detached ctx). Safety guards: foreign-owner HARD STOP, no-backup → NotFound, and
a disk-headroom precheck (`free >= dbSize × 1.25`) that refuses with CodeSafety
and issues NO restore when the volume is tight — a restore ≈ DB size could fill
the box and threaten the live dir, so the test must never run without margin.
Wired behind an explicit opt-in (`POST /backups/restore-test?deep=true`); the
default stays the always-safe cheap verify. Unit-tested via the fake Runner
(exact-scratch-dir isolation, never runs `backup`, isolated boot-failure records
a fail row while cleanup still stops the cluster, headroom-refusal issues no
restore, foreign-owner/no-backup/invalid-stanza, plus the real `defaultDiskFree`
/`defaultResolvePGBin` OS seams). Reviewed (feature-dev:code-reviewer): fixed two
vacuous test assertions, quoted the socket dir for space-safety, and MkdirAll the
scratch root so the headroom statfs is reliable. The real end-to-end integration
test (needs a populated pgBackRest repo) and the UI opt-in button are split out
as their own band-1.5 backlog items.

## 2026-06-24 · band 1.5 (data durability) · wire the restore-test job into the runtime scheduler
Restore verification (the verify-based `Manager.RestoreTest`, shipped last
iteration) only ever ran when an operator clicked "Test a restore" — the
`restore-test` cron job was never registered in `startBackgroundJobs`, even
though `cfg.Schedules.RestoreTest` (default `0 5 * * 0`, 05:00 Sundays, after the
weekly full) already existed. So a left-alone box's "have my backups been proven
recoverable?" banner could sit at "never" forever while the repo silently rotted
(a bit-flip, a truncated WAL). Registered the job so backups are proven
recoverable on a cadence with no manual click — closing the loop on "backups
proven restorable". Refactored `registerBackupJob` to share a generic
`registerJob(name, spec, fn, emptyWarn)` helper (DRY; same error-on-bad-spec /
warn-on-empty-spec behavior). Tests: restore-test is registered alongside the
backup + telemetry jobs; an empty schedule is the operator's opt-out (job not
registered). DESIGN/SAFETY: unlike the backup jobs, `scheduledRestoreTest`
deliberately does NOT call `ensureBackupConfigured` — that runs `stanza-create`
(exclusive stanza lock) and may restart Postgres to enable archiving, either of
which would COLLIDE with a backup still running from 03:00 (6h timeout) when the
verify fires at 05:00. Verify is a pure reader that only needs the config the
backup jobs already wrote, so it calls `RestoreTest` directly. This collision was
caught by the code-reviewer (feature-dev:code-reviewer) and fixed before commit;
the reviewer also confirmed verify correctly needs no single-flight/CodeConflict
guard (it never writes the repo) and that returning a verify failure (vs
swallowing) is right for a scheduled durability check. Full Go gate green; no web
changes. NOTE the digest job is still unregistered (no digest builder exists);
the deep scratch-restore proof (`verified_rows`) remains the one open 1.5 item.

## 2026-06-24 · band 1.5 (data durability) · restore-test EXECUTION (pgbackrest verify)
`handleRestoreTest` was a stub returning "not implemented", so the restore-test
surfacing could only ever show "never" — the operator had no way to prove a
backup is recoverable. Closed the EXECUTION gap. This was a DESIGN-FIRST item
(no `sm`/DEFAULTS precedent); I chose option (a) `pgbackrest verify` — a
read-only repository integrity check (every backup + WAL file present with
matching checksums) — over the heavier scratch-restore options. Rationale is the
security/safety tie-break: verify NEVER touches the live data directory, needs no
disk-headroom precheck (a restore ≈ DB size could fill the box and itself cause
data loss) and no scratch cleanup, so it cannot cause data loss. It's the
smallest slice that removes the most risk; the deeper scratch-restore-and-boot
proof (which would populate `verified_rows` with a real row count) is re-filed as
a follow-up backlog item rather than rushed. Added `Manager.RestoreTest` +
`VerifyCmd`: verifies (not claims) ownership exactly like Restore's read side (a
foreign owner is a HARD STOP), records a pass/fail `restore_tests` row on a
detached context (a shutdown mid-verify never loses the result), and labels the
verified backup. Wired the handler to it. Unit-tested via the fake Runner:
success records history and NEVER invokes backup/restore; failure records a fail
row; the fail row persists on a cancelled ctx; foreign-owner hard-stop; invalid
stanza. Also made the UI honest (the code-reviewer flagged the success banner as
overclaiming): "Your backups are proven recoverable" → "Your backup repository is
verified intact", and the Callout now describes the checksum check rather than a
full restore. Full Go gate + web typecheck/33 tests/build green.

## 2026-06-24 · band 1.5 (data durability) · surface restore-verification status at a glance
The Backups page already listed restore-test history in a table, but nothing
answered the durability question up front: *have my backups ever been proven
recoverable?* A backup you've never restored is one you don't know works. Closed
the surfacing item: added an exported pure `restoreTestStatus(tests)` classifier
(`never | passed | failed | never-passed`, mirroring `backupFreshness` and reusing
the shared SUCCESS/FAILURE result vocabulary so backups and restore tests classify
identically) and a `RestoreTestStatus` banner above the Restore-tests card that
states recoverability + when in one line. Crucially, the "never" state is
deliberately CALM (info tone, no call-to-action): automated restore-test execution
is still a stub, so in production only "never" can occur today, and the banner must
not nudge the operator toward a button that can't complete. The failed/never-passed
states shout danger for when execution lands. Added 9 vitest/RTL tests (classifier ×
all branches incl. unknown-latest result; component × all four tones incl. the
verified-rows display path). Reviewed (feature-dev:code-reviewer): no blocking
issues. Refiled the real next durability item — restore-test EXECUTION — as a
DESIGN-FIRST backlog entry (verify vs scratch-restore vs full-boot, each with its
disk-headroom/cleanup tradeoffs; no `sm` precedent to port), so it gets a deliberate
design pass rather than a rushed half-build that would give false durability
confidence. Web typecheck/33 tests/build + full Go gate green.

## 2026-06-24 · band 1.5 (data durability) · test-lock the local-only "move backups off-host" nudge
The off-host nudge already existed (Backups page badge + warn Callout, Settings
"recommended" copy), but the local-vs-S3 destination logic was computed inline in
`Backups.tsx` and had **zero test coverage** — a refactor could silently drop the
"your backups are on this server" warning, the panel's main push toward durable,
off-server backups. Closed the acceptance gap ("covered by a test"): extracted the
inline logic into an exported pure `backupDestination(backup, loaded)` returning a
`loading | local | s3` discriminated union, and lifted the local-only warning JSX
into an exported `LocalBackupWarning` component (mirroring the existing
`backupFreshness`/`BackupStatusSummary` precedent). Behavior preserved exactly;
the union just removes the prior `boolean | undefined` + parallel `bucketName`
bindings. Added 9 vitest/RTL tests: `backupDestination` across loading/local/
whitespace-only/bucket/endpoint-only/bucket-preferred, and `LocalBackupWarning`
asserting the warn-tone callout + Settings link fire only when local (nothing for
s3/loading). Reviewed (feature-dev:code-reviewer): no blocking issues; applied
both cleanups it raised — documented the deliberate `.trim()` divergence from the
server's untrimmed `remoteTargetConfigured` (Settings trims on save, so this is
belt-and-suspenders for a hand-edited DB value), and dropped the redundant
`bucketName` local in favor of reading the narrowed union directly. Full Go gate +
web typecheck/test/build green.

## 2026-06-24 · band 1.5 (data durability) · wire scheduled backups + overlap guard
The biggest data-loss gap: the scheduler was instantiated but only ran the
telemetry/alert loop, so **scheduled backups had never run** — a box left alone
made zero backups (only `POST /backups/run` did). Fixed: `startBackgroundJobs`
now registers `full-backup` + `incremental-backup` cron jobs from
`cfg.Schedules` (defaults: weekly full, daily incremental). Each
`scheduledBackup(type)` self-heals the pgBackRest config (same `config.Load`→
`ensureBackupConfigured` prereq the manual button runs) then calls
`backups.Backup`. An empty spec is the operator's opt-out → job not registered,
with a loud Warn (never a silent gap). Closed the overlap risk the backlog note
flagged: the Owner guard is cross-PANEL only (repo markers) and does NOT stop two
concurrent `Backup()` calls in one process — they'd collide on pgBackRest's own
on-disk lock and the loser would be recorded as a `fail` row → false critical
`backup-failed` alert. Added a process-local single-flight guard
(`Manager.backupMu` via `TryLock`): an overlap returns a typed `CodeConflict`
SKIP with NO history row, and the scheduled job swallows `CodeConflict` (Info
log, returns nil) so the scheduler never logs a spurious failure. No reentrancy —
`Restore`→`takeSafetyBackup`→`Backup` doesn't hold `backupMu`, so the nested
safety backup acquires it; a restore during an active backup safely HARD-STOPs.
Tests: manager concurrent-skip (no fail row, pgBackRest never invoked); server
jobs-registered + empty-schedule-disabled. Reviewed: no blocking issues. Full Go
gate green (gofmt/vet/test/build); web untouched. RestoreTest/Digest left for
their own items (restore-test execution is still a stub; no digest builder).

## 2026-06-24 · band 1.5 (data durability) · wire the dormant telemetry + alert loop
Closed the capstone: the whole alert subsystem (collector, engine, rules,
notifiers) was built and unit-tested but NEVER RAN — nothing in `indiepg serve`
called `Collector.SampleOnce` or `Engine.Evaluate`, so no alert could fire in
production. Added `internal/server/background.go`: `ListenAndServe` now calls
`startBackgroundJobs(ctx)`, which (1) seeds `alert.DefaultRules()` into the store
idempotently — only inserting missing IDs so an operator's edit or disable is
never clobbered — and (2) creates a `scheduler.Scheduler` and registers a
`telemetry-sample` job on `cfg.Schedules.TelemetrySample` (default `@every 30s`).
Each tick `runTelemetryCycle` samples (host/PG + backup health, buffering samples
for the dashboard) → evaluates every persisted rule → dispatches firing/recovery
events to every enabled stored Pushover/webhook channel. Collector+Engine are
built in `newServer`; the OTLP exporter is left nil (NewCollector degrades
gracefully and still buffers+evaluates — wiring export is a separate item). The
loop is tied to ctx, stops cleanly on shutdown, and the startup seed is bounded
by a 30s timeout so a hung store can't stall the listener. Refactored
`loadAlertChannels` to expose a ctx-based `loadAlertChannelsCtx` for the
dispatcher (no `*http.Request`). Tests: seed idempotency + preserves a disabled
rule; one full cycle fires `backup-failed` and delivers a "firing" payload to an
httptest webhook (+ asserts the rule persists as firing); fires-with-no-channel
is a clean no-op. Reviewed (feature-dev:code-reviewer): bounded the seed, added a
default-branch warn for unknown channel kinds, documented ctx-cancellation
shutdown. **Discovered + backlogged the next durability gap:** because the
scheduler was never instantiated, *scheduled backups have also never run* — only
the manual "Run backup" button does. The alert loop now makes that LOUD
(backup-stale/backup-failed will fire), but the fix — registering the
full/incremental/restore-test/digest jobs in `background.go` — is the new top
band-1.5 item.

## 2026-06-24 · band 1.5 (data durability) · loud immediate alert when a backup fails
Closed the "loud alert when a scheduled backup fails or hasn't succeeded within
its window" item. There was already a `backup-stale` default rule (no successful
backup in 26h), but two gaps: (1) a fresh *failure* of last night's backup
wouldn't trip staleness for ~26h — far too slow for a durability emergency; and
(2) the metric feeding `backup-stale` (`backup.last_age_seconds`) was **silently
always 0** because the real pg.Sampler never reads the backup tables, so the
rule could never fire even once the loop is wired. Added `LastBackupFailed` to
telemetry.Snapshot + a `backup.last_failed` metric (1 when the most recent
backup attempt failed), a new `backup-failed` default rule (SeverityCritical,
For:0 = fire immediately, 6h cooldown), and `Collector.enrichBackup` which folds
both backup metrics in FROM THE STORE during SampleOnce (the Collector, not the
Sampler, holds the store). Backup_history rows are terminal ("success"/"fail"),
so the newest row is the latest attempt's outcome; age comes from
LatestSuccessfulBackup. A box with no backups yet is left untouched (failed=0) so
a fresh install isn't falsely alarmed. Tests: engine fires `backup-failed` on a
failed snapshot and recovers on success; collector covers failed-latest /
success-latest / no-backups; metric/exporter/Value coverage extended.
**Discovered + backlogged the capstone:** the entire alert evaluation loop is
DORMANT — nothing calls Collector.SampleOnce or Engine.Evaluate at runtime, so no
alert fires in production yet. Wiring that loop (sample→evaluate→notify, seed
default rules) is now the top band-1.5 item. Reviewed (feature-dev:code-reviewer):
no blocking issues; adopted both suggestions (metricValue table coverage for the
new key; make enrichBackup set failed=0 unconditionally on success so the store
stays authoritative). Full Go gate green; web untouched.

## 2026-06-24 · band 1.5 (data durability) · surface "last good backup was N ago" on Backups page
First band-1.5 item. The Dashboard already surfaced the latest backup prominently
and the backend field (`store.LatestSuccessfulBackup`) was already covered by
`TestBackupHistory`. The real gap was the **Backups page**: it buried backup
freshness in the history table — an indie hacker had to read the rows to learn
whether their data was protected, and there was NO loud signal when the most
recent backup *failed* while an older one succeeded (a green row below conveys
"fine", not "your latest backup failed and everything since is unprotected").
Added an exported pure helper `backupFreshness(backups)` classifying the history
(newest-first server contract) into `none` / `good` / `stale` (good backup exists
but most recent attempt failed) / `never-good` (none ever succeeded), and a
`BackupStatusSummary` banner rendered prominently right under the page header. It
shouts in the danger tone for none/stale/never-good and shows an ok banner with
"Last good backup N ago (type)" otherwise. Frontend-only; backend untouched.
Tested with vitest/RTL (`Backups.test.tsx`, 9 tests): the helper's four
classifications incl. the stale path pinned to the *exact* server result string
`"fail"` (per internal/backup/manager.go), plus the rendered tone/title for each
state. Reviewed (feature-dev:code-reviewer): no production bug; adopted its
test-quality fixes — pin failure cases to the real `"fail"` value, reframe the
unknown-result case, add the never-good render test. Full gate green.

## 2026-06-24 · band 1 (security) · login brute-force lockout proven end-to-end (HTTP)
Closed the last open band-1 item. The lockout policy + read-modify-write were
already fully built and unit-tested in `internal/auth` (LockoutPolicy default
5/15min/15min, `recordFailure`, sliding window, unlock-after-LockFor, success
resets). The genuine gap was that nothing proved the throttle is wired through
the *HTTP login handler* — the security claim in `handlers_auth.go` ("Lockout
returns CodeLocked (HTTP 429)") was untested at the boundary. Added
`TestLoginLockoutThrottlesAfterMaxAttempts` (server_test.go): drives real
`POST /api/auth/login` requests through `srv.Handler()` with a tight 3-attempt
policy and asserts (a) the first N-1 wrong guesses → 401 CodeAuth, (b) the Nth
wrong guess → 429 CodeLocked, (c) the *correct* password is then ALSO 429 —
proving lockout is checked before password verification and can't be bypassed by
finally guessing right, and (d) the public `GET /api/auth/status` surfaces
`locked: true` + a future `locked_until`. Time assertion is flake-proof
(compares the deadline to a timestamp captured before the requests, 15-min
margin — no wall-clock race). Reviewed (no correctness issues; adopted the
reviewer's de-flake suggestion). Band 1 security complete → next band is 1.5
(data durability).


## 2026-06-24 · band 1 (security) · secrets never logged (log-scrubbing half of secrets-at-rest)
The state DB file `0600`/dir `0700` hardening and its mode test already existed,
so this closed the remaining "never logged" half. Inventoried every
secret-bearing struct and gave each a redacting `fmt.Stringer` + `slog.LogValuer`
+ `fmt.GoStringer`: `config.S3Target` (SecretKey, CipherPass), `store.AuthRecord`
(Argon2id password hash + the raw HMAC session signing secret — the crown
jewels), `server.alertChannelConfig` (Pushover token, webhook URL). New
`core.Redact`/`core.RedactBytes` produce a fixed `REDACTED` marker that leaks
nothing (not even length). Now no log line, error string, or fmt verb
(`%v/%+v/%s/%#v`, including a secret nested inside a parent's `%+v`) can surface
these — defense-in-depth against a future `log.Info("cfg", cfg)` regression.
Non-secret identifiers stay visible to match the codebase's own boundary (S3
AccessKey is serialized `json:"access_key"`; PushoverUser is kept by
`maskAlertChannels`). Per-struct log-scrubbing tests assert secrets absent +
marker present across text and JSON slog handlers and every fmt verb. JSON API
output is unchanged (`json:"-"` stands; LogValuer never touches encoding/json).
Reviewed by code-reviewer; closed the `%#v` GoStringer hole it flagged.

## 2026-06-24 · band 1 (security) · close the read-only CREATE-via-PUBLIC residual
Closed the last DB-level write vector for `indiepg_readonly`. On PG <= 14 the
`public` schema grants CREATE to the `PUBLIC` pseudo-role, which every role
inherits — so the old `REVOKE CREATE ON SCHEMA public FROM indiepg_readonly` was
a no-op against it, and the role could still `CREATE` (and thus own/write)
scratch objects once it reset its own `default_transaction_read_only` GUC.
`provisionSQL` now `REVOKE CREATE ON SCHEMA public FROM PUBLIC` and re-`GRANT`s
CREATE to `indiepg_admin` so guided actions still create objects. This is scoped
to the panel-managed `postgres` database (the only DB `provisionSQL` ever runs
against); operator app DBs are intentionally untouched — an accepted app-DB-only
limitation that never reaches operator *data* (writes to existing tables stay
privilege-denied). USAGE is left intact, preserving the read-only SELECT path.

Extended `TestReadOnlyRole_DBLevelWriteDenial` to assert a `CREATE TABLE` by the
read-only role is now denied with `42501` even with the GUC off, and that admin
CREATE still works in `postgres`. Proven green against a throwaway PG14 cluster
and verified non-vacuous (under the OLD SQL the read-only `CREATE TABLE`
succeeds). The code-reviewer also caught a real in-passing regression: the
`ALTER ROLE` (re-provision) branch had dropped `NOINHERIT`, so a second
`Provision` would silently leave the read-only role `INHERIT` — contradicting
this function's own documented privilege-denial invariant. Restored `NOINHERIT`
on the ALTER path and added a unit assertion; confirmed `rolinherit=f` survives a
double-provision. Reviewer's second note (admin-CREATE test only covers the
`postgres` DB) is by design — provisionSQL never touches app DBs — and the test
comment now says so plainly rather than overstating.

## 2026-06-24 · band 0 (priority-0 fix) · de-flake the auth tampered-key test
A failing test anywhere is always priority 0, so this iteration fixed it before
resuming band-1 work. `go test ./...` flaked ~10% (3/30 runs) on
`TestVerifyPasswordTamperedKeyReturnsFalse` in `internal/auth`. Root cause: the
test tampered an Argon2id hash by flipping the LAST base64 character of the
32-byte key. A 32-byte key encoded with `base64.RawStdEncoding` is 43 chars whose
final char carries only 4 significant bits + 2 padding bits — so flipping it
often decoded back to the SAME key bytes, leaving the hash untampered;
`VerifyPassword` then (correctly) returned true and the test failed. Fixed by
tampering a real key *byte* instead: decode `parts[5]`, `key[0] ^= 0xFF`,
re-encode. That always changes the derived key, so the assertion is deterministic
(0/50 failures after). Pure test change; no production code touched. Reviewed
(code-reviewer: no blocking issues). The band-1 CREATE-via-PUBLIC item that
prompted the discovery of this flake remains open and detailed in the backlog.

## 2026-06-24 · band 1 (security) · prove read-only role can't write at the DB level
Closed the "verify read-only role is enforced at the DB level" backlog item.
The query box runs through `ExecuteRead` on a pool connected as `indiepg_readonly`,
and the design claims a UI/guard bypass still can't write because the boundary is
DB-level privilege denial (not just the resettable `default_transaction_read_only`
GUC). That claim had a unit test over the provisioning *SQL* but no end-to-end
proof against a real server. Added `TestReadOnlyRole_DBLevelWriteDenial`
(integration-tagged, skips without `INDIEPG_TEST_SOCKET`): admin creates+seeds a
table granting the read-only role SELECT only, then (a) a write via the real
`ExecuteRead` path is refused by the read-only-transaction default (defense in
depth), and (b) with the GUC flipped OFF on a real read-pool connection, every
write against operator data (INSERT/UPDATE/DELETE/DROP) is STILL refused with
`42501` insufficient_privilege — proving privilege denial is the authoritative
boundary. Proven green against a throwaway PG14 cluster and verified non-vacuous
(granting the role write makes it fail at the 42501 assertion).

While making the test comprehensive it surfaced a real residual: `provisionSQL`'s
`REVOKE CREATE ON SCHEMA public FROM indiepg_readonly` does NOT remove the CREATE
the role inherits via `PUBLIC` on PG <= 14, so with the GUC off the role can still
create+own+write scratch tables in `public`. Rather than bundle a risky
privilege-model change, I scoped the test to operator-data writes (the core
"a SELECT can't become a DELETE" guarantee) and filed the CREATE-via-PUBLIC fix as
a tracked band-1 backlog item. Reviewer (code-reviewer subagent) flagged that the
original `ExecuteRead` write loop wasn't diagnostic and that the privilege check
should cover every write variant — both addressed in the restructure. All gates
green; test-only change, no `web/` touch.

## 2026-06-24 · band 1 (security) · CSRF proof on every state-changing endpoint
Closed the "confirm CSRF on every state-changing endpoint" backlog item. The
CSRF gate is centralized in `requireAuth` (cookie + unsafe method must carry a
same-origin Origin/Referer or the `X-Indiepg-Csrf` header, else 409 CodeSafety
before the handler), and `csrfOriginOK`/the gate were already unit-tested — but
only against a stand-in handler. The gap was proof that the property holds for
the *actual wired route table* and a guard against a future mutating route being
registered outside the protected group. Added `TestEveryStateChangingEndpointRejectsCSRF`:
it `chi.Walk`s the real router, and for every unsafe-method route (POST/PUT/PATCH/
DELETE) not on a small documented exempt set (`POST /api/auth/login`, `POST
/api/auth/logout` — login needs the password; logout gates its rotation
internally via `logoutAuthorized`), sends a valid-cookie + forged-Origin request
and asserts 409/CodeSafety. It also asserts every exempt entry maps to a
registered route, so a renamed/removed route can't leave a stale exemption. A new
mutating endpoint added outside `requireAuth` will fail the test, forcing a
conscious CSRF decision. Reviewer (code-reviewer subagent) found no blocking
issues. All gates green; test-only change, no `web/` touch.

## 2026-06-24 · band 1 (security) · logout invalidates session server-side
Closed the "logout invalidates server-side" half of the session-auth audit
item. The cookie hardening (HttpOnly/SameSite=Strict/Secure-aware), expiry, and
per-login rotation were already implemented and tested; the real gap was that
`handleLogout` only cleared the cookie while the stateless HMAC token stayed
valid until expiry (12h default) — a copied/stolen token survived logout. Now
logout rotates the server-side HMAC signing secret (`auth.Logout` →
`store.RotateSessionSecret`), instantly invalidating every issued token (for a
single-admin panel, the strongest + simplest invalidation, no schema change).
Because `/api/auth/logout` is public, rotation fires only when the caller proves
a live session: `logoutAuthorized` requires a valid token AND, for cookie flows,
the same CSRF origin check requireAuth uses — so an unauthenticated/cross-site
caller cannot force-invalidate the admin (DoS). Anonymous logout still clears
the cookie idempotently. Tests: store rotate (preserves hash/lockout, rejects
empty, NotFound before init), authenticator Logout (old token dies, fresh login
works), and handler-level proofs that authenticated cookie+CSRF and Bearer
logouts rotate while anonymous / cookie-without-CSRF do not. Reviewed by
feature-dev:code-reviewer (no blocking findings; added the Bearer-logout test
and a clarifying comment it suggested). All gates green.

## 2026-06-24 · band 0 (foundation) · executable verify gate
Closed the last foundation item: verified the web gate is green from a fresh
`npm ci` (typecheck/build/test all pass) and confirmed the build is
deterministic — the committed `internal/server/web/dist` is byte-identical after
a rebuild, so running the gate never dirties the tracked tree. Turned the gate
from prose into one reproducible command: added `make verify`
(fmt-check → vet → test → static build), `make verify-web`
(npm ci → typecheck → build → test), and `make fmt-check` — the latter runs the
`gofmt -l` "must print nothing" check that `go fmt` cannot do (it rewrites
rather than reports). AGENTS.md now points at these targets. Why: the verify
gate was re-typed by hand each iteration and free to drift from the docs; an
executable gate keeps every iteration consistent and is the literal meaning of
"wire the verify gate into the loop reality." Reviewed by
feature-dev:code-reviewer — fixed its one blocking finding (`fmt-check`
discarded `gofmt`'s non-zero exit, so a syntactically-broken file would silently
pass; now it captures `$?` and fails). `make verify` green (exit 0); web gate
green; tree clean. Foundation band done → moving to band 1 (security).

## 2026-06-24 · band 0 (foundation) · root AGENTS.md
Added a root `AGENTS.md` so every iteration (and any human) shares one
consistent set of build/test/run commands and conventions. It documents the
`make` targets (run/reset/test/vet/fmt/build/web/tidy), the web verify gate
(`cd web` → `npm ci`/`typecheck`/`build`/`test`, vitest+RTL+jsdom), the full
Go verify gate, and the project conventions (single trusted operator,
read-only enforced at the DB level, confirm-on-risky, best-defaults-first,
secrets never logged, atomic config writes, single-writer S3 ownership,
YAGNI/KISS) — linking `scripts/ralph/DEFAULTS.md` as the source of trusted
Postgres/PgBouncer/pgBackRest defaults. Why: closes a band-0 foundation item;
keeps future iterations aligned without re-deriving conventions. Reviewed by
feature-dev:code-reviewer — every documented command/path verified accurate;
applied its one fix (made the `web/` shell block's working directory explicit).
All Go gates green (gofmt clean, vet, test, build).

## 2026-06-24 · band 0 (foundation) · vitest + RTL test runner
Added vitest + React Testing Library + jsdom to `web/` and wired `npm test`
(`vitest run`, CI-less one-shot) plus `test:watch`. Config lives inline in
`vite.config.ts` (jsdom env, `src/test/setup.ts` setup with jest-dom matchers +
RTL cleanup, include `src/**/*.{test,spec}.{ts,tsx}`). Added a real component
test for `ui.tsx` covering `ResultBadge` tone mapping and `ErrorNotice`
ApiError-vs-plain-Error rendering (6 tests green). Why: unblocks the "every
frontend change is tested" north-star requirement — the web verify gate
(`npm test`) is now real. Reviewed by feature-dev:code-reviewer (no blocking
findings); typecheck/build/test all green; Go gates unaffected and green.
