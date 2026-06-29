# PostgreSQL Version Selection & Upgrade — Design

**Date:** 2026-06-29
**Branch:** `feat/pg-version-upgrade`
**Status:** Approved design, ready for implementation

## Summary

Give the panel three coupled capabilities, all built on one foundation (the PGDG
apt repository):

1. **Choose the PostgreSQL major version at install time.**
2. **Minor upgrades** (e.g. 16.2 → 16.4): fast, low-risk, apt + service restart.
3. **Major upgrades** (e.g. 16 → 17): `pg_upgrade --copy` via Debian's
   `pg_upgradecluster`, gated behind pre-flight checks + a mandatory backup, with a
   **two-phase finalize/rollback** model so the operation is reversible for a window.

Audience: indie hackers running a single panel-managed Postgres server. The emotional
core of the feature is **"I clicked upgrade and did not lose my database."** Safety and
reversibility beat speed.

Scope decision: **fresh installs only.** Existing distro-package boxes are NOT migrated
onto PGDG — the operator reinstalls. No adoption path.

---

## 1. Foundation — PGDG repo + version catalog + install-time selection

### PGDG repository
Fresh installs add the PostgreSQL Global Development Group apt repository
(`apt.postgresql.org`) and its signing key **before** installing Postgres. This replaces
the current generic `postgresql` metapackage in `internal/pg/manager.go` (`aptPackages`,
~line 53).

Why: only PGDG lets multiple majors coexist side-by-side
(`/usr/lib/postgresql/16/bin` and `/usr/lib/postgresql/17/bin`), which is the
precondition for both version selection and `pg_upgrade`.

### Supported-majors catalog (single source of truth)
A small Go list of supported majors with one marked default, e.g.:

```go
// internal/pg/versioncatalog.go
type MajorRelease struct {
    Major   int
    Default bool   // latest stable we recommend
    EOL     bool   // informational; still installable/upgradable
}
var SupportedMajors = []MajorRelease{
    {Major: 17, Default: true},
    {Major: 16},
    {Major: 15},
}
```

This drives both the installer's version picker and the upgrade target list. Adding a new
PG major to the panel = one entry here.

### Install picks a version
- `indiepg install` gains `--pg-version <major>` (default = catalog default).
- The web installer / first-run flow gets a dropdown of supported majors.
- Internally install the **versioned** packages: `postgresql-<major>`,
  `postgresql-<major>-contrib` (plus `pgbackrest`), NOT the generic metapackage.

### Pinning
Apt-pin the installed major so an unattended `apt upgrade` cannot silently jump majors.
Minor updates still flow; major moves happen only through our deliberate flow.

---

## 2. Pre-flight checks & conflict detection (reusable framework)

One `preflight` component that runs a list of named checks and returns structured
results. Each check: `pass` / `warn` / `fail` + human message + optional remediation hint.

- **Any `fail` blocks** the operation.
- **`warn`** is shown but proceedable with a confirm.
- Shared by install, minor upgrade, and major upgrade — each selects which checks apply.

```go
type CheckStatus string // "pass" | "warn" | "fail"
type Check struct {
    ID          string      `json:"id"`
    Title       string      `json:"title"`
    Status      CheckStatus `json:"status"`
    Message     string      `json:"message"`
    Remediation string      `json:"remediation,omitempty"`
}
```

