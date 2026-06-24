//go:build integration

// End-to-end integration proof that ApplyTuning actually persists host-sized
// settings against REAL Postgres — the one thing the fake-Runner unit tests in
// tuning_apply_test.go cannot cover. It stands up a throwaway cluster, applies a
// recommendation, and asserts:
//
//   - the settings land in pg_settings (memory normalised to bytes,
//     max_connections exact), proving the restart-requiring ones (shared_buffers,
//     max_connections) actually took effect via a real restart;
//   - a second ApplyTuning with the same recommendation is a no-op (nothing
//     changed, no restart) — what makes re-provisioning safe;
//   - a restart-requiring value the postmaster refuses to start with is rolled
//     back via restartWithRollback to last-known-good, with Postgres still UP and
//     a CodeSafety error surfaced — the self-healing config guarantee.
//
// Gated behind the "integration" build tag and skipped unless INDIEPG_PG_BINDIR
// points at a Postgres bin directory (e.g. /usr/lib/postgresql/14/bin). It NEVER
// runs in the loop's untagged `go test ./...` gate. Run it explicitly:
//
//	INDIEPG_PG_BINDIR=/usr/lib/postgresql/14/bin \
//	  go test -tags integration ./internal/pg/ -run TestApplyTuning_Integration -v
//
// The production code restarts Postgres with `systemctl restart postgresql`,
// which a throwaway cluster has no unit for; tuningTestRunner translates that one
// command into `pg_ctl -D <dataDir> restart` against the test cluster, so the
// REAL ApplyTuning → restartWithRollback path is exercised end to end. Every
// other command runs as the current OS user (AsUser stripped, no sudo) and psql
// is pointed at the throwaway socket via PGHOST/PGPORT/PGUSER. Stripping AsUser
// also means the cat/tee of postgresql.auto.conf inside snapshotAutoConf/
// restoreAutoConf run as the current user — fine here because initdb made the
// data dir current-user-owned, but it is why this test cannot exercise the
// production "as postgres over peer auth" permission path for those reads.
package pg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	iexec "github.com/venkatesh-sekar/indiepg/internal/exec"
)

// tuningTestRunner wraps a real OSRunner for the integration test. It drops the
// production AsUser="postgres" (so every command runs as the current user, no
// sudo), points psql at the throwaway cluster via env, and translates the one
// `systemctl restart postgresql` ApplyTuning issues into a pg_ctl restart of the
// test cluster — so restartWithRollback runs against real Postgres unmodified.
type tuningTestRunner struct {
	inner   iexec.Runner
	env     []string
	pgCtl   string
	dataDir string
	logFile string
}

func (r tuningTestRunner) Run(ctx context.Context, spec iexec.RunSpec) (iexec.RunResult, error) {
	spec.AsUser = ""
	spec.Env = append(append([]string{}, spec.Env...), r.env...)
	if spec.Name == "systemctl" && len(spec.Args) == 2 && spec.Args[0] == "restart" && spec.Args[1] == serviceName {
		// The synchronous systemd restart becomes a synchronous (-w) pg_ctl
		// restart of the throwaway cluster; a non-zero exit (a config the
		// postmaster rejects) is the same "did not come back up" signal. -l sends
		// the server's own output to a logfile so the daemonized postmaster does
		// not inherit (and hold open forever) the captured stdout pipe, which would
		// otherwise make the runner's cmd.Run block until the server exits.
		spec.Name = r.pgCtl
		spec.Args = []string{"-D", r.dataDir, "-w", "-t", "60", "-l", r.logFile, "restart"}
	}
	return r.inner.Run(ctx, spec)
}

func (r tuningTestRunner) DryRun() bool { return r.inner.DryRun() }

