# Progress — hardening loop

Reverse-chronological. One prepended entry per iteration:
`## Iter #N — <date> — <mode>/<band> — <verdict>` then 1–3 lines of what/why.
Older entries get archived once this file grows large.

---

## Iter #12 — 2026-07-01 — A/install (band 2, prove-it) — SHIPPED (test only; no bug)

Mode A on the two FakeRunner-driven guards that make `Manager.InstallPreflight`
(`internal/pg/preflight.go`) **refuse to clobber an existing PostgreSQL install** on a
fresh `Provision`: `portListening` (something already bound to 5432 → hard fail) and
`listClusters` (an existing Debian cluster → hard fail). Both were entirely untested;
both must **fail CLOSED** — a probe that can't run must surface as an error so the
preflight aborts, never a silent "clean host" that lets Provision overwrite a live
datadir. (The other InstallPreflight checks — the `/var/lib/postgresql` dir-scan
fallback, `detectOSCodename`, `freeBytes` — read the real FS and aren't deterministically
unit-testable here without adding FS seams; deliberately out of scope.)

New `internal/pg/preflight_test.go` (8 tests). `portListening`: detects a `127.0.0.1:5432`
listener and the IPv6/wildcard forms (`[::]:5432`, `*:5432`, `[::1]:5432`); reads free
when only near-miss rows exist; **fails closed** (returns an error, not "free") when the
`ss` probe errors. `listClusters`: parses columns row-for-row incl. a dotted `17.2`→`17`
Ver token; skips blank/short/non-numeric-Ver lines while keeping the valid one; **fails
closed** when `pg_lsclusters` errors (never swallowed into "no clusters"). No bug — both
guards were correct; the tests lock the contract.

Mutation-proven over 8 one-line source mutations (all caught): swallow ss error→`false,nil`;
`needle := ":"+port`→`port`; `HasSuffix`→`HasPrefix`; **`HasSuffix(token)`→`Contains(line)`**
(the test-skeptic's gap — closed by adding interior-substring near-miss rows `:54321` and
`[fe80::5432]:22`, which embed `5432` without being a real `:5432` token); swallow
pg_lsclusters error→`nil,nil`; drop `major==0` skip; drop `len(fields)<6` skip; cross-wire
`Port: fields[2]`→`fields[3]`. code-reviewer clean. Backend gate green (fmt/vet/test/build);
web untouched; e2e N/A (pure unit + Docker unavailable).

## Iter #11 — 2026-07-01 — A/web-extensions (band 1 correctness) — SHIPPED (test only; no bug)

Mode A on the Tier-3 ("needs_restart") extension-install gate in
`web/src/views/Extensions.tsx`. Installing such an extension runs a **server-wide
`systemctl restart postgresql`** (every database briefly down); the "Install for me"
button is gated behind a typed-name confirm (`confirmOk = !needsRestart || typed ===
ext.name`, `disabled={busy || !confirmOk}`), and there was NO `Extensions.test.tsx` —
the confirm-on-restart invariant was entirely unproven.

Added `web/src/views/Extensions.test.tsx` (2 tests) driving the real `<Extensions />`
(mocks only `sonner` + the `api.*` loads). Test 1 opens the Tier-3 Add dialog and pins
the gate: DISABLED on open, and for a DIFFERENT name (`postgis`), a prefix/substring
(`pg`), a SUPERSTRING (`pg_cron2` — kills `.includes`/`.startsWith` loosening), and a
whitespace-padded value (` pg_cron ` — locks the exact `===`, no `.trim()` here unlike
Version); only the exact `pg_cron` ENABLES it, and the restart-triggering
`installExtension` fires only after, with `confirm: "pg_cron"`. It also asserts the
server-wide-restart consequence copy is on screen before the operator can act. Test 2
pins the safe path: a Tier-1 ("ready") add opens NO dialog and posts `confirm: ""` —
never a restart token. No bug — both paths correct; tests lock the contract.

Mutation-proven over 7 one-line source mutations (each reds the suite, tree clean each
time): loosen `===` to `.startsWith(name)`/`name.startsWith(typed)`/`!== ""`; drop
`!confirmOk` from disabled; swap the confirm token to `"" : typed`; invert the
`ext.tier === "ready"` routing; `needsRestart = false`.