### Install-time checks
- **Existing cluster present** — detect via `pg_lsclusters` / `/var/lib/postgresql` /
  running `postgresql` service. If found → **fail** (don't clobber data) unless it's a
  known panel-managed reinstall.
- **Port 5432 in use** by a non-panel process → fail.
- **OS/codename supported by PGDG** → fail if the distro release isn't on PGDG's list.
- **Conflicting half-installed distro packages** → warn/fail with cleanup guidance.
- **Minimum free disk** for install → fail if absurdly low.

### Major-upgrade checks
- **Disk** — free space on the PGDATA filesystem ≥ data-dir size + ~20% margin. Hard fail
  otherwise. (This is the "100 GB data / 20 GB free" guard.)
- **`pg_upgrade --check`** — locale/encoding match, no leftover prepared transactions,
  etc. Surface its output verbatim.
- **Extension parity** — for every installed extension, confirm a
  `postgresql-<newmajor>-<ext>` package exists/installs. Missing one → fail with the exact
  package name.
- **Target binaries installed** — new major packages present.
- **Active replication slots / prepared transactions** that block pg_upgrade → fail.
- **Backup freshness** — a successful pgBackRest backup taken after we quiesce writes
  (enforced in the major-upgrade flow, not optional).

UI renders this as a checklist the user sees **before** committing: green ticks, red
blockers, each with a reason.

---

## 3. Version visibility

A **Version** panel (and a compact line on the dashboard) shows:

- **Running version** — full string + major, via the existing `pg.MajorVersion()` /
  `SHOW server_version`. This finally populates the `version` field already present in the
  API schema (`web/src/api/types.ts` `PGHealth.version`,
  `handlers_dashboard.go dashboardPGStatus.Version`) but never set today.
- **Available updates**:
  - **Minor**: after `apt-get update`, compare installed package candidate vs installed
    version → "16.2 → 16.4 available".
  - **Major**: list catalog majors newer than current → "Upgrade to 17 available".

---

## 4. Minor upgrade flow

Low-risk: on-disk format unchanged.

1. Run the **light** pre-flight subset (disk for the new package; service is
   panel-managed).
2. **Backup recommended, not mandatory** — warn if the last pgBackRest backup is stale,
   offer one-click backup, but allow proceed.
3. `apt-get install --only-upgrade postgresql-<major> postgresql-<major>-contrib`.
4. Restart the `postgresql` service (the only downtime — a few seconds).
5. Verify the service came back; re-read version; show "now on 16.4".

On restart failure: surface service status/logs, clear error (no infinite spinner).

---

## 5. Major upgrade flow

### Engine
Build on **`pg_upgradecluster`** (from `postgresql-common`), `--method=upgrade` (which is
`pg_upgrade --copy` underneath). It creates the new cluster, migrates
`postgresql.conf`/`pg_hba.conf`, swaps ports so the new cluster lands live on 5432, and
leaves the **old cluster stopped but intact** — exactly the two-phase rollback model, with
far less code than orchestrating raw `pg_upgrade` ourselves.

### Phase A — Prepare & preview (nothing destroyed)
1. User picks target major.
2. Install target packages: `postgresql-<new>`, `-contrib`, and a matching
   `postgresql-<new>-<ext>` for **every** installed extension. (Installing is
   non-destructive.)
3. Run the major pre-flight checklist (§2) — extension parity is now truly verifiable.
4. Show preview: source→target, estimated downtime, disk required vs free, list of
   extensions carried over, "old cluster preserved for rollback".

### Phase B — Execute (downtime window)
5. **Mandatory fresh pgBackRest backup** — hard gate, no skip.
6. Quiesce and run `pg_upgradecluster --method=upgrade <oldmajor> main`.
7. Verify panel-managed tuning / `pg_hba` survived the config migration; re-apply if
   needed.
8. Start the new cluster; run `vacuumdb --all --analyze-in-stages` (pg_upgrade does NOT
   carry planner stats — skipping makes the DB feel slow).
9. `ALTER EXTENSION … UPDATE` per extension.
10. Smoke test: service up, accepts a connection, version reads as new major.

Old cluster is now stopped, parked on a moved port, ready as rollback.

---

## 6. Two-phase finalize & rollback

After Phase B: live on new major; old cluster stopped on a moved port. Panel records a
durable **"upgrade pending finalization"** state and shows a persistent banner until the
user acts.

### Finalize (reclaim space) — point of no return
`pg_dropcluster <oldmajor> main`, optionally purge old `postgresql-<oldmajor>*` packages,
free the disk. Irreversible → confirm with a "type the version to confirm" guard.

### Roll back to old major
Stop the new cluster, move old cluster back onto 5432, start it. New cluster kept for
inspection or dropped.

**Critical caveat (must be surfaced loudly in the confirm dialog):** rollback returns you
to the old cluster, frozen at the upgrade moment. **Any writes made against the new major
during the verification window are discarded by a rollback.** For a single-server box in a
maintenance window that's usually nothing — but the dialog must spell it out.

### No auto-finalize
The old cluster lingers until the user clicks finalize — never auto-dropped (that would
silently delete the rollback). Nudge via banner; Version panel shows "X GB reclaimable".

### State durability
The pending-finalization state survives panel restarts (persisted, not in-memory), so a
panel update mid-window doesn't strand the user with two clusters and no UI to resolve
them.

---

## 7. API contract

All under `/api`. Long-running operations (minor upgrade, major preflight/start/finalize/
rollback) MUST follow the existing async-operation + progress pattern used by
backup/restore/migration (job handle + status polling/stream + persisted state) — mirror
that code rather than inventing a new pattern.

### `GET /api/pg/version`
Drives the Version panel + dashboard line.
```json
{
  "running": true,
  "current": { "full": "16.2 (Debian 16.2-1.pgdg120+2)", "major": 16 },
  "available": {
    "minor": { "available": true, "target": "16.4" },
    "majors": [ { "major": 17, "default": true }, { "major": 18 } ]
  },
  "pending_finalization": null
}
```
`pending_finalization` is non-null when a major upgrade awaits finalize:
```json
{ "from_major": 16, "to_major": 17, "reclaimable_bytes": 1234567890, "upgraded_at": "..." }
```

### `POST /api/pg/upgrade/minor`
Body: `{ "backup": true }`. Runs §4. Returns an operation handle.

### `POST /api/pg/upgrade/major/preflight`
Body: `{ "target_major": 17 }`. Runs §5 Phase A (installs target packages + checks),
non-destructive. Returns:
```json
{
  "checks": [ { "id": "disk", "title": "...", "status": "pass", "message": "..." } ],
  "preview": {
    "from_major": 16, "to_major": 17,
    "disk_required_bytes": 0, "disk_free_bytes": 0,
    "extensions": ["pgvector", "postgis"],
    "blocking": false
  }
}
```

### `POST /api/pg/upgrade/major/start`
Body: `{ "target_major": 17, "confirm": true }`. Runs §5 Phase B. Refuses unless the most
recent preflight for this target had no `fail`. Returns an operation handle; on success
the server is in `pending_finalization`.

### `GET /api/pg/upgrade/status`
Current operation state / pending-finalization state (for resuming the UI after reload).

### `POST /api/pg/upgrade/finalize`
Body: `{ "confirm_version": 16 }` (must match the old major). Runs §6 finalize.

### `POST /api/pg/upgrade/rollback`
Body: `{ "confirm": true }`. Runs §6 rollback. Clears `pending_finalization`.

---

## 8. Backend implementation plan (Go)

Follow the **extensions feature** as the template for a panel-backed capability
(`internal/server/handlers_extensions.go`, `internal/pg/extcatalog.go`).

New/changed files:
- `internal/pg/versioncatalog.go` — `SupportedMajors`, helpers (`DefaultMajor()`,
  `IsSupported(major)`).
- `internal/pg/install.go` (or extend `manager.go`) — PGDG repo + key setup; versioned
  package install; apt pinning. Replace `aptPackages` generic install.
- `internal/pg/preflight.go` — the `Check` framework + install/major check sets.
- `internal/pg/upgrade.go` — minor upgrade (apt + restart) and major upgrade
  orchestration via `pg_upgradecluster`, plus finalize/rollback. Reuse
  `pg.MajorVersion()`; reuse pgBackRest backup entrypoint used elsewhere.
- `internal/pg/upgradestate.go` — durable persistence of pending-finalization state
  (store alongside existing panel state/config; find where current panel state persists).
- `internal/server/handlers_pgversion.go` — the endpoints in §7; register in
  `router.go`.
- `internal/server/handlers_dashboard.go` — populate the existing `Version` field.
- `cmd/indiepg/main.go` — `--pg-version` flag on `installCmd()`.

Long-running ops: reuse the backup/restore/migrate async-operation machinery (find it;
do not reinvent). Persist operation + finalization state so a panel restart resumes the UI.

All shell-outs (`apt-get`, `pg_upgradecluster`, `pg_lsclusters`, `pg_dropcluster`,
`vacuumdb`) go through the existing command-runner abstraction with captured
output/logging, consistent with how `manager.go` runs apt today.

---

## 9. Frontend implementation plan (web/src)

Follow the **extensions panel** as the template (find it under `web/src` — the page +
API client + types).

- `web/src/api/types.ts` — add types matching §7 (`PGVersionInfo`, `PreflightResult`,
  `Check`, `PendingFinalization`, etc.); the `PGHealth.version` field already exists.
- API client module for the new endpoints (mirror the extensions client).
- **Version panel/page**: current version, available minor/major updates, and entry
  points to the flows.
- **Minor upgrade**: simple confirm (with stale-backup warning + one-click backup), run,
  show result.
- **Major upgrade wizard**: pick target → preflight checklist (green/red, blockers
  disable "Continue") → preview → mandatory-backup gate → run with progress → land in
  pending-finalization.
- **Pending-finalization banner** (dashboard + version panel): "Upgraded to 17 — verify,
  then finalize", with **Finalize (reclaim X GB)** [type-to-confirm] and **Roll back to
  16** [loud "discards post-upgrade writes" warning] buttons.
- Dashboard: show the running PG version (wire the now-populated field).

Match existing component/styling conventions; reuse shared UI primitives the extensions
panel uses.

---

## 10. Error handling & edge cases

- **Preflight fail** → block with the specific reason + remediation; never proceed past a
  `fail`.
- **Disk insufficient** → exact "need X, have Y" message.
- **Missing extension package for target major** → name the package; block.
- **`pg_upgradecluster` fails mid-run** → old cluster is preserved by design; surface the
  tool's stderr; leave a clear recoverable state (do NOT delete anything).
- **Service won't start post-upgrade** → offer rollback prominently.
- **Panel restart during pending-finalization** → state rehydrates; banner reappears.
- **Concurrent operations** → a single global lock; refuse a second upgrade while one is
  in progress or pending finalization.
- **Rollback after writes** → explicit data-loss warning in the confirm dialog.

---

## 11. Testing

- Unit: version catalog helpers; preflight check logic (table-driven, mock command
  outputs for `pg_lsclusters`, disk, `pg_upgrade --check`); minor/major package-name
  resolution; state persistence round-trip.
- Handler tests: each endpoint's request/response shape + the
  "no-start-without-clean-preflight" and "no-second-op" guards.
- Frontend: type checks (tsc) + build; component-level rendering of the checklist and the
  pending-finalization banner states.
- Keep `go build ./...`, `go vet ./...`, and `npm run build` green throughout.
- Note: full end-to-end upgrade requires a real Debian/PGDG box and is out of scope for
  automated tests here (manual/lab verification, consistent with the extensions feature
  which shipped not-yet-live-tested).

---

## 12. Out of scope / future

- `pg_upgrade --link` fast mode (same-filesystem, near-zero disk) as an opt-in when disk
  is tight.
- Migrating already-installed distro-package boxes onto PGDG (we reinstall instead).
- Dump/restore as an alternative major-upgrade strategy (we chose `pg_upgradecluster
  --copy`).
- Automatic/scheduled upgrades.
