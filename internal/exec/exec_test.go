package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestOSRunnerEcho(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	res, err := r.Run(context.Background(), RunSpec{Name: "echo", Args: []string{"hi"}})
	require.NoError(t, err)
	require.True(t, res.Success())
	require.Equal(t, "hi\n", res.Stdout)
}

func TestOSRunnerStdin(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	res, err := r.Run(context.Background(), RunSpec{Name: "cat", Stdin: "payload"})
	require.NoError(t, err)
	require.Equal(t, "payload", res.Stdout)
}

func TestOSRunnerNonZeroExit(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	res, err := r.Run(context.Background(), RunSpec{Name: "false"})
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.NotEqual(t, 0, res.ExitCode)
}

func TestOSRunnerFoldsStderrIntoError(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	// A failing command that prints a diagnostic to stderr — like pgbackrest does.
	res, err := r.Run(context.Background(), RunSpec{
		Name: "sh",
		Args: []string{"-c", "echo 'ERROR: unable to find a valid repository' >&2; exit 1"},
	})
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	// The actual reason must be in the rendered error string, not just a detail,
	// because callers persist err.Error() (e.g. backup_history.error).
	require.Contains(t, err.Error(), "unable to find a valid repository")
	// And it remains available as a structured detail.
	pe, ok := core.AsError(err)
	require.True(t, ok)
	require.Contains(t, pe.Details["stderr"], "unable to find a valid repository")
	require.Equal(t, 1, res.ExitCode)
}

func TestOSRunnerOmitsStderrForSensitive(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	res, err := r.Run(context.Background(), RunSpec{
		Name:      "sh",
		Args:      []string{"-c", "echo 'super-secret-token' >&2; exit 1"},
		Sensitive: true,
	})
	require.Error(t, err)
	// A sensitive command's stderr may carry secrets, so it must NOT be folded
	// into the (logged/persisted) error message.
	require.NotContains(t, err.Error(), "super-secret-token")
	require.Equal(t, 1, res.ExitCode)
}

func TestOSRunnerDryRun(t *testing.T) {
	r := NewOSRunner(core.Discard(), true)
	require.True(t, r.DryRun())
	res, err := r.Run(context.Background(), RunSpec{Name: "rm", Args: []string{"-rf", "/"}})
	require.NoError(t, err)
	require.True(t, res.DryRun)
}

func TestOSRunnerTimeout(t *testing.T) {
	r := NewOSRunner(core.Discard(), false)
	_, err := r.Run(context.Background(), RunSpec{Name: "sleep", Args: []string{"5"}, Timeout: 50 * time.Millisecond})
	require.Error(t, err)
}

func TestFakeRunnerMatch(t *testing.T) {
	f := NewFakeRunner().
		On("systemctl is-active", FakeResponse{Stdout: "active\n"}).
		On("pgbackrest", FakeResponse{ExitCode: 1, Err: errors.New("no stanza")})

	res, err := f.Run(context.Background(), RunSpec{Name: "systemctl", Args: []string{"is-active", "postgresql"}})
	require.NoError(t, err)
	require.Equal(t, "active\n", res.Stdout)

	_, err = f.Run(context.Background(), RunSpec{Name: "pgbackrest", Args: []string{"info"}})
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	require.Equal(t, 2, f.CallCount())
	require.Equal(t, "systemctl", f.Calls()[0].Name)
}

func TestFakeRunnerAsUserPrefix(t *testing.T) {
	f := NewFakeRunner().On("sudo -u postgres", FakeResponse{Stdout: "ok"})
	res, err := f.Run(context.Background(), RunSpec{Name: "psql", AsUser: "postgres"})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Stdout)
	require.Equal(t, []string{"sudo", "-u", "postgres", "psql"}, res.Command)
}
