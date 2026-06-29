# Migration "Drop-off link" mode — Design

**Status:** Approved, ready for implementation
**Date:** 2026-06-30
**Branch:** `feat/migrate-drop-off-link`

## Problem

The existing Migrate feature (design `2026-06-22`) has three modes, and every one
needs the panel to reach the source over the network:

- **Direct pull** (`single-db` / `cluster`) — the panel dials the source Postgres
  and runs `pg_dump` against it.
- **Cross-panel handshake** (`ssh-less`) — both ends are indiepg panels sharing an
  S3 bucket; each dumps its *own* local Postgres.

None of these works when the source Postgres sits behind NAT/a firewall with **no
inbound** access, the source host has **no panel installed** and **no AWS
credentials**, but it **can** reach the public internet (GitHub + S3). That is the
one ingress the feature lacks today.

**Drop-off link** closes it: the operator runs a one-line `curl … | sh` command
(mirroring `scripts/install.sh`) on the source box. That script dumps one database
and uploads it straight to S3 through a **presigned PUT URL** the panel minted —
no panel install, no AWS keys on the source, no inbound to the source. The panel
then imports from S3 exactly like the existing import worker, with the same
SHA-256 + row-count verification.

## Goals & non-goals

**Goals**

- Migrate **one** database from an unreachable-but-internet-connected source.
- Preserve the existing verification contract: SHA-256 integrity + per-table
  row-count parity (`CompareRowCounts`); only mark `completed` when every table
  matches.
- Reuse the engine, orchestrator verification, code generation, S3 store, the
  overwrite/typed-confirm helper, the SQLite store patterns, the worker
  machinery, and the SPA components. Add the *minimum* new surface.
- Best-in-class, dumb-proof UX: a copy-button command, numbered next-steps, live
  upload/import status, an expiry countdown, Start gated on upload-complete, and
  the same safety callouts the page already shows.
- Be honest about the one residual trust assumption (a drop link is a bearer
  *write*-credential to the target import).

**Non-goals (YAGNI for v1)**

- Multi-database / whole-cluster drop-off (single DB only; cluster stays
  direct/handshake).
- Resumable/multipart uploads. A single PUT is capped at 5 GiB; bigger ⇒ use
  direct-pull.
- MinIO **path-style** S3 endpoints (see Risks). Presigned URLs here are
  virtual-host style — correct for AWS S3 / Cloudflare R2 / Backblaze B2.

## Threat model (drives the security decisions)

In drop-off mode the **panel never connects to the source** — only the source-side
`migrate-push.sh` does. So:

- The panel handles **zero source DB credentials** — a security *win* over
  single-db/ssh-less, which pass a source password through the panel.
- The only secrets the panel mints are **two presigned S3 PUT URLs**. minio's V4
  signature binds each to **method (PUT) + bucket + one object key** (confirmed:
  `PresignedPutObject` → `presignURL(ctx, http.MethodPut, bucket, object, expires,
  nil, nil)`, `api-presigned.go:90`). A leaked PUT URL can only PUT that one key —
  it cannot GET, LIST, or touch any other key.
- **Honest residual:** whoever holds *both* presigned URLs can upload an
  attacker-chosen dump *and* a matching `meta.json` (with the correct SHA-256 of
  *their* dump). SHA-256 therefore protects against corruption/truncation **in
  transit**, not against a holder of the URLs. This is the same posture as the
  existing modes ("the person you hand the command to is trusted"), and the
  *only* real mitigations are the **short TTL** and the **"treat this command like
  a password"** UX framing. We must not over-claim integrity == authenticity.
- The bucket is assumed **private** (the same bucket pgBackRest already uses).
  Presigned PUT is the only write path; the panel's credentialed client is the
  only read path. So key *predictability* is not a security boundary — but codes
  stay crypto-random (`GenerateCode()`) to avoid collisions between concurrent
  sessions and to keep keys un-guessable as defense-in-depth.
- The mint endpoint sits behind `requireAuth` + `securityHeaders`, so only an
  authenticated admin can create a drop link.

## Architecture

One new transport on top of the existing engine + verification.

