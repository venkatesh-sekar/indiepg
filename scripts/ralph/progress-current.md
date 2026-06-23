# Progress (rolling)

Newest first. One short entry per iteration: date, band, what changed, why.
Keep ~20 entries; archive older ones if this grows large.

<!-- iterations will be prepended here -->

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
