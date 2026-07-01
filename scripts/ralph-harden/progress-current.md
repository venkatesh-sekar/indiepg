# Progress — hardening loop

Reverse-chronological. One prepended entry per iteration:
`## Iter #N — <date> — <mode>/<band> — <verdict>` then 1–3 lines of what/why.
Older entries get archived once this file grows large.

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
