package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg/admin"
)

// This file is the privileged execution layer: it runs the pure SQL builders in
// internal/pg/admin against the cluster over the admin (privileged) pool. It is
// the only place those builders are executed.
//
// Three rules govern execution:
//   - Role-level DDL (CREATE/DROP/ALTER ROLE) and CREATE/DROP DATABASE are
//     cluster-global and run on the privileged pool bound to the maintenance
//     database. CREATE/DROP DATABASE cannot run inside a transaction.
//   - Schema/object grants must run INSIDE the target database, so they go
//     through a transient privileged connection to that database (acquirePriv).
//   - Every statement recorded in the returned Result is redacted of any
//     PASSWORD literal before it is surfaced or audited.

// reservedRoles are the panel's own login roles, which must never be dropped via
// the admin surface (doing so would lock the panel out of Postgres).
var reservedRoles = map[string]bool{
	AdminRole:    true,
	ReadOnlyRole: true,
	"postgres":   true,
}

// reservedDatabases are databases the admin surface refuses to drop.
var reservedDatabases = map[string]bool{
	"postgres":  true,
	"template0": true,
	"template1": true,
}

// acquirePriv returns a privileged (AdminRole) queryable scoped to database.
// When database is the maintenance database the shared privileged pool is
// returned; otherwise a transient privileged pool to that database is opened and
// closed by the returned release func. Unlike acquireRead, the connection is
// writable — it is used to run guided DDL inside the target database.
func (m *Manager) acquirePriv(ctx context.Context, database string) (*pgxpool.Pool, func(), error) {
	if database == defaultConnectDatabase {
		pool := m.PrivPool()
		if pool == nil {
			return nil, nil, notConnected()
		}
		return pool, func() {}, nil
	}
	pool, err := m.openPool(ctx, connConfig{
		SocketDir: m.socketDir(),
		Database:  database,
		User:      AdminRole,
		// no statement timeout: guided DDL may legitimately run long.
	}, privPoolMaxConns)
	if err != nil {
		return nil, nil, core.InternalError("pg: connecting to %s as admin", database).Wrap(err)
	}
	return pool, pool.Close, nil
}

// notConnected is the standard error when the privileged pool is unavailable.
func notConnected() error {
	return core.InternalError("pg: not connected").
		WithHint("Postgres is not reachable from the panel")
}

// runStmtsTx runs stmts in a single transaction on pool and returns the redacted
// statements for the audit/dry-run record. Any failure rolls back.
func (m *Manager) runStmtsTx(ctx context.Context, pool *pgxpool.Pool, stmts []string) ([]string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, core.ExecError("pg: begin transaction").Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recorded := make([]string, 0, len(stmts))
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return nil, core.ExecError("admin statement failed: %s", firstLine(redactPasswordLiteral(err.Error())))
		}
		recorded = append(recorded, redactPasswordLiteral(stmt))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, core.ExecError("pg: commit transaction").Wrap(err)
	}
	return recorded, nil
}

// runStmtPriv runs a single statement (no transaction) on the privileged
// maintenance pool. Used for CREATE/DROP DATABASE, which Postgres forbids inside
// a transaction block.
func (m *Manager) runStmtPriv(ctx context.Context, stmt string) (string, error) {
	pool := m.PrivPool()
	if pool == nil {
		return "", notConnected()
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return "", core.ExecError("admin statement failed: %s", firstLine(redactPasswordLiteral(err.Error())))
	}
	return redactPasswordLiteral(stmt), nil
}

// AdminCreateRole creates a role (login or group). password is required when
// canLogin is true and is emitted only as a quoted literal, never logged.
func (m *Manager) AdminCreateRole(ctx context.Context, name, password string, canLogin bool) (core.Result, error) {
	stmt, err := admin.CreateRole(name, password, canLogin)
	if err != nil {
		return core.Result{}, err
	}
	pool := m.PrivPool()
	if pool == nil {
		return core.Result{}, notConnected()
	}
	recorded, err := m.runStmtsTx(ctx, pool, []string{stmt})
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("role %q created", name)).WithStatements(recorded...), nil
}

