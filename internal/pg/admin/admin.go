// Package admin holds pure SQL builders for the panel's guided role and
// database administration features. Every exported function returns safe,
// fully quoted SQL statements built only via core.QuoteIdent /
// core.QuoteQualified / core.QuoteLiteral after validating operator-supplied
// identifiers with core.ValidateIdentifier.
//
// Nothing in this package performs IO. Builders are deterministic and return
// typed *core.Error values (or *core.SafetyError for destructive operations)
// instead of panicking. Callers are expected to run the returned statements,
// ideally inside a single transaction so partial application can be rolled
// back.
package admin

import (
	"fmt"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// AccessLevel is the privilege tier for a grant or revoke. The tiers are
// cumulative in capability: readwrite implies everything readonly has plus
// write privileges; owner re-points database ownership.
type AccessLevel string

const (
	// AccessReadonly grants CONNECT on the database, USAGE on the schema,
	// SELECT on all existing tables/sequences, and installs ALTER DEFAULT
	// PRIVILEGES so future tables and sequences are covered automatically.
	AccessReadonly AccessLevel = "readonly"
	// AccessReadwrite grants everything readonly has plus INSERT/UPDATE/DELETE
	// on tables and USAGE/SELECT/UPDATE on sequences, with matching default
	// privileges for future objects.
	AccessReadwrite AccessLevel = "readwrite"
	// AccessOwner re-points database ownership (ALTER DATABASE ... OWNER TO)
	// and makes the role the owning role of the schema.
	AccessOwner AccessLevel = "owner"
)

// ParseAccessLevel parses an access level string, returning a typed
// *core.Error with CodeValidation for unknown values.
func ParseAccessLevel(s string) (AccessLevel, error) {
	switch AccessLevel(s) {
	case AccessReadonly:
		return AccessReadonly, nil
	case AccessReadwrite:
		return AccessReadwrite, nil
	case AccessOwner:
		return AccessOwner, nil
	default:
		return "", core.ValidationError("unknown access level %q", s).
			WithHint("expected one of: readonly, readwrite, owner")
	}
}

// validateSchemaName validates a schema identifier. The conventional default
// schema "public" is a reserved word for core.ValidateIdentifier, but it is a
// legitimate (indeed the default) schema name, so it is accepted explicitly.
func validateSchemaName(schema string) error {
	if schema == "public" {
		return nil
	}
	return core.ValidateIdentifier(schema, "schema")
}

// validatePassword performs minimal validation on a role password. Postgres
// itself imposes no character restrictions (the value is sent as a quoted
// literal), so the only failure mode we guard is the empty string, which would
// create a passwordless login role.
func validatePassword(password string) error {
	if password == "" {
		return core.ValidationError("password cannot be empty").
			WithHint("generate a strong password before creating the role")
	}
	return nil
}

// CreateRole builds a single CREATE ROLE statement. When canLogin is true the
// role is a LOGIN role with the supplied password (emitted as a quoted
// literal); otherwise it is a NOLOGIN group role and password is ignored.
func CreateRole(name, password string, canLogin bool) (string, error) {
	if err := core.ValidateIdentifier(name, "role"); err != nil {
		return "", err
	}
	if !canLogin {
		return fmt.Sprintf("CREATE ROLE %s NOLOGIN;", core.QuoteIdent(name)), nil
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD %s;",
		core.QuoteIdent(name),
		core.QuoteLiteral(password),
	), nil
}

// GrantRoleMembership builds a "GRANT group TO member" statement making member
// a member of group. The panel uses this so its non-superuser admin role can be
// made a member of an app's owner role — a prerequisite, on a non-superuser
// connection, for CREATE DATABASE ... OWNER on that role (and later DROP of that
// database) — and, transiently, a member of the read-write role so it can set
// that role's default privileges via ALTER DEFAULT PRIVILEGES FOR ROLE.
func GrantRoleMembership(group, member string) (string, error) {
	if err := core.ValidateIdentifier(group, "role"); err != nil {
		return "", err
	}
	if err := core.ValidateIdentifier(member, "role"); err != nil {
		return "", err
	}
	return fmt.Sprintf("GRANT %s TO %s;", core.QuoteIdent(group), core.QuoteIdent(member)), nil
}

// RevokeRoleMembership builds a "REVOKE group FROM member" statement, the
// inverse of GrantRoleMembership. Default privileges already installed via
// ALTER DEFAULT PRIVILEGES FOR ROLE persist after the grantor's membership is
// revoked, so this is used to relinquish a transient membership once defaults
// are set.
func RevokeRoleMembership(group, member string) (string, error) {
	if err := core.ValidateIdentifier(group, "role"); err != nil {
		return "", err
	}
	if err := core.ValidateIdentifier(member, "role"); err != nil {
		return "", err
	}
	return fmt.Sprintf("REVOKE %s FROM %s;", core.QuoteIdent(group), core.QuoteIdent(member)), nil
}

// DefaultReadFor builds ALTER DEFAULT PRIVILEGES statements so that future
// tables and sequences created *by* creator in schema are automatically
// readable by grantee. The FOR ROLE clause is essential: default privileges
// attach to the role that creates the object, so to make a read-only user see
// tables a read-write app creates later, the defaults must name the creating
// (read-write) role — not whoever runs this DDL. The executing role must be a
// member of creator.
func DefaultReadFor(creator, schema, grantee string) ([]string, error) {
	if err := core.ValidateIdentifier(creator, "role"); err != nil {
		return nil, err
	}
	if err := validateSchemaName(schema); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(grantee, "role"); err != nil {
		return nil, err
	}
	c := core.QuoteIdent(creator)
	sch := core.QuoteIdent(schema)
	g := core.QuoteIdent(grantee)
	return []string{
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON TABLES TO %s;", c, sch, g),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON SEQUENCES TO %s;", c, sch, g),
	}, nil
}

