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
	// CREATE on the public schema is revoked from the role itself (not PUBLIC) so
	// it holds no latent write capability even if the GUC is flipped off.
	require.Contains(t, joined,
		"REVOKE CREATE ON SCHEMA public FROM "+core.QuoteIdent(ReadOnlyRole))
	// and the read-only role is never a member of the writing admin role.
	require.Contains(t, joined,
		"REVOKE "+core.QuoteIdent(AdminRole)+" FROM "+core.QuoteIdent(ReadOnlyRole))

	// hardening is scoped to the role itself; never broaden to PUBLIC (that would
	// break the operator's own applications).
	require.NotContains(t, joined, "FROM PUBLIC")

	// admin role is explicitly not a superuser.
	require.Contains(t, joined, "NOSUPERUSER")

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