// AdminCreateReadonlyUser creates a read-only login user on a database/schema,
// including default privileges for future objects. All statements (including the
// CREATE ROLE) run inside the target database in one transaction.
func (m *Manager) AdminCreateReadonlyUser(ctx context.Context, username, password, database, schema string) (core.Result, error) {
	stmts, err := admin.CreateReadonlyUser(username, password, database, schema)
	if err != nil {
		return core.Result{}, err
	}
	pool, release, err := m.acquirePriv(ctx, database)
	if err != nil {
		return core.Result{}, err
	}
	defer release()
	recorded, err := m.runStmtsTx(ctx, pool, stmts)
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("read-only user %q created on %q", username, database)).WithStatements(recorded...), nil
}

// AdminCreateDatabase creates a database (optionally owned by owner). CREATE
// DATABASE is non-transactional, so it runs as a single statement.
func (m *Manager) AdminCreateDatabase(ctx context.Context, name, owner string) (core.Result, error) {
	stmt, err := admin.CreateDatabase(name, owner)
	if err != nil {
		return core.Result{}, err
	}
	recorded, err := m.runStmtPriv(ctx, stmt)
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("database %q created", name)).WithStatements(recorded), nil
}

// AdminNewApp materializes a full "new app" bundle: an owner group role, a
// database owned by it, and read-write + read-only login users with their
// grants. It sequences the work across the maintenance pool (roles + CREATE
// DATABASE) and the new database (grants), because CREATE DATABASE cannot run in
// a transaction and the grants must run inside the new database.
//
// Because the panel's admin role is NOSUPERUSER, the flow also manages role
// memberships Postgres requires of a non-superuser: the admin role is made a
// member of the owner role (so it may create — and later drop — the database it
// owns) and is transiently made a member of the read-write role (so it may set
// that role's FOR ROLE default privileges, which is what keeps the read-only
// user able to read objects the app creates after provisioning).
func (m *Manager) AdminNewApp(ctx context.Context, plan admin.NewAppPlan) (core.Result, error) {
	// Validate the whole plan (identifiers + distinct role names) up front.
	if _, err := admin.BuildNewApp(plan); err != nil {
		return core.Result{}, err
	}

	// Refuse collisions with panel-managed objects.
	for _, role := range []string{plan.OwnerRole, plan.ReadwriteUser, plan.ReadonlyUser} {
		if reservedRoles[role] {
			return core.Result{}, core.ValidationError("derived name %q is reserved by indiepg; choose a different database name", role)
		}
	}
	if reservedDatabases[plan.Database] {
		return core.Result{}, core.ValidationError("database %q is reserved by indiepg", plan.Database)
	}

	// This flow spans three phases (owner role, CREATE DATABASE, then login
	// roles + grants) and CREATE DATABASE cannot run in a transaction, so it is
	// not atomic. Refuse up front if any target object already exists, so a retry
	// after a partial failure reports exactly what to remove instead of failing
	// mid-sequence and leaving orphans.
	if conflicts, err := m.newAppConflicts(ctx, plan); err != nil {
		return core.Result{}, err
	} else if len(conflicts) > 0 {
		return core.Result{}, core.ValidationError(
			"cannot provision app %q: already present: %s", plan.Database, strings.Join(conflicts, ", ")).
			WithHint("drop the listed object(s) or choose a different database name, then retry")
	}

	recorded := make([]string, 0, 16)

	// 1. Owner group role (cluster-global, on the maintenance pool), plus
	// membership of it for the panel's admin role. The admin role is NOSUPERUSER,
	// so Postgres will refuse the CREATE DATABASE ... OWNER in step 2 (and a later
	// DROP DATABASE) unless the admin role is a member of the owning role. Grant
	// the membership in the SAME transaction as the role creation so the two
	// commit or roll back together — a failure never leaves an orphan owner role.
	ownerStmt, err := admin.CreateRole(plan.OwnerRole, "", false)
	if err != nil {
		return core.Result{}, err
	}
	ownerToAdmin, err := admin.GrantRoleMembership(plan.OwnerRole, AdminRole)
	if err != nil {
		return core.Result{}, err
	}
	priv := m.PrivPool()
	if priv == nil {
		return core.Result{}, notConnected()
	}
	rec, err := m.runStmtsTx(ctx, priv, []string{ownerStmt, ownerToAdmin})
	if err != nil {
		return core.Result{}, err
	}
	recorded = append(recorded, rec...)

	// 2. Database owned by the owner role (non-transactional).
	dbStmt, err := admin.CreateDatabase(plan.Database, plan.OwnerRole)
	if err != nil {
		return core.Result{}, err
	}
	dbRec, err := m.runStmtPriv(ctx, dbStmt)
	if err != nil {
		return core.Result{}, err
	}
	recorded = append(recorded, dbRec)

	// 3. Read-write and read-only login users + grants, inside the new database.
	target, release, err := m.acquirePriv(ctx, plan.Database)
	if err != nil {
		return core.Result{}, err
	}
	defer release()

	const schema = "public"
	stmts := make([]string, 0, 12)
	rwCreate, err := admin.CreateRole(plan.ReadwriteUser, plan.ReadwritePass, true)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, rwCreate)
	rwGrants, err := admin.Grant(admin.AccessReadwrite, plan.ReadwriteUser, plan.Database, schema)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, rwGrants...)
	roCreate, err := admin.CreateRole(plan.ReadonlyUser, plan.ReadonlyPass, true)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, roCreate)
	roGrants, err := admin.Grant(admin.AccessReadonly, plan.ReadonlyUser, plan.Database, schema)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, roGrants...)

	// Make future tables/sequences the read-write user creates automatically
	// readable by the read-only user. Default privileges attach to the *creating*
	// role, so this must be set FOR the read-write role — otherwise the read-only
	// user could see only objects that exist now, not ones the app adds later
	// (the read-only DSN would silently break). Setting another role's default
	// privileges requires membership in it, so the admin role is briefly made a
	// member of the read-write role and relinquishes it once the defaults are in
	// place; the installed defaults persist after the revoke.
	rwToAdmin, err := admin.GrantRoleMembership(plan.ReadwriteUser, AdminRole)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, rwToAdmin)
	roDefaults, err := admin.DefaultReadFor(plan.ReadwriteUser, schema, plan.ReadonlyUser)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, roDefaults...)
	rwFromAdmin, err := admin.RevokeRoleMembership(plan.ReadwriteUser, AdminRole)
	if err != nil {
		return core.Result{}, err
	}
	stmts = append(stmts, rwFromAdmin)

	rec3, err := m.runStmtsTx(ctx, target, stmts)
	if err != nil {
		return core.Result{}, err
	}
	recorded = append(recorded, rec3...)

	return core.Ok(fmt.Sprintf("app %q provisioned", plan.Database)).WithStatements(recorded...), nil
}

