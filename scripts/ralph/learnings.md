# Learnings

Active rules-of-thumb discovered while working. Keep it short — compact toward
the top, prune stale entries. One line each. Newest at the bottom of each group.

## Build / test / verify
- Verify gates: `gofmt -l $(git ls-files '*.go')` (must be empty), `go vet ./...`,
  `go test ./... -count=1`, `CGO_ENABLED=0 go build ./cmd/indiepg`. Web: `npm run
  typecheck && npm run build` (and `npm test` once vitest is added).
- The tracked tree must be clean at the end of every iteration; untracked
  sandbox dotfiles are ignored by the loop and must not be deleted.

## Conventions
- Single trusted operator, private instance. Most-secure option always wins.
- Best-defaults-first; overrides optional and labeled with their effect.
- Trusted Postgres/PgBouncer/pgBackRest defaults live in DEFAULTS.md (ported
  from the `sm` CLI at /primary01/git/server-management/src/sm/).

## Gotchas
- `REVOKE CREATE ON SCHEMA public FROM <role>` does NOT remove CREATE the role
  inherits via the `PUBLIC` pseudo-role (PG <= 14 grants public.CREATE to PUBLIC
  by default). To truly deny a role object creation you must revoke from PUBLIC
  (and re-grant to the roles that need it). FIXED for indiepg_readonly: provisionSQL
  now REVOKEs CREATE FROM PUBLIC + GRANTs to indiepg_admin, scoped to the `postgres`
  DB only (provisionSQL never runs against app DBs — revoking there would break
  apps). Leave USAGE alone so the SELECT path survives.
- Idempotent role provisioning via a DO-block IF/ELSE must repeat EVERY desired
  attribute on the ALTER (else) branch, not just CREATE. Dropping a word (e.g.
  NOINHERIT) on ALTER means a re-provision silently reverts the role to the PG
  default for that attribute (INHERIT). Assert security-load-bearing attrs on both
  paths (e.g. `strings.Count(joined, "NOINHERIT") >= 2`).
- This shell has an empty `$TMPDIR`; `mktemp -d "$TMPDIR/..."` resolves to `/...`
  and fails. Use `/tmp` explicitly (or `export TMPDIR=/tmp`) for throwaway clusters.
- DB-level role/privilege behavior can only be proven against a real Postgres.
  Pattern: integration-tagged test, `//go:build integration`, skips unless
  `INDIEPG_TEST_SOCKET` is set. To prove green locally, stand up a throwaway
  cluster: `initdb -A trust -U postgres` + `pg_ctl -o "-c listen_addresses=''
  -c unix_socket_directories=<dir>" start`, apply provisionSQL's role stmts via
  `psql`, point `INDIEPG_TEST_SOCKET` at the socket dir. Binaries in
  `/usr/lib/postgresql/14/bin`. The loop's `go test ./...` does NOT pass
  `-tags integration`, so these never run in the normal gate (by design).
- `go` here is a snap; the command sandbox blocks snap-confine
  (`cap_dac_override` missing). Run go/psql/pg_ctl with the sandbox disabled.
- Tampering a base64 value by flipping its LAST char can be a no-op: the final
  RawStdEncoding char of an N-byte blob whose length isn't a multiple of 3 carries
  padding bits that decode to nothing (a 32-byte key → 43 chars, last char = 4
  real bits + 2 padding). To reliably corrupt encoded bytes in a test, decode →
  flip a byte → re-encode, never flip a base64 char. (Flaked the auth tamper test.)
- To keep a secret out of logs/errors defensively (not just at known call sites),
  make the secret-bearing struct render itself redacted: implement `fmt.Stringer`
  AND `slog.LogValuer` AND `fmt.GoStringer`. String() covers `%v/%+v/%s` and a
  parent's `%+v` (fmt recurses into struct fields and calls their String());
  LogValue() covers slog structured logging (the panel's core.Logger); GoString()
  is required because `%#v` bypasses String() (testify diff output uses %#v).
  Helpers: `core.Redact(string)` / `core.RedactBytes([]byte)` → fixed `REDACTED`
  marker, "" when empty, never reveals length. Secret-bearing structs today:
  config.S3Target, store.AuthRecord, server.alertChannelConfig. Note LogValuer
  does NOT affect encoding/json — JSON API output still relies on `json:"-"`.
- Distinguish the two read-only refusal SQLSTATEs: `25006`
  (read_only_sql_transaction = the defense-in-depth GUC fired) vs `42501`
  (insufficient_privilege = the authoritative privilege-denial boundary). Assert
  42501 with the GUC off to prove the boundary isn't merely the resettable GUC.
