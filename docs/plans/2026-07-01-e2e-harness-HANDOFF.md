# E2E Test Harness — HANDOFF (2026-07-01)

Resume document for a fresh session. The harness is ~90% done. Read this top to bottom;
it is self-contained.

## 0. WHERE IS THE WORK (read this first)

All work is on branch **`feat/e2e-test-harness`** in a separate git worktree at
**`/primary01/git/indiepg-e2e`**. The main checkout `/primary01/git/indiepg` is on
`main` and is intentionally **untouched** — that's why nothing shows up there.

```sh
git -C /primary01/git/indiepg-e2e log --oneline b139dae..HEAD   # see all 12 commits
cd /primary01/git/indiepg-e2e                                    # work here
```

- Design: `docs/plans/2026-06-30-e2e-test-harness-design.md`
- Author rules + FROZEN harness API: `test/e2e/AUTHORS.md`
- Harness pkg: `test/e2e/harness/` · scenarios: `test/e2e/scenario_*_test.go` · docker: `test/e2e/docker/`
- Entrypoint: `make e2e` (and `make e2e-images`, `make e2e-clean`)
- Memory note: `~/.claude/projects/-primary01-git-indiepg/memory/e2e-test-harness.md`

## 1. WHAT THIS IS

A deterministic, backend-only integration harness: a privileged **systemd Debian
container** runs a real `indiepg install`, **MinIO** is the S3 target, and Go tests
(`//go:build e2e`) drive the real HTTP API and assert on **ground truth** (rows via
psql, objects in MinIO, `systemctl` state). Goal: a fully-passing `make e2e` that proves
every backend feature works on the real platform. Determinism: frozen-and-pinned packages
(base image pinned by digest; `LIVE_APT=1` escape hatch).

## 2. ENVIRONMENT GOTCHAS (critical — will waste hours otherwise)

- The command **sandbox blocks the Docker socket AND the `go` snap** → run every
  `docker`/`docker compose`/`go`/`git`(in worktree) command with the sandbox DISABLED.
- **`export DOCKER_CONTEXT=default`** everywhere — the active `desktop-linux` context
  points at a dead socket. (Harness sets this for its own docker subprocesses.)
- Privileged systemd needs **`--cgroupns=host`** (+ tmpfs `/run` `/run/lock` `/tmp`, bind
  `/sys/fs/cgroup`, `/sbin/init`). The harness already does this.
- The box is **I/O-slow / fsync-glacial**, especially under parallel load — use generous
  BOUNDED waits (`Await`), and run heavy scenarios (major upgrade) one at a time.
- **Subagent flakiness observed:** `general-purpose` subagents intermittently return a
  no-op (0 tool calls, ~27k-token prompt-echo result) — just re-dispatch. And subagents
  that background a long test (Monitor/`run_in_background`) often yield WITHOUT a verdict;
  for long runs (major upgrade) prefer running the `go test` as a `run_in_background` Bash
  job from the MAIN loop (reliable re-invocation on exit) or give the subagent explicit
  "make concrete progress on each re-invocation" instructions.

## 3. HOW TO RUN

```sh
# after ANY product-code change, rebuild the image so it bakes the new binary:
make e2e-images
# one scenario (sandbox disabled):
DOCKER_CONTEXT=default go test -tags e2e ./test/e2e/... -run TestBackupChain -v -count=1 -timeout 45m
# whole e2e suite:
make e2e
# unit suite (no docker):
CGO_ENABLED=0 go test ./... -count=1
```

## 4. SCENARIO STATUS

GREEN + committed:
- install (`532f124`), backup full (`532f124`), backup incr+diff chain (`9017ad6`),
  extensions tiers 1/2/3 incl. needs-restart (`3e2e7af`), restore-after-loss (`293c60c`),
  deep restore-test (`64c4c22`), migration direct/drop-off/session (`cc2badc`),
  roles/databases + read-only enforcement + ownership fail-closed (`72e626a`),
  **PG MINOR upgrade** (`8f65144`).

PARTIAL — committed but green UNVERIFIED:
- **PG MAJOR upgrade + ROLLBACK** (`8f65144`, subtests `TestPGVersionUpgrade/Major` and
  `/Rollback`). The agent confirmed only Minor green, then stalled on the slow major run.
  **Action: run them and confirm** (may surface bugs).

RED / blocked:
- **pooler** (`0441fd6`, `TestPoolerEnableDisable`) — blocked on **BUG-7**.
- **PITR** — files UNCOMMITTED (`test/e2e/scenario_pitr_test.go`,
  `test/e2e/harness/panel_pitr.go`) — blocked on **BUG-3**.

Also uncommitted: `test/e2e/AUTHORS.md` (committed by this handoff).

## 5. BUGS FOUND (the hardening haul) — 7 real product bugs + 2 follow-ons

