package pg

import (
	"fmt"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// provisionSQL builds the idempotent SQL run during Provision to create the two
// dedicated panel roles and enable pg_stat_statements.
//
// The read-only role is created NOLOGIN-safe and then hardened: it is granted
// only the ability to connect; the read-only enforcement at the DB level is the
// core safety idea, so the role is given no write defaults. The admin role is a
// privileged login used solely for guided actions.
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
		fmt.Sprintf(
			`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  ELSE
    ALTER ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
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
		// Default the read-only role to a read-only transaction state so even an
		// accidental write attempt is rejected at the server, independent of any
		// table grants. This is the DB-level read-only enforcement.
		fmt.Sprintf(`ALTER ROLE %s SET default_transaction_read_only = on;`, roQ),
		// Slow-query visibility.
		`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`,
	}
	return stmts, nil
}
