//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestPGVersionUpgrade is scenarios 13 + 14: the PostgreSQL version-upgrade
// engine, exercised end-to-end through the real async HTTP API against real
// ground truth (pg_lsclusters, server_version_num over the socket, seeded rows
// that must survive a major copy upgrade, systemd unit state).
//
// Source/target majors (from versioncatalog.go SupportedMajors {17 default,16,15}
// and the e2e base image, which caches PG 16 + PG 17 debs):
//   - Minor:    runs on the preinstalled cluster (PG 17). Pinned packages mean no
//               newer minor exists, so the panel correctly refuses with a typed
//               409 "no minor update available" — asserted, with the cluster left
//               healthy and unchanged.
//   - Major:    PG 16 -> PG 17. A real provision on the OLDER major (the base
//               image's `indiepg install --pg-version 16`), then preflight + start
//               + finalize.
//   - Rollback: PG 16 -> PG 17, then roll back to 16 BEFORE finalize.
//
// The two major paths each do a real ~200s install + a copy-mode pg_upgradecluster
// + a mandatory pre-upgrade pgBackRest backup, so they configure S3 first and use
// generous bounded Awaits. No time.Sleep anywhere — only Await/Poll.
func TestPGVersionUpgrade(t *testing.T) {
	t.Parallel()

	t.Run("Minor", testMinorUpgrade)
	t.Run("Major", testMajorUpgrade)
	t.Run("Rollback", testMajorUpgradeRollback)
}

// ---- Minor upgrade (on the preinstalled PG 17 cluster) ----

func testMinorUpgrade(t *testing.T) {
	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword), "login to the preinstalled panel")

	before, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.True(t, before.Running, "postgres should be running")
	major := before.Current.Major
	require.GreaterOrEqual(t, major, 15, "preinstalled on a supported major")

	st, err := env.Panel.MinorUpgrade(false)
	if before.Available.Minor.Available {
		// A newer minor genuinely exists: the async upgrade must run to success and
		// the cluster must stay on the same major.
		require.NoError(t, err, "minor upgrade should start")
		require.NotNil(t, st.Operation)
		op := env.Panel.AwaitUpgradeOp(t, "minor", 8*time.Minute)
		require.Equal(t, "success", op.Status, "minor upgrade should succeed; error=%s log=%v", op.Error, op.Log)
	} else {
		// Pinned packages: no newer minor. The endpoint must complete by correctly
		// refusing with a typed 409 conflict (NOT a silent no-op or a 500), and the
		// cluster must be untouched.
		require.Error(t, err, "minor upgrade must report there is nothing to do")
		var pe *harness.PanelError
		require.ErrorAs(t, err, &pe, "expected a typed panel error")
		require.Equal(t, 409, pe.Status)
		require.Equal(t, "conflict", pe.Code, "should be a conflict (no update available)")
	}

	// Ground truth either way: the cluster is healthy and still on the same major.
	requireClusterHealthy(t, env, major)
	after, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.Equal(t, major, after.Current.Major, "major unchanged by a minor upgrade")
}

// ---- Major upgrade 16 -> 17 with finalize ----

