# E2E Integration Test Harness — Design

- **Date:** 2026-06-30
- **Status:** Approved — implementing on branch `feat/e2e-test-harness`
- **Author:** design via brainstorming; built by an agent team

## 1. Goal & scope

A heavy, **deterministic**, backend-only integration test harness that stands up a
brand-new indiepg panel from scratch inside Docker and exercises **every** backend
feature end-to-end through the real HTTP API, asserting on **real ground truth**
(rows in Postgres, objects in MinIO, `systemctl` state) — not HTTP 200s.

Purpose: hardening. The end state is a **fully passing** suite that proves each
feature works, run in Docker so it never touches the developer's host Postgres.

Out of scope: the web UI / frontend. Backend + CLI + provisioning only.

## 2. Operating contract (TDD)

The software under test (indiepg) already exists. Therefore:

1. **The test scenarios are the source of truth for intended behavior.** Each
   scenario asserts what *should* happen.
2. When a scenario goes red, fix the **harness** (missing helper / wrong setup) or
   fix **indiepg itself** (a real bug — the point of the hardening pass).
3. **Never soften a test to make it pass.** A test changes only when the *test* is
   wrong — i.e. it asserts something other than the genuine intended behavior. Any
   such change must be called out explicitly with justification.
4. Green at the end means "every feature provably works," not "every assertion was
   sanded down."

## 3. Determinism strategy

**Frozen-and-pinned by default.** Reproducibility beats catching upstream drift:

- Base image pinned by **digest** (`debian:bookworm-slim@sha256:…`).
- MinIO pinned by tag/digest.
- All apt packages (two Postgres majors, `-contrib`, `pgbackrest`, `pgbouncer`,
  extension packages) **pinned to exact versions** and **pre-cached in the image**
  at build time, so the runtime `indiepg install` installs from the local cache —
  reproducible regardless of what PGDG publishes later. indiepg already writes an
  apt version pin (`/etc/apt/preferences.d/99-indiepg-pgdg.pref`); reuse it.
- Assertions avoid wall-clock flakiness: PITR targets use **xid/LSN** where possible
  (not "now minus N seconds"); fixed row counts and seeds; bounded **state polling**
  (`Await`) instead of `sleep`.

**Escape hatch:** `LIVE_APT=1` makes provisioning/upgrade scenarios hit live PGDG to
catch upstream drift. Off by default.

## 4. Topology

`docker compose` project per scenario (unique project name → full isolation, parallel-safe):

- **`panel`** service: privileged, **systemd as PID 1**, Debian pinned by digest.
  Runs a real `indiepg install` → real `apt`, real `systemctl enable --now`, real
  provisioned Postgres + panel unit.
- **`minio`** service: S3 target, bucket pre-created via an `mc` init step.
- Private compose network; tests reach the panel over a mapped port (or `docker exec`).

### Proven host facts (from feasibility probe — bake these in)

