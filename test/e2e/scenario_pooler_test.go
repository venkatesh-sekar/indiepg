//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// poolAppRole / poolAppPassword are the deterministic login role the scenario
// provisions in Postgres and then routes through the pooler. The password is
// stored as a SCRAM-SHA-256 verifier (set explicitly below), which is exactly
// what the enable flow copies verbatim from pg_authid into the pooler auth_file.
const (
	poolAppRole     = "e2e_pool_app"
	poolAppPassword = "E2ePoolAppPw-v1"
)

// TestPoolerEnableDisable is scenario 15: the opt-in PgBouncer connection pooler.
// It drives the real panel flow (GET /api/pooler -> POST /api/pooler/enable ->
// POST /api/pooler/disable) and asserts REAL ground truth at every step — the
// systemd unit's actual state, the config + auth_file actually written under
// /etc/pgbouncer, and a real client connection routed THROUGH the bouncer into
// Postgres — never just HTTP 200s.
//
//	1. pooler initially OFF: GET /api/pooler reports disabled and the pgbouncer
//	   unit is not active (the package is not even installed yet).
//	2. enable: POST /api/pooler/enable installs+starts pgbouncer; the unit becomes
//	   active, pgbouncer.ini + userlist.txt land under /etc/pgbouncer, GET reports
//	   enabled, and a real SCRAM client connecting to the bouncer's loopback port
//	   is proxied to the live Postgres (proven by reading Postgres' own port back).
//	3. disable: POST /api/pooler/disable stops the unit; it is no longer active and
//	   GET /api/pooler reports disabled again.
func TestPoolerEnableDisable(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// --- 1. Pooler initially OFF ---------------------------------------------

	status, err := env.Panel.GetPooler()
	require.NoError(t, err, "GET /api/pooler should succeed on the preinstalled box")
	require.False(t, status.Enabled, "the pooler must be OFF by default (opt-in)")
	require.Equal(t, "127.0.0.1", status.Host, "the pooler is only ever advertised on loopback")
	require.Equal(t, 6432, status.ListenPort, "the pooler listens on the standard 6432")
	require.NotEqual(t, "active", env.SystemctlIsActive("pgbouncer"),
		"the pgbouncer unit must not be active before the pooler is enabled")

	// Provision a deterministic login role with a SCRAM-SHA-256 password (the
	// verifier the pooler auth_file is built from) BEFORE enabling — the enable
	// flow reads pg_authid for exactly these roles and refuses a role with no
	// stored password. GROUND TRUTH path (psql as postgres over the socket).
	require.NoError(t, env.PG.Exec(fmt.Sprintf(
		"SET password_encryption = 'scram-sha-256'; CREATE ROLE %s LOGIN PASSWORD %s",
		poolAppRole, quoteLiteral(poolAppPassword))),
		"creating the pooled login role should succeed")
	require.NoError(t, env.PG.Exec(fmt.Sprintf(
		"GRANT CONNECT ON DATABASE postgres TO %s", poolAppRole)),
		"the pooled role must be able to connect to the target database")

	verifier, err := env.PG.Scalar(fmt.Sprintf(
		"SELECT rolpassword FROM pg_authid WHERE rolname = %s", quoteLiteral(poolAppRole)))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(verifier, "SCRAM-SHA-256$"),
		"the role's stored verifier must be SCRAM-SHA-256 (got %q)", verifier)

	// --- 2. Enable -----------------------------------------------------------

	res, err := env.Panel.EnablePooler([]string{poolAppRole}, "mixed")
	require.NoError(t, err, "POST /api/pooler/enable should succeed")
	require.True(t, res.Running, "enable must confirm the pgbouncer unit is running")
	require.Equal(t, []string{poolAppRole}, res.PooledRoles, "the requested role must be the pooled role")
	require.Greater(t, res.Pool.DefaultPoolSize, 0, "the pool must be sized against live max_connections")

	// Real ground truth: the systemd unit is actually active. (The handler already
	// confirmed this server-side, but assert it independently via systemctl.) Await
	// rather than sleep — the box is I/O-slow under load.
	harness.Await(t, 90*time.Second, time.Second, "pgbouncer unit active", func() (bool, error) {
		return env.SystemctlIsActive("pgbouncer") == "active", nil
	})

	// The managed config + auth_file were written under /etc/pgbouncer.
	_, err = env.Exec("test", "-f", "/etc/pgbouncer/pgbouncer.ini")
	require.NoError(t, err, "the managed pgbouncer.ini must exist under /etc/pgbouncer")
	_, err = env.Exec("test", "-f", "/etc/pgbouncer/userlist.txt")
	require.NoError(t, err, "the managed auth_file (userlist.txt) must exist under /etc/pgbouncer")

	// The config must point at loopback only and carry the pooled role in the
	// auth_file (proof the SCRAM verifier was installed, not just an empty file).
	ini, err := env.Exec("cat", "/etc/pgbouncer/pgbouncer.ini")
	require.NoError(t, err)
	require.Contains(t, ini, "listen_addr = 127.0.0.1", "the pooler must bind loopback only")
	require.Contains(t, ini, "listen_port = 6432")
	require.Contains(t, ini, "auth_type = scram-sha-256", "the pooler must require SCRAM (no auth downgrade)")

	userlist, err := env.Exec("cat", "/etc/pgbouncer/userlist.txt")
	require.NoError(t, err)
	require.Contains(t, userlist, `"`+poolAppRole+`"`, "the pooled role must appear in the auth_file")
	require.Contains(t, userlist, "SCRAM-SHA-256$", "the auth_file must store a SCRAM verifier")

	// GET /api/pooler now reports enabled.
	status, err = env.Panel.GetPooler()
	require.NoError(t, err)
	require.True(t, status.Enabled, "GET /api/pooler must report enabled after enable")

	// Real end-to-end: a SCRAM client connecting to the bouncer's loopback port is
	// proxied through to the live Postgres. Reading Postgres' own listen port back
	// (5432) over the 6432 connection proves the query executed on the real backend
	// the bouncer routed to (the pgbouncer admin console could not answer this).
	// Await the first successful route — the listener can lag the unit becoming
	// active, and SCRAM pass-through opens the first upstream connection lazily.
	var throughPort string
	harness.Await(t, 90*time.Second, 2*time.Second, "connection routed through pgbouncer", func() (bool, error) {
		out, err := env.Exec("env", "PGPASSWORD="+poolAppPassword,
			"psql", "-h", "127.0.0.1", "-p", "6432", "-U", poolAppRole, "-d", "postgres",
			"-tAqX", "-c", "SHOW port")
		if err != nil {
			return false, err
		}
		throughPort = strings.TrimSpace(out)
		return throughPort == "5432", nil
	})
	require.Equal(t, "5432", throughPort,
		"a query through the pooler must execute on the upstream Postgres (port 5432)")

	// --- 3. Disable ----------------------------------------------------------

	offStatus, err := env.Panel.DisablePooler()
	require.NoError(t, err, "POST /api/pooler/disable should succeed")
	require.False(t, offStatus.Enabled, "the disable response must report the pooler off")

	// Real ground truth: the unit is actually stopped.
	harness.Await(t, 90*time.Second, time.Second, "pgbouncer unit stopped", func() (bool, error) {
		return env.SystemctlIsActive("pgbouncer") != "active", nil
	})

	// GET /api/pooler reports disabled again.
	status, err = env.Panel.GetPooler()
	require.NoError(t, err)
	require.False(t, status.Enabled, "GET /api/pooler must report disabled after disable")
}

// quoteLiteral wraps a string in single quotes and doubles any embedded single
// quote, for safe interpolation of a literal into a psql -c statement. The values
// here are fixed test constants, so this is belt-and-suspenders rather than a
// defense against untrusted input.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
