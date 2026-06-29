# PostgreSQL Extensions — Design

**Status:** Approved, ready for implementation
**Date:** 2026-06-29

## Problem

The panel has no way to manage PostgreSQL extensions. An indie operator who wants
`pgvector` for embeddings, `postgis` for geo, or `pg_cron` for scheduled jobs has
to drop to a shell, figure out the Debian package name for their exact PG major
version, `apt-get install` it, and — for some — edit `postgresql.conf` and restart
Postgres by hand. That last part (`shared_preload_libraries`) is exactly the kind
of step where a typo leaves Postgres refusing to boot.

We want the panel to let users **discover, install, and remove extensions** —
including the ones that need an OS package and the ones that need a restart —
with full transparency and a self-healing safety net, while always offering the
option to run the commands themselves.

## Goals

- List installed extensions per database, plus what's available to add.
- One-click install for a curated catalog of popular extensions, auto-installing
  the OS package when the files aren't on disk.
- Handle the two "hard" extensions (`pg_cron`, `pg_stat_statements`) that require
  `shared_preload_libraries` + a Postgres restart — behind strong, typed
  confirmation, with automatic rollback if Postgres won't come back up.
- Always show the exact commands the panel will run, with an **"I'll run these
  myself"** escape hatch.
- Free-form "add by name" for anything outside the catalog (SQL-only).
- Drop and (optionally) update extensions, gated by the existing typed-name
  confirmation pattern.

## Non-goals (v1 / YAGNI)

- Non-apt hosts (yum/dnf/source builds). Package install is scoped to Debian/
  Ubuntu, matching how the panel already provisions Postgres. Non-apt hosts get a
  clear "install the package manually, then click Add" message.
- `DROP EXTENSION ... CASCADE`. v1 surfaces Postgres's dependency error and lets
  the user resolve it; cascade can be an explicit opt-in later.
- Guessing package names for free-form extensions. Free-form is SQL-only; we never
  feed a guessed package name to `apt-get`.

## Reused infrastructure

Almost every hard part already exists and is battle-tested. This design wires
these together rather than reinventing them:

| Need | Existing mechanism |
| --- | --- |
| Run privileged host commands | `exec.Runner` / `OSRunner` (`internal/exec/os.go`), with dry-run + `AsUser` |
| Install an OS package | `apt-get update` + `apt-get install -y` pattern (`internal/pg/manager.go` `Provision`, `internal/pgbouncer/service.go`) |
| `ALTER SYSTEM` as a superuser | `m.runPsql` — peer-auth psql as the `postgres` OS user (pool roles are NOSUPERUSER) — see `internal/pg/archive.go` |
| Restart Postgres + self-heal | `snapshotAutoConf` → write change → `restartWithRollback` → liveness probe → auto-revert (`internal/pg/safeconfig.go`, proven in `EnsureArchiving`) |
| Detect data dir / settings | `internal/pg/settings.go` (`DataDirectory`, `showSetting`) |
| Typed-name confirmation | `core.RequireConfirmation` + `TypedConfirmDialog` |
| Typed errors + envelope | `internal/core/errors.go`, `internal/server/respond.go` |
| Audit logging | `s.audit(ctx, action, resource, outcome, ...)` |

## Model & list view

