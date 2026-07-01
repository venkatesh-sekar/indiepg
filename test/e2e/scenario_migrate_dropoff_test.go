//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestMigrateDropoff is scenario 12: the "drop-off link" migration. The panel mints
// two presigned S3 PUT URLs (dump + meta.json) for a source it CANNOT reach. The
// source container pushes the dump + meta.json to those URLs itself (replicating
// scripts/migrate-push.sh's exact format — the panel's copy-paste command pipes a
// script from raw.githubusercontent.com, unreachable from the isolated test net),
// then the panel imports it with SHA-256 + per-table row-count verification.
//
// It asserts GROUND TRUTH on every layer: the pushed objects land at the drop-off
// keys in MinIO, the import completes (which itself proves the panel's SHA-256 and
// row-count verification both passed), and the imported database has exact row
// parity with the source (read directly via env.PG, bypassing the API).
//
// This mode REQUIRES S3 — but unlike the ssh-less session, the drop-off transport
// IS refreshed by a live PUT /api/config, so NO panel restart is needed.
func TestMigrateDropoff(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure S3 (MinIO). The drop-off transport is rebuilt live on this save, so
	// the mint/import endpoints work immediately.
	_, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")

	// Seed a fixed, deterministic source schema across two schemas, so the per-table
	// (schema, name) verification — not just a flat total — is exercised.
	const srcDB = "e2e_drop_src"
	const targetDB = "e2e_drop_tgt"
	src := harness.StartSourcePG(t, env)
	require.NoError(t, src.CreateDatabase(srcDB))
	src.MustExec(srcDB, "CREATE TABLE customers(id int PRIMARY KEY, name text)")
	src.MustExec(srcDB, "INSERT INTO customers SELECT g, 'c-'||g FROM generate_series(1,64) g")
	src.MustExec(srcDB, "CREATE TABLE invoices(id int PRIMARY KEY, cents bigint)")
	src.MustExec(srcDB, "INSERT INTO invoices SELECT g, g*100 FROM generate_series(1,29) g")
	src.MustExec(srcDB, "CREATE SCHEMA billing")
	src.MustExec(srcDB, "CREATE TABLE billing.ledger(id int PRIMARY KEY)")
	src.MustExec(srcDB, "INSERT INTO billing.ledger SELECT generate_series(1,13)")
	requireSourceCount(t, src, srcDB, "customers", 64)
	requireSourceCount(t, src, srcDB, "invoices", 29)
	requireSourceCount(t, src, srcDB, "billing.ledger", 13)

	// Mint the drop-off link: two presigned PUT URLs + a paste-able push command.
	drop, err := env.Panel.CreateDropoff(targetDB)
	require.NoError(t, err, "POST /api/migrate/drops should mint a drop-off session")
	require.NotEmpty(t, drop.Code)
	dumpURL, metaURL := drop.DumpURL(), drop.MetaURL()
	require.NotEmpty(t, dumpURL, "minted command must carry the presigned dump URL")
	require.NotEmpty(t, metaURL, "minted command must carry the presigned meta URL")

	// SOURCE push: dump srcDB, checksum it, build meta.json the way migrate-push.sh
	// does, and PUT both objects to the presigned URLs from inside the source box.
	sha, size, err := src.PushDropoff(srcDB, dumpURL, metaURL)
	require.NoError(t, err, "the source push (pg_dump + sha256 + meta.json + curl PUT) must succeed")
	require.NotEmpty(t, sha)
	require.Greater(t, size, int64(0))

	// (a) Ground truth in MinIO: both pushed objects landed at the drop-off keys.
	dumpKey := "pg-migrations/dropoff/" + drop.Code + "/dump"
	metaKey := "pg-migrations/dropoff/" + drop.Code + "/meta.json"
	dumpExists, err := env.S3.ObjectExists(dumpKey)
	require.NoError(t, err)
	require.Truef(t, dumpExists, "pushed dump must exist at %s", dumpKey)
	metaExists, err := env.S3.ObjectExists(metaKey)
	require.NoError(t, err)
	require.Truef(t, metaExists, "pushed meta.json must exist at %s", metaKey)

	// The status endpoint flips waiting -> uploaded once meta.json is present, and
	// reports the authoritative (panel-side StatObject) dump size.
	st, err := env.Panel.GetDropoff(drop.Code)
	require.NoError(t, err)
	require.Equal(t, "uploaded", st.Status, "status must flip to uploaded once meta.json is present")
	require.Equal(t, size, st.ByteSize, "the panel's authoritative object size must match the pushed dump")

	// Start the import: the panel streams the dump from S3, verifies its SHA-256
	// against meta.json, restores into the local target, and verifies per-table row
	// counts. Reaching "completed" proves BOTH the SHA-256 and row-count checks passed.
	id, err := env.Panel.StartDropoff(drop.Code)
	require.NoError(t, err, "POST /api/migrate/drops/{code}/start should be accepted")
	require.NotZero(t, id)

	rec := env.Panel.AwaitMigration(t, id, 5*time.Minute)
	require.Equalf(t, "completed", rec.Status,
		"drop-off import must complete (SHA-256 + row-count verified); phase=%q error=%q", rec.Phase, rec.Error)
	require.Equal(t, "drop-off", rec.Mode)
	require.Empty(t, rec.Error)

	// (b) Ground truth in Postgres: the imported target has exact row parity.
	requireTargetCount(t, env, targetDB, "customers", 64)
	requireTargetCount(t, env, targetDB, "invoices", 29)
	requireTargetCount(t, env, targetDB, "billing.ledger", 13)

	// The panel's recorded verified counts (per the SHA/row verification) must agree.
	require.Equal(t, int64(64), rec.RowCountsTgt["public.customers"])
	require.Equal(t, int64(29), rec.RowCountsTgt["public.invoices"])
	require.Equal(t, int64(13), rec.RowCountsTgt["billing.ledger"])
	require.Equal(t, rec.RowCountsSrc["public.customers"], rec.RowCountsTgt["public.customers"],
		"source (meta.json) and target verified counts must match")
}
