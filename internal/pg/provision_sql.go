package pg

import (
	"fmt"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// provisionSQL builds the idempotent SQL run during Provision to create the two
// dedicated panel roles and enable pg_stat_statements.
//
// Read-only safety model (the core invariant from the design doc, §4.3/§7):
// the DB-level read-only boundary is PRIVILEGE DENIAL, not a GUC. The read-only
// role is granted ONLY SELECT/USAGE/CONNECT (those grants live in the admin pkg
// when databases are browsed); it is never given any write capability and is
// never made a member of a writing role. Because it lacks the privilege, even a
// bug in the UI guard cannot turn a SELECT into an UPDATE/DELETE — the server
// rejects the write for want of permission, regardless of any session GUC the
// role could flip. The default_transaction_read_only GUC set below is therefore
// DEFENSE-IN-DEPTH only (the role can reset it in its own session), never the
// primary control.
//
// Hardening runs ONLY against the panel-managed `postgres` database (the DB
// Provision dials). On PostgreSQL <= 14 the public schema grants CREATE to the
// PUBLIC pseudo-role by default, and that grant is inherited by every role —
// a per-role REVOKE cannot strip it. So we REVOKE CREATE ON SCHEMA public FROM
// PUBLIC and re-GRANT it to the admin role, leaving the read-only role with no
// way to create (and thus own/write) scratch objects even if it flips its GUC
// off. This is safe precisely because it is scoped to `postgres`: that public
// schema is panel-managed, not an operator application schema. We never run it
// against the operator's user databases (where it would break their apps), so a
// read-only browse of an app DB can in principle still create scratch objects in
// that app's public schema — an accepted, app-DB-only limitation that never
// touches operator *data* (writes to existing tables remain privilege-denied).
//
// The admin role is a privileged login used solely for guided actions.
//
// Every identifier is validated then quoted; nothing is interpolated raw.
func provisionSQL() ([]string, error) {
	if err := core.ValidateIdentifier(ReadOnlyRole, "role"); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(AdminRole, "role"); err != nil {
		return nil, err
	}

	roQ := core.QuoteIdent(ReadOnlyRole)
	adQ := core.QuoteIdent(AdminRole)

	stmts := []string{
		// Read-only login role. Created idempotently via a DO block so re-running
		// Provision is safe. It logs in but is intentionally given no write
		// privileges; per-database SELECT grants are applied when databases are
		// browsed. Marking it explicitly NOSUPERUSER/NOCREATEDB/NOCREATEROLE makes
		// the read-only intent unmistakable even if the role pre-existed.
		// NOINHERIT (set below) plus never granting it membership in a writing role
		// is what makes privilege denial — not the session GUC — the real boundary.
		fmt.Sprintf(
			`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  ELSE
    ALTER ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
END $$;`,
			core.QuoteLiteral(ReadOnlyRole), roQ, roQ,
		),
		// Privileged admin login role for guided actions. NOSUPERUSER on purpose:
		// the panel never connects as a Postgres superuser; it is granted the
		// specific creation privileges it needs.
		fmt.Sprintf(
			`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN NOSUPERUSER CREATEDB CREATEROLE;
  ELSE
    ALTER ROLE %s LOGIN NOSUPERUSER CREATEDB CREATEROLE;
  END IF;
END $$;`,
			core.QuoteLiteral(AdminRole), adQ, adQ,
		),
		// DEFENSE-IN-DEPTH (secondary): default the read-only role's sessions to a
		// read-only transaction state. This is a self-resettable USERSET GUC, so it
		// is NOT the authoritative control — the role could flip it off in its own
		// session. The authoritative DB-level boundary is privilege denial (the role
		// is granted only SELECT/USAGE/CONNECT and holds no write privilege to
		// exploit); this GUC merely turns an accidental write into an early, clear
		// error before it even reaches a permission check.
		fmt.Sprintf(`ALTER ROLE %s SET default_transaction_read_only = on;`, roQ),
		// HARDENING (authoritative boundary): strip the latent CREATE capability the
		// read-only role inherits via the PUBLIC pseudo-role, so that even if the GUC
		// above is flipped off the role still cannot create (and thus own/write)
		// objects. On PostgreSQL <= 14 the public schema grants CREATE to PUBLIC by
		// default; that is inherited by every role, so a per-role REVOKE is a no-op
		// against it. We must revoke from PUBLIC — and then re-GRANT to the admin
		// role so guided actions can still create objects. USAGE is untouched, so the
		// read-only role keeps the SELECT path. Both statements are idempotent and run
		// only against the panel-managed `postgres` database (see the doc comment).
		`REVOKE CREATE ON SCHEMA public FROM PUBLIC;`,
		fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s;`, adQ),
		// Belt-and-suspenders: also revoke any CREATE granted directly to the
		// read-only role. A no-op given the role is never granted CREATE, but it
		// documents the invariant and is idempotent.
		fmt.Sprintf(`REVOKE CREATE ON SCHEMA public FROM %s;`, roQ),
		// Ensure the read-only role is never a member of a writing role. Membership
		// in a privileged role would let it inherit (or SET ROLE to) write
		// capability and defeat the privilege-denial boundary. Revoking admin-role
		// membership is idempotent (a no-op when no membership exists) and documents
		// the invariant in the provisioning SQL itself.
		fmt.Sprintf(`REVOKE %s FROM %s;`, adQ, roQ),
		// Slow-query visibility.
		`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`,
	}
	return stmts, nil
}
