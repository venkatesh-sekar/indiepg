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
	// The pooler is up after the SIGHUP, so the reload is the whole story.
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})

	require.NoError(t, m.Reload(context.Background()))

	calls := r.Calls()
	// A successful reload verifies the pooler is still up (reload + is-active) and
	// must NOT restart — a restart would needlessly drop every client connection.
	require.Len(t, calls, 2)
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
	require.Equal(t, "systemctl is-active pgbouncer", argvOf(calls[1]))
	require.NotContains(t, allArgv(r), "systemctl restart pgbouncer")
}

func TestReload_FallsBackToRestartWhenReloadFails(t *testing.T) {
	m, r := newServiceManager()
	r.On("reload pgbouncer", exec.FakeResponse{Err: errors.New("reload unsupported")})
	// The restart brings it back up; the post-apply verify confirms it.
	r.On("is-active pgbouncer", exec.FakeResponse{Stdout: "active\n"})

	require.NoError(t, m.Reload(context.Background()))

	calls := r.Calls()
	require.Len(t, calls, 3, "a failed reload falls back to a restart, then verifies the pooler is up")
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
	require.Equal(t, "systemctl restart pgbouncer", argvOf(calls[1]))
	require.Equal(t, "systemctl is-active pgbouncer", argvOf(calls[2]))
}

func TestReload_BothFailingSurfacesError(t *testing.T) {
	m, r := newServiceManager()
	r.On("reload pgbouncer", exec.FakeResponse{Err: errors.New("reload failed")})
	r.On("restart pgbouncer", exec.FakeResponse{Err: errors.New("restart failed")})

	err := m.Reload(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	// Both systemctl commands failed, so there is nothing to verify — is-active
	// must never run once the apply itself has already hard-failed.
	require.NotContains(t, allArgv(r), "systemctl is-active pgbouncer")
}

// TestReload_ErrorsWhenPoolerDeadAfterReload is the core guard: a SIGHUP reload
// can exit 0 while PgBouncer then dies re-parsing a bad config. Reload must
// confirm the unit is actually active afterward and, when it is not, return a
// loud error rather than reporting success over a dead pooler. It does NOT mask
// the failure behind an (equally doomed) restart of the same rejected config.
func TestReload_ErrorsWhenPoolerDeadAfterReload(t *testing.T) {
	m, r := newServiceManager()
	// reload exits 0 (default), but the pooler is down immediately afterward.
	r.On("is-active pgbouncer", exec.FakeResponse{
		Stdout: "failed\n", ExitCode: 3, Err: errors.New("exit status 3"),
	})

	err := m.Reload(context.Background())
	require.Error(t, err, "a reload that leaves the pooler dead must not report success")
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	calls := r.Calls()
	// Exactly reload → verify, nothing more: the count bound closes the escape
	// where a trailing extra systemctl call (a "diagnostic" is-failed/status) on
	// the failure path would slip past a positions-only assertion.
	require.Len(t, calls, 2)
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
	require.Equal(t, "systemctl is-active pgbouncer", argvOf(calls[1]))
	require.NotContains(t, allArgv(r), "systemctl restart pgbouncer",
		"a clean reload that left the pooler dead is a hard error, not a restart of the same rejected config")
}

// TestReload_ErrorsWhenPoolerDeadAfterRestart guards the fallback path: systemd
// can report a restart succeeded before a unit that crashes on startup is caught.
// Reload must verify the pooler is actually up after the restart, not trust the
// command's 0 exit.
func TestReload_ErrorsWhenPoolerDeadAfterRestart(t *testing.T) {
	m, r := newServiceManager()
	r.On("reload pgbouncer", exec.FakeResponse{Err: errors.New("reload unsupported")})
	// restart exits 0 (default), but the unit is not active afterward.
	r.On("is-active pgbouncer", exec.FakeResponse{
		Stdout: "failed\n", ExitCode: 3, Err: errors.New("exit status 3"),
	})

	err := m.Reload(context.Background())
	require.Error(t, err, "a restart that reports success while the pooler stays down must not report success")
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	calls := r.Calls()
	// Exactly reload → restart → verify, nothing more (see the count-bound note on
	// the dead-after-reload test).
	require.Len(t, calls, 3)
	require.Equal(t, "systemctl reload pgbouncer", argvOf(calls[0]))
	require.Equal(t, "systemctl restart pgbouncer", argvOf(calls[1]))
	require.Equal(t, "systemctl is-active pgbouncer", argvOf(calls[2]))
}

// TestReload_ErrorsWhenRunStateUndeterminableAfterApply proves "couldn't even ask
// systemctl" after applying config surfaces as the error it is, never a silent
// success. Empty stdout AND a runner error means the state is genuinely unknown
// (systemctl absent from PATH / cancelled ctx), which IsRunning reports as an
// error — Reload must propagate it, not assume the pooler is up.
func TestReload_ErrorsWhenRunStateUndeterminableAfterApply(t *testing.T) {
	m, r := newServiceManager()
	r.On("is-active pgbouncer", exec.FakeResponse{Err: errors.New("exec: \"systemctl\": not found")})

	err := m.Reload(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Contains(t, err.Error(), "could not determine service state",
		"an undeterminable state must surface as exactly that, not be conflated with a confirmed-down pooler")
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
