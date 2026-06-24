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
- Integration tests that boot a throwaway cluster via the OSRunner: `pg_ctl
  start/restart` MUST get `-l <logfile>`, else the daemonized postmaster inherits
  the runner's captured stdout pipe and never closes it → `cmd.Run` blocks FOREVER
  (the test hangs to the `go test` timeout). To force a DETERMINISTIC postmaster
  boot failure (e.g. to exercise restartWithRollback), use a cross-GUC constraint
  like `max_connections=1` (reserved 3 + max_wal_senders 10 >= it) — NOT an
  oversized memory GUC (shared_buffers), which Linux overcommit lets mmap
  succeed/stall instead of failing fast, so the test hangs. To exercise a path
  that calls `systemctl restart postgresql` on a cluster with no systemd unit,
  wrap the OSRunner and translate that exact 2-arg invocation into `pg_ctl
  restart <dataDir>`; strip `AsUser="postgres"` and inject PGHOST/PGPORT/PGUSER so
  psql (run as the current user) hits the throwaway socket.
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
- ALERTING IS NOW WIRED (2026-06-24): `internal/server/background.go` runs the
  loop. `ListenAndServe`→`startBackgroundJobs(ctx)` seeds DefaultRules idempotently
  (insert-missing-IDs only; never clobbers operator edits) and registers a
  `telemetry-sample` scheduler job on `cfg.Schedules.TelemetrySample`; each tick
  `runTelemetryCycle` = Collector.SampleOnce → Engine.Evaluate → dispatch to
  enabled stored channels. The `scheduler.Scheduler` (robfig/cron wrapper) was
  ALSO never instantiated in prod before this — which means SCHEDULED BACKUPS have
  never run either (only `POST /backups/run`). Registering the
  full/incremental/restore-test/digest jobs in background.go is the new top
  band-1.5 item. Pattern for wiring a periodic job: add it in startBackgroundJobs
  via `s.sched.Register(name, cfg.Schedules.X, fn)`; empty spec disables it.
  Also: the real pg.Sampler
  (internal/pg/sampler.go) only reads host /proc + PG stats; it NEVER populated
  the backup.* snapshot fields, so `backup.last_age_seconds` was always 0.
  `telemetry.Collector.enrichBackup` now folds backup age + last-failed from the
  store (ListBackups(ctx,1) → newest terminal row; LatestSuccessfulBackup → age),
  since the Collector — not the Sampler — is the seam that has the store. Mirror
  this whenever a metric needs store/DB data the host/PG sampler can't see.
- Two SEPARATE metric-key namespaces exist and they DIVERGE: telemetry/snapshot.go
  uses dotted keys (`host.cpu.percent`) for buffering+OTLP; alert/metrics.go has
  its OWN constants (`host.cpu_percent`) and its own `metricValue(snap, key)`
  switch that reads Snapshot *fields* directly. Alert rules match against the
  alert-package keys, NOT the telemetry ones. When adding an alert metric, add the
  field to telemetry.Snapshot AND a case in alert/metrics.go's metricValue (+ a
  telemetry MetricKeys/Value/exporter-metadata entry if it should also be buffered/
  exported; exporter_test enforces description+unit for every MetricKeys entry).