// CreateReadonlyUser builds the full ordered statement set to create a
// read-only login user "done correctly": the LOGIN role itself, CONNECT on the
// database, USAGE on the schema, SELECT on all current tables and sequences,
// and — critically — ALTER DEFAULT PRIVILEGES so any future tables and
// sequences created in the schema are automatically granted to the user.
//
// Statements are returned in dependency order; callers should run them in a
// single transaction.
func CreateReadonlyUser(username, password, database, schema string) ([]string, error) {
	if err := core.ValidateIdentifier(username, "role"); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}
	if err := validateSchemaName(schema); err != nil {
		return nil, err
	}

	role := core.QuoteIdent(username)
	stmts := []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;", role, core.QuoteLiteral(password)),
	}
	stmts = append(stmts, grantReadonly(username, database, schema)...)
	return stmts, nil
}

// CreateDatabase builds a CREATE DATABASE statement. owner is optional; when
// empty the database is created with the default owner (the connecting role).
func CreateDatabase(name, owner string) (string, error) {
	if err := core.ValidateIdentifier(name, "database"); err != nil {
		return "", err
	}
	if owner == "" {
		return fmt.Sprintf("CREATE DATABASE %s;", core.QuoteIdent(name)), nil
	}
	if err := core.ValidateIdentifier(owner, "role"); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"CREATE DATABASE %s OWNER %s;",
		core.QuoteIdent(name),
		core.QuoteIdent(owner),
	), nil
}

// grantReadonly returns the ordered statements granting read-only access on a
// database/schema to a (validated) role, including default privileges for
// future objects. Identifiers are assumed validated by the caller.
func grantReadonly(role, database, schema string) []string {
	r := core.QuoteIdent(role)
	sch := core.QuoteIdent(schema)
	return []string{
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;", core.QuoteIdent(database), r),
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s;", sch, r),
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO %s;", sch, r),
		fmt.Sprintf("GRANT SELECT ON ALL SEQUENCES IN SCHEMA %s TO %s;", sch, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO %s;", sch, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON SEQUENCES TO %s;", sch, r),
	}
}

// grantReadwrite returns the ordered statements granting read-write access:
// everything readonly has, plus DML on tables and USAGE/SELECT/UPDATE on
// sequences, with matching default privileges for future objects.
func grantReadwrite(role, database, schema string) []string {
	r := core.QuoteIdent(role)
	sch := core.QuoteIdent(schema)
	stmts := grantReadonly(role, database, schema)
	stmts = append(stmts,
		fmt.Sprintf("GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s;", sch, r),
		fmt.Sprintf("GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s TO %s;", sch, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT INSERT, UPDATE, DELETE ON TABLES TO %s;", sch, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s;", sch, r),
	)
	return stmts
}

// grantOwner returns the statements re-pointing database and schema ownership
// to a (validated) role.
func grantOwner(role, database, schema string) []string {
	r := core.QuoteIdent(role)
	return []string{
		fmt.Sprintf("ALTER DATABASE %s OWNER TO %s;", core.QuoteIdent(database), r),
		fmt.Sprintf("ALTER SCHEMA %s OWNER TO %s;", core.QuoteIdent(schema), r),
	}
}

// Grant builds the ordered statement set granting level access on
// database/schema to role.
func Grant(level AccessLevel, role, database, schema string) ([]string, error) {
	if err := validateGrantArgs(level, role, database, schema); err != nil {
		return nil, err
	}
	switch level {
	case AccessReadonly:
		return grantReadonly(role, database, schema), nil
	case AccessReadwrite:
		return grantReadwrite(role, database, schema), nil
	case AccessOwner:
		return grantOwner(role, database, schema), nil
	default:
		// Unreachable: validateGrantArgs rejects unknown levels.
		return nil, core.ValidationError("unknown access level %q", string(level))
	}
}