- **S3 is the only channel.** The source PUTs a dump + a `meta.json` manifest
  through presigned URLs; the panel reads both with its own credentialed client.
- **`meta.json` presence == upload complete & verifiable.** The script uploads the
  dump first, then `meta.json` **only after** the dump PUT returns 2xx. The panel
  treats meta-absent as "not uploaded yet" and meta-present as "ready to import".
- **Streaming import (improvement over ssh-less).** The panel streams the dump
  from S3 to disk via `FGetObject` — **no in-memory 1 GiB cap** (the ssh-less mode
  buffers the whole dump in a `[]byte`; this one does not). The dump size ceiling
  is enforced *panel-side* against the authoritative `StatObject` size.
- **Local store is the source of truth.** A new `dropoff_sessions` table tracks the
  drop session; when the operator clicks Start we also insert a `migrations` row
  (mode `drop-off`) so the import reuses the existing `storeRecorder` + the
  `GET /migrate/{id}` poll path the SPA already speaks.

**Layering**

- `internal/backup` — add `PresignPut`, `StatObject`, `DownloadToFile` to the
  concrete `*S3ObjectStore`.
- `internal/migrate` — new `dropoff.go`: drop-off constants, the `DropTransport`
  capability interface, `DropMeta`/`DropTable`, and
  `Orchestrator.ImportFromDrop` (a streaming sibling of `ImportFromSession`,
  **not** a fork of it).
- `internal/store` — new `dropoff_sessions` table + a typed CRUD/sweep file.
- `internal/server` — mint/get/start/cancel handlers, a drop import worker, the
  `s.drops` wiring, route registration, and a startup + periodic sweep.
- `web/` — a new "Drop-off link" tab + client methods + types, rebuilt `web/dist`.
- `scripts/migrate-push.sh` — the new source-side uploader.

## Why a new table, not new columns on `migrations`

The SQLite schema runner (`store.go:132-149`) executes the `schemaStatements`
list verbatim and **only** ever runs `CREATE TABLE/INDEX IF NOT EXISTS`. There is
no `ALTER`/additive-column path, and SQLite's `ADD COLUMN` has **no** `IF NOT
EXISTS`, so adding a column to the existing `migrations` table would crash
`migrate()` on the *second* startup. We add a **new** `dropoff_sessions` table via
`CREATE TABLE IF NOT EXISTS` (idempotent, no runner change).

## Data model

### `dropoff_sessions` table (new; local store)

`CREATE TABLE IF NOT EXISTS dropoff_sessions (...)` appended to
`schemaStatements`, plus an `expires_at` index. **Stores object keys only — never
the presigned URLs, never a password.**

| column | purpose |
|---|---|
| `id` | PK |
| `code` | crypto-random session code (`UNIQUE`) — the public handle |
| `migration_id` | FK-ish link to the `migrations` row created at Start (NULL until then) |
| `dump_key` | S3 key of the dump object (non-secret) |
| `meta_key` | S3 key of the `meta.json` object (non-secret) |
| `target_database` | where it lands on this panel |
| `overwrite` | bool — was a destructive replace authorized at mint |
| `status` | `waiting_for_upload` / `uploaded` / `importing` / `completed` / `failed` / `expired` |
| `error` | scrubbed failure message |
| `expires_at` | session deadline == the presigned-URL TTL |
| `created_at` / `updated_at` | timestamps |

`DropoffRecord` struct in `models.go` mirrors `MigrationRecord` field/JSON
conventions. CRUD lives in a new `internal/store/dropoff.go`:
`InsertDropoff`, `GetDropoffByCode`, `UpdateDropoff`, `ListExpiredDropoffs` (for
the S3-object sweep), and `SweepExpiredDropoffs` (mirrors
`SweepRunningMigrations:170` — but see the sweep section: marking the row is *not*
enough; the worker must also delete the S3 objects).

### `meta.json` manifest (uploaded by `migrate-push.sh`)

```json
{
  "schema_version": 1,
  "source_db": "myapp",
  "pg_version": "16.3",
  "sha256": "<hex sha256 of the dump>",
  "byte_size": 12345678,
  "created_at": "2026-06-30T12:00:00Z",
  "tables": [
    { "schema": "public", "name": "users", "rows": 1024 },
    { "schema": "public", "name": "orders", "rows": 8891 }
  ],
  "total_rows": 9915
}
```

**Hard cross-perspective contract — row-count parity.** `tables[]` MUST enumerate
exactly the set `engine.RowCounts` uses: `information_schema.tables` with
`table_type='BASE TABLE'` excluding `pg_catalog`/`information_schema`
(`engine.go:556-559`), with **exact** `count(*)` per table (never
`pg_stat_user_tables.n_live_tup` estimates). The panel derives the
`CompareRowCounts` map key as `schema + "." + name` to **exactly** match
`engine.RowCounts` (`engine.go:577`). Any divergence makes `CompareRowCounts`
(`session.go:190`) report false mismatches and the import never verifies. The
script lets **Postgres** emit the `tables`/`total_rows` JSON (`json_agg` over the
base-table set) so identifier escaping is correct — it never hand-builds the JSON
array in shell.

Count ordering mirrors the orchestrator: `pg_dump -Fc` snapshots at start, counts
are read **after** the dump completes. Writes on the source between dump and count
can cause a benign false mismatch — we keep the existing ordering and do not
"fix" it unilaterally.

## Drop-off types & constants (`internal/migrate/dropoff.go`)

```go
const (
    DropPrefix            = "pg-migrations/dropoff"   // never interpolate the DB name
    DropDefaultTTL        = 2 * time.Hour             // == session expiry
    MaxDropBytes    int64 = 5 << 30                   // 5 GiB == S3 single-PUT ceiling
    MaxDropMetaBytes int64 = 1 << 20                  // cap meta.json before json.Unmarshal
    DropMetaSchemaVersion = 1
)

