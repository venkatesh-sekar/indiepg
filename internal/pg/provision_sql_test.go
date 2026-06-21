package pg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
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

	// read-only enforcement at the DB level.
	require.Contains(t, joined, "default_transaction_read_only = on")

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