// Revoke builds the ordered statement set revoking level access. Revoke
// mirrors Grant: it drops the default privileges first so future objects are
// no longer covered, then revokes the existing object privileges, and finally
// the schema/database-level grants.
func Revoke(level AccessLevel, role, database, schema string) ([]string, error) {
	if err := validateGrantArgs(level, role, database, schema); err != nil {
		return nil, err
	}
	r := core.QuoteIdent(role)
	sch := core.QuoteIdent(schema)
	db := core.QuoteIdent(database)

	switch level {
	case AccessReadonly:
		return revokeReadonly(db, sch, r), nil
	case AccessReadwrite:
		// Drop the write privileges first, then fall through to readonly.
		stmts := []string{
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE INSERT, UPDATE, DELETE ON TABLES FROM %s;", sch, r),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE USAGE, SELECT, UPDATE ON SEQUENCES FROM %s;", sch, r),
			fmt.Sprintf("REVOKE INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s FROM %s;", sch, r),
			fmt.Sprintf("REVOKE USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s FROM %s;", sch, r),
		}
		stmts = append(stmts, revokeReadonly(db, sch, r)...)
		return stmts, nil
	case AccessOwner:
		// Revoking owner-level access re-points ownership back to the
		// connecting role; the operator chooses the new owner explicitly via a
		// subsequent Grant(owner, ...). Here we only relinquish via REASSIGN is
		// unsafe without a target, so we surface a clear validation error.
		return nil, core.ValidationError("owner access cannot be revoked directly").
			WithHint("re-point ownership by granting owner access to a different role")
	default:
		return nil, core.ValidationError("unknown access level %q", string(level))
	}
}

// revokeReadonly returns the statements revoking read-only privileges. db, sch
// and r are already-quoted identifiers.
func revokeReadonly(db, sch, r string) []string {
	return []string{
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE SELECT ON TABLES FROM %s;", sch, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE SELECT ON SEQUENCES FROM %s;", sch, r),
		fmt.Sprintf("REVOKE SELECT ON ALL TABLES IN SCHEMA %s FROM %s;", sch, r),
		fmt.Sprintf("REVOKE SELECT ON ALL SEQUENCES IN SCHEMA %s FROM %s;", sch, r),
		fmt.Sprintf("REVOKE USAGE ON SCHEMA %s FROM %s;", sch, r),
		fmt.Sprintf("REVOKE CONNECT ON DATABASE %s FROM %s;", db, r),
	}
}

// validateGrantArgs validates the level and identifiers shared by Grant and
// Revoke.
func validateGrantArgs(level AccessLevel, role, database, schema string) error {
	if _, err := ParseAccessLevel(string(level)); err != nil {
		return err
	}
	if err := core.ValidateIdentifier(role, "role"); err != nil {
		return err
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return err
	}
	if err := validateSchemaName(schema); err != nil {
		return err
	}
	return nil
}

// RotatePassword builds an ALTER ROLE ... PASSWORD statement with the new
// password emitted as a quoted literal.
func RotatePassword(role, newPassword string) (string, error) {
	if err := core.ValidateIdentifier(role, "role"); err != nil {
		return "", err
	}
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"ALTER ROLE %s PASSWORD %s;",
		core.QuoteIdent(role),
		core.QuoteLiteral(newPassword),
	), nil
}

// DropRole builds a DROP ROLE statement guarded by a typed-name confirmation.
// It returns a *core.SafetyError (CodeSafety) unless confirmTyped exactly
// equals role.
func DropRole(role, confirmTyped string) (string, error) {
	if err := core.ValidateIdentifier(role, "role"); err != nil {
		return "", err
	}
	if serr := core.RequireConfirmation("drop role", role, confirmTyped); serr != nil {
		return "", serr
	}
	return fmt.Sprintf("DROP ROLE %s;", core.QuoteIdent(role)), nil
}

// DropDatabase builds a DROP DATABASE statement guarded by a typed-name
// confirmation. It returns a *core.SafetyError (CodeSafety) unless confirmTyped
// exactly equals database.
func DropDatabase(database, confirmTyped string) (string, error) {
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return "", err
	}
	if serr := core.RequireConfirmation("drop database", database, confirmTyped); serr != nil {
		return "", serr
	}
	return fmt.Sprintf("DROP DATABASE %s;", core.QuoteIdent(database)), nil
}

// CreateExtension builds a CREATE EXTENSION IF NOT EXISTS statement for the
// target database. The name is validated as an identifier and quoted; the
// IF NOT EXISTS clause makes re-adding an already-installed extension a no-op.
func CreateExtension(name string) (string, error) {
	if err := core.ValidateExtensionName(name); err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s;", core.QuoteIdent(name)), nil
}