// AdminGrant applies an access grant. Schema/object grants run inside the target
// database in one transaction.
func (m *Manager) AdminGrant(ctx context.Context, level admin.AccessLevel, role, database, schema string) (core.Result, error) {
	if reservedRoles[role] {
		return core.Result{}, core.ValidationError("role %q is managed by indiepg and cannot be modified", role)
	}
	stmts, err := admin.Grant(level, role, database, schema)
	if err != nil {
		return core.Result{}, err
	}
	pool, release, err := m.acquirePriv(ctx, database)
	if err != nil {
		return core.Result{}, err
	}
	defer release()
	recorded, err := m.runStmtsTx(ctx, pool, stmts)
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("granted %s on %q to %q", level, database, role)).WithStatements(recorded...), nil
}

// AdminRevoke removes an access grant, mirroring AdminGrant.
func (m *Manager) AdminRevoke(ctx context.Context, level admin.AccessLevel, role, database, schema string) (core.Result, error) {
	if reservedRoles[role] {
		return core.Result{}, core.ValidationError("role %q is managed by indiepg and cannot be modified", role)
	}
	stmts, err := admin.Revoke(level, role, database, schema)
	if err != nil {
		return core.Result{}, err
	}
	pool, release, err := m.acquirePriv(ctx, database)
	if err != nil {
		return core.Result{}, err
	}
	defer release()
	recorded, err := m.runStmtsTx(ctx, pool, stmts)
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("revoked %s on %q from %q", level, database, role)).WithStatements(recorded...), nil
}