func TestApplyTuning_Integration(t *testing.T) {
	binDir := os.Getenv("INDIEPG_PG_BINDIR")
	if binDir == "" {
		t.Skip("set INDIEPG_PG_BINDIR to a Postgres bin dir (e.g. /usr/lib/postgresql/14/bin) to run the ApplyTuning integration")
	}
	for _, b := range []string{"initdb", "pg_ctl", "psql"} {
		if _, err := os.Stat(filepath.Join(binDir, b)); err != nil {
			t.Skipf("%s not found in INDIEPG_PG_BINDIR=%s; skipping", b, binDir)
		}
	}
	bin := func(name string) string { return filepath.Join(binDir, name) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	sockDir := filepath.Join(base, "sock")
	logFile := filepath.Join(base, "pg.log")
	require.NoError(t, os.MkdirAll(sockDir, 0o700))

	runner := tuningTestRunner{
		inner:   iexec.NewOSRunner(core.Discard(), false),
		pgCtl:   bin("pg_ctl"),
		dataDir: dataDir,
		logFile: logFile,
	}
	raw := func(spec iexec.RunSpec) iexec.RunResult {
		t.Helper()
		res, err := runner.Run(ctx, spec)
		require.NoError(t, err, "command failed: %s %v\nstderr: %s", spec.Name, spec.Args, res.Stderr)
		return res
	}

	// 1. Initialize and start a throwaway cluster on a private socket (no TCP
	//    listener). trust auth + PGUSER=postgres lets psql connect with no
	//    password as the bootstrap superuser, so no extra role is needed.
	const port = "5497"
	raw(iexec.RunSpec{Name: bin("initdb"), Args: []string{"-A", "trust", "-U", "postgres", "-D", dataDir}, Timeout: 2 * time.Minute})

	pgConf := fmt.Sprintf("\nport=%s\nlisten_addresses=''\nunix_socket_directories='%s'\n", port, sockDir)
	f, err := os.OpenFile(filepath.Join(dataDir, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(pgConf)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	raw(iexec.RunSpec{Name: bin("pg_ctl"), Args: []string{"-D", dataDir, "-w", "-t", "60", "-l", logFile, "start"}, Timeout: 90 * time.Second})
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_, _ = runner.inner.Run(stopCtx, iexec.RunSpec{Name: bin("pg_ctl"), Args: []string{"-D", dataDir, "-m", "immediate", "-w", "stop"}, Timeout: 30 * time.Second})
	}()

	// Now that the socket exists, point psql at it via the runner env.
	runner.env = []string{"PGHOST=" + sockDir, "PGPORT=" + port, "PGUSER=postgres"}

	m := New(Options{
		Runner: runner,
		Config: config.Config{PGSocketDir: sockDir},
		Logger: core.Discard(),
	})

	// 2. Apply a recommendation that changes both restart-requiring settings
	//    (shared_buffers, max_connections) and the reloadable ones, so the real
	//    restart path is exercised. The values are small and safe to allocate.
	rec := TuningRecommendation{
		Profile:              ProfileMixed,
		SharedBuffersMB:      256, // restart required
		EffectiveCacheMB:     1024,
		WorkMemMB:            8,
		MaintenanceWorkMemMB: 128,
		MaxConnections:       120, // restart required
	}
	changed, err := m.ApplyTuning(ctx, rec)
	require.NoError(t, err, "applying a sane recommendation must succeed")
	require.True(t, changed, "a fresh cluster differs from the recommendation, so something must change")

	// 3. The settings landed in pg_settings. readTunableSettings normalises memory
	//    to bytes via the unit column, so comparing against tunedSetting.wanted is
	//    exact and unit-agnostic. shared_buffers and max_connections only change on
	//    a real restart, so reading them back proves the restart actually happened.
	assertApplied := func(rec TuningRecommendation) {
		t.Helper()
		current, err := m.readTunableSettings(ctx)
		require.NoError(t, err)
		for _, s := range tunedSettings(rec) {
			require.Equal(t, s.wanted, current[s.name],
				"%s must read back at the applied value", s.name)
		}
	}
	assertApplied(rec)

	// 4. A second apply of the SAME recommendation is a no-op: everything already
	//    holds the wanted value, so nothing is written and Postgres is not
	//    restarted. This is what makes re-provisioning safe.
	changed, err = m.ApplyTuning(ctx, rec)
	require.NoError(t, err)
	require.False(t, changed, "re-applying the same recommendation must be a no-op")
	assertApplied(rec)

	// 5. Self-healing: max_connections=1 is within the GUC's range (so ALTER SYSTEM
	//    accepts it) but the postmaster refuses to start because
	//    superuser_reserved_connections (3) + max_wal_senders (10) must be less than
	//    max_connections — a deterministic, instant startup failure that, unlike an
	//    oversized shared_buffers, does not hinge on the kernel's memory-overcommit
	//    behaviour. ApplyTuning must roll back to last-known-good, leaving Postgres
	//    UP on the prior config, and surface a CodeSafety error. Only
	//    max_connections changes here, so the rollback is exercised on a single
	//    restart-requiring setting.
	bad := rec
	bad.MaxConnections = 1 // unstartable, but in-range for the GUC
	changed, err = m.ApplyTuning(ctx, bad)
	require.Error(t, err, "a config the postmaster rejects must not silently succeed")
	require.Equal(t, core.CodeSafety, core.CodeOf(err),
		"a rolled-back config change is surfaced as CodeSafety")
	require.False(t, changed, "a rolled-back apply reports no change")

	// Postgres is still running on last-known-good: the query succeeds and
	// max_connections is back at the previously-applied 120, not the 1 we tried.
	current, err := m.readTunableSettings(ctx)
	require.NoError(t, err, "Postgres must still be reachable after the rollback")
	require.Equal(t, int64(120), current["max_connections"],
		"max_connections must be rolled back to last-known-good, not the rejected value")
	require.Equal(t, int64(256)*bytesPerMB, current["shared_buffers"],
		"the other restart-requiring setting must be unchanged by the rollback")
}