func DropDumpKey(code string) string { return DropPrefix + "/" + code + "/dump" }
func DropMetaKey(code string) string { return DropPrefix + "/" + code + "/meta.json" }

// DropTransport is the capability surface the drop-off import needs from the S3
// store. It is satisfied by *backup.S3ObjectStore. The Service's 3-method
// migrate.ObjectStore stays intact (existing fakes keep compiling).
type DropTransport interface {
    PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
    StatObject(ctx context.Context, key string) (size int64, exists bool, err error)
    DownloadToFile(ctx context.Context, key, dest string) error
    GetObject(ctx context.Context, key string) ([]byte, error) // meta.json read
    DeleteObject(ctx context.Context, key string) error        // cleanup
}

type DropTable struct {
    Schema string `json:"schema"`
    Name   string `json:"name"`
    Rows   int64  `json:"rows"`
}
type DropMeta struct {
    SchemaVersion int         `json:"schema_version"`
    SourceDB      string      `json:"source_db"`
    PGVersion     string      `json:"pg_version"`
    SHA256        string      `json:"sha256"`
    ByteSize      int64       `json:"byte_size"`
    CreatedAt     time.Time   `json:"created_at"`
    Tables        []DropTable `json:"tables"`
    TotalRows     int64       `json:"total_rows"`
}

