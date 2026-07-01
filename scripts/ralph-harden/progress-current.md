# Progress — hardening loop

Reverse-chronological. One prepended entry per iteration:
`## Iter #N — <date> — <mode>/<band> — <verdict>` then 1–3 lines of what/why.
Older entries get archived once this file grows large.

---

## Iter #6 — 2026-07-01 — A/migrate (band 1 correctness) — SHIPPED (bug fixed)

Mode A on the four pure helpers in `internal/server/migrate_worker.go` that sit
on the operator-facing migrate path but were untested in isolation:
`failErrorText` (:173), `boundDiagnostic` (:192), `unmarshalCounts` (:419),
`toMigrationResponse` (:393). Every failed migration renders through
`failErrorText`; every `GET /migrations` renders through
`toMigrationResponse`→`unmarshalCounts`. They take adversarial input — an
external pg_restore's stderr and JSON blobs round-tripped through SQLite.

**Bug found + fixed:** `unmarshalCounts("null")` returned `nil`, not the
documented non-nil `{}`. `json.Unmarshal([]byte("null"), &mapVar)` sets the map
to nil WITHOUT an error, so a JSON-null blob slips past the `err != nil` branch
and the function returns the now-nil map — which then serializes back as JSON
`null` on the API, the exact thing the doc says it prevents ("empty (non-nil)
map … so the field serializes as {} rather than null"). Added an `if out == nil`
guard. The new test failed before the fix, passes after.

Added `internal/server/migrate_helpers_test.go` (table/subtests). Mutation-proven
against the test-skeptic's findings: newline→space separator in `failErrorText`,
dropped `TrimSpace` in `boundDiagnostic`, `Phase:=Status` and
`ProgressDone↔Total` cross-wires in `toMigrationResponse`, and dropping the
nil-guard — each reds the suite (verified in place, then reverted). First test
draft under-asserted `toMigrationResponse` (6 wire fields unpinned) and the two
separators; strengthened to distinct-per-field values + a whitespace-boundary
case before commit. code-reviewer clean. Gates green (gofmt/vet/`go test
./...`/CGO-static build); web untouched; e2e not needed (pure unit).

Also (rule 3 backlog grooming): rejected the band-1 pg/hba
`injectHBARules`-self-heal-a-widened-block item — its proposed "normalize back
to loopback+socket-only" fix would revert a documented operator hardening
(replacing the trust lines with scram-sha-256, per hba.go:26) and thus *widen*
access, violating the security tie-break; and a widened managed block requires
root/postgres write to the 0600 file (not an escalation). Moved to the Rejected
list.

## Iter #5 — 2026-07-01 — A/upgrade (band 1 correctness) — SHIPPED

Mode A on `validateUpgradeTarget` (`internal/server/handlers_pgversion.go:858`),
the sole guard that stops a destructive same-major / downgrade / unsupported-
target "major upgrade". It gates BOTH endpoints (preflight :240 and start :324)
yet had ZERO tests. A major upgrade runs `pg_upgradecluster` over the live
datadir, so accepting a same-major (16→16), downgrade (17→16), or unsupported
(16→99) target — or proceeding when the current major couldn't be read
(current≤0) — is a data-loss-class mistake. No bug found (the guard is correct);
this iteration locks its contract so a future edit can't silently weaken it.

Added `TestValidateUpgradeTarget`: a table test driving the real pure function —
accept 16→17 and skip-a-major 15→17; reject downgrade 17→16 and same-major 16→16
(both `CodeValidation`, msg "newer"), unsupported 16→99 (`CodeValidation`, "not a
supported"), and unknown/negative current 0→17 and -1→17 (`CodeInternal`,
"current"). Each rejecting case pins a distinct code+message so no branch can
borrow another's error. Mutation-proven: flipping `target <= current`→`<`,
`current <= 0`→`<`, and `!IsSupported(target)`→`false` each reds the exact
matching subtest (verified, then reverted). code-reviewer clean; test-skeptic
enumerated every one-line mutation class and found no escaping mutation — the
same-major case sits exactly on the `<=` boundary that a weaker test would omit.
Gates green (gofmt/vet/`go test ./...`/CGO-static build); web untouched; e2e not
needed (pure unit).

## Iter #4 — 2026-07-01 — A/pgbouncer (band 1 correctness) — SHIPPED

Mode A on `(*Manager).Reload` (`internal/pgbouncer/service.go`). Fixed a real
"reports success over a dead pooler" bug: `Reload` returned nil the instant
`systemctl reload` (or its `restart` fallback) exited 0 — it never verified
PgBouncer was still running, contradicting DEFAULTS.md ("reload via SIGHUP,
restart as fallback; **verify it's still running after**"). A SIGHUP reload can
exit 0 while PgBouncer then dies re-parsing a bad config, and `systemctl restart`
can return before a unit that crashes on startup is caught — so a config apply
that killed the pooler was silently reported as applied.

Fix: after either the reload OR the restart-fallback exits 0, `Reload` now calls
`IsRunning` and (a) propagates an undeterminable-state error ("couldn't ask
systemctl"), or (b) returns a loud `core.ExecError` (with a hint to check status
/logs and restore the previous config) when the pooler is down, or (c) logs
success. Deliberate call: a reload that exits 0 but leaves the pooler dead means
the on-disk config is bad, so it errors immediately rather than bouncing into an
equally-doomed restart that would needlessly drop the connections a SIGHUP was
chosen to preserve. `CodeExec` matches the codebase convention for "service didn't
come up after a systemd op" (safeconfig.go:151, upgrade.go:235). Only caller is
`Enable`, whose own later `IsRunning` gate is preserved (belt-and-suspenders for
the no-change path).

Test-first + mutation-proven: updated the two existing Reload tests to register a
healthy is-active and pin the new verify call; added 3 guards —
`TestReload_ErrorsWhenPoolerDeadAfterReload` (no restart of the same rejected
config), `_DeadAfterRestart`, `_RunStateUndeterminableAfterApply` (distinguishes
false,err from false,nil via the "could not determine service state" message).
Confirmed the guards fail under an inverted-condition mutation (`if running`),
then reverted. Updated `TestEnable_ServiceNotRunningAfterStartIsNotRecorded`
(CodeInternal→CodeExec: the dead pooler is now caught one step earlier, in Reload).

Reviewers: code-reviewer (no blocking issues) + test-skeptic (found the two new
dead-pooler tests asserted call positions but not count → a trailing extra
systemctl call could escape; closed with `require.Len` bounds).

Gates: gofmt clean, vet clean, `go test ./...` green, static build OK. web
untouched. e2e (pooler enable) is Docker-heavy and not needed for this unit fix.

## Iter #3 — 2026-07-01 — A/alert (band 1 security) — SHIPPED

Mode A on the alert webhook notifier (`internal/alert/notifier.go`). Fixed a real
secret leak: `(*WebhookNotifier).post` embedded the webhook URL in error text at
two sites — the `NewRequestWithContext` path (`invalid webhook url %q` + wrapped
`url.Parse` error) and the `client.Do` path (wrapped `*url.Error`, whose text
carries the full request URL). Both errors are logged by the dispatch loop
(`background.go:285`) AND returned to the operator's "send test" API, so a webhook
URL that embeds an auth token (Slack/Discord/n8n put the secret in the path) leaked
into logs and the API. Now both return a redaction-safe message + actionable hint,
no URL, no wrapped cause — honoring "secrets never logged" and the security
tie-break. Codes preserved (CodeValidation vs CodeExec); no caller depends on the
wrapped cause (grepped — nothing does errors.Is/As on `*url.Error`/`net.Error`).

Test-first: two tests drive the real paths (a real `*url.Error` from a stubbed
`Do`, a real NUL-byte `url.Parse` rejection) and FAIL pre-fix. Per the test-skeptic,
strengthened `requireNoLeak` to assert the token is absent from ALL operator-visible
channels — message, Hint, AND Details — because `toAPIError` (respond.go:122-125)
serializes Hint+Details to the wire while `err.Error()` renders only the message;
a URL-in-hint mutation is now caught (verified: injecting the URL into the hint
turns the test red).

Reviewers: code-reviewer (solid, no changes) + test-skeptic (found the Hint/Details
channel gap → closed; flagged the non-2xx `body` detail + pushover paths as
lower-risk follow-ups → backlogged).

Mode S (folded in): the top band-1 items were stale — a 6-agent parallel audit
(scheduler, store, alert, pgbouncer, install/upgrade, web) confirmed auth/session,
login-lockout, config atomic-write, config self-heal, migrate verification, and S3
ownership are ALL already covered by strong tests (moved to Done), and surfaced
~20 fresh, evidence-grounded, unit-testable gaps (added to backlog).

Gates: fmt ✓ vet ✓ `go test ./...` ✓ static build ✓. Web untouched. Docker N/A.

---

## Iter #2 — 2026-07-01 — A/pg-guard (band 1 correctness) — SHIPPED

Mode A on the query-box auto-LIMIT path (`internal/pg/guard`). Found & fixed a
real bug: the injection gate keyed only on a top-level `LIMIT` keyword
(`hasTopLevelLimit`), so a valid read using the SQL-standard `FETCH FIRST ... ROWS
ONLY` clause got ` LIMIT n` appended after it — and PostgreSQL rejects a query
carrying both LIMIT and FETCH, so a valid, already-bounded read the operator
submitted failed with a confusing syntax error it never wrote (violates "never
gets confused"). Fix: added `hasTopLevelFetch` + `hasTopLevelRowBound = LIMIT ||
FETCH`, and gate injection (in `Check` via `cls.HasLimit`, and in `EnsureLimit`)
on the broader bound. `HasLimit`→`Limited` now honestly reports a FETCH-bounded
result as limited. Corrected `injectLimit`'s stale doc that claimed FETCH handling
it never did.

Test-first: 3 new/extended tables drive the real `Check`/`EnsureLimit` paths and
FAIL against the pre-fix code. Depth-scoped (subquery FETCH still gets a top
LIMIT), OFFSET-without-FETCH still limited, quoted `"fetch"` column still limited
(FETCH is reserved → bare `fetch` can't be an identifier), and lower/mixed-case
FETCH covered (per test-skeptic: catches a case-sensitivity regression the
uppercase-only cases would miss).

Reviewers: code-reviewer (no ≥80 findings; the "identifier named fetch" concern is
moot — FETCH is reserved, quoted → tokQuoted → ignored; documented + tested) and a
test-skeptic (confirmed the bug real vs the PG grammar, tests non-tautological;
surfaced the casing gap, now closed).

Gates: fmt ✓ vet ✓ `go test ./...` ✓ static build ✓. Web untouched. Docker N/A →
the DB-level read-only-role + statement-timeout half of the pg/guard item stays
open (needs the integration cluster).

---

## Iter #1 — 2026-07-01 — P/backup (band 1 PITR) — SHIPPED

Restore now preflights the recovery target *before* any destructive step: a TIME
target earlier than the earliest available backup can never be reached (recovery
replays WAL forward from a base backup), so `preflightTargetReachable` refuses it
with a clear `CodeValidation` error that names the target + earliest backup —
instead of stopping the cluster, taking a safety backup, and only then letting
pgBackRest fail with an opaque error at the most data-critical moment. Fail-open
on uncertainty (nil/non-time target, `Info` error, or no usable backup start
time → proceed; pgBackRest stays the final arbiter). Tests drive the real
`Restore` path and assert NO stop/safety-backup/restore ran on rejection. Both
reviewers (code-reviewer + fail-fast critic) passed with no blocking findings;
added a non-TIME-scope test per the critic.

Also (priority-0, separate commit): gofmt-cleaned 3 pre-existing files
(internal/server/server.go + 2 e2e scenario files) that had drifted and were
failing the fmt gate — no behavior change.

Gates: fmt ✓ vet ✓ `go test ./...` ✓ static build ✓. Docker unavailable → e2e
skipped (future/xid target rejection needs a live cluster; tracked in backlog).

---

(no iterations yet — the loop will prepend here)
