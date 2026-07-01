//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestPITR is scenario 5: the deterministic point-in-time-recovery proof. It
// configures S3, takes a BASELINE full backup, then seeds a deterministic
// PRE-target batch, captures a recovery target as a TRANSACTION ID (xid — NO
// wall-clock), then writes a POST-target batch. Restoring the live cluster to that
// xid must rewind the cluster so the pre-target rows survive and the post-target
// rows are PROVABLY gone. The backup precedes the target data so the target xid is
// always reachable by forward WAL replay (see the ordering note below).
//
// Why xid and not time: a wall-clock target races the test (and the server clock);
// a transaction id is exact. pgBackRest recovery is INCLUSIVE by default
// (target-exclusive is off and the product's RecoveryTarget exposes no
// inclusive/exclusive knob — internal/backup/command.go), so recovery stops just
// AFTER applying the transaction whose id equals the target. The target txn here is
// a read-only `pg_current_xact_id()` call that changes no rows, so inclusive vs
// exclusive cannot move the row count — what the target excludes is the LATER
// 200-row INSERT, whose xid is strictly greater than the target.
func TestPITR(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// Configure the S3 target: renders pgBackRest config, enables WAL archiving,
	// runs stanza-create. BackupConfigured=true means the repo initialized so WAL
	// is being archived from here on — a prerequisite for any PITR.
	cfgResp, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")
	require.True(t, cfgResp.BackupConfigured,
		"pgBackRest must be configured (stanza-create succeeded); warning=%q hint=%q detail=%q",
		cfgResp.BackupWarning, cfgResp.BackupHint, cfgResp.BackupDetail)

	// --- Baseline full backup, taken BEFORE any target data. ---
	// This ordering is load-bearing: the recovery target's xid MUST fall strictly
	// AFTER this backup's redo point, or pgBackRest/PostgreSQL replay starts past the
	// target and recovery ends with "recovery ended before configured recovery target
	// was reached" (the postmaster then FATAL-exits). Backing up AFTER the pre-target
	// rows races the backup's start-checkpoint redo LSN against the target xid and
	// flakes under load. Backing up first makes the target unconditionally reachable
	// by forward WAL replay.
	require.NoError(t, env.PG.Exec("DROP TABLE IF EXISTS t"))
	run, err := env.Panel.RunBackup("full")
	require.NoError(t, err, "POST /api/backups/run full should be accepted")
	require.Equal(t, "running", run.Result)
	rec, err := env.Panel.AwaitBackup(run.ID, 5*time.Minute)
	require.NoError(t, err)
	require.Equalf(t, "success", rec.Result, "full backup must succeed; error=%q", rec.Error)
	require.Equal(t, "full", rec.BackupType)

	// --- PRE-TARGET batch: 500 rows committed AFTER the base backup, so they exist
	// only in archived WAL and are recovered by forward replay. ---
	require.NoError(t, env.PG.Exec("CREATE TABLE t(id int)"))
	require.NoError(t, env.PG.Exec("INSERT INTO t SELECT generate_series(1,500)"))
	pre, err := env.PG.CountRows("t")
	require.NoError(t, err)
	require.Equal(t, 500, pre, "pre-target batch must be exactly 500 rows")

	// Capture the recovery target as a transaction id. pg_current_xact_id() (PG13+)
	// returns the current xid8; on a fresh cluster the epoch is 0 so it equals the
	// plain transaction id pgBackRest's --type=xid wants. This SELECT runs in its
	// own (autocommit) transaction whose committed xid is the target — strictly after
	// the base backup above.
	T, err := env.PG.Scalar("SELECT pg_current_xact_id()")
	require.NoError(t, err, "capture recovery target xid")
	require.NotEmpty(t, T, "recovery target xid must be non-empty")
	t.Logf("recovery target xid T = %s (recover to AND including this txn; --type=xid, inclusive default)", T)

	// Force the segment holding the pre-target writes + the target txn into the
	// archive (no half-written current segment) so replay can reach T.
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// --- POST-TARGET batch: 200 more rows committed in a txn whose xid is strictly
	// greater than T. These exist ONLY in WAL archived after the target; a restore
	// to T must NOT replay them. ---
	require.NoError(t, env.PG.Exec("INSERT INTO t SELECT generate_series(501,700)"))
	post, err := env.PG.CountRows("t")
	require.NoError(t, err)
	require.Equal(t, 700, post, "after the post-target batch the live table has 700 rows")
	// Archive the post-target WAL too, so a latest-WAL restore WOULD see all 700 —
	// proving it is the recovery TARGET (not missing WAL) that excludes the 200.
	require.NoError(t, env.PG.Exec("SELECT pg_switch_wal()"))

	// --- Restore the live cluster to xid=T. delta restores in place over the
	// existing data dir; promote brings the cluster back as a writable primary at
	// the target. Confirm must equal the stanza name (config.DefaultStanza="main";
	// objects land under backup/main/...). ---
	res, err := env.Panel.RestoreToTarget(&harness.PITRTarget{
		XID:    T,
		Action: "promote",
	}, true /* delta */, "main")
	if err != nil {
		// Diagnostic for a restore failure: the live restore runs `pgbackrest
		// restore` directly against the data dir. Capture pgBackRest's own log and
		// the postgresql unit state to pinpoint the cause precisely.
		if logOut, lerr := env.Exec("sh", "-c",
			"tail -n 30 /var/log/pgbackrest/main-restore.log 2>/dev/null || true"); lerr == nil {
			t.Logf("=== /var/log/pgbackrest/main-restore.log (tail) ===\n%s", logOut)
		}
		t.Logf("postgresql unit state at restore time: %q", env.SystemctlIsActive("postgresql"))
	}
	require.NoErrorf(t, err, "POST /api/backups/restore to xid=%s should succeed", T)
	require.True(t, res.OK, "restore result must be ok: %+v", res)
	require.True(t, res.IsPITR(), "restore must record pitr=true for a non-empty target")
	require.NotEmpty(t, res.SafetyBackupLabel(),
		"a pre-restore safety backup must be recorded as the recovery point")
	t.Logf("restore ok: safety_backup_label=%s", res.SafetyBackupLabel())

	// The restore stops Postgres, restores, then starts it again; on start
	// PostgreSQL replays WAL to xid=T and PROMOTES (target-action=promote). /readyz
	// only checks the panel's own store (internal/server/handlers_health.go keeps
	// the panel working even when the managed Postgres is down), so it passes well
	// before the cluster has finished recovery — this is only a first, coarse gate.
	env.AwaitReady(2 * time.Minute)

	// --- THE PROOF: the cluster rewound to T. The 500 pre-target rows survive; the
	// 200 post-target rows are provably gone. ---
	//
	// Poll the table directly (the sanctioned wait, never a fixed sleep) until
	// Postgres has finished promoting and answers. Right after the restart it
	// transiently refuses with "the database system is starting up" while it replays
	// WAL from the repo and promotes, which can take minutes on a slow host; Await
	// treats that transient error as "not ready yet" and keeps polling. The count is
	// monotonic at 500 here — the 500 pre-target rows are already in the base backup
	// and the 200 post-target rows (xid > T) are never replayed — so the FIRST
	// successful read is the final answer. A genuinely-wrong restore still fails the
	// Equal assertion (or the deadline) rather than passing on a transient read.
	// Wait for the cluster to FINISH recovery and PROMOTE before reading — not merely
	// to accept connections. With hot_standby on, PostgreSQL accepts READ-ONLY
	// connections the instant it reaches consistency (the base backup's end LSN),
	// which is BEFORE it has replayed forward to the xid target. The base backup here
	// is empty (taken before the table), so a read in that window can observe `t`
	// after its CREATE but before the 500-row INSERT replays and return 0 — a false
	// "ready". Gate on pg_is_in_recovery()=false so the count is read only once redo
	// has applied everything through the target (xid=T, inclusive) and the cluster has
	// promoted to read-write. On a slow host the post-restore data-directory fsync
	// alone can take minutes, so the bound is generous; the assertion — not the
	// deadline — proves correctness.
	var got int
	harness.Await(t, 12*time.Minute, 2*time.Second, "promoted row count after PITR", func() (bool, error) {
		inRecovery, err := env.PG.Scalar("SELECT pg_is_in_recovery()")
		if err != nil {
			return false, err // "starting up" / socket not up yet — keep polling
		}
		if inRecovery != "f" {
			return false, nil // still replaying toward the target in hot standby — wait for promote
		}
		n, err := env.PG.CountRows("t")
		if err != nil {
			return false, err
		}
		got = n
		return true, nil
	})
	require.Equalf(t, 500, got,
		"PITR to xid=%s must leave exactly the 500 pre-target rows (the 200 post-target rows must be gone); got %d",
		T, got)
}