Extensions are **per-database** — `CREATE EXTENSION` installs into one database,
not the cluster. The feature is therefore scoped to a **target database** the user
selects (defaulting to the DB they're already browsing, else `postgres`).

The `Extensions.tsx` view has a database selector at the top, then two lists:

- **Installed** — from `pg_extension` joined to `pg_available_extensions`: name,
  installed version, default available version (so we can nudge "update available"
  via `ALTER EXTENSION ... UPDATE`).
- **Available to add** — a merge of (a) the **curated catalog** and (b) any other
  `pg_available_extensions` entry not already installed. Each row shows name, short
  description, and a badge:
  - **Ready** — files on disk, plain `CREATE EXTENSION`.
  - **Needs package** — curated, files not yet present; we'll `apt-get install`.
  - **Needs restart** — requires `shared_preload_libraries`; install + restart.

A free-form "add by name" field sits alongside for anything not in the catalog.

## Install flow — three tiers

Clicking "Add" runs a single server-side orchestrated operation that returns a
step-by-step `core.Result` so the UI can show exactly what ran. The tier is chosen
from catalog metadata + `pg_available_extensions`:

- **Tier 1 — Plain.** Files on disk. `CREATE EXTENSION IF NOT EXISTS <name>` on the
  target DB via the `privPool`. No confirmation beyond the click.
- **Tier 2 — Needs package.** Resolve the package name from the catalog with the
  detected PG major version (`postgresql-17-pgvector`), `apt-get update` +
  `apt-get install -y <pkg>` via the runner, then `CREATE EXTENSION`. One
  confirmation ("this installs an OS package").
- **Tier 3 — Needs preload + restart.** Install the package if needed, then
  **read-modify-write** `shared_preload_libraries` (append the lib only if absent,
  preserving existing entries and order), `snapshotAutoConf`, then
  `restartWithRollback`. After Postgres is confirmed back up, `CREATE EXTENSION`.
  Requires the strong **typed-name** confirmation because it restarts the database.
  A bad change auto-rolls-back to last-known-good — Postgres is never left down.

Each operation is audited with name, target DB, and tier.

## Catalog, package mapping & version detection

The curated catalog is a small Go table (`internal/pg/extcatalog.go`):

```go
type CatalogEntry struct {
    Name            string // extension name, e.g. "vector"
    Description     string
    PackageTemplate string // e.g. "postgresql-%d-pgvector"; %d = PG major
    RequiresPreload bool
    PreloadLib      string // e.g. "pg_cron"; empty unless RequiresPreload
}
```

Starter set: `pgvector` (vector), `postgis`, `pg_stat_statements`, `pg_cron`,
`hstore`, `uuid-ossp`, `citext`, `pg_trgm`, `btree_gin`, `btree_gist`. (Open to
additions before/while implementing.)

**PG major version** comes from a new small read in `settings.go` querying
`server_version_num` (e.g. `170004` → `17`), used to fill `%d` in package
templates.

**Package naming is Debian/Ubuntu-specific**, consistent with the rest of the
codebase (apt, the `postgresql` systemd unit, peer auth). Non-apt hosts fail with
an actionable message rather than pretending to support yum.

**Free-form path:** validate the name with `core.ValidateIdentifier` (no shell
risk — it only ever reaches SQL), attempt `CREATE EXTENSION`. If Postgres reports
the control file is missing, surface a friendly miss: "`<name>` isn't installed on
disk; install its package and retry, or add it to the catalog." Free-form never
triggers an auto apt-install.

## API surface

Mirroring the databases/roles convention in `internal/server/router.go`:

```
GET    /extensions?database=<db>        → installed + available (with badges)
POST   /extensions                       → add   { database, name, confirm? }
DELETE /extensions/{name}?database=<db>  → drop  { confirm }
POST   /extensions/{name}/update         → ALTER EXTENSION ... UPDATE (optional)
```

New code:
- `internal/server/handlers_extensions.go` — handlers, following
  `handlers_pgadmin.go` (`handleListExtensions`, `handleInstallExtension`,
  `handleDropExtension`, optional `handleUpdateExtension`).
- `internal/pg` Manager methods — `ListExtensions`, `InstallExtension`,
  `DropExtension` (orchestrate the tiers, package install, preload+restart).
- `internal/pg/admin/admin.go` builders — `CreateExtension`, `DropExtension`,
  `AlterExtensionUpdate` (pure SQL, `QuoteIdent`, no IO).

## Frontend UX

New `Extensions.tsx` view + route + nav entry; `api.listExtensions` /
`installExtension` / `dropExtension` in `client.ts`; types mirroring the Go JSON.

The **Add dialog** is the key piece (per "give the option to do it themselves"):

- For Tier 2/3, before doing anything, the dialog shows **exactly the commands the
  panel will run** — the `apt-get install …`, the `ALTER SYSTEM SET
  shared_preload_libraries = …`, the `systemctl restart`, the `CREATE EXTENSION …`.
- Two actions: **"Install for me"** (panel runs it all) and **"I'll run these
  myself"** (closes the dialog; user copies + runs the commands, then clicks a
  lightweight **"Re-check"** that just re-lists). Tier 3 additionally requires
  typing the extension name to confirm the restart.

`DROP EXTENSION` uses the existing `TypedConfirmDialog`; no `CASCADE` in v1.

Every privileged action is transparent and copy-pasteable; nothing touches the DB
without one click of explicit consent or the user running it by hand.

## Safety, errors & audit

- Extension names: `core.ValidateIdentifier` everywhere; SQL via `QuoteIdent`.
  Package names come only from catalog templates, passed as an exec **arg vector**
  (never a shell string) — no injection surface.
- `shared_preload_libraries` is read-modify-write: append only if absent, preserve
  order/existing entries. Idempotent — re-adding an already-loaded extension is a
  no-op (no needless restart).
- Tier 3 always `snapshotAutoConf` **before** writing, so `restartWithRollback`
  can self-heal. Postgres is never left down.
- Errors reuse the typed hierarchy — `CodeValidation` (bad name), `CodeSafety`
  (missing confirmation / rolled-back restart), `CodeExec` (apt or CREATE failed) —
  each with an actionable `Hint`, surfaced through the existing `{code,message,hint}`
  envelope.
- Audit: `extension_install` / `extension_drop` / `extension_update` with name,
  target DB, tier, and outcome.

## Testing

- Admin SQL builders: pure unit tests for quoting and statement shape.
- `shared_preload_libraries` merge: its own table tests (the trickiest logic —
  empty value, single entry, append, already-present, ordering).
- Manager methods: against the `exec.Runner` fake (dry-run), asserting the exact
  command sequence per tier without touching real apt/systemctl.
- List queries: against the existing PG integration harness if available.

## Build sequence

1. Catalog table + `server_version_num` major-version read.
2. Admin SQL builders + unit tests.
3. `shared_preload_libraries` merge helper + table tests.
4. Manager `ListExtensions` / `InstallExtension` (tiers 1→2→3) / `DropExtension`,
   reusing apt, `runPsql`, `snapshotAutoConf`, `restartWithRollback`.
5. Handlers + routes + audit.
6. Frontend types, API client, `Extensions.tsx`, Add dialog, nav entry.
7. Rebuild + commit `web/dist` (CI embeds committed dist, never rebuilds the SPA).
