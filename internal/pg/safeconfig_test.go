package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// flakyRestartRunner wraps a FakeRunner but can force the first `systemctl
// restart` to fail — simulating a config change that stops Postgres from coming
// back up. All other calls (including later restarts) pass through to the
// FakeRunner, which records them.
type flakyRestartRunner struct {
	*exec.FakeRunner
	failFirstRestart bool
	restartCalls     int
}

func (f *flakyRestartRunner) Run(ctx context.Context, spec exec.RunSpec) (exec.RunResult, error) {
	if spec.Name == "systemctl" && len(spec.Args) > 0 && spec.Args[0] == "restart" {
		f.restartCalls++
		if f.failFirstRestart && f.restartCalls == 1 {
			return exec.RunResult{ExitCode: 1}, core.ExecError("postgresql.service failed to start")
		}
	}
	return f.FakeRunner.Run(ctx, spec)
}

// flakyLivenessRunner wraps a FakeRunner where `systemctl restart` ALWAYS
// succeeds (exit 0) — modelling the Debian/Ubuntu `postgresql` oneshot wrapper
// unit, which exits 0 even when the real postgresql@<ver>-main.service fails to
// start. The only signal that Postgres is actually down is the post-restart
// liveness probe (a `SELECT 1`), which this runner fails for the first
// failChecks probes. It lets us prove the rollback fires on the liveness signal,
// NOT on the (lying) systemd exit code.
type flakyLivenessRunner struct {
	*exec.FakeRunner
	failChecks int // number of leading liveness probes to fail
	checkCalls int
	restarts   int
}

func (f *flakyLivenessRunner) Run(ctx context.Context, spec exec.RunSpec) (exec.RunResult, error) {
	if spec.Name == "systemctl" && len(spec.Args) > 0 && spec.Args[0] == "restart" {
		f.restarts++
	}
	if spec.Name == "psql" && argvContains(spec.Args, "SELECT 1") {
		f.checkCalls++
		if f.checkCalls <= f.failChecks {
			// Postgres is not accepting connections, even though systemd said OK.
			return exec.RunResult{ExitCode: 2}, core.ExecError("could not connect to server")
		}
	}
	return f.FakeRunner.Run(ctx, spec)
}

func argvContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// THE regression test for the systemd-wrapper-lie bug: on Debian/Ubuntu
// `systemctl restart postgresql` exits 0 even when the cluster fails to start,
// so trusting that exit code lets a bad config leave Postgres down while the
// panel reports success. The rollback MUST instead key off a real liveness
// probe: a restart that systemd reports OK but that left Postgres not accepting
// connections must still roll back to last-known-good (which then comes up).
func TestRestartWithRollback_RollsBackWhenSystemdLiesAboutStartup(t *testing.T) {
	base := exec.NewFakeRunner()
	// failChecks=1: the post-change restart's liveness probe fails (PG down),
	// the post-rollback restart's probe succeeds (last-known-good comes up).
	r := &flakyLivenessRunner{FakeRunner: base, failChecks: 1}
	m := newManager(r)

	snap := autoConfSnapshot{path: "/data/postgresql.auto.conf", content: "# last known good\n"}

	err := m.restartWithRollback(context.Background(), snap, "host-sized tuning")
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err),
		"systemd reported success but Postgres was down; the change must still roll back")

	require.Equal(t, 2, r.restarts, "one restart on the new config, one after rollback")
	require.Equal(t, 2, r.checkCalls, "liveness must be probed after BOTH restarts, not the systemd exit code")

	var restored bool
	for _, c := range base.Calls() {
		if c.Name == "tee" {
			require.Equal(t, snap.content, c.Stdin,
				"a systemd-OK-but-actually-down restart must restore last-known-good")
			restored = true
		}
	}
	require.True(t, restored, "rollback must fire on the liveness signal, not the lying systemd exit")
}

// If Postgres never accepts connections even after the rollback restart (despite
// systemd reporting every restart OK), the operator must get an honest
// CodeInternal "Postgres is down" — NOT a soft CodeSafety "rolled back; running"
// that the systemd exit code would otherwise wrongly produce.
func TestRestartWithRollback_HonestWhenStillDownDespiteSystemdOK(t *testing.T) {
	base := exec.NewFakeRunner()
	// systemctl restart always exits 0, but Postgres never accepts connections.
	base.On("SELECT 1", exec.FakeResponse{ExitCode: 2, Err: errors.New("could not connect to server")})
	m := newManager(base)

	snap := autoConfSnapshot{path: "/data/postgresql.auto.conf", content: "# good\n"}

	err := m.restartWithRollback(context.Background(), snap, "test setting")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err),
		"if Postgres stays down after rollback, that is an internal failure, not a clean safety stop")
	require.Contains(t, err.Error(), "down")
}

