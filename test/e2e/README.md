# E2E Integration Test Harness

A deterministic, **backend-only** integration harness for indiepg. It stands up a
brand-new panel from scratch inside Docker and exercises every backend feature
end-to-end through the **real HTTP API**, asserting on **real ground truth** — rows
in Postgres, objects in MinIO, `systemctl` unit state — not HTTP 200s.

- **Design:** `docs/plans/2026-06-30-e2e-test-harness-design.md`
- **Author rules + frozen harness API:** `test/e2e/AUTHORS.md` (source of truth for writing scenarios)
- **Current pass/blocked status & bug history:** `docs/plans/2026-07-01-e2e-harness-HANDOFF.md`

## 1. What it is

- A privileged **systemd Debian container** (`/sbin/init` as PID 1) runs a real
  `indiepg install` → real `apt`, real `systemctl enable --now`, a real provisioned
  Postgres cluster and panel unit.
- **MinIO** is the S3 target (path-style + TLS, bucket pre-created via an `mc` init step).
- Go tests tagged `//go:build e2e` drive the real panel HTTP API and assert on ground
  truth read **out-of-band**: `psql` over the cluster socket, `minio-go` against the
  bucket, `systemctl is-active` via `docker exec`.
- **Determinism** comes from frozen-and-pinned packages: the base image is pinned by
  **digest**, the two Postgres majors + `pgbackrest` are pinned to exact PGDG versions
  and **pre-cached in the image**, so the runtime install provisions offline and
  reproducibly. PITR/recovery targets use **xid/LSN** (never wall-clock), with fixed
  row counts and bounded polling (no `time.Sleep`).
- **Escape hatch:** the `LIVE_APT` Docker build-arg (default `0`) makes the base image
  pull live from PGDG to catch upstream drift — rebuild the base image with
  `docker build --build-arg LIVE_APT=1 -f test/e2e/docker/Dockerfile.base test/e2e/docker`.
  It is **not** wired into a make target.

Two images are built once and cached (see `test/e2e/docker/`):

- **`indiepg-e2e-base`** — systemd box with pinned PGDG packages *downloaded but not
  installed*, so the runtime `indiepg install` does a real offline provision. Used by
  the install-from-scratch scenario (and the major-upgrade provision on PG 16).
- **`indiepg-e2e-preinstalled`** — built FROM base by running `indiepg install` once at
  build time and committing the result. Every other scenario starts from "installed" in
  seconds, each in its own isolated container.

## 2. How to run

All commands run from the repo root. Both `go` and `docker` are sandbox-blocked in the
dev environment — run with the sandbox disabled.

```sh
# whole suite (builds images if needed, then runs every scenario):
make e2e

# one scenario:
make e2e SCENARIO=TestBackupFull

# rebuild the images after ANY product-code change (rebakes the binary):
make e2e-images

# tear down: remove both e2e images and the staged binary:
make e2e-clean
```

`make e2e` builds the images (`e2e-images` → `e2e-preinstalled` → `e2e-base` → `build`)
then runs `go test -tags e2e ./test/e2e/... -count=1 -timeout 30m -v`, forcing
`DOCKER_CONTEXT=default`. `SCENARIO=TestName` becomes `-run TestName`.

### Direct `go test` (one scenario, custom timeout)

```sh
DOCKER_CONTEXT=default go test -tags e2e ./test/e2e/... -run TestPGVersionUpgrade \
  -v -count=1 -timeout 45m
```

Set `DOCKER_CONTEXT=default` yourself for direct runs (the harness only sets it for its
own `docker` subprocesses; the active `desktop-linux` context may point at a dead
socket). Heavy scenarios (major upgrade ≈ two ~200s installs + a copy upgrade) want a
generous timeout.

### Serializing the suite

Every scenario calls `t.Parallel()` and is fully parallel-safe (unique compose project
name + ephemeral loopback ports), but the dev box is I/O-slow and contends under parallel
load. On a slow/contended box, run the suite **serialized** with `-parallel 1`:

```sh
make e2e-images
DOCKER_CONTEXT=default go test -tags e2e ./test/e2e/... -count=1 -parallel 1 -v -timeout 90m
```

(The `make e2e` target itself runs at Go's default parallelism — use the direct form
above to serialize.)

## 3. Environment requirements & gotchas

- **Docker with a working default context.** Export `DOCKER_CONTEXT=default`; the
  `desktop-linux` context may point at a dead socket. The dev sandbox also blocks the
  Docker socket and the `go` snap — run `docker`/`go`/`git`(in-worktree) with the sandbox
  disabled.
- **Privileged systemd.** The container needs `--privileged --cgroupns=host`, tmpfs on
  `/run` `/run/lock` `/tmp`, a `/sys/fs/cgroup` bind, and `/sbin/init` as PID 1 (cgroup v2
  host). `--security-opt apparmor=unconfined` is set as belt-and-suspenders. **The
  harness/compose already does all of this** — see `test/e2e/docker/compose.yaml`.
- **Slow box → bounded waits.** The box is fsync-glacial under load. Waits are generous
  but always **bounded** via `harness.Await`/`Poll` — never `time.Sleep`.
- **Rebuild after code changes.** After ANY product-code change you must `make e2e-images`
  to rebake the binary into both images; otherwise scenarios run against a stale build.

## 4. Scenario inventory

One file per scenario group under `test/e2e/`, each `//go:build e2e`. (Live pass/blocked
status is tracked in the HANDOFF doc, not here.)

| Test func | File | What it proves |
|-----------|------|----------------|
| `TestInstallFromScratch` | `scenario_install_test.go` | Real `indiepg install` on the bare base image: `postgresql` + `indiepg` units active, login works, `/readyz` ok, cluster provisioned. |
| `TestBackupFull` | `scenario_backup_test.go` | Full pgBackRest backup to MinIO: objects land under `backup/main/…`+`archive/main/…` and the panel lists the completed full. |
| `TestBackupChain` | `scenario_backup_chain_test.go` | Incr + diff backups build on the full; the prior/reference dependency chain is visible in `pgbackrest info`. |
| `TestExtensions` | `scenario_extensions_test.go` | Per-DB extension mgmt across 3 install tiers — `TierReady` (citext), `TierNeedsPackage` (apt install), `TierNeedsRestart` (preload GUC + restart-with-rollback); asserts `pg_extension`, the GUC, the live postmaster, the apt pkg. |
| `TestRestoreAfterLoss` | `scenario_restore_test.go` | Guarded restore recovers data dropped from the live cluster, taking a pre-restore safety backup first. |
| `TestRestoreTestDeep` | `scenario_restoretest_test.go` | Deep `restore-test?deep=true` restores the newest backup into a throwaway scratch cluster, boots it with WAL replay, asserts `verified_rows > 0`, cleans up. |
| `TestPITR` | `scenario_pitr_test.go` | Point-in-time recovery to a captured **xid** target: post-target rows provably gone, pre-target rows survive. |
| `TestMigrateDirectPull` | `scenario_migrate_direct_test.go` | Direct-pull migration (no S3) from a 2nd Postgres container: `single-db` + `cluster` modes, exact row parity on the target. |
| `TestMigrateS3Session` | `scenario_migrate_session_test.go` | ssh-less shared-S3 session migration: dump moves through MinIO under `pg-migrations/sessions/<code>/`, imported + row-parity verified (no restart). |
| `TestMigrateDropoff` | `scenario_migrate_dropoff_test.go` | Drop-off link migration: source pushes dump+meta to presigned PUT URLs; panel imports with SHA-256 + per-table row-count verification. |
| `TestRolesAndDatabases` | `scenario_roles_test.go` | Guided database/role actions via the API create real DBs/roles and mint one-time credentials. |
| `TestReadOnlyEnforcement` | `scenario_roles_test.go` | A panel-created read-only user is enforced at the **database** level (INSERT/CREATE rejected, reads succeed); the read-only query box refuses writes pre-flight. |
| `TestOwnershipFailClosed` | `scenario_ownership_test.go` | Single-writer ownership: a forged foreign owner marker in the repo makes the next backup fail fast and refuse to clobber the repo (no silent corruption). |
| `TestPGVersionUpgrade` | `scenario_upgrade_test.go` | PG version-upgrade engine — `Minor` (typed 409 "no minor update", cluster unchanged), `Major` (PG 16→17 via `pg_upgradecluster` + finalize), `Rollback` (16→17 then roll back to 16 before finalize). |
| `TestPoolerEnableDisable` | `scenario_pooler_test.go` | PgBouncer pooler: enable → unit active + a real client routed *through* the bouncer into Postgres; disable → stopped. |