func testMajorUpgrade(t *testing.T) {
	const (
		fromMajor = 16
		toMajor   = 17
		seedRows  = 777
	)
	env := installPanelOnMajor(t, fromMajor)
	configureBackupTarget(t, env)
	seedTable(t, env, seedRows)

	// Preflight 16 -> 17: installs the PG 17 packages and runs the checklist. It is
	// synchronous and apt-heavy, so allow a generous request timeout.
	pf, err := env.Panel.MajorPreflight(toMajor, 8*time.Minute)
	require.NoError(t, err, "major preflight should complete")
	require.False(t, pf.HasFail(), "preflight must have no blocking checks: %+v", pf.Checks)
	require.False(t, pf.Preview.Blocking)
	require.Equal(t, fromMajor, pf.Preview.FromMajor)
	require.Equal(t, toMajor, pf.Preview.ToMajor)
	if c, ok := pf.Check("target_binaries"); ok {
		require.Equal(t, "pass", c.Status, "target binaries: %s", c.Message)
	}
	if c, ok := pf.Check("pg_upgrade_check"); ok {
		require.Equal(t, "pass", c.Status, "pg_upgrade check: %s", c.Message)
	}

	// Start the async major upgrade and wait for it to verify into pending-finalize.
	_, err = env.Panel.MajorStart(toMajor, true)
	require.NoError(t, err, "major upgrade should start")
	op := env.Panel.AwaitUpgradeOp(t, "major", 20*time.Minute)
	require.Equal(t, "success", op.Status, "major upgrade should succeed; error=%s\nlog=%s", op.Error, strings.Join(op.Log, "\n"))

	// API view: now live on the new major, awaiting finalization.
	info, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.True(t, info.Running)
	require.Equal(t, toMajor, info.Current.Major, "version endpoint reports the NEW major")
	require.NotNil(t, info.PendingFinalization, "a pending finalization must be recorded")
	require.Equal(t, fromMajor, info.PendingFinalization.FromMajor)
	require.Equal(t, toMajor, info.PendingFinalization.ToMajor)

	// Ground truth: the live cluster is the new major, healthy, and the seeded rows
	// survived the copy upgrade.
	requireClusterHealthy(t, env, toMajor)
	n, err := env.PG.CountRows("e2e_upgrade_seed")
	require.NoError(t, err)
	require.Equal(t, seedRows, n, "seeded rows must survive the major upgrade")

	// Before finalize both clusters exist (old parked, new live on 5432).
	clusters := lsClusters(t, env)
	require.True(t, hasCluster(clusters, fromMajor), "old cluster lingers as a rollback point pre-finalize")
	require.True(t, hasOnlineCluster(clusters, toMajor, "5432"), "new cluster is live on 5432")

	// Finalize: drop the old cluster (confirm with the OLD major).
	_, err = env.Panel.FinalizeUpgrade(fromMajor)
	require.NoError(t, err, "finalize should start")
	fop := env.Panel.AwaitUpgradeOp(t, "finalize", 5*time.Minute)
	require.Equal(t, "success", fop.Status, "finalize should succeed; error=%s", fop.Error)

	// Ground truth after finalize: only the new cluster remains; nothing pending.
	clusters = lsClusters(t, env)
	require.False(t, hasCluster(clusters, fromMajor), "old PostgreSQL %d cluster must be dropped after finalize", fromMajor)
	require.True(t, hasOnlineCluster(clusters, toMajor, "5432"), "only the new cluster remains, live on 5432")
	require.Len(t, clusters, 1, "exactly one cluster after finalize: %+v", clusters)

	post, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.Nil(t, post.PendingFinalization, "no pending finalization after finalize")
	require.Equal(t, toMajor, post.Current.Major)
	requireClusterHealthy(t, env, toMajor)
}

// ---- Major upgrade 16 -> 17 then ROLLBACK before finalize ----

func testMajorUpgradeRollback(t *testing.T) {
	const (
		fromMajor = 16
		toMajor   = 17
		seedRows  = 333
	)
	env := installPanelOnMajor(t, fromMajor)
	configureBackupTarget(t, env)
	seedTable(t, env, seedRows)

	pf, err := env.Panel.MajorPreflight(toMajor, 8*time.Minute)
	require.NoError(t, err, "major preflight should complete")
	require.False(t, pf.HasFail(), "preflight must have no blocking checks: %+v", pf.Checks)

	_, err = env.Panel.MajorStart(toMajor, true)
	require.NoError(t, err, "major upgrade should start")
	op := env.Panel.AwaitUpgradeOp(t, "major", 20*time.Minute)
	require.Equal(t, "success", op.Status, "major upgrade should succeed; error=%s\nlog=%s", op.Error, strings.Join(op.Log, "\n"))

	// Now live on 17 with a pending finalization — roll back instead of finalizing.
	info, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.Equal(t, toMajor, info.Current.Major, "upgrade landed on the new major before rollback")
	require.NotNil(t, info.PendingFinalization)

	// Rollback: confirm with the LIVE (new) major whose post-upgrade writes are
	// discarded.
	_, err = env.Panel.RollbackUpgrade(toMajor)
	require.NoError(t, err, "rollback should start")
	rop := env.Panel.AwaitUpgradeOp(t, "rollback", 8*time.Minute)
	require.Equal(t, "success", rop.Status, "rollback should succeed; error=%s\nlog=%s", rop.Error, strings.Join(rop.Log, "\n"))

	// Ground truth: back on the OLD major, healthy, with data intact (the seed
	// predated the upgrade, so it lives on the restored old cluster).
	requireClusterHealthy(t, env, fromMajor)
	back, err := env.Panel.PGVersion()
	require.NoError(t, err)
	require.Equal(t, fromMajor, back.Current.Major, "version endpoint reports the OLD major after rollback")
	require.Nil(t, back.PendingFinalization, "pending finalization cleared by rollback")

	n, err := env.PG.CountRows("e2e_upgrade_seed")
	require.NoError(t, err)
	require.Equal(t, seedRows, n, "data intact on the rolled-back cluster")

	// pg_lsclusters: the old cluster is live again on 5432; the new cluster is left
	// in place (stopped) for inspection.
	clusters := lsClusters(t, env)
	require.True(t, hasOnlineCluster(clusters, fromMajor, "5432"), "old cluster restored live on 5432")
	require.True(t, hasCluster(clusters, toMajor), "new cluster left in place (stopped) after rollback")
}

