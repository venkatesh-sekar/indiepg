//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestMigrateDirectPull is scenario 10: the two DIRECT-PULL migration modes, which
// need NO S3. A second Postgres container is the source; the panel runs pg_dump
// against it over TCP and pg_restore into its OWN local Postgres, then verifies
// row counts. The test drives the real HTTP API (POST /api/migrate/single-db and
// /api/migrate/cluster), polls the worker to completion, and asserts GROUND TRUTH:
// the target databases exist on the panel's Postgres with exact row parity to the
// source (read directly via env.PG.CountRowsDB, bypassing the API).
//
// Determinism: fixed seeded row counts; bounded Await polling (no time.Sleep).
func TestMigrateDirectPull(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Bring up the source Postgres on the panel's compose network and seed a fixed,
	// deterministic schema in two databases: one for the single-db pull, one extra
	// so the whole-cluster pull provably moves more than a single database.
	src := harness.StartSourcePG(t, env)

	require.NoError(t, src.CreateDatabase("e2e_src_single"))
	src.MustExec("e2e_src_single", "CREATE TABLE orders(id int PRIMARY KEY, note text)")
	src.MustExec("e2e_src_single", "INSERT INTO orders SELECT g, 'o-'||g FROM generate_series(1,120) g")
	src.MustExec("e2e_src_single", "CREATE TABLE items(id int PRIMARY KEY)")
	src.MustExec("e2e_src_single", "INSERT INTO items SELECT generate_series(1,45)")

	require.NoError(t, src.CreateDatabase("e2e_cluster_a"))
	src.MustExec("e2e_cluster_a", "CREATE TABLE widgets(id int PRIMARY KEY, kind text)")
	src.MustExec("e2e_cluster_a", "INSERT INTO widgets SELECT g, 'w-'||g FROM generate_series(1,33) g")

	// Sanity: the source counts are what we seeded.
	requireSourceCount(t, src, "e2e_src_single", "orders", 120)
	requireSourceCount(t, src, "e2e_src_single", "items", 45)
	requireSourceCount(t, src, "e2e_cluster_a", "widgets", 33)

	// ---- single-database direct pull -------------------------------------
	t.Run("single-db", func(t *testing.T) {
		id, err := env.Panel.MigrateSingleDB(src.Conn("e2e_src_single"), "e2e_tgt_single")
		require.NoError(t, err, "POST /api/migrate/single-db should be accepted")
		require.NotZero(t, id)

		rec := env.Panel.AwaitMigration(t, id, 4*time.Minute)
		require.Equalf(t, "completed", rec.Status,
			"single-db migration must complete; phase=%q error=%q", rec.Phase, rec.Error)
		require.Equal(t, "single-db", rec.Mode)

		// Ground truth: the target database exists with exact row parity.
		requireTargetCount(t, env, "e2e_tgt_single", "orders", 120)
		requireTargetCount(t, env, "e2e_tgt_single", "items", 45)

		// The panel's own verified row counts (recorded on success) must agree.
		require.Equal(t, int64(120), rec.RowCountsTgt["public.orders"], "recorded target count for orders")
		require.Equal(t, rec.RowCountsSrc["public.orders"], rec.RowCountsTgt["public.orders"],
			"verified source and target counts must match")
	})

	// ---- whole-cluster direct pull (pg_dumpall globals + per-db) ---------
	t.Run("cluster", func(t *testing.T) {
		id, err := env.Panel.MigrateCluster(src.Conn(""))
		require.NoError(t, err, "POST /api/migrate/cluster should be accepted")
		require.NotZero(t, id)

		rec := env.Panel.AwaitMigration(t, id, 5*time.Minute)
		require.Equalf(t, "completed", rec.Status,
			"cluster migration must complete; phase=%q error=%q", rec.Phase, rec.Error)
		require.Equal(t, "cluster", rec.Mode)

		// Ground truth: every non-template source database is recreated locally with
		// row parity (the cluster move drops/recreates databases by name via --create).
		requireTargetCount(t, env, "e2e_src_single", "orders", 120)
		requireTargetCount(t, env, "e2e_src_single", "items", 45)
		requireTargetCount(t, env, "e2e_cluster_a", "widgets", 33)

		// The recorded counts are merged across databases under a "<db>.<schema>.<table>"
		// key; the extra database proves the cluster moved more than one DB.
		require.Equal(t, int64(33), rec.RowCountsTgt["e2e_cluster_a.public.widgets"],
			"recorded target count for the second database's table")
	})
}

// requireSourceCount asserts a row count on the source database (ground truth in).
func requireSourceCount(t *testing.T, src *harness.SourcePG, db, table string, want int) {
	t.Helper()
	got, err := src.CountRows(db, table)
	require.NoError(t, err)
	require.Equalf(t, want, got, "source %s.%s row count", db, table)
}

// requireTargetCount asserts a row count on the panel's local Postgres (ground
// truth out), read directly over the socket — bypassing the API.
func requireTargetCount(t *testing.T, env *harness.Env, db, table string, want int) {
	t.Helper()
	got, err := env.PG.CountRowsDB(db, table)
	require.NoErrorf(t, err, "count %s.%s on the panel target", db, table)
	require.Equalf(t, want, got, "target %s.%s row count", db, table)
}
