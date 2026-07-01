//go:build e2e

package e2e

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// resultEnvelope decodes a core.Result returned directly (POST /api/databases).
type resultEnvelope struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// credentialEnvelope decodes the credentialResult returned by the guided actions
// that mint secrets (new-app, create role, create read-only user). The plaintext
// secrets are returned exactly once, here.
type credentialEnvelope struct {
	Result  resultEnvelope    `json:"result"`
	Secrets map[string]string `json:"secrets"`
}

// TestRolesAndDatabases is the first half of scenario 16: it drives the panel's
// real database/role guided actions through the HTTP API and asserts the result
// against Postgres GROUND TRUTH (pg_database / pg_roles read directly over the
// socket), not the API's own 200s.
//
//   - POST /api/databases creates a plain database.
//   - POST /api/databases/new-app provisions a one-click app bundle: an owner
//     group role, the database owned by it, plus read-write and read-only login
//     users (with their generated secrets returned once).
//   - POST /api/roles creates a login role (secret returned) and a NOLOGIN group
//     role (no secret).
func TestRolesAndDatabases(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// --- POST /api/databases: a plain database ---
	const soloDB = "e2e_solo_db"
	var dbRes resultEnvelope
	require.NoError(t,
		env.Panel.PostLong("/api/databases", map[string]string{"name": soloDB}, &dbRes),
		"POST /api/databases should succeed")
	require.True(t, dbRes.OK, "create-database result should be ok")

	// Ground truth: the database actually exists in pg_database.
	got, err := env.PG.Scalar("SELECT count(*) FROM pg_database WHERE datname = '" + soloDB + "'")
	require.NoError(t, err)
	require.Equal(t, "1", got, "the database must exist in pg_database")

	// --- POST /api/databases/new-app: the one-click app bundle ---
	const app = "e2e_shop"
	ownerRole := app + "_owner"
	rwUser := app + "_app"
	roUser := app + "_readonly"

	var appRes credentialEnvelope
	require.NoError(t,
		env.Panel.PostLong("/api/databases/new-app", map[string]string{"database": app}, &appRes),
		"POST /api/databases/new-app should succeed")
	require.True(t, appRes.Result.OK, "new-app result should be ok")

	// The two login users' secrets are returned exactly once, here.
	require.Contains(t, appRes.Secrets, rwUser, "new-app must return the read-write user's secret")
	require.Contains(t, appRes.Secrets, roUser, "new-app must return the read-only user's secret")
	require.NotEmpty(t, appRes.Secrets[rwUser])
	require.NotEmpty(t, appRes.Secrets[roUser])

	// Ground truth: the app database exists and is owned by the owner GROUP role.
	ownedByOwner, err := env.PG.Scalar(
		"SELECT count(*) FROM pg_database d JOIN pg_roles r ON d.datdba = r.oid " +
			"WHERE d.datname = '" + app + "' AND r.rolname = '" + ownerRole + "'")
	require.NoError(t, err)
	require.Equal(t, "1", ownedByOwner, "the app database must be owned by %q", ownerRole)

	// Ground truth: the three roles exist with the right login attributes.
	ownerLogin, err := env.PG.Scalar("SELECT rolcanlogin FROM pg_roles WHERE rolname = '" + ownerRole + "'")
	require.NoError(t, err)
	require.Equal(t, "f", ownerLogin, "the owner role must be a NOLOGIN group role")

	rwLogin, err := env.PG.Scalar("SELECT rolcanlogin FROM pg_roles WHERE rolname = '" + rwUser + "'")
	require.NoError(t, err)
	require.Equal(t, "t", rwLogin, "the read-write user must be a login role")

	roLogin, err := env.PG.Scalar("SELECT rolcanlogin FROM pg_roles WHERE rolname = '" + roUser + "'")
	require.NoError(t, err)
	require.Equal(t, "t", roLogin, "the read-only user must be a login role")

	// --- POST /api/roles: a standalone login role (secret returned) ---
	const loginRole = "e2e_role_u1"
	var roleRes credentialEnvelope
	require.NoError(t, env.Panel.PostLong("/api/roles", map[string]any{
		"username":          loginRole,
		"can_login":         true,
		"generate_password": true,
	}, &roleRes), "POST /api/roles (login) should succeed")
	require.Contains(t, roleRes.Secrets, loginRole, "a login role must return a generated secret")
	require.NotEmpty(t, roleRes.Secrets[loginRole])

	canLogin, err := env.PG.Scalar("SELECT rolcanlogin FROM pg_roles WHERE rolname = '" + loginRole + "'")
	require.NoError(t, err)
	require.Equal(t, "t", canLogin, "the created role must be able to log in")

	isSuper, err := env.PG.Scalar("SELECT rolsuper FROM pg_roles WHERE rolname = '" + loginRole + "'")
	require.NoError(t, err)
	require.Equal(t, "f", isSuper, "a created login role must not be a superuser")

	// --- POST /api/roles: a NOLOGIN group role (no secret) ---
	const groupRole = "e2e_group_g1"
	var groupRes credentialEnvelope
	require.NoError(t, env.Panel.PostLong("/api/roles", map[string]any{
		"username":  groupRole,
		"can_login": false,
	}, &groupRes), "POST /api/roles (group) should succeed")
	require.Empty(t, groupRes.Secrets, "a NOLOGIN group role produces no secret")

	groupLogin, err := env.PG.Scalar("SELECT rolcanlogin FROM pg_roles WHERE rolname = '" + groupRole + "'")
	require.NoError(t, err)
	require.Equal(t, "f", groupLogin, "the group role must be NOLOGIN")
}

