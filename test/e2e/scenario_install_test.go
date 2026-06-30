//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestInstallFromScratch is scenario 1: a real `indiepg install` on the bare
// systemd base image. It asserts on real ground truth — systemd unit state, the
// live HTTP API, and the provisioned Postgres — not just HTTP 200s.
func TestInstallFromScratch(t *testing.T) {
	t.Parallel()

	// Base image: nothing is installed yet, so don't wait for /readyz at Up.
	env := harness.Up(t, harness.Options{Image: harness.ImageBase, SkipReadyWait: true})

	// Trigger the real provision: apt install from the pre-cached PGDG packages,
	// systemctl enable --now postgresql, role creation, tuning, and the panel unit.
	// No --password, so install GENERATES one and prints it exactly once.
	out, err := env.Install()
	require.NoError(t, err, "indiepg install should succeed from scratch")

	password, err := harness.ParseAdminPassword(out)
	require.NoError(t, err, "install should print a one-time admin password")
	require.NotEmpty(t, password)

	// Ground truth: both systemd units are genuinely active.
	require.Equal(t, "active", env.SystemctlIsActive("postgresql"),
		"postgresql unit must be active after install")
	require.Equal(t, "active", env.SystemctlIsActive("indiepg"),
		"indiepg panel unit must be active after install")

	// The panel must come up and report ready.
	env.AwaitReady(90 * time.Second)
	require.NoError(t, env.Panel.Readyz(), "/readyz must report ok")
	require.NoError(t, env.Panel.Healthz(), "/healthz must report ok")

	// Log in over HTTP with the captured one-time password.
	require.NoError(t, env.Panel.Login(password), "login with the generated password must succeed")

	// A trivial authenticated GET must work and reflect a real provisioned cluster:
	// the instance identity carries the Postgres system id read from the live cluster.
	inst, err := env.Panel.Instance()
	require.NoError(t, err)
	require.NotEmpty(t, inst.InstanceID, "instance id should be set")
	require.NotEmpty(t, inst.PGSystemID, "pg system id should be read from the provisioned cluster")

	health, err := env.Panel.Health()
	require.NoError(t, err)
	require.Equal(t, "ok", health.Panel)
	require.Equal(t, "ok", health.Store)

	// Postgres ground truth over the socket: a real, queryable, supported cluster.
	major, err := env.PG.ServerVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, major, 15, "provisioned a supported PostgreSQL major")

	// The panel's dedicated read-only role exists (proof provisionSQL ran).
	exists, err := env.PG.Scalar("SELECT count(*) FROM pg_roles WHERE rolname = 'indiepg_readonly'")
	require.NoError(t, err)
	require.Equal(t, "1", exists, "the indiepg_readonly role should have been created")
}