- **`--cgroupns=host` is required** (cgroup-v2 host; systemd won't boot without it).
- Working launch flags:
  `--privileged --cgroupns=host --tmpfs /run --tmpfs /run/lock --tmpfs /tmp -v /sys/fs/cgroup:/sys/fs/cgroup:rw … /sbin/init`
- **`export DOCKER_CONTEXT=default`** everywhere — the active `desktop-linux`
  context points at a dead socket.
- AppArmor is active; fallback `--security-opt apparmor=unconfined` if a build trips it.
- **This session's command sandbox blocks the Docker socket** — every `docker`
  command must run with the sandbox disabled (`dangerouslyDisableSandbox: true`),
  else a healthy daemon mis-reads as down.
- Docker Engine 28.5.1, compose v2 plugin, overlay2, cgroup v2, data-root `/hdd1`
  (753G free).

## 5. Image strategy

Two images, built once, cached:

- **`indiepg-e2e-base`**: systemd Debian + PGDG repo + **pre-cached pinned** packages
  (download/cache, do not pre-install Postgres) so the runtime `indiepg install`
  performs a *real, offline, deterministic* fresh provision. Used by the
  **install-from-scratch** scenario.
- **`indiepg-e2e-preinstalled`**: built FROM base by running `indiepg install` once
  at build time and committing the result (Postgres provisioned, panel unit enabled).
  Used by **all other** scenarios so they start from "installed" in seconds, each in
  its own isolated container.

The freshly-built `indiepg` binary (from this branch) is copied into both images.

## 6. Harness package API (`test/e2e/harness`)

Go, `//go:build e2e`. **Frozen after the foundation phase** — scenario authors
consume it, do not change it. Surfaces:

- **Compose/Container lifecycle**: `Up(t, opts) *Env` / `Env.Close()` — starts a
  uniquely-named compose project (panel + minio), waits for `/readyz`, returns
  handles; tears down on cleanup. Logs dumped on failure.
- **Panel HTTP client** (`*Panel`): `Login(password)` → stores bearer token; typed
  methods per endpoint (`BackupRun(type)`, `Restore(target)`, `RestoreTest()`,
  `Extensions...`, `Migrate...`, `Upgrade...`, `Pooler...`, `Query(sql)`, …). Sends
  `Authorization: Bearer` + `X-Indiepg-Csrf: 1` on writes. Captures the one-time
  admin password printed by install.
- **PG ground truth** (`*PG`): runs `psql` inside the panel container as `postgres`
  over the socket — `CountRows`, `Exec`, `Scalar`, `XID()`, `Now()`.
- **S3 assertions** (`*S3`): minio-go/mc against MinIO — `ListObjects(prefix)`,
  `ObjectExists`, counts.
- **Service state**: `SystemctlIsActive(unit)` via `docker exec`.
- **Await**: bounded, deterministic polling on an explicit predicate (no fixed sleeps).

## 7. Per-scenario isolation

Each scenario: own compose project (unique name) from the **preinstalled** image
(except install-from-scratch, which uses **base**), ephemeral state, destroyed at end.
No shared mutable state → parallel-safe and order-independent.

## 8. Scenario catalog (the "everything")

Each is a `//go:build e2e` test asserting real ground truth.

| # | Scenario | Key assertions |
|---|----------|----------------|
| 1 | **install from scratch** (base image) | apt provision succeeds; `postgresql` + `indiepg` units active; login works; readyz ok |
| 2 | **backup full** | `POST /backups/run` full → objects land in MinIO; `info` lists the backup |
| 3 | **backup incr + diff chain** | incr & diff build on full; chain visible in info |
| 4 | **restore after loss** | drop data → restore latest → rows return; safety backup taken first |
| 5 | **PITR** | mark xid T, write post-T batch → restore to T → post-T data provably gone, pre-T present |
| 6 | **deep restore-test** | `/backups/restore-test` boots scratch cluster, `verified_rows > 0`, scratch cleaned |
| 7 | **extension tier-1 (ready)** | `CREATE EXTENSION` (e.g. citext) succeeds; listed as installed |
| 8 | **extension tier-2 (needs package)** | apt installs the pkg, then extension created |
| 9 | **extension tier-3 (needs restart)** | flags restart-required; `shared_preload_libraries` updated; restart-with-rollback; extension loads (e.g. pg_stat_statements) |
| 10 | **migration direct-pull** | 2nd source-PG container → single-db + cluster pull; row parity on target |
| 11 | **migration S3 session** | session handshake moves dump through MinIO; imported + verified |
| 12 | **migration drop-off link** | presigned PUT push from a 2nd container → import + SHA/row verify |
| 13 | **upgrade minor** | `apt --only-upgrade` + restart; version bumped; service up |
| 14 | **upgrade major** | `pg_upgradecluster` across two majors; finalize; **+ a rollback variant** restores old cluster |
| 15 | **pooler** | enable → `pgbouncer` active + routes; disable → stopped |
| 16 | **roles/databases + read-only enforcement** | create db/role; read-only role rejects writes at DB level |
| 17 | **single-writer ownership fail-closed** | a foreign owner marker in the repo is rejected (no silent corruption) |

## 9. Layout & entrypoint

```
test/e2e/
  harness/            # frozen shared API (§6)
  docker/             # Dockerfile.base, Dockerfile.preinstalled, compose.yaml, minio init
  scenario_*_test.go  # one file per scenario group, //go:build e2e
Makefile: `make e2e`  # builds images + runs `go test -tags e2e ./test/e2e/...`
```

Excluded from `go test ./...` (build tag) so CI's per-push gate is unaffected.
Optional **manual/gated** CI job runs the full suite on demand.

## 10. Agent-team plan

Conductor (main) does **zero implementation** — only orchestration and review.

1. **Foundation agent** (solo): builds both images, the compose stack, and the
   `harness` package; drives scenarios **1 (install)** and **2 (backup full)** fully
   green to *prove the rig*; **freezes + documents the harness API** for authors.
2. **Scenario authors** (parallel, bounded batches): each owns 1–2 scenarios from §8,
   TDD to green against the frozen rig, adding its own `scenario_*_test.go`. Real
   indiepg bugs → fix indiepg (hardening), never weaken the test.
3. **Green-up agent** (solo): full-suite run, eliminate any nondeterminism, finalize
   `make e2e` + optional gated CI + README for the harness.

## 11. Constraints recap (for every agent)

- Run all `docker`/`docker compose` with the **sandbox disabled**; `export DOCKER_CONTEXT=default`.
- Keep `--cgroupns=host` and the systemd flags from §4.
- `go` is a snap and is sandbox-blocked → run `go build/test/vet` with the sandbox disabled too.
- Work in the worktree `/primary01/git/indiepg-e2e` on branch `feat/e2e-test-harness`.