// TestReadOnlyEnforcement is the safety-invariant half of scenario 16: it proves
// a panel-created read-only user is enforced at the DATABASE level, not merely in
// the UI. It creates a read-only user via the panel, then opens a REAL
// authenticated connection AS that role and shows the database itself rejects
// writes (INSERT and CREATE) — while reads succeed. It also shows the panel's
// read-only query box refuses a write statement before it ever reaches Postgres.
func TestReadOnlyEnforcement(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Create the target database via the panel, then mint the read-only user on it
	// BEFORE creating any table. The panel grants the read-only role via its
	// non-superuser admin role, which can only GRANT on objects it owns — so the
	// read-only user must be created while the schema is still empty. The probe
	// table is created afterwards (as the postgres superuser) purely as a write
	// target; the read-only role having no SELECT on a postgres-owned table is
	// irrelevant here — its INSERT is denied by the table-level privilege check,
	// which is exactly what we assert.
	const db = "e2e_roenf_db"
	var dbRes resultEnvelope
	require.NoError(t,
		env.Panel.PostLong("/api/databases", map[string]string{"name": db}, &dbRes),
		"POST /api/databases should succeed")
	require.True(t, dbRes.OK)

	// Mint the read-only user via the panel; its password is returned exactly once.
	const roUser = "e2e_ro_user"
	var roRes credentialEnvelope
	require.NoError(t, env.Panel.PostLong("/api/roles/readonly", map[string]string{
		"username": roUser,
		"database": db,
		"schema":   "public",
	}, &roRes), "POST /api/roles/readonly should succeed")
	roPass := roRes.Secrets[roUser]
	require.NotEmpty(t, roPass, "the read-only user's password must be returned once")

	// Now seed a table as the postgres superuser (ground truth) — a write target
	// for the read-only role to be rejected against.
	require.NoError(t, env.PG.ExecDB(db,
		"CREATE TABLE public.widgets(id int PRIMARY KEY, name text)"))
	require.NoError(t, env.PG.ExecDB(db,
		"INSERT INTO public.widgets SELECT g, 'w-'||g FROM generate_series(1,3) g"))

	// Ground truth: it is a non-superuser login role.
	roSuper, err := env.PG.Scalar("SELECT rolsuper FROM pg_roles WHERE rolname = '" + roUser + "'")
	require.NoError(t, err)
	require.Equal(t, "f", roSuper, "a read-only user must never be a superuser")

	// (1) The connection is REAL and authenticated: a read succeeds as the role.
	one, err := env.PG.PsqlAsRole(roUser, roPass, db, "SELECT 1")
	require.NoError(t, err, "the read-only role must be able to connect and read")
	require.Equal(t, "1", one)

	// (2) A write is REJECTED BY THE DATABASE — not the panel. The role holds no
	// INSERT grant, so Postgres' own privilege check denies it.
	_, err = env.PG.PsqlAsRole(roUser, roPass, db,
		"INSERT INTO public.widgets(id, name) VALUES (99, 'intruder')")
	require.Error(t, err, "INSERT as the read-only role must be rejected at the DB level")
	require.Contains(t, strings.ToLower(err.Error()), "permission denied",
		"the rejection must be a database privilege denial")

	// (3) DDL is likewise rejected at the DB level: the role has no CREATE on the
	// schema.
	_, err = env.PG.PsqlAsRole(roUser, roPass, db, "CREATE TABLE public.evil(id int)")
	require.Error(t, err, "CREATE as the read-only role must be rejected at the DB level")
	require.Contains(t, strings.ToLower(err.Error()), "permission denied",
		"the DDL rejection must be a database privilege denial")

	// Ground truth: the table is untouched — no intruder row landed.
	rows, err := env.PG.CountRowsDB(db, "public.widgets")
	require.NoError(t, err)
	require.Equal(t, 3, rows, "no write by the read-only role may have succeeded")

	// (4) The panel's read-only query box blocks a write statement BEFORE it ever
	// reaches Postgres: a safety guard rejection (409 / code "safety").
	err = env.Panel.POST("/api/query",
		map[string]string{"sql": "INSERT INTO public.widgets(id, name) VALUES (1, 'x')"}, nil)
	require.Error(t, err, "the read-only query box must reject a write statement")
	var pe *harness.PanelError
	require.True(t, errors.As(err, &pe), "expected a typed panel error, got %T: %v", err, err)
	require.Equal(t, "safety", pe.Code, "a write in the query box must be a safety rejection")

	// And a read through the query box still works.
	var q struct {
		Classification string `json:"classification"`
	}
	require.NoError(t, env.Panel.POST("/api/query", map[string]string{"sql": "SELECT 1"}, &q),
		"the read-only query box must allow a read")
	require.Equal(t, "read", q.Classification)
}