// AlterExtensionUpdate builds an ALTER EXTENSION ... UPDATE statement, which
// upgrades an installed extension to the default available version.
func AlterExtensionUpdate(name string) (string, error) {
	if err := core.ValidateExtensionName(name); err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER EXTENSION %s UPDATE;", core.QuoteIdent(name)), nil
}

// DropExtension builds a DROP EXTENSION statement guarded by a typed-name
// confirmation. It returns a *core.SafetyError (CodeSafety) unless confirmTyped
// exactly equals name. No CASCADE is emitted: a dependency error from Postgres
// is surfaced to the operator to resolve explicitly.
func DropExtension(name, confirmTyped string) (string, error) {
	if err := core.ValidateExtensionName(name); err != nil {
		return "", err
	}
	if serr := core.RequireConfirmation("drop extension", name, confirmTyped); serr != nil {
		return "", serr
	}
	return fmt.Sprintf("DROP EXTENSION %s;", core.QuoteIdent(name)), nil
}

// NewAppPlan describes the one-click "new app" bundle: a database owned by a
// dedicated owner role, a read-write login user, and a read-only login user.
type NewAppPlan struct {
	Database      string
	OwnerRole     string
	ReadwriteUser string
	ReadwritePass string
	ReadonlyUser  string
	ReadonlyPass  string
}

// BuildNewApp returns the ordered statements that materialize a NewAppPlan:
//  1. the owner (NOLOGIN) role,
//  2. the database owned by it,
//  3. the read-write login user with read-write grants,
//  4. the read-only login user with read-only grants (incl. default privileges),
//  5. default privileges so future objects the read-write user creates are
//     automatically readable by the read-only user.
//
// All objects target the conventional "public" schema. Statements should be
// run in order; the database creation cannot run inside the same transaction
// as the grants because CREATE DATABASE is not transactional in Postgres — the
// caller is responsible for that sequencing.
//
// This is the logical definition of the bundle. When a *non-superuser* role
// executes it (as the panel's admin role does), additional role-membership
// plumbing is required and is the executor's responsibility (see
// Manager.AdminNewApp): the executor must be a member of the owner role to
// create the database it owns, and transiently a member of the read-write role
// to install step 5's FOR ROLE default privileges.
func BuildNewApp(p NewAppPlan) ([]string, error) {
	if err := core.ValidateIdentifier(p.Database, "database"); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(p.OwnerRole, "role"); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(p.ReadwriteUser, "role"); err != nil {
		return nil, err
	}
	if err := validatePassword(p.ReadwritePass); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(p.ReadonlyUser, "role"); err != nil {
		return nil, err
	}
	if err := validatePassword(p.ReadonlyPass); err != nil {
		return nil, err
	}
	if err := ensureDistinct(p); err != nil {
		return nil, err
	}

	const schema = "public"
	stmts := make([]string, 0, 16)

	// 1. Owner group role.
	stmts = append(stmts,
		fmt.Sprintf("CREATE ROLE %s NOLOGIN;", core.QuoteIdent(p.OwnerRole)),
	)
	// 2. Database owned by the owner role.
	stmts = append(stmts,
		fmt.Sprintf("CREATE DATABASE %s OWNER %s;",
			core.QuoteIdent(p.Database), core.QuoteIdent(p.OwnerRole)),
	)
	// 3. Read-write login user.
	stmts = append(stmts,
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;",
			core.QuoteIdent(p.ReadwriteUser), core.QuoteLiteral(p.ReadwritePass)),
	)
	stmts = append(stmts, grantReadwrite(p.ReadwriteUser, p.Database, schema)...)
	// 4. Read-only login user.
	stmts = append(stmts,
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;",
			core.QuoteIdent(p.ReadonlyUser), core.QuoteLiteral(p.ReadonlyPass)),
	)
	stmts = append(stmts, grantReadonly(p.ReadonlyUser, p.Database, schema)...)
	// 5. Future objects created by the read-write user are readable by the
	// read-only user (default privileges attach to the creating role).
	roDefaults, err := DefaultReadFor(p.ReadwriteUser, schema, p.ReadonlyUser)
	if err != nil {
		return nil, err
	}
	stmts = append(stmts, roDefaults...)

	return stmts, nil
}

// ensureDistinct guards against a plan that names the same role twice, which
// would produce a duplicate CREATE ROLE and fail mid-sequence.
func ensureDistinct(p NewAppPlan) error {
	roles := map[string]string{
		p.OwnerRole:     "owner",
		p.ReadwriteUser: "readwrite user",
		p.ReadonlyUser:  "readonly user",
	}
	if len(roles) != 3 {
		return core.ValidationError("owner, readwrite user, and readonly user must be distinct roles").
			WithHint("use three different role names")
	}
	return nil
}
