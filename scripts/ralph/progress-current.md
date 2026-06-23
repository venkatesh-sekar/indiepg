# Progress (rolling)

Newest first. One short entry per iteration: date, band, what changed, why.
Keep ~20 entries; archive older ones if this grows large.

<!-- iterations will be prepended here -->

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
