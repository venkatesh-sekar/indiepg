//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// BLOCKED ON CENTRAL FIX (product bug in internal/backup/restore_deep.go): the
// deep restore-test cannot boot the scratch cluster on the Debian/Ubuntu PGDG
// layout indiepg installs. pgCtlStartCmd boots `pg_ctl -D <scratch> start`, which
// requires <scratch>/postgresql.conf, but on Debian/Ubuntu postgresql.conf lives
// in /etc/postgresql/<major>/<cluster>/ — NOT inside PGDATA — so pgBackRest never
// backs it up and the restored scratch dir has none. The boot dies instantly with
// `postgres: could not access the server configuration file ".../postgresql.conf":
// No such file or directory` and the endpoint returns 500. This test encodes the
// intended contract and will pass once restore_deep.go materializes the missing
// config into the scratch dir before boot. See the agent's report for the full
// root-cause + proposed fix. The assertions below are NOT weakened.
//
// TestRestoreTestDeep is scenario 6: the deep, non-destructive restore-test. It
// proves a backup is actually RECOVERABLE — not merely repo-checksum-clean — by
// driving the panel's POST /api/backups/restore-test?deep=true, which restores the
// newest backup into a throwaway scratch cluster, boots it with full WAL replay,
// and counts user rows over a private socket, then tears the scratch cluster down.
//
// It asserts three real ground truths: (1) the synchronous response reports the
// scratch-restore+boot method and a non-zero verified row count; (2) the persisted
// restore_tests history row records success with VerifiedRows>0; and (3) no scratch
// directory leaked under the deep drill's ScratchRoot (os.TempDir() == /tmp, since
// the panel unit sets neither TMPDIR nor PrivateTmp and runs as root) — the
// guaranteed-cleanup contract held.
func TestRestoreTestDeep(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target against MinIO: renders the pgBackRest config, enables
	// WAL archiving, and runs stanza-create. BackupConfigured=true => repo ready.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured (stanza-create succeeded); warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// Seed a deterministic, fixed-size table AFTER archiving is on. ANALYZE persists
	// pg_class.reltuples into the heap *before* the backup, so the restored clone's
	// reltuples-based deep row count sees ~1234 (the table lives in "postgres", which
	// is exactly the database the deep row-count query targets).
	require.NoError(t, env.PG.Exec("CREATE TABLE durability_probe(id int)"))
	require.NoError(t, env.PG.Exec("INSERT INTO durability_probe SELECT generate_series(1,1234)"))
	require.NoError(t, env.PG.Exec("ANALYZE durability_probe"))
	rows, err := env.PG.CountRows("durability_probe")
	require.NoError(t, err)
	require.Equal(t, 1234, rows, "deterministic seed must be exactly 1234 rows")

	// Force the seed (and its ANALYZE) into an archived WAL segment so a from-scratch
	// restore + boot provably replays them.
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// Take the FULL backup the deep restore-test will recover from. Returns a history
	// id immediately; poll to terminal success.
	run, err := env.Panel.RunBackup("full")
	require.NoError(t, err, "POST /api/backups/run full should be accepted")
	require.Equal(t, "running", run.Result)

	rec, err := env.Panel.AwaitBackup(run.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", rec.Result, "full backup must succeed; error=%q", rec.Error)
	require.Equal(t, "full", rec.BackupType)

	// (1) Drive the DEEP restore-test. The handler runs the whole drill inline and
	// only responds when restore + boot + row count + teardown have completed, so a
	// successful return already means the scratch cluster booted and was queried.
	res, err := env.Panel.RestoreTest(true)
	require.NoError(t, err, "deep restore-test should complete successfully")
	require.True(t, res.OK, "deep restore-test result.ok should be true; message=%q", res.Message)
	require.Equal(t, "scratch restore + boot", res.Method(),
		"deep path must report the scratch-restore+boot method, not the cheap verify")
	require.Greater(t, res.VerifiedRows(), int64(0),
		"the booted scratch cluster must report a non-zero verified row count")

	// (2) The pass/fail outcome is persisted in restore_tests history. Match the row
	// the drill recorded by its history_id and assert success + VerifiedRows>0.
	histID := res.HistoryID()
	require.Greater(t, histID, int64(0), "deep result should surface the recorded history_id")

	hist, err := env.Panel.ListBackups()
	require.NoError(t, err)
	require.NotEmpty(t, hist.RestoreTests, "GET /api/backups should list the restore-test row")

	var found bool
	for _, rt := range hist.RestoreTests {
		if rt.ID != histID {
			continue
		}
		found = true
		require.Equal(t, "success", rt.Result,
			"persisted restore-test row must record success; detail=%q", rt.Detail)
		require.Greater(t, rt.VerifiedRows, int64(0),
			"persisted restore-test row must record a non-zero verified row count")
	}
	require.Truef(t, found, "restore_tests history must contain the recorded row id %d", histID)

	// (3) No scratch dir leaked. The deep drill creates indiepg-restoretest-* dirs
	// under its ScratchRoot (default os.TempDir() == /tmp; the panel unit sets no
	// TMPDIR and no PrivateTmp, and Exec shares the panel's mount namespace) and
	// guarantees their removal even on error/panic. Assert none survive.
	lsOut, err := env.Exec("ls", "-a", "/tmp")
	require.NoError(t, err)
	require.NotContains(t, lsOut, "indiepg-restoretest-",
		"the deep restore-test must leave no scratch directory behind under /tmp:\n%s",
		strings.TrimSpace(lsOut))
}