// AdminRotatePassword sets a new password on a role. The new password is emitted
// only as a quoted literal and never logged.
func (m *Manager) AdminRotatePassword(ctx context.Context, role, password string) (core.Result, error) {
	if reservedRoles[role] {
		return core.Result{}, core.ValidationError("role %q is managed by indiepg and cannot be modified", role)
	}
	stmt, err := admin.RotatePassword(role, password)
	if err != nil {
		return core.Result{}, err
	}
	pool := m.PrivPool()
	if pool == nil {
		return core.Result{}, notConnected()
	}
	recorded, err := m.runStmtsTx(ctx, pool, []string{stmt})
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("password rotated for %q", role)).WithStatements(recorded...), nil
}

// AdminDropRole drops a role after a typed-name confirmation. The panel's own
// login roles are refused.
func (m *Manager) AdminDropRole(ctx context.Context, role, confirm string) (core.Result, error) {
	if reservedRoles[role] {
		return core.Result{}, core.ValidationError("role %q is managed by indiepg and cannot be dropped", role)
	}
	stmt, err := admin.DropRole(role, confirm)
	if err != nil {
		return core.Result{}, err
	}
	pool := m.PrivPool()
	if pool == nil {
		return core.Result{}, notConnected()
	}
	recorded, err := m.runStmtsTx(ctx, pool, []string{stmt})
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("role %q dropped", role)).WithStatements(recorded...), nil
}

// terminateBackendsSQL disconnects all sessions on a database (other than this
// one) so a subsequent DROP DATABASE is not blocked by live connections. The
// database name is bound as a parameter, never interpolated.
const terminateBackendsSQL = `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = $1 AND pid <> pg_backend_pid()`

// AdminDropDatabase drops a database after a typed-name confirmation. Reserved
// databases are refused. Live sessions on the target are terminated first so the
// drop is not blocked, then DROP DATABASE runs (non-transactional).
func (m *Manager) AdminDropDatabase(ctx context.Context, database, confirm string) (core.Result, error) {
	if reservedDatabases[database] {
		return core.Result{}, core.ValidationError("database %q is managed by indiepg and cannot be dropped", database)
	}
	stmt, err := admin.DropDatabase(database, confirm)
	if err != nil {
		return core.Result{}, err
	}
	pool := m.PrivPool()
	if pool == nil {
		return core.Result{}, notConnected()
	}
	// Best-effort: disconnect live sessions so the drop is not blocked.
	if _, err := pool.Exec(ctx, terminateBackendsSQL, database); err != nil {
		return core.Result{}, core.ExecError("pg: terminating connections to %s", database).Wrap(err)
	}
	recorded, err := m.runStmtPriv(ctx, stmt)
	if err != nil {
		return core.Result{}, err
	}
	return core.Ok(fmt.Sprintf("database %q dropped", database)).WithStatements(recorded), nil
}

// newAppConflicts returns the human-readable names of any roles or the database
// in plan that already exist, so AdminNewApp can refuse before partially
// provisioning. An empty result means the bundle is safe to create.
func (m *Manager) newAppConflicts(ctx context.Context, plan admin.NewAppPlan) ([]string, error) {
	pool := m.PrivPool()
	if pool == nil {
		return nil, notConnected()
	}

	var conflicts []string

	roleNames := []string{plan.OwnerRole, plan.ReadwriteUser, plan.ReadonlyUser}
	rows, err := pool.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname = ANY($1)`, roleNames)
	if err != nil {
		return nil, core.InternalError("pg: checking existing roles").Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, core.InternalError("pg: scanning role row").Wrap(err)
		}
		conflicts = append(conflicts, "role "+name)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading roles").Wrap(err)
	}

	var dbExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, plan.Database).Scan(&dbExists); err != nil {
		return nil, core.InternalError("pg: checking existing database").Wrap(err)
	}
	if dbExists {
		conflicts = append(conflicts, "database "+plan.Database)
	}

	return conflicts, nil
}