// snapshotAutoConf must capture the exact file content (for an exact rollback)
// and fail closed when the file cannot be read — a restart we cannot undo must
// never proceed.
func TestSnapshotAutoConf(t *testing.T) {
	t.Run("captures path and content", func(t *testing.T) {
		r := exec.NewFakeRunner()
		r.On("data_directory", exec.FakeResponse{Stdout: "/var/lib/postgresql/14/main"})
		r.On("postgresql.auto.conf", exec.FakeResponse{Stdout: "shared_buffers = '128MB'\n"})
		m := newManager(r)

		snap, err := m.snapshotAutoConf(context.Background())
		require.NoError(t, err)
		require.Equal(t, "/var/lib/postgresql/14/main/postgresql.auto.conf", snap.path)
		require.Equal(t, "shared_buffers = '128MB'\n", snap.content)

		// It must read the file as the postgres OS user (mode 0600, postgres-owned).
		var sawCatAsPostgres bool
		for _, c := range r.Calls() {
			if c.Name == "cat" {
				require.Equal(t, "postgres", c.AsUser)
				sawCatAsPostgres = true
			}
		}
		require.True(t, sawCatAsPostgres, "auto.conf must be read via cat")
	})

	t.Run("fails closed when the file cannot be read", func(t *testing.T) {
		r := exec.NewFakeRunner()
		r.On("data_directory", exec.FakeResponse{Stdout: "/data"})
		r.On("cat", exec.FakeResponse{Err: errors.New("permission denied")})
		m := newManager(r)

		_, err := m.snapshotAutoConf(context.Background())
		require.Error(t, err)
	})
}

// The acceptance test for self-healing config: a change that stops Postgres
// (first restart fails) must be rolled back to last-known-good and Postgres
// brought back up, surfacing a CodeSafety error.
func TestRestartWithRollback_RecoversFromBadConfig(t *testing.T) {
	base := exec.NewFakeRunner()
	r := &flakyRestartRunner{FakeRunner: base, failFirstRestart: true}
	m := newManager(r)

	snap := autoConfSnapshot{
		path:    "/var/lib/postgresql/14/main/postgresql.auto.conf",
		content: "# last known good\nwork_mem = '4MB'\n",
	}

	err := m.restartWithRollback(context.Background(), snap, "test setting")
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err),
		"a rolled-back change is a safety stop, not an internal failure")

	require.Equal(t, 2, r.restartCalls, "one failed restart, then one rollback restart")

	var restored bool
	for _, c := range base.Calls() {
		if c.Name == "tee" {
			require.Equal(t, "postgres", c.AsUser, "auto.conf must be restored as the postgres user")
			require.Equal(t, []string{snap.path}, c.Args)
			require.Equal(t, snap.content, c.Stdin,
				"rollback must restore the EXACT last-known-good content")
			restored = true
		}
	}
	require.True(t, restored, "a failed restart must restore last-known-good auto.conf")
}

// If the rollback restart ALSO fails, the operator must get an honest
// CodeInternal "Postgres is down" error, not a soft safety message.
func TestRestartWithRollback_RollbackRestartAlsoFails(t *testing.T) {
	base := exec.NewFakeRunner()
	// Every restart fails: the bad change AND the rollback both fail to start.
	base.On("restart postgresql", exec.FakeResponse{Err: errors.New("failed to start")})
	m := newManager(base)

	snap := autoConfSnapshot{path: "/data/postgresql.auto.conf", content: "# good\n"}

	err := m.restartWithRollback(context.Background(), snap, "test setting")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.Contains(t, err.Error(), "down")
}

// On a clean restart there is no rollback: auto.conf is never rewritten.
func TestRestartWithRollback_NoRollbackOnSuccess(t *testing.T) {
	r := exec.NewFakeRunner()
	m := newManager(r)

	snap := autoConfSnapshot{path: "/data/postgresql.auto.conf", content: "# good\n"}
	require.NoError(t, m.restartWithRollback(context.Background(), snap, "test setting"))

	var probed bool
	for _, c := range r.Calls() {
		require.NotEqual(t, "tee", c.Name, "no rollback write when the restart succeeds")
		if c.Name == "psql" && argvContains(c.Args, "SELECT 1") {
			probed = true
		}
	}
	require.True(t, probed,
		"even a clean restart must verify Postgres actually came back up, not trust the systemd exit code")
}
