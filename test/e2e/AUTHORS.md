# E2E Scenario Authors — Brief & Rules

You are writing ONE (or a few) integration scenarios against the **frozen** harness
the foundation built. Read `docs/plans/2026-06-30-e2e-test-harness-design.md` (the
contract; §8 has each scenario's intended assertions). This file is the operational
brief. The rig is proven green (`make e2e SCENARIO=TestBackupFull` works today).

## Frozen harness API (`github.com/venkatesh-sekar/indiepg/test/e2e/harness`)

Every scenario file: `//go:build e2e`, `package e2e`, under `test/e2e/`.

```go
env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
// Options{ Image PanelImage; SkipReadyWait bool; PGMajor int }  // extend only by ADDING fields
// ImagePreinstalled (default: provisioned cluster + panel unit) | ImageBase (bare box; pair SkipReadyWait:true)
```

**`*Env`** (`Project`, `Panel`, `PG`, `S3`): `Close()`, `Exec(argv...)`,
`ExecAsUser(user, argv...)`, `ExecCapture(argv...) (out,errout,err)`,
`SystemctlIsActive(unit)`, `Install(extraArgs...)`, `AwaitReady(timeout)`,
`PanelContainer()`, `DumpLogs()`. Isolation: unique project `e2e-<test>-<rand8>`,
ephemeral loopback ports → fully parallel-safe with `t.Parallel()`.

**`*Panel`** (HTTP; sends `Authorization: Bearer` + `X-Indiepg-Csrf:1` on writes):
generic seam `Do/GET/POST/PUT/DELETE`, `Response.DecodeData(out)`, `Response.Err()`
(`*PanelError{Status,Code,Message,Hint}`). Implemented: `Login`, `Token`, `Readyz`,
`Healthz`, `Instance`, `Health`, `GetConfig`, `ConfigureS3`, `RunBackup`,
`ListBackups`, `AwaitBackup`; helpers `harness.MinIOS3Config()`,
`harness.ParseAdminPassword(out)`.

**`*PG`** (psql as postgres over the socket — GROUND TRUTH, bypasses the API):
`Scalar`, `Exec`, `CountRows`, `XID`, `Now`, `ServerVersion`, and
`ScalarDB/ExecDB/CountRowsDB(db, …)`.

**`*S3`** (minio-go from host): `EnsureBucket`, `ListObjects(prefix)`,
`CountObjects(prefix)`, `ObjectExists(key)`, `Bucket`.

**`Await`/`Poll`** — the ONLY sanctioned waits. `Await(t, timeout, interval, desc,
func()(bool,error))` (fatal) / `Poll(...)` (non-fatal). **Never `time.Sleep`.**

S3/backup flow that works: `Panel.ConfigureS3(harness.MinIOS3Config())` (path-style +
TLS already solved — just works), check `ConfigResponse.BackupConfigured`, then
`RunBackup("full")` → `AwaitBackup(id, timeout)`. Backup objects land under
`backup/main/…` and `archive/main/…`.

## Running

```
make e2e SCENARIO=TestYourScenario
DOCKER_CONTEXT=default go test -tags e2e ./test/e2e/... -run TestYourScenario -v -count=1 -timeout 25m
```
(Run `docker`/`go`/`git` with the sandbox DISABLED — this env sandboxes the docker
socket and the `go` snap. The harness sets `DOCKER_CONTEXT=default` for its own docker
subprocesses; set it yourself for direct `go test` runs.)

## RULES (non-negotiable — keeps parallel authors from colliding)

1. **Your scenario lives in its OWN file**: `scenario_<name>_test.go`. Pick a unique name.
2. **Do NOT modify the frozen harness core** (`harness/env.go`, `panel.go`, `pg.go`,
   `s3.go`, `await.go`, `options.go`, etc.). Add new typed Panel methods or helpers in a
   **new** file you own (`harness/panel_<name>.go`, `harness/<feature>.go`) or inline in
   your scenario via `Panel.Do/GET/POST`. Additive only.
3. **Determinism**: no `time.Sleep` (use `Await`/`Poll`); PITR/recovery targets use
   **xid/LSN**, not wall-clock; fixed row counts and seeds.
4. **TDD contract**: the test is the source of truth — **never weaken a test** to make it
   pass. If a test is genuinely asserting the wrong thing, change it and **call that out
   loudly**. **During parallel waves, do NOT modify indiepg product code and do NOT rebuild
   the shared e2e images** (both race the other agents). Your changes must be ADDITIVE: new
   `scenario_*_test.go` + new `harness/*.go` files only. If a genuine product bug blocks
   your scenario, STOP and report it precisely (file, line, root cause, proposed fix) and
   mark the scenario "blocked on central fix" — the conductor batches product fixes and
   re-runs you. (This is exactly how the `-contrib` bug was handled.)
5. **Commit ONLY your own files**: `git -C /primary01/git/indiepg-e2e add -- <your files>`
   then `git commit` (sandbox disabled; trailer
   `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`). **Never**
   `git add -A` / `git commit -a` (would sweep other agents' in-flight work). If `git
   commit` fails on `index.lock`, wait ~2s and retry.
6. **Prove it**: your final report MUST paste the real passing `go test -tags e2e`
   output. Do not claim green without it. Also report: files added, any indiepg bug you
   fixed (file + why), any shared-file edit you were forced to make, and per-scenario runtime.

## Already handled — do NOT redo

- The `postgresql-<major>-contrib` package bug is fixed **centrally** across `internal/pg`
  (install, preflight, upgrade). Do not re-patch contrib references.
- MinIO path-style + TLS is solved in the harness/config. `ConfigureS3` just works.