Review: `feature-dev:code-reviewer` clean (faithful real-path test, correct API-arg
expectations, matches Version/RolesDatabases conventions). The **test-skeptic** flagged
`confirm: needsRestart ? typed : ""`→`? ext.name : ""` as a passing mutation. I verified
it and REJECTED it as behavior-preserving: the button only enables when `typed ===
ext.name`, so the two emit the identical wire value on every reachable path, AND the
server independently re-checks (`core.RequireConfirmation` → exact `Typed == name`,
`extensions.go:292`) — so it's a client-side refactor, not a safety break, and no
UI-driven test can distinguish them without pinning implementation detail. Recorded as
an active rule. Web gate green: typecheck clean, build OK, **169/169** vitest (+2).
Backend untouched (no Go changed). e2e N/A (pure RTL).

---

## Iter #10 — 2026-07-01 — A/web-version (band 1 correctness) — SHIPPED (test only; no bug)

Mode A on the two irreversible confirm gates in `web/src/views/Version.tsx`:
`FinalizeDialog` (permanently deletes the old cluster — "point of no return", no
rollback after) and `RollbackDialog` (permanent data loss — discards every write
made against the new major since the upgrade). Both are typed-confirmation gates
and had NO test (there was no `Version.test.tsx`). These are the two most
destructive one-click actions in the panel, so the "confirm-on-risky" invariant
here was entirely unproven.

Added `web/src/views/Version.test.tsx` (2 tests, one per dialog), driving the real
path via the exported `PendingFinalizationBanner`. The load-bearing detail the
tests pin: the two dialogs demand DIFFERENT numbers — finalize wants the OLD major
(`from_major`, the cluster it deletes), rollback wants the NEW major (`to_major`,
the version it abandons). With `from=16, to=17`, each test types the OTHER dialog's
number first and asserts the confirm button stays DISABLED, then types its own
(whitespace-padded, to lock the `.trim()`) and asserts ENABLED, then asserts the
destructive API fires only after the gate is satisfied and with the correct major.
Each test also asserts the irreversible-consequence copy is on screen before the
operator can act. No bug — both gates are correct; the tests lock the contract.

Mutation-proven over 13 one-line source mutations (each reds the matching test,
baseline 2-passed, tree clean after each revert): finalize from→to cross-wire,
rollback to→from cross-wire, drop each dialog's `disabled={busy || !matches}` gate,
drop `.trim()`, cross-wire each dialog's API argument, and — the test-skeptic's
escapes — loosen `=== expected` to `.includes`/`.startsWith`/`Number(typed) ===`
on each dialog. The skeptic caught that the first draft only fed the wrong-exact
and right-exact strings, so a substring/prefix/numeric-coercion gate stayed green
(a fat-fingered "169"/"16.0" would fire the irreversible delete): added a
superstring reject ("169"/"179") AND a numeric-equivalent reject ("16.0"/"17.0")
to each test before commit, and re-verified all three escapes now red on both
dialogs. code-reviewer clean (faithful real-path test, not hollowed by the
api/toast mocks; matches `RolesDatabases.test.tsx` conventions). Full web gate
green: typecheck, build, 167/167 vitest. Backend gate untouched (no Go changed).

## Iter #9 — 2026-07-01 — A/store (band 1 correctness) — SHIPPED (test only; no bug)

Mode A on the single-row `CHECK (id = 1)` guard on the `instance` and `auth`
singleton tables (`internal/store/schema.go:15,33`). Every accessor hardcodes
`WHERE id = 1` (`instance.go:17,60`; `auth.go:17,65,86,105`), so a second row would
silently diverge — `GetInstance`/`GetAuth` keep reading id=1 while an id≠1 row lives
on unseen. The DB-level CHECK is the only thing making a second row impossible, and
NOTHING tested it: the existing `COUNT(*)=1` assertions all write through the id=1
accessors, so they'd still pass with the CHECK dropped.

Added `TestSingleRowCheckRejectsSecondIdentityRow` to `store_test.go` (table-driven
over both tables): a RAW `INSERT` of a non-1 id must be refused by the DB with
`CHECK constraint failed`. Positive control (id=1 accepted) proves the row is
otherwise valid, so the id≠1 rejection can only be the CHECK — not a stray NOT
NULL/type failure (false green). Probes id=2 (with the id=1 row present, so it's
the CHECK, not PK uniqueness), id=0, AND id=-1. No bug — the CHECK is correct; the
test locks the contract.