## 5. Adding a scenario

`test/e2e/AUTHORS.md` is the **source of truth** — read it before writing one. The short
version:

- **Additive only.** Your scenario lives in its own `scenario_<name>_test.go`
  (`//go:build e2e`, `package e2e`). Do not modify the frozen harness core
  (`env.go`, `panel.go`, `pg.go`, `s3.go`, `await.go`, …); add new typed Panel methods or
  helpers in a **new** file you own (`harness/panel_<name>.go`, `harness/<feature>.go`).
- **Never weaken a test** to make it pass. The test is the source of intended behavior; if
  a scenario goes red, fix the harness or fix indiepg (the hardening point) — and during
  parallel waves, **report** product bugs (file, line, root cause, proposed fix) rather
  than patch product code or rebuild the shared images yourself.
- **Determinism:** no `time.Sleep` (use `Await`/`Poll`); recovery targets use xid/LSN, not
  wall-clock; fixed row counts and seeds.

Frozen harness API surface authors consume (see `AUTHORS.md` for the exact signatures):

- **`harness.Up(t, harness.Options{Image: …})` → `*Env`** — isolated compose project
  (`Close`, `Exec`/`ExecAsUser`/`ExecCapture`, `SystemctlIsActive`, `Install`,
  `AwaitReady`, `PanelContainer`, `DumpLogs`). `ImagePreinstalled` (default) or
  `ImageBase` (+ `SkipReadyWait`).
- **`Env.Panel`** — HTTP client (`Login`, `Token`, generic `Do/GET/POST/PUT/DELETE`,
  typed `ConfigureS3`/`RunBackup`/`ListBackups`/`AwaitBackup`/…; `Response.DecodeData`,
  `Response.Err()` → `*PanelError`). Sends bearer + CSRF on writes.
- **`Env.PG`** — psql ground truth as `postgres` over the socket (`Scalar`, `Exec`,
  `CountRows`, `XID`, `Now`, `ServerVersion`, and `…DB(db, …)` variants).
- **`Env.S3`** — minio-go from the host (`EnsureBucket`, `ListObjects`, `CountObjects`,
  `ObjectExists`, `Bucket`).
- **`harness.Await` / `harness.Poll`** — the only sanctioned waits (bounded predicate
  polling; `Await` is fatal, `Poll` returns the error).

## 6. Troubleshooting

- **On failure the harness dumps diagnostics to the test log** (`Env.DumpLogs`): the panel
  journal (`indiepg` + `postgresql` + `pgbouncer`, last 160 lines), `systemctl status
  pgbouncer`, and the last 60 lines of compose logs. Read the test output first.
- **Timeouts on a slow/contended box** → run serialized (`-parallel 1`) and/or raise
  `-timeout`; the heavy upgrade paths each take minutes. All waits are bounded `Await`s, so
  a hang surfaces as a clear "Await timed out waiting for …" rather than wedging.
- **Stale behavior after a code change** → you forgot `make e2e-images`; the image still
  has the old binary baked in.
- **`docker` reports the daemon down / context errors** → set `DOCKER_CONTEXT=default` and
  run with the sandbox disabled.
