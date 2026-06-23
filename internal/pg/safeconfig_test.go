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

	for _, c := range r.Calls() {
		require.NotEqual(t, "tee", c.Name, "no rollback write when the restart succeeds")
	}
}
