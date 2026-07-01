//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/test/e2e/harness"
)

// TestExtensions is scenarios 7-9: per-database PostgreSQL extension management
// across all three install tiers, with the needs-RESTART tier as the centerpiece.
// It drives the panel's real catalog Add path (POST /api/extensions, server-side
// tier selection) and asserts ground truth in Postgres (pg_extension, the GUC,
// the live postmaster) and on the OS (the apt package, the systemd unit) — never
// just HTTP 200s.
//
// The three sub-tests share one preinstalled Env (one cluster boot) and run
// sequentially: each installs into its own fresh database, and the Tier 3 restart
// is cluster-wide but ordered last so it cannot perturb the earlier tiers.
//
// Tiers VERIFIED against internal/pg/extcatalog.go (not assumed):
//   - citext             -> contrib, no preload  => Tier 1 "ready" (files on disk)
//   - vector (pgvector)  -> postgresql-<major>-pgvector, no preload, not on disk
//     => Tier 2 "needs_package"
//   - pg_stat_statements -> contrib, RequiresPreload=true => Tier 3 "needs_restart"
func TestExtensions(t *testing.T) {
	t.Parallel()

	env := harness.Up(t, harness.Options{Image: harness.ImagePreinstalled})
	require.NoError(t, env.Panel.Login(harness.AdminPassword))

	major, err := env.PG.ServerVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, major, 15, "expected a supported PostgreSQL major")

	// ---- Tier 1: ready (on disk, no package, no restart) ----
	t.Run("TierReady", func(t *testing.T) {
		const db = "ext_ready"
		const ext = "citext"

		require.NoError(t, env.PG.Exec("CREATE DATABASE "+db))

		// Catalog/tier ground truth from the live API: citext is available to add,
		// classified "ready" (its contrib files are on disk), in-catalog, no preload.
		before, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		avail, ok := before.FindAvailable(ext)
		require.Truef(t, ok, "%s should be available to add in %s", ext, db)
		require.Equal(t, "ready", avail.Tier, "citext is a contrib (on-disk) extension")
		require.True(t, avail.InCatalog)
		require.False(t, avail.RequiresPreload)
		_, alreadyInstalled := before.FindInstalled(ext)
		require.False(t, alreadyInstalled, "%s must not be pre-installed in a fresh db", ext)

		// Postgres ground truth: not yet present.
		require.Equal(t, "0", scalarCount(t, env, db, ext))

		// Install via the catalog Add path (no confirm needed for a ready tier).
		res, err := env.Panel.InstallExtension(db, ext, "")
		require.NoError(t, err, "POST /api/extensions citext should succeed")
		require.True(t, res.OK)
		require.Equal(t, "ready", res.Tier(), "server must classify citext as the ready tier")

		// Ground truth: the extension now exists in the target database.
		require.Equal(t, "1", scalarCount(t, env, db, ext),
			"citext must exist in pg_extension after install")

		// And the API now lists it as installed (no longer available to add).
		after, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		inst, ok := after.FindInstalled(ext)
		require.True(t, ok, "citext must be listed as installed")
		require.NotEmpty(t, inst.InstalledVersion, "an installed extension reports its version")
		_, stillAvail := after.FindAvailable(ext)
		require.False(t, stillAvail, "an installed extension drops off the available list")
	})

	// ---- Tier 2: needs_package (apt install, no restart) ----
	t.Run("TierNeedsPackage", func(t *testing.T) {
		const db = "ext_pkg"
		const ext = "vector"
		pkg := fmt.Sprintf("postgresql-%d-pgvector", major)

		require.NoError(t, env.PG.Exec("CREATE DATABASE "+db))

		// Precondition: the OS package is NOT installed yet (dpkg has no "ii" line).
		require.False(t, dpkgInstalled(env, pkg),
			"%s must not be installed before the Tier 2 flow runs", pkg)

		// Determinism guard (not a weakening): warm the apt index and pre-download
		// the .deb (download-only => NOT installed; leaves no dpkg "ii" entry) so the
		// panel's INLINE apt-get install resolves from the local cache and finishes
		// within the harness HTTP client's timeout. The package is still installed
		// solely by the panel's Tier 2 flow, which the assertions below prove.
		_, err := env.Exec("bash", "-lc", "DEBIAN_FRONTEND=noninteractive apt-get update")
		require.NoError(t, err, "pre-warm apt-get update")
		_, err = env.Exec("bash", "-lc",
			"DEBIAN_FRONTEND=noninteractive apt-get install -y --download-only "+pkg)
		require.NoError(t, err, "pre-warm download-only of "+pkg)

		// Still not installed after a download-only fetch.
		require.False(t, dpkgInstalled(env, pkg),
			"download-only must not install %s; only the panel does", pkg)

		// Catalog/tier ground truth: vector is needs_package and the API previews the
		// exact resolved OS package for the major.
		before, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		avail, ok := before.FindAvailable(ext)
		require.Truef(t, ok, "%s should be available to add", ext)
		require.Equal(t, "needs_package", avail.Tier,
			"pgvector's files are not on disk, so it needs an apt package")
		require.Equal(t, pkg, avail.Package, "the Add dialog previews the resolved package")
		require.Equal(t, "0", scalarCount(t, env, db, ext))

		// Install: the panel apt-get installs the package, then CREATE EXTENSION.
		res, err := env.Panel.InstallExtension(db, ext, "")
		require.NoError(t, err, "POST /api/extensions vector should succeed")
		require.True(t, res.OK)
		require.Equal(t, "needs_package", res.Tier())

		// Ground truth #1: the OS package is now genuinely installed (dpkg "ii").
		require.True(t, dpkgInstalled(env, pkg),
			"the panel's Tier 2 flow must apt-install %s", pkg)

		// Ground truth #2: the extension was created in the target database.
		require.Equal(t, "1", scalarCount(t, env, db, ext),
			"vector must exist in pg_extension after the package install")

		after, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		_, ok = after.FindInstalled(ext)
		require.True(t, ok, "vector must be listed as installed")
	})

	// ---- Tier 3: needs_restart (shared_preload_libraries + restart-with-rollback) ----
	// The centerpiece: pg_stat_statements requires being listed in
	// shared_preload_libraries, which only takes effect after a Postgres restart.
	t.Run("TierNeedsRestart", func(t *testing.T) {
		const db = "ext_restart"
		const ext = "pg_stat_statements"

		require.NoError(t, env.PG.Exec("CREATE DATABASE "+db))

		// Precondition that makes the restart meaningful: the library is NOT preloaded
		// on a fresh preinstalled image (provision runs CREATE EXTENSION but never sets
		// the GUC), so this install must really edit the GUC and restart.
		initialSPL, err := env.PG.Scalar("SHOW shared_preload_libraries")
		require.NoError(t, err)
		require.NotContains(t, initialSPL, ext,
			"precondition: %s must not be preloaded before the Tier 3 install", ext)
		startEpoch := postmasterEpoch(t, env)

		// Catalog/tier ground truth: pg_stat_statements is needs_restart + requires
		// preload, straight from the curated catalog.
		before, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		avail, ok := before.FindAvailable(ext)
		require.Truef(t, ok, "%s should be available to add", ext)
		require.Equal(t, "needs_restart", avail.Tier)
		require.True(t, avail.RequiresPreload)
		require.Equal(t, "0", scalarCount(t, env, db, ext))

		// Safety gate: a Tier 3 install WITHOUT the typed-name confirmation must be
		// refused BEFORE any side effect (no GUC change, no restart).
		resp, err := env.Panel.InstallExtensionResp(db, ext, "")
		require.NoError(t, err, "the gate request itself must transport ok")
		require.Equal(t, http.StatusConflict, resp.Status,
			"a Tier 3 install without confirm must be refused as a safety conflict")
		var pe *harness.PanelError
		require.True(t, errors.As(resp.Err(), &pe), "gate error must be a typed PanelError")
		require.Equal(t, "safety", pe.Code, "the gate is the typed-name confirmation guard")

		// Fail-closed: the refused gate left the cluster untouched.
		splAfterGate, err := env.PG.Scalar("SHOW shared_preload_libraries")
		require.NoError(t, err)
		require.Equal(t, initialSPL, splAfterGate, "the gate must not change the GUC")
		require.Equal(t, startEpoch, postmasterEpoch(t, env),
			"the gate must not restart Postgres")

		// Real install WITH the typed-name confirmation (confirm == the extension name,
		// per core.RequireConfirmation in InstallExtension). This edits
		// shared_preload_libraries, snapshots auto.conf, and restarts with rollback.
		res, err := env.Panel.InstallExtension(db, ext, ext)
		require.NoError(t, err, "POST /api/extensions pg_stat_statements with confirm should succeed")
		require.True(t, res.OK)
		require.Equal(t, "needs_restart", res.Tier())
		joined := strings.Join(res.Statements, "\n")
		require.Contains(t, joined, "ALTER SYSTEM SET shared_preload_libraries",
			"the recorded steps must include the GUC change")
		require.Contains(t, joined, "systemctl restart postgresql",
			"the recorded steps must include the restart")

		// Ground truth #1: the GUC now carries the library.
		finalSPL, err := env.PG.Scalar("SHOW shared_preload_libraries")
		require.NoError(t, err)
		require.Contains(t, finalSPL, ext,
			"shared_preload_libraries must now contain %s", ext)

		// Ground truth #2: Postgres really restarted (a fresh postmaster) and the
		// restart-with-rollback left a healthy, active cluster.
		require.Greater(t, postmasterEpoch(t, env), startEpoch,
			"the postmaster must have restarted (start time advanced)")
		require.Equal(t, "active", env.SystemctlIsActive("postgresql"),
			"the postgresql unit must be active after the restart-with-rollback")

		// Ground truth #3: the extension exists in the target database...
		require.Equal(t, "1", scalarCount(t, env, db, ext),
			"pg_stat_statements must exist in pg_extension after install")

		// ...and it FUNCTIONS only because the library is now preloaded: querying the
		// view raises "must be loaded via shared_preload_libraries" when it is not
		// loaded, so a clean read proves the restart actually took effect.
		_, err = env.PG.ScalarDB(db, "SELECT count(*) FROM pg_stat_statements")
		require.NoError(t, err,
			"pg_stat_statements view must be queryable post-restart (library loaded)")

		// And the API reflects the install.
		after, err := env.Panel.ListExtensions(db)
		require.NoError(t, err)
		_, ok = after.FindInstalled(ext)
		require.True(t, ok, "pg_stat_statements must be listed as installed")
	})
}