// ---- shared helpers (scenario-owned) ----

// installPanelOnMajor brings up a bare base box and runs a REAL provision on the
// requested (older) major, then logs in and asserts the live cluster is on it.
func installPanelOnMajor(t *testing.T, major int) *harness.Env {
	t.Helper()
	env := harness.Up(t, harness.Options{Image: harness.ImageBase, SkipReadyWait: true})

	out, err := env.Install("--pg-version", strconv.Itoa(major))
	require.NoError(t, err, "indiepg install --pg-version %d should provision from the cached packages", major)
	password, err := harness.ParseAdminPassword(out)
	require.NoError(t, err, "install should print a one-time admin password")

	env.AwaitReady(120 * time.Second)
	require.NoError(t, env.Panel.Login(password), "login with the generated password")

	live, err := env.PG.ServerVersion()
	require.NoError(t, err)
	require.Equal(t, major, live, "source cluster must be on PostgreSQL %d", major)
	require.Equal(t, "active", env.SystemctlIsActive(fmt.Sprintf("postgresql@%d-main", major)),
		"the source cluster's instance unit must be active")
	return env
}

// configureBackupTarget points the panel at the e2e MinIO so the mandatory
// pre-upgrade pgBackRest backup can run; it asserts the repo initialized.
func configureBackupTarget(t *testing.T, env *harness.Env) {
	t.Helper()
	cfg, err := env.Panel.ConfigureS3(harness.MinIOS3Config())
	require.NoError(t, err, "configuring the S3 backup target should succeed")
	require.True(t, cfg.BackupConfigured, "pgBackRest must initialize (mandatory pre-upgrade backup depends on it): warning=%q detail=%q",
		cfg.BackupWarning, cfg.BackupDetail)
}

// seedTable creates a deterministic table of n rows in the default database so the
// scenario can prove the data survives a copy upgrade (or a rollback).
func seedTable(t *testing.T, env *harness.Env, n int) {
	t.Helper()
	require.NoError(t, env.PG.Exec("DROP TABLE IF EXISTS e2e_upgrade_seed"))
	require.NoError(t, env.PG.Exec(fmt.Sprintf(
		"CREATE TABLE e2e_upgrade_seed AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1,%d) g", n)))
	got, err := env.PG.CountRows("e2e_upgrade_seed")
	require.NoError(t, err)
	require.Equal(t, n, got, "seed table should have %d rows", n)
}

// requireClusterHealthy asserts the panel is ready, the expected major's instance
// unit is active, and the live cluster reports that major over the socket.
func requireClusterHealthy(t *testing.T, env *harness.Env, major int) {
	t.Helper()
	require.NoError(t, env.Panel.Readyz(), "/readyz must be ok")
	require.Equal(t, "active", env.SystemctlIsActive(fmt.Sprintf("postgresql@%d-main", major)),
		"postgresql@%d-main must be active", major)
	v, err := env.PG.ServerVersion()
	require.NoError(t, err)
	require.Equal(t, major, v, "live cluster must report major %d", major)
}

// clusterRow is one parsed `pg_lsclusters --no-header` line.
type clusterRow struct {
	Major  int
	Port   string
	Status string
}

// lsClusters reads ground truth from `pg_lsclusters --no-header` inside the panel
// container. Columns: Ver Cluster Port Status Owner Data-directory Log-file.
func lsClusters(t *testing.T, env *harness.Env) []clusterRow {
	t.Helper()
	out, err := env.Exec("pg_lsclusters", "--no-header")
	require.NoError(t, err, "pg_lsclusters should be available and succeed")
	var rows []clusterRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		major, perr := strconv.Atoi(fields[0])
		if perr != nil {
			continue
		}
		rows = append(rows, clusterRow{Major: major, Port: fields[2], Status: fields[3]})
	}
	return rows
}

func hasCluster(rows []clusterRow, major int) bool {
	for _, r := range rows {
		if r.Major == major {
			return true
		}
	}
	return false
}

func hasOnlineCluster(rows []clusterRow, major int, port string) bool {
	for _, r := range rows {
		if r.Major == major && r.Port == port && r.Status == "online" {
			return true
		}
	}
	return false
}
