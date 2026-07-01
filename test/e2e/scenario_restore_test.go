//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestRestoreAfterLoss is scenario 4: prove a guarded restore recovers data lost
// from the live cluster AND that the manager takes a pre-restore safety backup
// first (so the destructive overwrite is itself recoverable).
//
// BLOCKED ON CENTRAL FIX (do not commit; RED gate). The live restore path is a
// genuine product bug, independently confirmed by the PITR author:
//
//	internal/backup/manager.go Restore (~L430) runs `pgbackrest --delta restore`
//	against a STILL-RUNNING cluster; internal/server/handlers_backups.go
//	handleRestore (L168) does not stop/start the cluster either. pgBackRest
//	refuses: "ERROR: [038]: unable to restore while PostgreSQL is running"
//	(postmaster.pid present). Observed exit 38 in this scenario's first run.
//
// The fix must stop the managed cluster, run the restore, then start it again (the
// existing pre-restore safety backup must still be taken while Postgres is up).
// Once that lands, this test asserts the real end state below.
//
// SECOND PRODUCT FINDING (recovery target, not the stop/start gap): the manager
// ALWAYS takes a full safety backup immediately before the restore, so the
// just-taken safety backup is the NEWEST backup set in the repo. pgBackRest only
// auto-selects an OLDER set for --type=time; for xid/lsn/name (the product's
// RecoveryTarget has no --set knob) it uses the newest set — i.e. the post-loss
// safety backup — and cannot reach a pre-loss point. Therefore a nil-target
// "restore latest" CANNOT bring dropped rows back (it restores the post-drop
// safety snapshot and replays the DROP); only a --type=time target before the loss
// recovers the data. This scenario uses a time target for exactly that reason.
func TestRestoreAfterLoss(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target; BackupConfigured=true means stanza-create + archiving
	// succeeded and the repo initialized.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err)
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured; warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// Seed deterministic data AFTER archiving is on, then force it into an archived
	// WAL segment so a from-backup restore sees it.
	require.NoError(t, env.PG.Exec("CREATE TABLE probe(id int)"))
	require.NoError(t, env.PG.Exec("INSERT INTO probe SELECT generate_series(1,1000)"))
	seeded, err := env.PG.CountRows("probe")
	require.NoError(t, err)
	require.Equal(t, 1000, seeded)
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// Full backup B1: the recovery source — probe (1000 rows) is captured here.
	run, err := env.Panel.RunBackup("full")
	require.NoError(t, err)
	require.Equal(t, "running", run.Result)
	b1, err := env.Panel.AwaitBackup(run.ID, 4*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", b1.Result, "explicit full backup must succeed; error=%q", b1.Error)
	require.Equal(t, "full", b1.BackupType)
	require.NotNil(t, b1.StoppedAt, "a successful backup records its stop time")
	t.Logf("explicit full B1: id=%d label=%q stopped=%s", b1.ID, b1.Label, b1.StoppedAt)

	// Pick a recovery target STRICTLY AFTER B1's stop (so it clears B1's minimum
	// recovery/consistency point — the product passes --target at second precision)
	// and STRICTLY BEFORE the loss. We separate the three instants by whole seconds
	// by polling the server clock (the sanctioned wait — never time.Sleep), so the
	// second-granular time target is unambiguous and the recovered state is fixed.
	awaitClockAfter(t, env, b1.StoppedAt.Add(1*time.Second))
	recoverAt, err := env.PG.Now() // UTC, second precision; strictly after B1.stop
	require.NoError(t, err)
	t.Logf("recovery target time: %s", recoverAt.Format(time.RFC3339))
	awaitClockAfter(t, env, recoverAt.Add(1*time.Second))

	// Simulate data loss: drop the table (strictly after the recovery target).
	require.NoError(t, env.PG.Exec("DROP TABLE probe"))
	gone, err := env.PG.Scalar("SELECT to_regclass('public.probe') IS NULL")
	require.NoError(t, err)
	require.Equal(t, "t", gone, "probe must be gone before the restore")

	// Force the post-target WAL — including the DROP's commit, whose timestamp is
	// strictly after the recovery target — into an archived segment. Time-target
	// recovery stops+promotes only once it replays a record PAST the target; if that
	// segment is still the un-switched current WAL, recovery can stall in
	// "online,recovery" waiting for restore_command to fetch a segment that hasn't
	// been archived yet (observed under heavy host load). Switching here makes the
	// post-target boundary archived and the stop point deterministic.
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	fullsBefore := successfulFullLabels(t, env)
	require.Len(t, fullsBefore, 1, "exactly the explicit full exists before the restore")

	// Guarded restore to the pre-loss target. confirm == stanza name ("main"). The
	// manager takes a safety backup first, then (post central fix) stops Postgres,
	// runs `pgbackrest --type=time restore`, and starts Postgres to replay WAL up to
	// the target and promote.
	res, err := env.Panel.RunRestore(&harness.RestoreSpec{
		Time:   recoverAt.Format(time.RFC3339),
		Action: "promote",
	}, true /*delta*/, "main")
	require.NoErrorf(t, err, "restore must succeed (BLOCKED: central stop/start fix)")
	require.True(t, res.OK, "restore result must be ok: %+v", res)

	// The panel stops Postgres, restores, then starts it again; on start PostgreSQL
	// replays WAL to the target and PROMOTES. /readyz can briefly pass while the
	// cluster is still finishing recovery/promotion (hot standby answers the panel's
	// connection before the cluster is fully up and writable), so this is only a
	// first, coarse gate.
	env.AwaitReady(2 * time.Minute)

	// (a) GROUND TRUTH: the lost rows are back, exactly 1000. Poll the probe table
	// directly (the sanctioned wait, never a fixed sleep) until Postgres has
	// finished promoting and answers — right after the restart it transiently
	// refuses with "the database system is starting up" while it replays WAL from
	// the repo (each segment fetched via the restore_command) and promotes, which
	// can take a few minutes on a busy host. The generous window absorbs that
	// recovery time; a genuinely-unrecoverable restore still fails (it never
	// returns 1000) once the deadline trips.
	var restored int
	harness.Await(t, 6*time.Minute, 2*time.Second, "restored probe row count", func() (bool, error) {
		n, err := env.PG.CountRows("probe")
		if err != nil {
			return false, err // transient "starting up"/"in recovery" — keep polling
		}
		restored = n
		return true, nil
	})
	require.Equal(t, 1000, restored, "the restore must return all 1000 lost rows")

	// (b) A SAFETY backup was taken BEFORE the overwrite. Two independent proofs:
	//   1) the result records the safety backup's pgBackRest label;
	//   2) backup history now has a SECOND successful full (the safety snapshot),
	//      distinct from B1, whose label matches that recorded on the result.
	safety := res.SafetyLabel()
	require.NotEmpty(t, safety, "restore result must record the pre-restore safety_backup_label")
	require.NotEqual(t, b1.Label, safety, "safety backup must be distinct from the explicit full")

	fullsAfter := successfulFullLabels(t, env)
	require.GreaterOrEqual(t, len(fullsAfter), 2,
		"a pre-restore safety full must have been recorded in addition to B1")
	require.Contains(t, fullsAfter, safety,
		"the safety backup label must appear in backup history as a successful full")

	// Corroborate against the live pgBackRest repo view.
	require.GreaterOrEqual(t, repoFullCount(t, env), 2,
		"pgbackrest info must show the explicit full plus the safety full")
}

// awaitClockAfter blocks (via the sanctioned poll, not time.Sleep) until the
// server clock is strictly past the given instant, so second-granular recovery
// targets can be separated deterministically from surrounding events.
func awaitClockAfter(t *testing.T, env *harness.Env, after time.Time) {
	t.Helper()
	harness.Await(t, 15*time.Second, 200*time.Millisecond,
		"server clock past "+after.Format(time.RFC3339), func() (bool, error) {
			now, err := env.PG.Now()
			if err != nil {
				return false, err
			}
			return now.After(after), nil
		})
}

// successfulFullLabels returns the pgBackRest labels of all successful FULL
// backups in the panel history.
func successfulFullLabels(t *testing.T, env *harness.Env) []string {
	t.Helper()
	h, err := env.Panel.ListBackups()
	require.NoError(t, err)
	var labels []string
	for _, b := range h.Backups {
		if b.BackupType == "full" && b.Result == "success" {
			labels = append(labels, b.Label)
		}
	}
	return labels
}

// repoFullCount counts full backups in the live pgBackRest repo (ground truth,
// independent of the panel's history table).
func repoFullCount(t *testing.T, env *harness.Env) int {
	t.Helper()
	out, err := env.ExecAsUser("postgres", "pgbackrest", "--stanza=main", "info")
	require.NoError(t, err)
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "full backup:") {
			count++
		}
	}
	return count
}
