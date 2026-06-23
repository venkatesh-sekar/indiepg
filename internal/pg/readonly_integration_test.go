//go:build integration

// Integration test proving the panel's read-only boundary is enforced by
// Postgres itself — not by the UI or the query guard. It exercises the real
// read-only pool (the exact path the query box uses) and asserts that a write
// is refused at the server, and — crucially — that the refusal holds via
// PRIVILEGE DENIAL even when the defense-in-depth GUC is reset.
//
// Gated behind the "integration" build tag; skips unless INDIEPG_TEST_SOCKET
// points at a cluster already provisioned with the indiepg_readonly and
// indiepg_admin login roles (i.e. provisionSQL() has been applied). Run via:
//
//	go test -tags integration ./internal/pg/ -run TestReadOnlyRole -v
package pg

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
)

func TestReadOnlyRole_DBLevelWriteDenial(t *testing.T) {
	sock := os.Getenv("INDIEPG_TEST_SOCKET")
	if sock == "" {
		t.Skip("set INDIEPG_TEST_SOCKET to a cluster provisioned with the indiepg roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := New(Options{Config: config.Config{
		PGSocketDir:      sock,
		StatementTimeout: 5 * time.Second,
	}})
	require.NoError(t, m.Connect(ctx))
	defer m.Close()

	// A uniquely named, schema-qualified probe table so the test is re-runnable
	// and robust against any role-level search_path. Admin (the privileged pool)
	// sets it up and grants the read-only role SELECT only — the role is never
	// granted any write privilege.
	const table = "public.indiepg_ro_probe"
	priv := m.PrivPool()
	require.NotNil(t, priv, "privileged pool must be connected")

	_, err := priv.Exec(ctx, "DROP TABLE IF EXISTS "+table)
	require.NoError(t, err)
	_, err = priv.Exec(ctx, "CREATE TABLE "+table+" (id int PRIMARY KEY, note text)")
	require.NoError(t, err)
	// Clean up with a fresh context so teardown still runs if ctx is cancelled.
	defer func() { _, _ = priv.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) }()
	_, err = priv.Exec(ctx, "INSERT INTO "+table+" (id, note) VALUES (1, 'seed')")
	require.NoError(t, err)
	_, err = priv.Exec(ctx, fmt.Sprintf("GRANT SELECT ON %s TO %s", table, ReadOnlyRole))
	require.NoError(t, err)

	// Sanity: the read-only role CAN read through the real query path.
	rows, err := m.ExecuteRead(ctx, "SELECT id, note FROM "+table)
	require.NoError(t, err, "read-only role must be able to SELECT a granted table")
	require.Equal(t, 1, rows.RowCount)

	// (1) Defense-in-depth, via the real query-box path. ExecuteRead does NO
	// classification of its own, yet a write is still refused — because the
	// read-only pool's connections default to a read-only transaction. We assert
	// the surfaced reason so a wrong-reason failure (e.g. a typo'd table) can't
	// pass this check.
	_, werr := m.ExecuteRead(ctx, "INSERT INTO "+table+" (id, note) VALUES (2, 'x')")
	require.Error(t, werr, "a write through the read-only pool must be refused")
	require.Contains(t, werr.Error(), "read-only transaction",
		"the query-box path is guarded by the read-only transaction default")

	// (2) Authoritative boundary: PRIVILEGE DENIAL, independent of the GUC. Flip
	// the defense-in-depth GUC off on a real read-pool connection — the role may
	// reset its own session GUC — then prove EVERY kind of write is still refused
	// with insufficient_privilege (42501), not read_only_sql_transaction (25006).
	// This is the property that holds even if a reused connection ever had
	// default_transaction_read_only turned off.
	conn, err := m.ReadPool().Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "SET default_transaction_read_only = off")
	require.NoError(t, err, "the role may reset its own session GUC")

	// Writes against the operator's data — read, modify, delete, and drop — are
	// all denied for lack of privilege. (Object CREATION in a public schema is a
	// distinct, separately-tracked residual on PG <= 14, where the schema grants
	// CREATE to PUBLIC; see the band-1 backlog item. It is not exercised here
	// because closing it is a deliberate, larger change to provisionSQL.)
	writes := []string{
		"INSERT INTO " + table + " (id, note) VALUES (3, 'z')",
		"UPDATE " + table + " SET note = 'y' WHERE id = 1",
		"DELETE FROM " + table + " WHERE id = 1",
		"DROP TABLE " + table,
	}
	for _, w := range writes {
		_, werr := conn.Exec(ctx, w)
		require.Error(t, werr, "privilege denial must refuse the write even with the GUC off: %s", w)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, werr, &pgErr, "expected a Postgres error for: %s", w)
		require.Equal(t, "42501", pgErr.Code,
			"write must be denied for lack of privilege (42501), not merely the read-only GUC: %s", w)
	}

	// The probe table and its seed row must survive — every write above failed.
	rows, err = m.ExecuteRead(ctx, "SELECT count(*) AS n FROM "+table)
	require.NoError(t, err)
	require.Equal(t, 1, rows.RowCount)
}
