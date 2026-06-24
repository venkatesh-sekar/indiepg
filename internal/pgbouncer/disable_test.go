package pgbouncer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// newDisableManager builds a Manager over a FakeRunner (no temp confdir needed —
// Disable never touches files). systemctl disable --now succeeds by default.
func newDisableManager(t *testing.T) (*Manager, *exec.FakeRunner) {
	t.Helper()
	r := exec.NewFakeRunner()
	m := New(Options{Runner: r, Logger: core.Discard(), ConfDir: t.TempDir()})
	return m, r
}

func TestDisable_StopsServiceThenPersistsOff(t *testing.T) {
	m, r := newDisableManager(t)
	state := newFakeState()
	// Simulate a pooler that was previously enabled.
	require.NoError(t, state.SetConfig(context.Background(), EnabledConfigKey, enabledValue))
	state.setKeys = nil // reset so we observe only the Disable write below

	require.NoError(t, m.Disable(context.Background(), state))

	// The service was stopped + disabled.
	require.Contains(t, allArgv(r), "systemctl disable --now pgbouncer")

	// The off state was persisted and now reads as disabled.
	require.Equal(t, []string{EnabledConfigKey}, state.setKeys)
	require.Equal(t, disabledValue, state.kv[EnabledConfigKey])
	enabled, err := IsEnabled(context.Background(), state)
	require.NoError(t, err)
	require.False(t, enabled)
}

// The load-bearing safety property: if stopping the service fails, the enabled
// flag is NEVER cleared, so the panel cannot claim the pooler is off while it is
// in fact still running. This is what makes "service first, then persist" the
// correct order — a persist-first implementation would flip the flag off here.
func TestDisable_ServiceStopFailureDoesNotPersist(t *testing.T) {
	m, r := newDisableManager(t)
	r.On("disable --now pgbouncer", exec.FakeResponse{Err: errors.New("unit transition failed")})
	state := newFakeState()
	require.NoError(t, state.SetConfig(context.Background(), EnabledConfigKey, enabledValue))
	state.setKeys = nil

	err := m.Disable(context.Background(), state)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	// The flag was never touched: the pooler still reads as enabled.
	require.Empty(t, state.setKeys, "the off flag must not be set when the service fails to stop")
	enabled, ierr := IsEnabled(context.Background(), state)
	require.NoError(t, ierr)
	require.True(t, enabled, "a failed stop must leave the pooler reported as still on")
}

// Turning off a pooler that was never on (or is already off) is a clean no-op
// success: the idempotent systemctl call succeeds and the off state is recorded.
func TestDisable_IdempotentWhenAlreadyOff(t *testing.T) {
	m, _ := newDisableManager(t)
	state := newFakeState()

	require.NoError(t, m.Disable(context.Background(), state))
	require.Equal(t, disabledValue, state.kv[EnabledConfigKey])

	// A second call is also fine.
	require.NoError(t, m.Disable(context.Background(), state))
	enabled, err := IsEnabled(context.Background(), state)
	require.NoError(t, err)
	require.False(t, enabled)
}

// A persist failure AFTER the service is stopped surfaces as an error so the
// operator can retry. The service is already down (the safe direction), and a
// retry re-stops idempotently then re-persists.
func TestDisable_PersistFailureSurfaces(t *testing.T) {
	m, _ := newDisableManager(t)
	state := newFakeState()
	state.setErr = errors.New("db locked")

	err := m.Disable(context.Background(), state)
	require.Error(t, err)
}

func TestDisable_RequiresRunnerAndState(t *testing.T) {
	m, _ := newDisableManager(t)

	// nil state
	err := m.Disable(context.Background(), nil)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))

	// nil runner
	mNoRunner := New(Options{Logger: core.Discard(), ConfDir: t.TempDir()})
	err = mNoRunner.Disable(context.Background(), newFakeState())
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}
