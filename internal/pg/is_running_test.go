package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// IsRunning must judge "is Postgres up?" by a real liveness probe, not by
// `systemctl is-active postgresql`. On Debian/Ubuntu — the platform we provision
// via apt — that unit is a oneshot wrapper that reports "active" even when the
// real postgresql@<ver>-main.service failed to start, so trusting is-active lets
// a down cluster masquerade as running on the dashboard health badge.
func TestIsRunning_ProbesPostmasterNotSystemdWrapper(t *testing.T) {
	t.Run("down when the postmaster refuses connections despite the lying wrapper", func(t *testing.T) {
		r := exec.NewFakeRunner()
		// The oneshot wrapper lies: is-active says "active"...
		r.On("is-active", exec.FakeResponse{Stdout: "active"})
		// ...but the real cluster is down — SELECT 1 over the socket fails.
		r.On("SELECT 1", exec.FakeResponse{ExitCode: 2, Err: errors.New("could not connect to server")})
		m := newManager(r)

		running, err := m.IsRunning(context.Background())
		require.NoError(t, err, "a down cluster is a normal, queryable state, not an error")
		require.False(t, running,
			"a down postmaster must report not-running even though the systemd wrapper says active")

		// It must have keyed off the real probe, as the postgres OS user, and
		// must NEVER consult the lying `systemctl is-active` wrapper (the bug).
		var probed bool
		for _, c := range r.Calls() {
			if c.Name == "psql" && argvContains(c.Args, "SELECT 1") {
				probed = true
				require.Equal(t, "postgres", c.AsUser, "the liveness probe runs as the postgres OS user")
			}
			if c.Name == "systemctl" {
				require.NotContains(t, c.Args, "is-active",
					"IsRunning must not trust the systemd wrapper's is-active")
			}
		}
		require.True(t, probed, "IsRunning must probe the postmaster with SELECT 1, not trust is-active")
	})

	t.Run("running when the postmaster accepts connections", func(t *testing.T) {
		r := exec.NewFakeRunner()
		r.On("SELECT 1", exec.FakeResponse{Stdout: "1"})
		m := newManager(r)

		running, err := m.IsRunning(context.Background())
		require.NoError(t, err)
		require.True(t, running, "an accepting postmaster reports running")
	})

	t.Run("requires a runner", func(t *testing.T) {
		m := newManager(nil)
		_, err := m.IsRunning(context.Background())
		require.Error(t, err)
	})
}