FIXED + verified green (commits `872b1e4`, `e539b75`, and restore_deep changes within):
- nonexistent `postgresql-<major>-contrib` package broke every fresh install/upgrade.
- BUG-1 deep restore-test couldn't boot the scratch cluster on Debian split-config
  (config in `/etc`, not PGDATA) → materialize a minimal conf set before `pg_ctl start`.
- BUG-2 restore ran against a RUNNING cluster (pgBackRest err 038) → stop → restore → start
  with safety (`backup.ClusterController` / `pg.Manager` StopCluster/StartCluster).
- BUG-4 migration staging `/var/lib/indiepg/migrate/<id>` was 0700 root, but pg_restore
  runs as `postgres` → make the chain 0711 + dump 0644.
- BUG-5 cluster migration treated benign `role already exists` as fatal → filter globals.
- BUG-6 live `PUT /api/config` didn't rebuild the session `Service` → rebuild `s.migrate`.
- follow-ons: deep-restore postmaster wedged the runner via an inherited stdout pipe
  (`pg_ctl -l <logfile>`); guarded restore exceeded the 120s server `WriteTimeout`
  (`extendForLongOperation` clears the per-request write deadline).

OPEN — must fix to finish:
- **BUG-3 (PITR)**: `Manager.Restore` always takes a full SAFETY backup first, which
  becomes the NEWEST set. pgBackRest auto-selects an older set only for `--type=time`; for
  **xid/lsn/name it uses the newest set** and the product exposes no `--set` knob → recovery
  to an xid/lsn target before that safety backup is unreachable. DECISION TAKEN:
  **fix-properly** — add `--set` selection so a recovery target picks the newest backup whose
  stop precedes the target (mirror pgBackRest's time-based selection for all target types);
  also make the time target sub-second (currently truncated to whole seconds,
  `internal/backup/command.go:145`). Touch points: `internal/backup/command.go` (RecoveryTarget /
  RestoreCmd), `internal/backup/manager.go`. Then re-run `TestPITR` (uses an xid target) green
  and COMMIT `scenario_pitr_test.go` + `harness/panel_pitr.go`.
  (Alternative if you'd rather: scope PITR to time-based targets and flag xid/lsn as a known
  limitation — simpler, but xid/lsn PITR stays broken. Not chosen.)
- **BUG-7 (pooler)**: `POST /api/pooler/enable` apt-installs pgbouncer, which ships a
  pristine default `/etc/pgbouncer/pgbouncer.ini`; then `EnsureConfig`'s marker guard 409s on
  it ("config indiepg did not create"). Fix: after `InstallPackage`, clear the UNMODIFIED
  package conffiles before `EnsureConfig`/`EnsureUserlist` — detect via dpkg
  (`dpkg-query --showformat='${Conffiles}' -W pgbouncer`, compare recorded md5 to on-disk) so a
  genuinely operator-edited file still 409s. Touch points: `internal/pgbouncer/enable.go`
  (~119 InstallPackage, ~144 EnsureConfig), `install.go` (~95/109-114), `config.go`
  (`HasManagedMarker` ~172). Then re-run `TestPoolerEnableDisable` green.

## 6. REMAINING WORK (ordered, actionable)

1. **Verify upgrade major/rollback**: run `TestPGVersionUpgrade/Major` and `/Rollback`
   (slow — use `run_in_background`, idle box). If they surface product bugs, fix centrally.
2. **Central fix BUG-3 + BUG-7** in product code, then `make e2e-images` (ONE rebuild),
   then re-run `TestPITR` + `TestPoolerEnableDisable` green; commit the PITR files.
3. **Green-up** (the last task): run the FULL suite end-to-end — SEQUENTIALLY (the box is
   slow/contended in parallel; parallel runs flaked on timeouts before). Eliminate any
   remaining nondeterminism, finalize `make e2e`, add an optional gated CI job, and write
   `test/e2e/README.md`. Confirm `CGO_ENABLED=0 go test ./...` (unit) still all-green.
4. **Finish the branch**: `superpowers:finishing-a-development-branch` — merge or PR
   `feat/e2e-test-harness` into `main` (it carries both the harness AND the product hardening
   fixes). Rebuild + commit `web/dist`? No — no SPA changes here.

## 7. ORCHESTRATION MODEL (how this was built)

Conductor (main) does ZERO implementation; a team of subagents writes everything, each in
its OWN `scenario_*_test.go` + additive `harness/*.go`. Rules in `test/e2e/AUTHORS.md`:
additive-only during parallel waves, NEVER weaken a test, report product bugs (don't patch
in parallel) — the conductor batches product fixes centrally + rebuilds the image ONCE, then
re-runs. Tests are the source of truth; on red, fix the harness or fix indiepg (hardening).

## 8. OPEN DESIGN THEME (non-blocking)

Several endpoints run long operations SYNCHRONOUSLY on the HTTP request (restore, deep
restore-test, extension install, guided DDL), bumping into client/server timeouts.
`StartBackup` already uses an async/detached pattern. Worth deciding whether these should
follow. Partially mitigated already (restore `WriteTimeout` patch; harness `PostLong`/long
clients). Not required for green.