// SourceRowCounts builds the "schema.name" -> rows map keyed EXACTLY like
// engine.RowCounts so CompareRowCounts matches.
func (m DropMeta) SourceRowCounts() map[string]int64 { /* schema+"."+name */ }
```

`Orchestrator.ImportFromDrop(ctx, transport DropTransport, spec DropImportSpec,
rec Recorder)` is a **new, streaming** method (do **not** fork
`ImportFromSession`, which is welded to the S3 `session.json` and buffers the
whole dump). It:

1. `StatObject(metaKey)` — absent ⇒ a `CodeConflict` "not uploaded yet". Cap
   meta at `MaxDropMetaBytes` before `GetObject` + `json.Unmarshal`.
2. Parse `meta.json`; reject `schema_version != DropMetaSchemaVersion`.
3. `StatObject(dumpKey)` — the **authoritative** size. Refuse `size == 0`
   (empty dump) and `size > MaxDropBytes` with the `errDumpTooLarge`-style "use
   direct-pull for bigger" message. Cross-check `size == meta.ByteSize` (mismatch
   ⇒ truncated/partial upload, reject before download).
4. `DownloadToFile(dumpKey, <workDir>/import.dump)` — streaming, no in-memory cap.
5. `fileSHA256` (`engine.go:603`, streaming) vs `meta.SHA256` (lowercased hex);
   reject on mismatch — **before** `pg_restore`.
6. Re-gate overwrite at import time via `prepareTarget` / `validateTargetOverwrite`
   (`orchestrator.go:568,586`) — the destructive drop must be re-checked at the
   moment it happens, not only at mint (the target can become non-empty between
   mint and Start). Reuse `engine.Restore` with `NoOwner: true` and
   `Clean: spec.Overwrite`.
7. `engine.RowCounts` on the target; `CompareRowCounts(meta.SourceRowCounts(),
   tgt)`; any diff ⇒ fail.
8. On success: `DeleteObject(dumpKey)` + `DeleteObject(metaKey)` (best-effort,
   idempotent), `rec.Succeed`.

`pg_version`: if the source major > target major, **warn** (do not silently fail)
— a cross-major downgrade restore can fail; surfacing it gives a clear signal.

## HTTP endpoints

All under the authenticated `/api` group (`router.go:38`), registered next to the
existing migrate routes (`router.go:124-131`).

### `POST /api/migrate/drops` — mint a drop link

Request:
```json
{ "target_database": "myapp", "overwrite": false, "confirm": "" }
```
Behaviour:
- `s.drops == nil` ⇒ typed `errDropRequiresS3()` (CodeInternal, message contains
  "S3" so the SPA `/S3/i` callout fires), mirroring `errSSHLessRequiresS3`
  (`handlers_migrate.go:395`).
- `core.ValidateIdentifier(target_database, "database")`.
- If `overwrite`, `core.RequireConfirmation("overwrite database "+target, target,
  confirm)` (mirrors `handlers_migrate.go:118-124`) — typed `CodeSafety` on
  mismatch.
- `code := migrate.GenerateCode()`; two `PresignPut` calls (`DropDumpKey(code)`,
  `DropMetaKey(code)`) with `ttl = DropDefaultTTL`; `expires_at = now + ttl`
  (**TTL == expiry** so a leaked URL cannot write after the sweep).
- Insert the `dropoff_sessions` row (keys + target + overwrite + status
  `waiting_for_upload` + `expires_at`). **Persist only keys, never the URLs.**
- Audit logs **code + target only** (never the URLs).

Response (the URLs/command are returned **exactly once**, here):
```json
{
  "code": "K7P2QX",
  "target_database": "myapp",
  "overwrite": false,
  "expires_at": "2026-06-30T14:00:00Z",
  "command_docker": "curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh | sh -s -- --dump-url '<presigned-dump-url>' --meta-url '<presigned-meta-url>' --db <sourcedb> --docker <container>",
  "command_native": "curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh | sh -s -- --dump-url '<presigned-dump-url>' --meta-url '<presigned-meta-url>' --db <sourcedb> --host <host> --port 5432 --user <user>"
}
```

### `GET /api/migrate/drops/{code}` — poll session status

Enforces expiry-on-read (past `expires_at` and non-terminal ⇒ report `expired`)
and probes readiness via `StatObject(metaKey)` to flip `waiting_for_upload` →
`uploaded`. **Never re-serves the command or presigned URLs.** A page refresh that
loses the minted command is an accepted UX cost — the user re-mints (fresh URLs,
reset TTL). Response:
```json
{
  "code": "K7P2QX",
  "status": "uploaded",
  "target_database": "myapp",
  "overwrite": false,
  "expires_at": "2026-06-30T14:00:00Z",
  "migration_id": 0,
  "byte_size": 12345678,
  "error": ""
}
```
(`byte_size` is best-effort from `StatObject(dumpKey)` once present; `migration_id`
is non-zero after Start so the SPA can switch to the `GET /migrate/{id}` poll.)

### `POST /api/migrate/drops/{code}/start` — begin the import

- `s.drops == nil` ⇒ `errDropRequiresS3()`.
- Load the session; require `StatObject(metaKey).exists` — absent ⇒ `CodeConflict`
  "not uploaded yet, return after the command finishes".
- Insert a `migrations` row (`mode: "drop-off"`, `role: "target"`,
  `status: importing`, `phase: validating`, `target_database`, `overwrite`,
  `code`); link `migration_id` back onto the dropoff row; set dropoff status
  `importing`; spawn `runDropImportWorker(migrationID, code)`.
- Response: `{ "id": <migration_id>, "status": "importing" }` — the SPA polls the
  existing `GET /api/migrate/{id}`.

### `DELETE /api/migrate/drops/{code}` — cancel

`DeleteObject(dumpKey)` + `DeleteObject(metaKey)` (best-effort, idempotent), mark
the dropoff row `failed`/cancelled and any linked `migrations` row `failed`
(mirrors `handleCancelMigrationSession:358-389`). Audit `code` only.

## Server wiring

- `Server.drops migrate.DropTransport` field (nil when no S3).
- `dropTransportFor(cfg, log) migrate.DropTransport` — a free function mirroring
  `migrateServiceFor` (`server.go:303-319`): returns nil when
  `cfg.Backup.Bucket == "" && cfg.Backup.Endpoint == ""`; otherwise builds a
  `*backup.S3ObjectStore` from the backup S3 params (reusing the bucket the panel
  already uses) and returns it (the concrete type satisfies `DropTransport`).
  Wire it in `newServer` next to `migrate: migrateServiceFor(...)`.
- **Region caveat (see Risks):** `PresignedPutObject` builds the URL from the
  client's known region; pass `cfg.Backup.Region` through. If empty, minio falls
  back to a `getBucketLocation` network probe on first presign — set/require
  Region, or accept the one-time probe and surface a clear error if it fails.

## The import worker (`internal/server/dropoff_worker.go`)

`runDropImportWorker(migrationID int64, code string)` mirrors `runImportWorker`:

- `ctx, cancel := workerContext()` (the 6h backstop); `rec :=
  newStoreRecorder(s.store, migrationID)`.
- `tgt, err := s.localTargetConn(ctx)` (unix-socket peer auth, no password).
- `workDir, _ := jobWorkDir(migrationID)` (per-job `0700` dir).
- Load the dropoff row → `DropImportSpec{ DumpKey, MetaKey, TargetDatabase: tgt
  database, Overwrite }`.
- `orch := migrate.NewOrchestrator(s.migrateEngine, s.migrate, nil, workDir,
  s.log)` — **unchanged constructor signature** (it's depended on at
  `migrate_worker.go:199,219,262`). Pass `s.drops` as an **argument** to
  `ImportFromDrop`, not via the constructor.
- `orch.ImportFromDrop(ctx, s.drops, spec, rec)`.
- After the import returns, update the dropoff row's status (`completed`/`failed`)
  using the **`context.WithoutCancel` + re-bound timeout** pattern from
  `storeRecorder.Fail/Succeed` (`migrate_worker.go:124-139`) so a shutdown racing
  the terminal write can't wedge the row in `importing`.

Reuse `storeRecorder` for the `migrations` row so the SPA's existing
`GET /migrate/{id}` polling, verification rendering, and history "just work".

## S3 cleanup / expiry sweep (data hygiene — load-bearing)

A drop-off dump is the operator's **full database at rest in S3**. Marking only
the SQLite row expired (like `SweepRunningMigrations:170`) would leave that dump
in the bucket indefinitely. So the sweep must **delete the S3 objects** of
expired/abandoned sessions:

- `sweepExpiredDropoffs(ctx)` — list expired, non-terminal dropoff rows; for each,
  `DeleteObject(dumpKey)` + `DeleteObject(metaKey)` (best-effort) then mark the row
  `expired`.
- Wire it **both** at startup (next to `SweepRunningMigrations` in `New`,
  `server.go:170`) **and** on a periodic scheduler ticker (a new registered job in
  `background.go`, alongside the backup/restore-test jobs). A source can upload and
  then the operator never clicks Start while the panel keeps running for days —
  without a periodic sweep that dump lingers. Align `expires_at` with the
  presigned TTL so neither outlives the other.
- On success/cancel the worker/handler already deletes both objects; the sweep is
  the safety net for the abandoned case.

## `scripts/migrate-push.sh` — CLI contract

New POSIX-`sh` source-side uploader, hosted in the repo exactly like
`scripts/install.sh`, mirroring its conventions: `#!/bin/sh`, `set -eu`,
`say()`/`die()` with the `indiepg:` prefix, a Usage+Knobs header, tool-detection
preflight, and a `trap … EXIT` cleanup. Run `sh -n scripts/migrate-push.sh` (and
shellcheck if available) — strict POSIX, no bashisms (no `read -s`/`[[ ]]`/arrays).

