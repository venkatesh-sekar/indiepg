package pgbouncer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// newServiceManager builds a Manager over a fresh FakeRunner and returns both so
// tests can drive responses and assert the exact systemctl/apt invocations.
func newServiceManager() (*Manager, *exec.FakeRunner) {
	r := exec.NewFakeRunner()
	m := New(Options{Runner: r, Logger: core.Discard()})
	return m, r
}

// argvOf joins a recorded call's resolved argv for substring assertions.
func argvOf(c exec.RunSpec) string {
	return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
}

func TestInstallPackage_RefreshesIndexThenInstalls(t *testing.T) {
	m, r := newServiceManager()

	require.NoError(t, m.InstallPackage(context.Background()))

	calls := r.Calls()
	require.Len(t, calls, 2, "must run apt-get update then apt-get install")
	require.Equal(t, "apt-get update", argvOf(calls[0]))
	require.Equal(t, "apt-get install -y pgbouncer", argvOf(calls[1]))
	// Both apt steps must run non-interactively so a prompt can't wedge the install.
	for _, c := range calls {
		require.Contains(t, c.Env, "DEBIAN_FRONTEND=noninteractive")
	}
}

func TestInstallPackage_UpdateFailureStopsBeforeInstall(t *testing.T) {
	m, r := newServiceManager()
	r.On("apt-get update", exec.FakeResponse{Err: errors.New("index unreachable")})

	err := m.InstallPackage(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	// install must NOT run once update failed.
	require.Equal(t, 1, r.CallCount())
}

func TestInstallPackage_InstallFailurePropagatesWithHint(t *testing.T) {
	m, r := newServiceManager()
	r.On("install -y pgbouncer", exec.FakeResponse{Err: errors.New("no candidate")})

	err := m.InstallPackage(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Contains(t, err.Error(), "pgbouncer")
}

func TestInstallPackage_RequiresRunner(t *testing.T) {
	m := New(Options{Logger: core.Discard()})
	err := m.InstallPackage(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestEnableNow_EnablesAndStartsIdempotently(t *testing.T) {
	m, r := newServiceManager()

	require.NoError(t, m.EnableNow(context.Background()))

	calls := r.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, "systemctl enable --now pgbouncer", argvOf(calls[0]))
}

func TestEnableNow_FailurePropagates(t *testing.T) {
	m, r := newServiceManager()
	r.On("enable --now pgbouncer", exec.FakeResponse{Err: errors.New("unit not found")})

	err := m.EnableNow(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestEnableNow_RequiresRunner(t *testing.T) {
	m := New(Options{Logger: core.Discard()})
	err := m.EnableNow(context.Background())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestReload_PrefersSIGHUPReloadAndNeverRestartsOnSuccess(t *testing.T) {
	m, r := newServiceManager()

	require.NoError(t, m.Reload(context.Background()))

	calls := r.Calls()
	require.Len(t, calls, 1, "a successful reload must NOT also restart (would drop client connections)")
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
}

func TestReload_FallsBackToRestartWhenReloadFails(t *testing.T) {
	m, r := newServiceManager()
	r.On("reload pgbouncer", exec.FakeResponse{Err: errors.New("reload unsupported")})

	require.NoError(t, m.Reload(context.Background()))

	calls := r.Calls()
	require.Len(t, calls, 2, "a failed reload must fall back to a restart")
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
	require.Equal(t, "systemctl restart pgbouncer", argvOf(calls[1]))
}

func TestReload_BothFailingSurfacesError(t *testing.T) {
	m, r := newServiceManager()
	r.On("reload pgbouncer", exec.FakeResponse{Err: errors.New("reload failed")})
	r.On("restart pgbouncer", exec.FakeResponse{Err: errors.New("restart failed")})

	err := m.Reload(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestReload_RequiresRunner(t *testing.T) {
	m := New(Options{Logger: core.Discard()})
	err := m.Reload(context.Background())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestIsRunning_TrueWhenActive(t *testing.T) {
	m, r := newServiceManager()
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})

	running, err := m.IsRunning(context.Background())
	require.NoError(t, err)
	require.True(t, running)
}

func TestIsRunning_FalseWhenInactiveExitNonZero(t *testing.T) {
	m, r := newServiceManager()
	// systemctl is-active exits non-zero for an inactive/failed unit; with a
	// reported state ("inactive") that is a legitimate "not running" answer, not a
	// Runner failure.
	r.On("is-active pgbouncer", exec.FakeResponse{
		Stdout: "inactive\n", ExitCode: 3, Err: errors.New("exit status 3"),
	})

	running, err := m.IsRunning(context.Background())
	require.NoError(t, err)
	require.False(t, running)
}

func TestIsRunning_ErrorsWhenStateUndeterminable(t *testing.T) {
	m, r := newServiceManager()
	// Empty stdout AND a runner error means systemctl could not run at all (absent
	// from PATH, cancelled ctx). The state is unknown, so the enable flow must see
	// the error rather than a silent "not running".
	r.On("is-active pgbouncer", exec.FakeResponse{Err: errors.New("exec: \"systemctl\": not found")})

	running, err := m.IsRunning(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.False(t, running)
}

func TestIsRunning_RequiresRunner(t *testing.T) {
	m := New(Options{Logger: core.Discard()})
	_, err := m.IsRunning(context.Background())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}
