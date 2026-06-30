//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestBackupFull is scenario 2: a full pgBackRest backup to MinIO on the
// preinstalled image. It drives the panel's real S3 flow (PUT /api/config ->
// render conf + enable archiving + stanza-create, then POST /api/backups/run) and
// asserts BOTH real ground truths: objects actually land in the MinIO bucket and
// the panel's backup history lists the completed full.
func TestBackupFull(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target against MinIO (path-style, TLS). The panel renders
	// the pgBackRest config, enables WAL archiving (restarting Postgres if needed),
	// and runs stanza-create — so BackupConfigured=true means the repo initialized.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured (stanza-create succeeded); warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// Seed deterministic data AFTER archiving is on, so the backup has real content
	// and its WAL is archived.
	require.NoError(t, env.PG.Exec(
		"CREATE TABLE IF NOT EXISTS e2e_backup_probe(id int PRIMARY KEY, note text)"))
	require.NoError(t, env.PG.Exec(
		"INSERT INTO e2e_backup_probe SELECT g, 'row-'||g FROM generate_series(1,100) g ON CONFLICT DO NOTHING"))
	rows, err := env.PG.CountRows("e2e_backup_probe")
	require.NoError(t, err)
	require.Equal(t, 100, rows)

	// Force the inserts into an archived WAL segment so a from-scratch restore would
	// see them (and so the archive/ tree is populated for the assertion below).
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// Run a FULL backup. It returns immediately with a history id; poll to terminal.
	run, err := env.Panel.RunBackup("full")
	require.NoError(t, err, "POST /api/backups/run full should be accepted")
	require.Equal(t, "running", run.Result)

	rec, err := env.Panel.AwaitBackup(run.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", rec.Result, "full backup must succeed; error=%q", rec.Error)
	require.Equal(t, "full", rec.BackupType)

	// (a) Ground truth in MinIO: real objects landed in the bucket.
	total, err := env.S3.CountObjects("")
	require.NoError(t, err)
	require.Greater(t, total, 0, "the backup must write objects into the MinIO bucket")

	// The stanza's backup metadata and at least one archived WAL must be present.
	backupInfo, err := env.S3.ObjectExists("backup/main/backup.info")
	require.NoError(t, err)
	require.True(t, backupInfo, "backup/main/backup.info must exist in the bucket")

	backupSet, err := env.S3.CountObjects("backup/main/")
	require.NoError(t, err)
	require.Greater(t, backupSet, 1, "a full backup set (not just backup.info) must be present")

	archived, err := env.S3.CountObjects("archive/main/")
	require.NoError(t, err)
	require.Greater(t, archived, 0, "WAL must be archived to the repo")

	// (b) The panel's backup history lists the completed full.
	hist, err := env.Panel.ListBackups()
	require.NoError(t, err)
	listed, ok := hist.FindBackup(run.ID)
	require.True(t, ok, "the backup must appear in GET /api/backups")
	require.Equal(t, "success", listed.Result)
	require.Equal(t, "full", listed.BackupType)
	require.NotEmpty(t, listed.Label, "a successful backup should record its pgBackRest label")
	require.Greater(t, listed.RepoBytes, int64(0), "info enrichment should record a non-zero repo size")
}
