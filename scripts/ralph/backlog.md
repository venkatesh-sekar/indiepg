# Backlog

One item per iteration, highest band first. When you finish an item, check it
off and add a dated line to `progress-current.md`. When you drop an item, delete
it with a one-line reason. Keep this list alive — append concrete items when it
runs thin (auditing the panel against the north star is itself a valid iteration).

Format: `- [ ] (band) item — acceptance: how we know it's done`

## 0 · Foundation
- [x] (0) Add vitest + React Testing Library to `web/`; wire `npm test`; add one real test for an existing component — done 2026-06-24: vitest run + RTL/jsdom, config in vite.config.ts, `ui.tsx` component test (6 tests green).
- [x] (0) Add root `AGENTS.md` capturing build/test/run commands + conventions (read-only role, confirm-on-risky, best-defaults) so every iteration is consistent — done 2026-06-24: `AGENTS.md` at repo root references all `make` targets, the web npm scripts/verify gate, and links `scripts/ralph/DEFAULTS.md`; conventions section mirrors README invariants. Reviewed (one doc fix applied).
- [x] (0) Wire the web verify gate into the loop reality: ensure `npm run typecheck && npm run build` pass clean from a fresh `npm ci` — done 2026-06-24: verified the full gate green from a fresh `npm ci` (typecheck/build/test all pass; build is deterministic — committed `internal/server/web/dist` unchanged, tree stays clean). Made the gate executable instead of prose: added `make verify` (fmt-check → vet → test → build), `make verify-web` (npm ci → typecheck → build → test), and `make fmt-check` (the `gofmt -l` must-be-empty check, which `go fmt` does not enforce; fails on unparseable files too). AGENTS.md points at them. Foundation band complete → next is band 1 (security).

## 1 · Security
- [ ] (1) Audit session auth: confirm cookies are HttpOnly + SameSite + Secure-where-applicable, session expiry/rotation on login, logout invalidates server-side — acceptance: tests covering each property in `internal/auth`.
- [ ] (1) Confirm CSRF protection on every state-changing endpoint (or that the auth model makes it moot) — acceptance: a test that a forged cross-origin POST is rejected.
- [ ] (1) Verify the read-only role is enforced at the DB level (not just UI): the read-only pool/role cannot write even if the API is bypassed — acceptance: a test issuing a write through the read-only role fails at Postgres.
- [ ] (1) Secrets at rest: S3 creds, Pushover tokens, admin hash in the state DB are never logged and the DB file is `0600` — acceptance: test asserting file mode + a log-scrubbing test.
- [ ] (1) Login brute-force protection: rate-limit / lockout after N failed attempts with backoff — acceptance: test that the (N+1)th rapid attempt is throttled.

## 1.5 · Data durability
- [ ] (1.5) Surface "last good backup was N ago" prominently (Dashboard + Backups) — acceptance: UI shows last successful backup time + state; test for the backend field.
- [ ] (1.5) Loud alert when a scheduled backup fails or hasn't succeeded within its window — acceptance: alert rule + test firing on a stale/failed backup.
- [ ] (1.5) Nudge backups off-host: make S3 the recommended default and warn clearly when only local backups exist — acceptance: UI conveys local-only risk; covered by a test.
- [ ] (1.5) Restore-test surfacing: show when the last restore-test ran and whether it passed — acceptance: backend field + UI; test.

## 2 · Stability
- [ ] (2) Audit every web API call for explicit error + loading + empty states (no silent failures, no infinite spinners) — acceptance: per-view, an error path renders a clear message; tests once vitest exists.
- [ ] (2) Provisioning is idempotent: re-running setup on an already-provisioned box is safe and reports "already done" — acceptance: test that a second provision is a no-op.
- [ ] (2) Verify query-box guards: auto-LIMIT and statement_timeout actually applied to the read pool — acceptance: tests asserting both.

## 2.5 · Resource & config safety
- [ ] (2.5) Disk headroom alert: warn well before the data/WAL volume fills (default threshold) — acceptance: alert rule + test.
- [ ] (2.5) Self-healing config: applying a postgresql.conf change (default or override) that prevents Postgres from starting auto-rolls-back to last-known-good and surfaces the error; PG never left down — acceptance: test simulating a bad setting → rollback → PG up.
- [ ] (2.5) Host-sized tuning at provision time using DEFAULTS.md math (shared_buffers/work_mem/effective_cache_size/max_connections from detected RAM/CPU; workload profile selectable, default mixed) — acceptance: test of the sizing function against sample RAM values.
- [ ] (2.5) Connection saturation alert: warn as active connections approach max_connections — acceptance: alert rule + test.

## 3 · Usability
- [ ] (3) Provision flow shows the computed best-defaults up front with each override clearly labeled by effect; safe to accept blindly — acceptance: UI lists defaults + overrides; copy reviewed.
- [ ] (3) Every destructive action's confirm dialog states exactly what will happen and what is irreversible — acceptance: audit of ConfirmDialog usages; each has explicit consequence text.
- [ ] (3) PgBouncer as an opt-in pooler: a simple toggle that, when on, installs/configures with DEFAULTS.md pool math and shows the app connection string (via pooler) — acceptance: backend + UI + test; off by default.

## 4 · UI redo (shadcn)
- [ ] (4) Scaffold Tailwind + shadcn into the Vite app without regressing existing views (config, base components, one pilot view migrated) — acceptance: build green, pilot view visually intact, test passes.
- [ ] (4) Migrate remaining views to shadcn one per iteration (Login, Dashboard, RolesDatabases, Query, Backups, Migrate, Alerts, Settings) — acceptance per view: same behavior, a test, no new complexity.
