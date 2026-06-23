package pg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestProvisionSQL(t *testing.T) {
	stmts, err := provisionSQL()
	require.NoError(t, err)
	require.NotEmpty(t, stmts)

	joined := strings.Join(stmts, "\n")

	// roles are referenced as quoted identifiers, never bare.
	require.Contains(t, joined, core.QuoteIdent(ReadOnlyRole))
	require.Contains(t, joined, core.QuoteIdent(AdminRole))

	// role existence checks use quoted literals.
	require.Contains(t, joined, core.QuoteLiteral(ReadOnlyRole))
	require.Contains(t, joined, core.QuoteLiteral(AdminRole))

	// defense-in-depth GUC remains, but it is no longer the primary control.
	require.Contains(t, joined, "default_transaction_read_only = on")

	// authoritative boundary: the read-only role is hardened by privilege denial.
	// On PG <= 14 CREATE on public is inherited via the PUBLIC pseudo-role, so the
	// only way to deny it is to revoke from PUBLIC and re-grant to the admin role.
	require.Contains(t, joined, "REVOKE CREATE ON SCHEMA public FROM PUBLIC")
	require.Contains(t, joined,
		"GRANT CREATE ON SCHEMA public TO "+core.QuoteIdent(AdminRole))
	// belt-and-suspenders: any direct CREATE grant to the read-only role is revoked.
	require.Contains(t, joined,
		"REVOKE CREATE ON SCHEMA public FROM "+core.QuoteIdent(ReadOnlyRole))
	// and the read-only role is never a member of the writing admin role.
	require.Contains(t, joined,
		"REVOKE "+core.QuoteIdent(AdminRole)+" FROM "+core.QuoteIdent(ReadOnlyRole))

	// The REVOKE ... FROM PUBLIC is scoped (at the call site) to the panel-managed
	// `postgres` database only; it must always be paired with a re-GRANT to admin
	// so guided object creation still works. USAGE is never revoked, so the
	// read-only SELECT path is preserved.
	require.NotContains(t, joined, "REVOKE USAGE")

	// admin role is explicitly not a superuser.
	require.Contains(t, joined, "NOSUPERUSER")

	// NOINHERIT is load-bearing for the privilege-denial boundary and must be set
	// on BOTH the create and the re-provision (ALTER) path for the read-only role,
	// or a re-run would silently leave it INHERIT.
	require.GreaterOrEqual(t, strings.Count(joined, "NOINHERIT"), 2,
		"read-only role must be NOINHERIT on both the CREATE and ALTER paths")

	// idempotent extension + idempotent role creation.
	require.Contains(t, joined, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
	require.Contains(t, joined, "IF NOT EXISTS (SELECT 1 FROM pg_roles")

	// the read-only role must never be granted write/superuser powers here.
	require.NotContains(t, joined, ReadOnlyRole+" SUPERUSER")
}

func TestProvisionSQL_RolesAreValidIdentifiers(t *testing.T) {
	require.True(t, core.IsValidIdentifier(ReadOnlyRole))
	require.True(t, core.IsValidIdentifier(AdminRole))
}