**Invocation (mirrors `install.sh`'s `curl … | sh -s --`):**
```
curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh \
  | sh -s -- --dump-url '<url>' --meta-url '<url>' --db <sourcedb> --docker <container>
```

**Flags**
- `--dump-url <url>` (required) — presigned PUT for the dump.
- `--meta-url <url>` (required) — presigned PUT for `meta.json`.
- `--db <name>` (required) — source database to dump.
- `--docker <container>` — run `pg_dump`/`psql` via `docker exec -i <container>`.
- **XOR** `--host <h> [--port <p>] [--user <u>]` — native libpq connection.
- `--help` — usage and exit 0.
- Password: **`PGPASSWORD` env or an interactive `stty -echo` prompt only — never
  a `--password`/`-W` flag** (no leak in `ps`/argv). Docker mode passes it via
  `docker exec -e PGPASSWORD`. Mirrors `engine.go:103-106`.

**Behaviour (order is security-load-bearing)**
1. Validate args (required present; `--docker` XOR `--host`); preflight `curl` +
   (`pg_dump` or `docker`) exist with clear errors.
2. `pg_dump -Fc` (custom format, so the panel's `pg_restore` reads it,
   `engine.go:386-400`) to a `mktemp` file created `0600`, with `trap 'rm -f …'
   EXIT` so a full-DB dump never lingers world-readable in `/tmp`.
3. Check the `pg_dump` exit code; **refuse a 0-byte dump** before uploading.
4. Refuse a dump `> 5 GiB` (S3 single-PUT ceiling) with a "use direct-pull for
   bigger DBs" message (the panel guards too).
5. SHA-256 via `sha256sum` **or** `shasum -a 256` (portability, like
   `install.sh:83-86`).
6. Upload the dump: `curl -f -T <file> -H 'Expect:' '<dump-url>'` and verify 2xx
   before proceeding. **No extra signed headers / no Content-Type** — minio's
   presign signs only host with `UNSIGNED-PAYLOAD`, so any signed header would
   force a matching header or break the signature. `-H 'Expect:'` avoids the
   `100-continue` gotcha.
7. Build `meta.json`: let **Postgres** emit the `tables`/`total_rows` JSON
   (`json_agg` over the same base-table set as `engine.RowCounts`, exact
   `count(*)`); the script only injects escaped scalars (`source_db`, `pg_version`)
   and numbers. Write to a second `mktemp` `0600` (trap-cleaned).
8. Upload `meta.json` to `--meta-url` **only after** the dump upload succeeds
   (meta present == complete & verifiable — the panel's readiness gate).
9. Print "return to the panel and click **Start**". **Never echo the full
   presigned URL** in progress/error text (redact to scheme+host+path, strip the
   query token); never `set -x`.

## SPA — "Drop-off link" tab (`web/src/views/Migrate.tsx`)

A fourth `ModeTab` + `TabsContent` alongside `single-db` / `cluster` / `ssh-less`,
matching the existing styling and help-text tone.

- **Form:** target DB + overwrite checkbox, reusing the `SingleDBForm` typed-name
  confirm `Modal` (`Migrate.tsx:336-389`) for the destructive case (confirm at
  **mint** time; Start stays a single click).
- **Generated command:** rendered **once** from the mint response and kept in
  React state — mirror the "cannot be retrieved again" pattern. A `CommandList`
  copy block (reuse the one in `Extensions.tsx:624-657`) with a Copy button, a
  Docker/Native toggle, and obvious `<container>` / `<host>` / `--db <sourcedb>`
  "edit me" placeholders. A `"copy now — it won't be shown again"` note + a
  "Generate a new link" action (the GET endpoint never re-serves it).
- **Security callout:** *"Treat this command like a password — it lets the source
  upload one database to this server. It expires in ~2h, is used once, and is
  never stored."* (the honest mitigation for the both-URLs-holder threat).
- **Numbered next-steps:** run the command on the source → wait for the upload →
  click Start.
- **Live status badges:** `waiting_for_upload` → `uploaded` → `importing` →
  verified/failed, polled via `getDropOff`. **Start is disabled until
  `status === 'uploaded'`** (meta present).
- **After Start:** switch to the existing `DirectJobProgress`-style poll on the
  returned `migration_id` (reuse the stepper, `useVerification`/`VerificationView`,
  cancel/terminal patterns).
- **Expiry countdown:** a local 1s `Countdown` using `duration()` from
  `lib/format`.
- **Never render the presigned URL** in history/logs; the command never appears in
  `MigrationHistory`.
- Add `'drop-off'` to **every** exhaustive `Record<MigrationMode, …>` map
  (`MODE_LABELS`) and extend `MigrationStatus` with `waiting_for_upload` /
  `uploaded` so `tsc` stays green.

**Client + types** (`web/src/api/client.ts`, `types.ts`): add
`createDropOff` / `getDropOff` / `startDropOff` / `cancelDropOff` mirroring the
existing session methods; add `CreateDropOffRequest`, `DropOffCreated`
(`command_docker`/`command_native` — secrets present **only** here), and
`DropOffSession` (status/expiry/counts, **no** command/urls). Extend
`MigrationMode` with `'drop-off'`.

**Generalize the S3-absent callout:** `S3OrError` currently says "Cross-panel
migration needs S3" (`Migrate.tsx:1155-1165`); branch/generalize the title for
drop-off, and ensure the backend's drop-off S3-absent error message contains "S3"
so the `/S3/i` match still fires.

**Rebuild `web/dist`:** any `web/` change requires `make web` + committing the
rebuilt `web/dist` (CI embeds the committed dist and never rebuilds the SPA).

## Files to create / modify

**Create**
- `internal/migrate/dropoff.go` — constants, `DropTransport`, `DropMeta`/`DropTable`
  + `SourceRowCounts()`, `DropImportSpec`, `errDropTooLarge` reuse,
  `Orchestrator.ImportFromDrop`.
- `internal/store/dropoff.go` — `Insert/GetByCode/Update/ListExpired/SweepExpired`.
- `internal/server/handlers_dropoff.go` — mint/get/start/cancel + `errDropRequiresS3`.
- `internal/server/dropoff_worker.go` — `runDropImportWorker`.
- `scripts/migrate-push.sh` — source-side uploader.
- Tests: `internal/migrate/dropoff_test.go`, `internal/store/dropoff_test.go`,
  `internal/server/handlers_dropoff_test.go`.

**Modify**
- `internal/backup/objectstore.go` — `PresignPut`, `StatObject`, `DownloadToFile`
  (reuse `classifyGet` for NotFound). Never log the presigned URL.
- `internal/store/schema.go` — append `dropoff_sessions` table + `expires_at` index.
- `internal/store/models.go` — `DropoffRecord` struct.
- `internal/server/server.go` — `Server.drops` field; `dropTransportFor`; wire in
  `newServer`; startup sweep call near `server.go:170`.
- `internal/server/background.go` — register the periodic drop-off sweep job.
- `internal/server/router.go` — register the 4 drop routes near `:124-131`.
- `web/src/views/Migrate.tsx`, `web/src/api/client.ts`, `web/src/api/types.ts`,
  `web/src/views/Migrate.test.tsx`, `web/dist` (rebuilt), `README.md:20`.

## Test plan (TDD; mirror existing `*_test.go`)

- **`internal/backup`** — `PresignPut` returns a PUT URL for the right key/bucket;
  `StatObject` maps NotFound to `exists=false`; `DownloadToFile` round-trips.
- **`internal/migrate/dropoff_test.go`** — key layout (`DropDumpKey`/`DropMetaKey`);
  `DropMeta.SourceRowCounts()` produces `schema.name` keys identical to
  `engine.RowCounts`; `ImportFromDrop` happy path, **checksum mismatch**,
  **row-count mismatch**, **oversize** (`StatObject` > `MaxDropBytes`),
  **byte_size mismatch** (truncated), **meta-absent** (not uploaded) — using an
  extended fake `DropTransport` + the existing `FakeRunner`/fake engine from
  `orchestrator_test.go`.
- **`internal/store/dropoff_test.go`** — Insert/Get/Update + `SweepExpiredDropoffs`
  (expired non-terminal rows flip to `expired`; terminal rows untouched), mirroring
  `migration_test.go`.
- **`internal/server/handlers_dropoff_test.go`** — mint S3-required (CodeInternal,
  message contains "S3") when `s.drops == nil` (mirror
  `TestMigrateSessionEndpointsRequireS3`); mint overwrite typed-confirm
  (`CodeSafety` without confirm, success with it); start-not-ready ⇒ `CodeConflict`;
  cancel deletes objects + marks failed. Inject a fake `DropTransport` into the
  test server (extend `newTestServer`).
- **`web/src/views/Migrate.test.tsx`** — use the shared `usePolling` mock +
  `fireEvent.mouseDown(tab, {button:0})` (Radix tabs activate on mousedown): Start
  disabled until `uploaded`; command + Copy render; countdown renders; S3-absent
  shows the needs-S3 callout; first-load failure shows the error, not a spinner.
- **Build/vet/test:** `go build ./...`, `go vet ./...`, `go test ./...` via the
  Bash tool with `dangerouslyDisableSandbox: true` (go is a snap the sandbox
  blocks). `npm run build` / `make web` for the SPA. Never claim green without
  seeing it pass.

## Risks & residuals

1. **Both-URLs holder (residual).** SHA-256 + row counts do **not** defend against
   someone holding both presigned URLs (they control dump *and* meta). The only
   mitigation is the **short TTL** + **"treat as a password"** UX. Documented,
   accepted — same posture as the existing modes.
2. **MinIO path-style not supported.** `migrateServiceFor`/`dropTransportFor` do
   not set `PathStyle` (the field exists but is unused, `objectstore.go:43`), so
   presigned URLs are **virtual-host** style — correct for AWS S3 / R2 / B2 but a
   path-style MinIO deployment gets a non-resolving host. Flag this explicitly in
   the UI/docs; full support is future work.
3. **Region probe.** Empty `cfg.Backup.Region` makes minio do a one-time
   `getBucketLocation` HEAD on first presign. Pass Region through; surface a clear
   error if the probe fails.
4. **Presigned PUT cannot cap size.** Only `PresignedPostPolicy`
   (`content-length-range`) can, and that needs a multipart POST form incompatible
   with `curl --upload-file`. The cap is enforced **panel-side** via the
   authoritative `StatObject(dumpKey).Size` (never `meta.byte_size`, which is
   source-controlled — same caveat as `orchestrator.go:472-477`).
5. **Don't change `NewOrchestrator`'s signature** (`orchestrator.go:100-112`,
   depended on at `migrate_worker.go:199,219,262`) — pass `DropTransport` to
   `ImportFromDrop` as an argument.
6. **Row-count parity is the #1 functional failure mode** — the `migrate-push.sh`
   table set + `schema.name` key must match `engine.RowCounts` exactly, or every
   drop-off "fails verification".
7. **Secret hygiene** — never log/audit/persist the signed URLs; return them once
   at mint; `GET` never re-serves them; a page refresh loses the command (user
   re-mints). `dropoff_sessions` stores only object keys.

## Out of scope (v1)

- Multi-DB/cluster drop-off; resumable/multipart uploads; MinIO path-style
  presign; re-fetching a minted command after navigation.
