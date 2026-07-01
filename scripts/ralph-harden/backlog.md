# Hardening backlog

One item per iteration, highest-priority band first (see PROMPT.md → Priority).
Format: `- [ ] (band · mode) <subsystem> — <gap> → <what "done" looks like>`
- Mark `- [x]` when shipped; add a line to `progress-current.md`.
- Drop an item with a one-line reason if not worth doing, and add it to the
  "Rejected ideas" list in `learnings.md` so it's never re-proposed.

This is a **starter seed** — deliberately concrete but not exhaustive. The first
few iterations should run **Mode S** (parallel audit panel) to ground and expand
it against the real code with `file:line` evidence. Prefer audit-grounded items
over these once they exist.

## Open

### Band 1 — Correctness (Mode A: prove it does what it claims)

Docker-blocked (need the e2e/integration cluster; can't run in this environment):
- [ ] (1 · A/e2e) backup/restore — assert a full backup is actually restorable: seed data → backup → restore into a fresh cluster → row-for-row match. Extend the e2e scenario if unit coverage can't reach it.
- [ ] (1 · A/e2e) backup PITR (future/xid half) — with a live cluster, assert a TIME target in the future and an xid target beyond the latest committed xid are **rejected** (or handled loudly), not silently promoted-to-latest. Needs Docker/e2e — a future TIME target may be valid PITR into live WAL, so this can't be range-checked at unit level. See Iter #1: the before-earliest-backup half shipped.
- [ ] (1 · A/integration) pg/guard (DB-level + timeout half) — assert the read-only role truly cannot write at the **DB level** (INSERT/UPDATE/DELETE/DDL all rejected), not just hidden in the UI, and that the read pool's statement_timeout actually cancels a long query. Needs the integration cluster (`//go:build integration`, `readonly_integration_test.go`); Docker/socket required. See Iter #2: the auto-LIMIT half shipped.

Unit-testable (audit-grounded, Iter #3 panel; ranked):
- [ ] (1 · A) pgbouncer — enable's "verify running before recording success" invariant is only proven for the `enable --now` path; the reload→dead-unit path (enable.go:201-216) has no test. → Test: reload OK + is-active "failed" must return an error and NOT persist `enabled=true`. (Iter #4: partially covered — `TestEnable_ServiceNotRunningAfterStartIsNotRecorded` now drives reload-OK + is-active-"failed" and asserts the error + no persist; the failure is caught in `Reload`'s post-apply verify. Keep open only if a dedicated no-config-change reload→dead path is still wanted.)
- [x] (1 · A) store/instance — `SaveInstance` ON CONFLICT omits `created_at` to preserve birth time + normalizes it to canonical UTC on write. `store_test.go: TestSaveInstancePreservesCreatedAtOnResave` (birth in a non-UTC zone; asserts the RAW stored TEXT is `...Z`, created_at unchanged across a re-save that changes every other column, COUNT(*)=1) + `TestSaveInstanceStampsCreatedAtWhenZero` (zero CreatedAt → stamped now, never 0001-01-01). Mutation-proven over 4 mutations: `created_at = excluded.created_at`, `ON CONFLICT DO NOTHING`, drop `created.UTC()`, drop the `!created.IsZero()` guard. test-skeptic caught the `.UTC()` escape on the first draft → strengthened before commit. No bug. Iter #8.
- [x] (1 · A) store/schema — the single-row `CHECK (id = 1)` on auth/instance (schema.go:15,33) now has `store_test.go: TestSingleRowCheckRejectsSecondIdentityRow` (table-driven over both tables). Raw `INSERT` of a non-1 id is refused with `CHECK constraint failed`; positive control (id=1 accepted) isolates the CHECK from a stray NOT NULL/type failure; probes id=2 (proves CHECK not PK uniqueness), id=0, AND id=-1. Mutation-proven over 4 mutations: drop-on-instance, drop-on-auth, `= 1`→`>= 1`, `= 1`→`id * id = 1`. The last is the test-skeptic's escape (admits a negative second identity row while rejecting 0/2) → id=-1 added before commit. No bug. Iter #9.
- [x] (1 · A) web/Extensions — the Tier-3 "needs_restart" install gate (`confirmOk = !needsRestart || typed === ext.name`, Extensions.tsx:452; disabled at :519) triggers a server-wide `systemctl restart postgresql`. Now covered by `web/src/views/Extensions.test.tsx` (2 tests, real `<Extensions />`): "Install for me" stays DISABLED for a different name / prefix `pg` / SUPERSTRING `pg_cron2` / padded ` pg_cron ` and only ENABLES on the exact `pg_cron`, then the restart-install fires with `confirm:"pg_cron"` and the server-wide-restart copy is asserted on screen first; a Tier-1 add opens NO dialog and posts `confirm:""`. Mutation-proven over 7 mutations. test-skeptic's `confirm:typed`→`confirm:ext.name` mutation verified + REJECTED as behavior-preserving (gate forces typed===name on every reachable path; server re-checks exact `Typed==name` at extensions.go:292) — see the new active rule in learnings.md. No bug. Iter #11.
- [x] (1 · A) web/Version — the two irreversible typed-confirm gates now have `web/src/views/Version.test.tsx` (2 tests, one per dialog, driven via the exported `PendingFinalizationBanner`). `FinalizeDialog` demands the OLD major (`from_major`, the cluster it deletes) and `RollbackDialog` the NEW major (`to_major`, the version it abandons); each test types the OTHER dialog's number, a superstring (`169`/`179`), and a numeric-equivalent (`16.0`/`17.0`) — all must keep the confirm DISABLED — then its own value (whitespace-padded, to lock `.trim()`) enables it, and the destructive API fires only after with the correct major. Copy-states-the-irreversible-effect asserted too. Mutation-proven over 13 mutations; test-skeptic's substring/prefix/`Number()`-coercion escapes closed before commit. No bug. Iter #10.

### Band 2 — Preflight & fail-fast (Mode P)
- [ ] (2 · A) install/provision — `InstallPreflight` clobber guards: the two FakeRunner-driven halves are now covered by `internal/pg/preflight_test.go` (Iter #12) — `portListening` (busy 5432 → hard fail; **fails closed** on `ss` probe error) and `listClusters` (existing Debian cluster → hard fail; **fails closed** on `pg_lsclusters` error, never swallowed into "no clusters"), mutation-proven over 8 mutations incl. the test-skeptic's `HasSuffix(token)`→`Contains(line)` gap. RESIDUAL (keep open, lower value): the FS-reading checks — `existingInstallClusters`'s `/var/lib/postgresql` dir-scan fallback, `detectOSCodename` (its pure parser `parseOSReleaseCodename` is already tested in versionupgrade_test.go), and the `freeBytes` disk floor — need FS seams injected on `Manager`/preflight before they're deterministically unit-testable; a full end-to-end `InstallPreflight` test would be host-flaky without them.
- [ ] (2 · A) install/upgrade — `MajorUpgradePreflight` (pg/preflight.go:212-363) is the sole gate proving a major upgrade refuses when pg_upgrade would (prepared_xacts, logical slots, extension parity via missing control file, disk shortfall, missing target binary). No test references it. → Drive each blocker branch with a FakeRunner + temp control-file paths and assert `CheckSet.HasFail()` (and false on the clean path).
- [ ] (2 · A) alert — `handleTestAlertChannel` (handlers_alerts.go:291-342). NOTE (Iter #12 analysis): the alleged nil-deref is **unreachable** — `req.Kind` is validated to `pushover`/`webhook` (line 300), `ch` is selected only when `channels[i].Kind == req.Kind` (line 313), so `ch.Kind ∈ {pushover,webhook}` and the switch (line 327) always assigns `notifier`; an unexpected-kind stored row simply isn't selected → clean `NotFoundError` at line 318, not a panic. Next iteration: either DROP this (add to Rejected) or convert to a Mode-A test proving an unexpected-kind row yields the NotFound error (pins the loop guard against a future refactor that switches on `req.Kind` before selecting `ch`).
- [ ] (2 · P) migrate — preflight source reachability, target existence, and free space before starting; don't half-migrate then error. (Partly covered: `validateSource`/`validateTargetOverwrite`/dump-too-large exist + tested; the free-space precheck does not.)
- [ ] (2 · P) config write — parse/validate the new config (and, where possible, a dry `postgres -C`/`--check`) before replacing the live file. (Note: pgbouncer already rejects injection before any write; pg config goes via ALTER SYSTEM + restart-with-rollback.)

### Band 3 — Durability
- [ ] (3 · A) alert — a failed critical Send is silenced for a full cooldown. The engine persists `StateFiring`+`LastFiredAt=now` independent of delivery (engine.go:213-223); dispatch only LOGS a send failure and never retries (background.go:281-288), so a transient blip on a `backup-failed`/`pg-down` page suppresses re-notify for 6h/15m. → Test a failing Send re-notifies on the next eval (or don't start the cooldown until delivered); drive a retry/backoff.
- [ ] (3 · A) alert — `backup-stale` never fires on a box that never produced a backup: `enrichBackup` leaves `LastBackupAgeSeconds=0` on NotFound (collector.go:134-140) and the rule is `age > 26h` (rule.go:233-242), so `0 > 93600` is false. The canonical "no backup" durability alert is silent on exactly the install that never backed up. → sentinel age or a `backup-never-succeeded` signal; test the alert fires.
- [ ] (3 · A) migrate worker — `storeRecorder.Succeed` (migrate_worker.go:207-222) carries the same detached-`context.WithoutCancel` durability claim as `Fail` (tested) but has NO test. → Test with an already-cancelled ctx: status flips to `completed`, `FinishedAt` + counts persisted, so a shutdown can't wedge a finished migration in "importing".
- [ ] (3 · A) drop-off worker — `finishDropoff` (dropoff_worker.go:103-122) claims it persists the terminal status even when the worker ctx has expired (the stalled-transfer case), but both tests pass a live ctx. → Test with an already-cancelled ctx: `FinalizeDropoffFromImporting` still lands completed/failed.
- [ ] (3 · A) store — `migrate()` applies all schema + additive-column DDL in one tx with `defer tx.Rollback()` (store.go:132-164) and claims atomic apply, but no test drives the failure path. → Inject a failing statement / conflicting object and assert the error carries the offending SQL and no partial table survives.
- [ ] (3 · D) config durability — `writePreserving` (pg/hba.go:117-148) and `backup.writeConfigFile` (backup/provision.go:150-185) do temp-write+rename but never fsync the temp file before rename nor the parent dir after, and take no pre-change backup — so a power loss mid-rewrite can leave a torn config Postgres/pgBackRest refuses to load, with no copy to restore. → Add `tmp.Sync()` + dir-fsync (and a pre-change copy); regression-test recoverability.
- [ ] (3) surface "last good backup" (age + result) on the Dashboard; loud, immediate alert when a scheduled backup fails or is overdue.
- [ ] (3) verify off-host (S3) backups are the default and a local-only config is clearly labeled as risky.

### Band 4 — Self-heal & defaults (Mode D)
- [ ] (4 · A) pgbouncer — `EnsureRuntimeDir` (enable.go:193-195) guards the documented post-boot pidfile race, but its failure is untested; a swallowed daemon-reload/`install -d` error must abort before `EnableNow` and leave `enabled` unset. → Test: `daemon-reload` err → Enable returns it, never runs `enable --now`, never persists.
- [ ] (4 · D) host-sized tuning — confirm shared_buffers/work_mem/max_connections defaults match DEFAULTS.md and are sized to the host, with early alerts on disk/conn/WAL headroom.

### Band 5 — Clarity
- [ ] (5 · A) alert — `firingMessage`/`recoveryMessage` (engine.go:240-249) print `formatValue` unit-agnostically, so `backup-stale` pages as a raw five-digit seconds number (`... > 93601`) and disk/conn rules drop the `%`. → Per-metric unit formatting (hours/percent); test the backup-stale message is human-readable.
- [ ] (5 · D) alert redaction pass (test-skeptic follow-up to Iter #3) — the webhook non-2xx path (notifier.go:225-226) echoes the endpoint's raw response body into `WithDetail("body", ...)` (reaches the API), and the pushover transport path (:119) still wraps a `*url.Error` (pushover URL is a public constant so no token leak, but it's the same un-redacted pattern the webhook fix removed). → Cap/skip the body detail; drop the pushover wrap for consistency.
- [ ] (5 · A) pgbouncer — `service.go:120` logs the reload→restart fallback (which drops all client connections — the most operationally significant event here) with a non-ctx `Warn`, losing trace correlation. Same at install.go:268. → Switch to `WarnCtx(ctx, ...)`.
- [ ] (5 · A) store — `DeleteConfig` (config.go:60) and `DeleteAlert` (alert.go:80) document "removing a missing key/rule is not an error" and ignore RowsAffected, but neither is tested for the missing-key case. → One-liners asserting delete of an absent key/rule returns nil (locks the idempotent-delete contract vs the NotFound-on-n==0 mutators).
- [ ] (5 · A) web/Version + web/Extensions — both views (async-polling) have no empty/loading/error-state tests: Version's spinner/ErrorNotice/StaleBanner (Version.tsx:110-123) and Extensions' per-database Installed/Available empty+error states (Extensions.tsx:178-186,250-258). → RTL tests: first-load spinner (not blank), first-load failure shows the error (not a frozen spinner), post-success poll failure surfaces the stale banner.
- [ ] (5) audit destructive-action confirms: every one states exactly what it will do and requires typed-name confirmation; no secret is ever surfaced or logged.
- [ ] (5) audit empty/loading/error states across views (Login, Dashboard, Backups, Migrate, Query, Settings, Pooler, DatabaseTuning, Extensions, Version) for clear, non-confusing copy.

### Band 0 — Foundation (only if a gate is red or infra is missing)
- [ ] (0) if any `make verify` / `make verify-web` gate is red, fix it before anything else.

## Done

- [x] (1 · A) store/auth `InitAuth` reset-password branch — `store_test.go:
  TestInitAuthOverwritesExistingRowAndResetsLockout`. The docstring claims InitAuth
  "overwrites any existing row (used by install and reset-password)" and its
  `ON CONFLICT` resets `failed_attempts=0`/`locked_until=NULL`, but `TestAuthRoundTrip`
  only hit the INSERT (one InitAuth call); the reset UPDATE branch was unproven, and
  the live caller `SetPassword` routes existing accounts to `SetPasswordHash` so
  nothing else exercised it. New test drives the branch: re-init on a locked-out
  account must overwrite the hash, ROTATE the session secret (so old-secret tokens
  can't be replayed post-reset — the security point), clear the lockout, bump
  `updated_at`, and update the single row in place. No bug (clause correct); test
  locks the contract. Mutation-proven over all six SET-clause mutations incl.
  `DO NOTHING` and the test-skeptic's stale-`updated_at` escape (strict-After added
  before commit). Iter #7.

- [x] (1 · A) migrate worker pure helpers — `internal/server/migrate_helpers_test.go`
  drives the four operator-facing pure helpers in `migrate_worker.go`:
  `failErrorText` (:173), `boundDiagnostic` (:192), `unmarshalCounts` (:419),
  `toMigrationResponse` (:393). Found + fixed a BUG: `unmarshalCounts("null")`
  returned `nil` (a JSON-null blob unmarshals a map to nil WITHOUT error, slipping
  past the err-branch), so the field serialized back as JSON `null` — the exact
  thing its doc says it prevents; added an `if out == nil` guard. Tests pin every
  branch and are mutation-proven (newline→space, dropped `TrimSpace`, `Phase:=Status`
  / `ProgressDone↔Total` cross-wires, dropped nil-guard each red the suite). First
  draft under-asserted (test-skeptic caught 6 unpinned wire fields + 2 separators);
  strengthened before commit. Iter #6.

- [x] (1 · A) install/upgrade `validateUpgradeTarget` — the sole guard stopping a
  destructive same-major/downgrade/unsupported-target "major upgrade" (gates both
  the preflight :240 and start :324 endpoints) now has a mutation-proven table test.
  `handlers_pgversion_test.go: TestValidateUpgradeTarget` drives the real pure
  function: accept 16→17 / 15→17; reject downgrade 17→16 + same-major 16→16
  (`CodeValidation`, "newer"), unsupported 16→99 (`CodeValidation`, "not a
  supported"), unknown/negative current 0→17 / -1→17 (`CodeInternal`, "current").
  Each rejecting case pins a distinct code+message. Flipping `target<=current`→`<`,
  `current<=0`→`<`, and `!IsSupported(target)`→`false` each reds the matching
  subtest. No bug (guard was correct); the test locks the contract. reviewers
  clean; test-skeptic found no escaping mutation. Iter #5.

- [x] (1 · A) pgbouncer Reload verifies the pooler is still running after applying
  config — `(*Manager).Reload` (`internal/pgbouncer/service.go`) no longer returns
  nil the instant `systemctl reload`/`restart` exits 0. After a 0-exit it calls
  `IsRunning` and returns a loud `core.ExecError` (+ hint) when the pooler is down,
  propagates an undeterminable-state error, else logs success — honoring DEFAULTS.md
  ("verify it's still running after"). A reload that exits 0 but left the pooler dead
  errors immediately (no restart of the same rejected config). `service_test.go`:
  `TestReload_ErrorsWhenPoolerDeadAfterReload`, `_DeadAfterRestart`,
  `_RunStateUndeterminableAfterApply` (+ the two existing Reload tests now pin the
  verify call); `enable_test.go`: `TestEnable_ServiceNotRunningAfterStartIsNotRecorded`
  (CodeExec — caught in Reload). Mutation-proven; reviewers clean. Iter #4.

- [x] (1 · A) alert webhook secret leak — `(*WebhookNotifier).post`
  (`internal/alert/notifier.go`) no longer leaks the webhook URL (which may embed
  an auth token) into error text on the `NewRequestWithContext` path (was
  `invalid webhook url %q` + wrapped `url.Parse` error) or the `client.Do` path
  (was a wrapped `*url.Error` whose text carries the full request URL). Both are
  logged (background.go:285) and returned to the "send test" API; now both return
  a redaction-safe message + hint, no URL, no wrapped cause. `notifier_test.go`:
  `TestWebhookTransportErrorDoesNotLeakURL` + `TestWebhookInvalidURLDoesNotLeakURL`
  drive the real stdlib failure shapes and assert the token is absent from the
  message, Hint, AND Details (the full API-serialized surface). Iter #3.

Verified already-covered by existing tests (Iter #3 audit — moved out of Open):
- [x] (1 · A) auth/session logout invalidates server-side + DoS guard —
  `server_test.go`: `TestLogoutInvalidatesSessionServerSide`,
  `TestLogoutWithBearerInvalidatesSession`, `TestLogoutWithoutProofDoesNotInvalidate`;
  `authenticator_test.go`: `TestLogoutInvalidatesIssuedTokens`. (Session fixation is
  structurally impossible: tokens are stateless HMAC-signed and minted only server-
  side at login; the server never adopts a client-supplied session id.)
- [x] (1 · A) auth login brute-force lockout (correct password stays locked) —
  `server_test.go: TestLoginLockoutThrottlesAfterMaxAttempts`;
  `authenticator_test.go` lockout suite. (Single-admin panel: no username to enumerate.)
- [x] (1 · A) migrate data verification (direct-pull + S3 + checksum) —
  `orchestrator_test.go`: `TestDirect_single_rowMismatch`,
  `TestImportFromSession_rowMismatch`, `TestImportFromSession_checksumMismatch`.
- [x] (4 · D) config self-heal auto-rollback — `safeconfig_test.go`:
  `TestRestartWithRollback_RecoversFromBadConfig`, `_RollsBackWhenSystemdLiesAboutStartup`,
  `_HonestWhenStillDownDespiteSystemdOK`, `_RollbackRestartAlsoFails`.
- [x] (1 · A) config atomic write preserves mode + no-write-on-error —
  `pgbouncer/install_test.go`: `TestEnsureConfig_WritesMarkedFile0640`,
  `_RejectsInjectionBeforeAnyWrite`, foreign-config/symlink refusal;
  `userlist_install_test.go: TestEnsureUserlist_WritesFile0640`. (pg auto.conf is
  snapshotted before restart — see safeconfig. Residual fsync/pre-change-backup gap
  for pg_hba/pgbackrest.conf re-filed under Band 3.)
- [x] (3) S3 single-writer ownership foreign-owner HARD STOP —
  `backup/manager_test.go`: `TestManagerBackup_ForeignOwnerHardStop`,
  `TestManagerStartBackup_ForeignOwnerHardStopNoRow`,
  `TestManagerRestore_ForeignOwnerHardStop`, `TestManagerRestoreTest_ForeignOwnerHardStop`.

- [x] (1 · A) pg/guard auto-LIMIT (query box) — the guard no longer appends
  ` LIMIT n` to a read that already carries a top-level `FETCH FIRST ... ROWS
  ONLY` clause (which PostgreSQL rejects alongside LIMIT), so a valid bounded
  read runs verbatim instead of failing with a confusing syntax error.
  `hasTopLevelFetch`/`hasTopLevelRowBound` gate both `Check` and `EnsureLimit`;
  `HasLimit`→`Limited` now reports a FETCH-bounded result as limited.
  `internal/pg/guard/guard.go` + `guard_test.go` (FETCH first/next/single,
  offset+fetch, subquery-scoping, bare-OFFSET-still-limited, quoted-`"fetch"`-
  column-still-limited, lower/mixed-case). Iter #2.
- [x] (1 · P) backup PITR (before-base half) — restore preflights the recovery
  target and rejects a TIME target earlier than the earliest available backup
  with a clear `CodeValidation` error, BEFORE the destructive safety-backup/
  cluster-stop/overwrite. Fail-open on uncertainty. `internal/backup/manager.go`
  (`preflightTargetReachable`, `earliestBackupStart`) +
  `internal/backup/restore_preflight_test.go`. Iter #1.
