# Database Migration — Design

**Status:** Approved, ready for implementation
**Date:** 2026-06-22

## Problem

The panel's migration endpoints (`POST /migrate/sessions`, `GET`/`DELETE
/migrate/sessions/{code}`, `POST /migrate/single-db`, `POST /migrate/cluster`)
are hardcoded stubs in `internal/server/handlers_migrate.go`. Every one returns:

> migration is not available in this build
> migration requires S3 object storage and a dump/restore engine that are not configured

That message is **misleading**: the handlers never read any config, so the error
fires regardless of whether S3 is configured. The truth is the feature was never
wired in. The session-coordination half (`internal/migrate.Service`) is fully
implemented and unit-tested, but:

1. The actual `pg_dump`/`pg_restore` data-movement engine was never built.
2. The server never imports `internal/migrate`, has no migrate field, and never
   constructs the Service. The five handlers are placeholder stubs.

Inspiration: `/primary01/git/server-management` (`sm postgres migrate`), a Python
CLI that does this across two hosts. indiepg is a single-binary **web panel**, so
the design adapts that flow to a browser-driven, no-SSH context.

## Goals

- Both migration topologies, with the panel presenting the choice and recommending
  the safer one:
  - **Direct pull** — import from any reachable Postgres (no S3, no second panel).
  - **Cross-panel handshake** — move from another indiepg server over a shared S3
    bucket (the existing code-handshake flow).
- Long-running work runs as a background job the UI polls; progress surfaced
  wherever possible.
- Destructive imports are gated behind explicit, typed confirmation.
- The "S3 required" error is only ever shown for the mode that truly needs S3.

## Architecture & modes

**Two user-facing modes, one engine.**

- **Direct pull** *(recommended in the UI)* — user supplies a source connection
  (host/port/user/password/db or DSN). The panel runs `pg_dump` locally against
  that source and `pg_restore` into its own Postgres. No S3, no second panel.
  Scope is `single-db` (one database) or `cluster` (all non-template databases +
  globals/roles via `pg_dumpall -g`) — matching the two existing endpoints.
- **Cross-panel handshake** — the existing code-handshake
  (`internal/migrate.Service`): target creates a session → 6-char code → source
  panel joins with the code and exports to S3 → target downloads and imports.
  Requires S3 on both ends.

**One engine underneath.** Both modes are a row in a new local `migrations` table
driven by a background worker through the **same status machine** already defined
in `internal/migrate/session.go`:

```
waiting_for_export -> exporting -> exported -> importing -> completed
```

with `failed` / `expired` as terminal off-ramps. Direct pull is "the easy case"
where this panel plays both source and target, so the worker walks the whole chain
itself.

**Layering:**

- `internal/migrate` — gains a `PgEngine` (pg_dump/pg_restore/pg_dumpall wrappers
  over `exec.Runner`) and an orchestrator that drives a session row through its
  phases.
- `internal/store` — new `migrations` table + queries (the panel's source of
  truth).
- `internal/server` — real handlers replacing the five stubs, plus the worker.

## Source of truth: local store, not S3

The **local store (SQLite) is this panel's source of truth** for every in-flight
and historical migration — status, phase, progress numbers, errors, row-count
verification. It is what the progress UI polls, what backs a migration history
list, and what the audit log hangs off of.

The **S3 session doc is only the cross-panel coordination channel**, used by the
handshake mode on top of the local record. `internal/migrate.Service` keeps doing
that job, unchanged.

Net effect: **direct pull works with zero S3 configured**; only the handshake mode
requires S3, and only then is the honest "S3 required for cross-panel migration"
message shown.

## Data model

### `migrations` table (local store, source of truth)

| column | purpose |
|---|---|
| `id` | PK, also the public job id the UI polls |
| `mode` | `direct-single`, `direct-cluster`, `ssh-less` |
| `status` | the `Status` state-machine value |
| `phase` | finer step: `validating`/`dumping`/`uploading`/`downloading`/`restoring`/`verifying` |
| `source_summary` | host:port/db for display — **never the password** |
| `target_database` | where it lands |
| `overwrite` | bool — was a destructive replace authorized |
| `code` | handshake code (NULL for direct) |
| `progress_done` / `progress_total` | best-effort counts (bytes or TOC/db items) |
| `bytes_total` | dump size when known |
| `error` | scrubbed failure message |
| `row_counts_src` / `row_counts_tgt` | JSON, for verification diff |
| `created_at` / `updated_at` / `finished_at` | timestamps |

Credentials are **never** a column — the source password lives only in the worker
goroutine's memory for the job's lifetime.

The S3 session doc (`internal/migrate.MigrationSession`) stays exactly as-is, used
only by `ssh-less` mode. The local row mirrors it for this panel's UI.

### `PgEngine` (new, in `internal/migrate`, over `exec.Runner`)

- `Version(ctx, conn) (string, error)`
- `ListDatabases(ctx, conn) ([]DatabaseSize, error)` — excludes templates
- `DatabaseExists(ctx, conn, name) (bool, error)`
- `Dump(ctx, conn, db, outPath, opts) (DumpInfo, error)` — `-Fc`, compression,
  checksum
- `DumpGlobals(ctx, conn, outPath) error` — `pg_dumpall -g` (cluster)
- `Restore(ctx, conn, dumpPath, targetDB, opts) error` — `-j` parallel,
  `--clean` when overwrite
- `RowCounts(ctx, conn, db) (map[string]int64, error)` — for verify

`DumpInfo` / `DatabaseSize` mirror the inspiration. A `connInfo` struct carries
host/port/user/password/sslmode; its `String()` / log form **redacts the
password**.

## Execution flow & the worker

A single background worker per migration, spawned as a goroutine when a session is
created, writing progress to the `migrations` row as it goes. HTTP handlers never
block on the job — they create the row, kick the worker, and return the job id.

**Direct pull (`single-db`):**

1. `validating` — connect to source, check `pg_dump`/`pg_restore` exist, resolve
   versions, confirm target-safety policy (refuse non-empty target unless
   `overwrite`). Fail here = nothing moved.
2. `dumping` — `pg_dump -Fc` source → temp file under a private per-job work dir
   (`0700`). Record `bytes_total`, checksum.
3. `restoring` — create target DB fresh (or drop/recreate if `overwrite`
   confirmed) → `pg_restore -j`.
4. `verifying` — compare source vs target row counts via `CompareRowCounts`;
   mismatches → `failed` with the diff.
5. `completed` — delete the temp dump.

`cluster` is the same with `pg_dumpall -g` globals first, then a loop over each
database; the row's progress counts databases done/total.

**Cross-panel handshake (`ssh-less`):** unchanged coordination via the S3 session
doc — target creates/polls, source exports+uploads, target downloads+restores+
verifies. The local row mirrors each S3 status change so the UI shows the same
progress. This is the only mode that errors cleanly with "S3 required" when S3 is
absent.

**Progress, best-effort:** phase is always known. Within `uploading`/`downloading`
we track bytes against `bytes_total`. Within `dumping`/`restoring` (custom format
gives no clean percent) we surface phase + elapsed, and for `cluster`,
databases-done/total. **No fake percentages.**

**Temp files:** per-job `0700` dir (e.g. `/var/lib/indiepg/migrate/<id>/`), always
cleaned on terminal state (deferred), even on failure.

## Target safety & credentials

- **Default: refuse if the target database exists and is non-empty.** The session
  fails fast in `validating` before any data moves.
- **Importing into a brand-new database is the happy path** — the panel
  `CREATE DATABASE`s it fresh. Target name defaults to the source name and can be
  renamed (the inspiration's `-t myapp_migrated` pattern), so the safe path is
  "import alongside," not "overwrite."
- **Overwrite is gated:** the user must explicitly choose "replace existing
  database," and the UI makes them re-type the database name to confirm
  (server-side echo check). Only then does the worker drop/recreate.
- **Source credentials** are used for the job and never persisted — held in memory
  for the worker's lifetime, never written to the store, never logged, scrubbed
  from any error text. The handshake mode sidesteps this since each side uses its
  own local Postgres.

## Errors, lifecycle, security

**Error handling.** Every failure sets `status=failed` with a scrubbed, typed
`core.Error` and leaves the target untouched where possible (validation fails
before any write; restore into a *fresh* DB never touches existing data). Passwords
are stripped from all error text and logs via the redacting `connInfo`.
`pg_restore` warnings-vs-errors are distinguished like the inspiration (fatal only
on `error:` / `fatal:`).

**Lifecycle / restart.** A worker is an in-process goroutine, so a panel restart
orphans in-flight jobs. On startup the server marks any `migrations` row still in a
live (non-terminal) status as `failed` with "interrupted by restart" — no zombie
"running forever" rows — and sweeps temp dirs from dead jobs. Expiry is enforced on
read for handshake sessions (already in the Service).

**Security.** Mutating endpoints stay behind the existing auth middleware; every
create/cancel writes an audit entry (the stubs currently skip audit — real ones
won't). Overwrite requires the typed-name confirmation echoed server-side. Source
credentials never hit the store, logs, or S3.

## Testing (TDD)

- `PgEngine` — `exec.Runner` fake asserts exact `pg_dump`/`pg_restore` argv;
  password redaction in `connInfo.String()`.
- Orchestrator — fake engine + fake ObjectStore drive a session through every phase
  and every failure off-ramp; assert row/state transitions and temp-file cleanup.
- Store — `migrations` CRUD + the restart-sweep query.
- Handlers — `httptest` over all five endpoints: validation errors, the honest
  "S3 required" only for handshake, audit entries written.
- Reuse the existing `handlers_reconciled_test.go` pattern so routes stay covered.

## Out of scope (YAGNI for v1)

- Direct SSH-pipe transport (the inspiration's `--direct`); direct pull connects
  over the Postgres wire protocol instead.
- Resumable/chunked transfers; a failed job is re-run, not resumed.
- Scheduling / recurring migrations.
