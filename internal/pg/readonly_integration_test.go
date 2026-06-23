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
	// all denied for lack of privilege. Object CREATION in the public schema is
	// also denied: provisionSQL revokes CREATE from the PUBLIC pseudo-role in the
	// panel-managed `postgres` database, so the read-only role cannot create (and
	// thus own/write) scratch objects even with its GUC off (PG <= 14 would
	// otherwise inherit CREATE via PUBLIC).
	writes := []string{
		"INSERT INTO " + table + " (id, note) VALUES (3, 'z')",
		"UPDATE " + table + " SET note = 'y' WHERE id = 1",
		"DELETE FROM " + table + " WHERE id = 1",
		"DROP TABLE " + table,
		"CREATE TABLE public.indiepg_ro_scratch (id int)",
	}
	for _, w := range writes {
		_, werr := conn.Exec(ctx, w)
		require.Error(t, werr, "privilege denial must refuse the write even with the GUC off: %s", w)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, werr, &pgErr, "expected a Postgres error for: %s", w)
		require.Equal(t, "42501", pgErr.Code,
			"write must be denied for lack of privilege (42501), not merely the read-only GUC: %s", w)
	}

	// Revoking CREATE from PUBLIC must NOT break the admin role in the database we
	// revoked it in: the same provisioning step re-grants CREATE to admin in this
	// (panel-managed `postgres`) DB, so guided object creation still works here.
	// Operator app DBs are intentionally untouched by provisionSQL, so their
	// public-schema CREATE is unaffected and not exercised here.
	const adminTable = "public.indiepg_admin_create_probe"
	_, err = priv.Exec(ctx, "DROP TABLE IF EXISTS "+adminTable)
	require.NoError(t, err)
	_, err = priv.Exec(ctx, "CREATE TABLE "+adminTable+" (id int)")
	require.NoError(t, err, "admin object creation must still work after REVOKE CREATE FROM PUBLIC")
	defer func() { _, _ = priv.Exec(context.Background(), "DROP TABLE IF EXISTS "+adminTable) }()

	// The probe table and its seed row must survive — every write above failed.
	rows, err = m.ExecuteRead(ctx, "SELECT count(*) AS n FROM "+table)
	require.NoError(t, err)
	require.Equal(t, 1, rows.RowCount)
}

// TestReadOnlyPool_StatementTimeoutEnforced proves the read pool's
// statement_timeout is honored by Postgres at runtime — not merely present in
// the DSN string (which buildDSN's unit test already covers). The query box
// relies on this cap so a runaway SELECT cannot pin a pooled connection forever;
// if the param were ever silently dropped (e.g. pgx ignoring an unknown key),
// pg_sleep would run to completion and these assertions would fail.
//
// Gated identically to TestReadOnlyRole_DBLevelWriteDenial (INDIEPG_TEST_SOCKET,
// integration build tag).
func TestReadOnlyPool_StatementTimeoutEnforced(t *testing.T) {
	sock := os.Getenv("INDIEPG_TEST_SOCKET")
	if sock == "" {
		t.Skip("set INDIEPG_TEST_SOCKET to a cluster provisioned with the indiepg roles")
	}
	// A short server-side cap on the read pool, paired with a generous client
	// context so the cancellation we observe is Postgres killing the statement,
	// never the Go context deadline tripping first.
	const stmtTimeout = 250 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := New(Options{Config: config.Config{
		PGSocketDir:      sock,
		StatementTimeout: stmtTimeout,
	}})
	require.NoError(t, m.Connect(ctx))
	defer m.Close()

	// (1) The real query-box path: ExecuteRead must surface the server-side
	// statement timeout. pg_sleep(5) far exceeds the 250ms cap, so it is killed
	// long before it returns.
	t0 := time.Now()
	_, err := m.ExecuteRead(ctx, "SELECT pg_sleep(5)")
	elapsed := time.Since(t0)
	require.Error(t, err, "a query exceeding statement_timeout must be cancelled")
	require.Contains(t, err.Error(), "statement timeout",
		"the read pool's statement_timeout must cancel the runaway query")
	require.Less(t, elapsed, 5*time.Second,
		"the query must be killed at the cap, well before pg_sleep(5) completes")

	// (2) Prove it is SQLSTATE 57014 (query_canceled) raised by Postgres on the
	// read pool itself — i.e. the cap is enforced at the DB level on the very
	// pool the query box uses, not by a client-side deadline. ExecuteRead
	// collapses the PgError to its message, so assert the code on a raw conn.
	conn, err := m.ReadPool().Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "SELECT pg_sleep(5)")
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "57014", pgErr.Code,
		"statement_timeout must raise query_canceled (57014) on the read pool")

	// Positive control: the privileged pool carries NO forced statement_timeout
	// (so long guided maintenance like CREATE INDEX is never killed). A sleep
	// past the read pool's 250ms cap therefore completes here — proving the cap
	// is scoped to the read pool, not the whole Manager.
	priv := m.PrivPool()
	require.NotNil(t, priv)
	_, err = priv.Exec(ctx, "SELECT pg_sleep(0.5)")
	require.NoError(t, err,
		"the privileged pool must not impose the read pool's statement_timeout")
}