Mutation-proven: dropping the CHECK on instance, dropping it on auth, weakening
`= 1` → `>= 1`, and (the test-skeptic's escape) weakening `= 1` → `id * id = 1`
each red the matching subtest. The `id * id = 1` mutation is the reason id=-1 is
probed: it admits a negative second identity row while still rejecting 0 and 2, so
an id=0/id=2-only test stayed green against it — the skeptic caught this on the
first draft; strengthened before commit and re-verified caught. code-reviewer clean.

## Iter #8 — 2026-07-01 — A/store (band 1 correctness) — SHIPPED (test only; no bug)

Mode A on `(*Store).SaveInstance` (`internal/store/instance.go:36`). It upserts the
single (id=1) instance-identity row; its `ON CONFLICT(id) DO UPDATE SET`
deliberately OMITS `created_at` so the panel's recorded birth time survives every
later re-save (a `panel_version` bump on upgrade, a label edit, a `pg_system_id`
capture). `TestInstanceRoundTrip` only ever saved once, so the preserve-on-re-save
contract — and the fact the rest of the row still updates — was unproven. A one-line
regression (`created_at = excluded.created_at` in the UPDATE set) would silently
reset an install's birth time on the next save.

Added `TestSaveInstancePreservesCreatedAtOnResave` to `store_test.go`: save with a
fixed `birth` → re-save with EVERY field changed AND a different `CreatedAt` →
assert `created_at` still equals `birth` (and did NOT adopt the re-save's value),
every other column reflects the re-save, and `COUNT(*)=1` (single-row invariant).
The birth time is supplied in a **non-UTC zone** (`+05:30`) and the test asserts
the RAW stored TEXT is canonical UTC (`...Z`), not the parsed instant — because
`Time.Equal` compares instants and is blind to the zone. Added
`TestSaveInstanceStampsCreatedAtWhenZero`: a zero `CreatedAt` must be stamped
`now`, never persisted as `0001-01-01`. No bug — the code is correct; the tests
lock the contract.

Mutation-proven (4 mutations, each reds the suite; `instance.go` restored clean
each time): `created_at = excluded.created_at` in the UPDATE set, `ON CONFLICT(id)
DO NOTHING`, dropping `created.UTC()` on the write path, and dropping the
`if !created.IsZero()` zero-fallback guard.

Review: code-reviewer clean. test-skeptic found ONE escaping mutation on the first
draft — dropping `created.UTC()` slipped past a UTC-only `birth` + instant-comparing
`Time.Equal` (a real canonical-UTC-storage regression); it also flagged the
untested zero-fallback branch. Addressed both before commit (raw-string
UTC assertion + the zero-value test), then re-verified both mutations are now
caught. Gates green (gofmt, vet 0, `go test ./...` 0, static build). web untouched;
pure unit, no e2e.

## Iter #7 — 2026-07-01 — A/store (band 1 correctness) — SHIPPED (test only; no bug)

Mode A on `(*Store).InitAuth` (`internal/store/auth.go:44`). Its docstring claims
it "overwrites any existing row (used by install and reset-password)" and its
`ON CONFLICT(id) DO UPDATE` resets `failed_attempts=0`/`locked_until=NULL` — but
the only test (`TestAuthRoundTrip`) calls `InitAuth` once, hitting only the
INSERT. The reset-password UPDATE branch (and its security-critical session-secret
rotation) was entirely unproven at the store level. The live caller `SetPassword`
routes existing accounts to `SetPasswordHash`, so nothing else exercises it either
— a broken `ON CONFLICT` clause would silently violate the documented contract.

Added `TestInitAuthOverwritesExistingRowAndResetsLockout` to `store_test.go`:
init → lock the account (`SetLockout 5, +1h`) → re-init with a new hash+secret →
assert the reset (1) overwrites the hash, (2) **rotates the session secret** (so
tokens minted under the old secret can't be replayed after a reset), (3) clears
`failed_attempts`+`locked_until`, (4) bumps `updated_at`, and (5) updates the
single row in place (`COUNT(*)=1`, no second row). No bug — the code is correct;
the test locks the contract.

Mutation-proven: keep-old-hash, drop-secret-rotation, keep-old-failed_attempts,
keep-old-locked_until, `DO NOTHING`, and `updated_at=auth.updated_at` (keep-stale)
each red the test; baseline green; `auth.go` restored clean each time.
code-reviewer clean. test-skeptic found ONE escaping mutation (stale `updated_at`)
on the first draft → added the strict-`After` assertion (flake-safe: a full
GetAuth round-trip + asserts run between the two writes) → re-verified caught.
Gates green (gofmt, vet 0, `go test ./...` 0, static build). web untouched; pure
unit, no e2e.

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