// scalarCount returns the pg_extension count for ext in db as a string ("0"/"1").
func scalarCount(t *testing.T, env *harness.Env, db, ext string) string {
	t.Helper()
	out, err := env.PG.ScalarDB(db,
		"SELECT count(*) FROM pg_extension WHERE extname='"+ext+"'")
	require.NoError(t, err)
	return out
}

// postmasterEpoch reads pg_postmaster_start_time() as a Unix epoch — a stable,
// wall-clock-free marker of the current postmaster, so a restart is provable by a
// strictly larger value (no dependency on the test host's clock).
func postmasterEpoch(t *testing.T, env *harness.Env) int64 {
	t.Helper()
	out, err := env.PG.Scalar("SELECT extract(epoch from pg_postmaster_start_time())::bigint")
	require.NoError(t, err)
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	require.NoError(t, err)
	return n
}

// dpkgInstalled reports whether pkg is installed (a dpkg "ii" status line). A
// download-only fetch leaves no "ii" line, so this is true only after a real
// install/configure.
func dpkgInstalled(env *harness.Env, pkg string) bool {
	out, err := env.Exec("bash", "-lc",
		fmt.Sprintf("dpkg-query -W -f='${Status}' %s 2>/dev/null", pkg))
	if err != nil {
		return false
	}
	return strings.Contains(out, "install ok installed")
}
