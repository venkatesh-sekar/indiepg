//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestMigrateS3Session is scenario 11: the ssh-less (shared-S3) migration. The
// dump moves through MinIO. In this single-box test the SAME panel plays both
// roles: it creates a TARGET session, then joins it as the SOURCE and exports the
// source database (a second Postgres container) to S3, after which the target
// worker downloads + restores + verifies locally. The test asserts GROUND TRUTH:
// the dump object lands under pg-migrations/sessions/<code>/ in MinIO, and the
// imported database verifies with exact row parity (read directly via env.PG).
//
// This mode REQUIRES S3 to be configured. The panel rebuilds its ssh-less session
// Service (s.migrate) on a live PUT /api/config — the same self-heal the backup
// manager and drop-off transport already do — so configuring S3 makes session
// migration available immediately, with NO restart. (Previously this test had to
// `systemctl restart indiepg` after ConfigureS3 to work around s.migrate only
// being built at startup; that asymmetry is the fixed product bug.)
func TestMigrateS3Session(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	// The source database name MUST equal the session database name: the export
	// dumps the session's database FROM the source (orchestrator.ExportToSession
	// uses sess.Database), and the target imports into a local database of the same
	// name. Seed a fixed, deterministic schema there.
	const sessionDB = "e2e_session_db"
	src := harness.StartSourcePG(t, env)
	require.NoError(t, src.CreateDatabase(sessionDB))
	src.MustExec(sessionDB, "CREATE TABLE accounts(id int PRIMARY KEY, email text)")
	src.MustExec(sessionDB, "INSERT INTO accounts SELECT g, 'u'||g||'@e2e' FROM generate_series(1,77) g")
	src.MustExec(sessionDB, "CREATE TABLE events(id bigint PRIMARY KEY, kind text)")
	src.MustExec(sessionDB, "INSERT INTO events SELECT g, 'k-'||(g%4) FROM generate_series(1,88) g")
	requireSourceCount(t, src, sessionDB, "accounts", 77)
	requireSourceCount(t, src, sessionDB, "events", 88)

	// Configure S3 (MinIO). The PUT rebuilds the ssh-less session Service in place,
	// so no restart is needed before creating a session.
	_, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "PUT /api/config with the S3 target should succeed")

	// TARGET role: create the session. This spawns the import worker, which polls
	// the session over S3 until the source has exported.
	sess, err := env.Panel.CreateMigrationSession(sessionDB)
	require.NoError(t, err, "POST /api/migrate/sessions should create the session")
	require.NotEmpty(t, sess.Code)
	require.Equal(t, "waiting_for_export", sess.Status)

	// SOURCE role: join the session and export the source database to S3. The panel
	// dumps from the source container over TCP and uploads the dump to the shared
	// bucket, then the target worker imports it locally.
	exportID, err := env.Panel.ExportMigrationSession(sess.Code, src.Conn(sessionDB), sessionDB)
	require.NoError(t, err, "POST /api/migrate/sessions/{code}/export should be accepted")
	require.NotZero(t, exportID)

	// Drive the whole handshake to terminal via the cross-panel session document.
	final := env.Panel.AwaitSession(t, sess.Code, 5*time.Minute)
	require.Equalf(t, "completed", final.Status,
		"ssh-less session must complete; error=%q", final.Error)

	// The source-side export job also reaches completed (uploaded + recorded).
	exportRec := env.Panel.AwaitMigration(t, exportID, 1*time.Minute)
	require.Equalf(t, "completed", exportRec.Status, "export job error=%q", exportRec.Error)

	// (a) Ground truth in MinIO: the dump object moved through the shared bucket and
	// is present under pg-migrations/sessions/<code>/ (key layout migrate.DumpKey).
	dumpKey := "pg-migrations/sessions/" + sess.Code + "/dump.bin"
	exists, err := env.S3.ObjectExists(dumpKey)
	require.NoError(t, err)
	require.Truef(t, exists, "the session dump must exist at %s in MinIO", dumpKey)

	objs, err := env.S3.CountObjects("pg-migrations/sessions/" + sess.Code + "/")
	require.NoError(t, err)
	require.Greaterf(t, objs, 0, "objects must exist under the session prefix in MinIO")

	// (b) Ground truth in Postgres: the imported database verifies with row parity.
	requireTargetCount(t, env, sessionDB, "accounts", 77)
	requireTargetCount(t, env, sessionDB, "events", 88)
}
