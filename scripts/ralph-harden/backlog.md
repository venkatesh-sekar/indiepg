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
- [ ] (1 · A) backup/restore — assert a full backup is actually restorable: seed data → backup → restore into a fresh cluster → row-for-row match. Extend the e2e scenario if unit coverage can't reach it.
- [ ] (1 · A/e2e) backup PITR (future/xid half) — with a live cluster, assert a TIME target in the future and an xid target beyond the latest committed xid are **rejected** (or handled loudly), not silently promoted-to-latest. Needs Docker/e2e — can't be range-checked at unit level (a future TIME target may be valid PITR into live WAL). See Iter #1: the before-earliest-backup half shipped.
- [ ] (1 · A) auth/session — assert logout invalidates the session **server-side** (a captured cookie is dead after logout), and that session fixation is impossible (new session id on login).
- [ ] (1 · A) auth — assert login brute-force lockout actually triggers and resets correctly; wrong-password does not leak whether the user exists.
- [ ] (1 · A) pg/guard — assert the read-only role truly cannot write at the **DB level** (INSERT/UPDATE/DELETE/DDL all rejected), not just hidden in the UI; and the query box enforces auto-LIMIT + statement timeout on real queries.
- [ ] (1 · A) config — assert atomic config writes preserve ownership/mode (0600 where required) and that the pre-change backup is created before the write.
- [ ] (1 · A) migrate — assert the direct-pull and S3-handshake paths verify the migrated data (row counts / checksums) and surface a mismatch loudly rather than reporting success.

### Band 2 — Preflight & fail-fast (Mode P)
- [ ] (2 · P) restore — preflight free disk + inodes against the backup size before restoring; refuse with a specific shortfall message instead of failing mid-restore.
- [ ] (2 · P) backup/S3 — preflight repo reachability + single-writer ownership marker; fail fast on a foreign owner before writing anything.
- [ ] (2 · P) migrate — preflight source reachability, target existence, and free space before starting; don't half-migrate then error.
- [ ] (2 · P) install/provision — preflight that no conflicting Postgres/cluster already exists and required ports are free; make provisioning idempotent + re-runnable.
- [ ] (2 · P) config write — parse/validate the new config (and, where possible, a dry `postgres -C`/`--check`) before replacing the live file.

### Band 3 — Durability
- [ ] (3) surface "last good backup" (age + result) on the Dashboard; loud, immediate alert when a scheduled backup fails or is overdue.
- [ ] (3) verify off-host (S3) backups are the default and a local-only config is clearly labeled as risky.

### Band 4 — Self-heal & defaults (Mode D)
- [ ] (4 · D) config self-heal — a bad change (incl. an operator override) that stops Postgres auto-rolls-back to last-known-good and alerts; prove it with a test that first breaks the config.
- [ ] (4 · D) host-sized tuning — confirm shared_buffers/work_mem/max_connections defaults match DEFAULTS.md and are sized to the host, with early alerts on disk/conn/WAL headroom.

### Band 5 — Clarity
- [ ] (5) audit destructive-action confirms: every one states exactly what it will do and requires typed-name confirmation; no secret is ever surfaced or logged.
- [ ] (5) audit empty/loading/error states across views (Login, Dashboard, Backups, Migrate, Query, Settings, Pooler, DatabaseTuning, Extensions, Version) for clear, non-confusing copy.

### Band 0 — Foundation (only if a gate is red or infra is missing)
- [ ] (0) if any `make verify` / `make verify-web` gate is red, fix it before anything else.

## Done

- [x] (1 · P) backup PITR (before-base half) — restore preflights the recovery
  target and rejects a TIME target earlier than the earliest available backup
  with a clear `CodeValidation` error, BEFORE the destructive safety-backup/
  cluster-stop/overwrite. Fail-open on uncertainty. `internal/backup/manager.go`
  (`preflightTargetReachable`, `earliestBackupStart`) +
  `internal/backup/restore_preflight_test.go`. Iter #1.
