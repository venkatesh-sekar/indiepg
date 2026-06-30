package pg

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/pg/admin"
)

// This file holds the version-upgrade engine primitives the server's async
// worker drives: reading the current version, the minor upgrade (apt
// --only-upgrade + restart), the major upgrade via Debian's pg_upgradecluster
// (which does pg_upgrade --copy underneath and leaves the old cluster stopped
// but intact — exactly the two-phase rollback model), and the finalize/rollback
// operations. Every shell-out goes through the exec.Runner, matching how
// manager.go runs apt today.
//
// The mandatory pre-upgrade pgBackRest backup is enforced one layer up, in the
// server's upgrade worker (which owns the backup.Manager): internal/pg must not
// import internal/backup, and the server already orchestrates cross-package
// long-running jobs this way (see the migration worker).

// pgUpgradeBinDir is where a major's server binaries live on Debian/PGDG.
func pgUpgradeBinDir(major int) string { return fmt.Sprintf("/usr/lib/postgresql/%d/bin", major) }

// A copy-mode upgrade and the post-upgrade analyze can legitimately take much
// longer than ordinary provisioning commands on a large cluster. The worker
// context remains the outer cancellation bound; these per-command limits stop
// the generic ten-minute command timeout from killing a healthy upgrade.
const majorUpgradeCommandTimeout = 24 * time.Hour

// clusterConfPath is the Debian cluster's postgresql.conf for a (major, "main").
func clusterConfPath(major int) string {
	return fmt.Sprintf("/etc/postgresql/%d/main/postgresql.conf", major)
}

// pgCluster is one row of `pg_lsclusters`: a Debian-managed cluster.
type pgCluster struct {
	Major   int
	Name    string
	Port    string
	Status  string
	Owner   string
	DataDir string
}

// listClusters parses `pg_lsclusters --no-header`. Columns are:
// Ver Cluster Port Status Owner Data-directory Log-file. A best-effort parse:
// malformed lines are skipped rather than failing the whole read.
func (m *Manager) listClusters(ctx context.Context) ([]pgCluster, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: listClusters requires a Runner")
	}
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "pg_lsclusters", Args: []string{"--no-header"}, Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: pg_lsclusters failed").Wrap(err)
	}
	var out []pgCluster
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		major := parseMajorToken(fields[0])
		if major == 0 {
			continue
		}
		out = append(out, pgCluster{
			Major:   major,
			Name:    fields[1],
			Port:    fields[2],
			Status:  fields[3],
			Owner:   fields[4],
			DataDir: fields[5],
		})
	}
	return out, nil
}

// parseMajorToken parses the integer major from a pg_lsclusters Ver token (e.g.
// "16" or, defensively, "16.4" → 16). Returns 0 when it is not a number.
func parseMajorToken(tok string) int {
	if i := strings.IndexByte(tok, '.'); i >= 0 {
		tok = tok[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(tok))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func mainClusterFor(clusters []pgCluster, major int) (pgCluster, bool) {
	for _, c := range clusters {
		if c.Major == major && c.Name == "main" {
			return c, true
		}
	}
	return pgCluster{}, false
}

// CurrentVersion reports whether Postgres is up and, when it is, the full
// server_version string and the numeric major. When Postgres is down it returns
// running=false with empty/zero version fields (a queryable answer, not an
// error).
func (m *Manager) CurrentVersion(ctx context.Context) (full string, major int, running bool, err error) {
	running, err = m.IsRunning(ctx)
	if err != nil {
		return "", 0, false, err
	}
	if !running {
		return "", 0, false, nil
	}
	full, err = m.ServerVersion(ctx)
	if err != nil {
		return "", 0, true, err
	}
	major, err = m.MajorVersion(ctx)
	if err != nil {
		return full, 0, true, err
	}
	return full, major, true, nil
}

// MinorUpdateAvailable reports whether a newer minor of the installed major is
// available from apt, comparing the package's Installed vs Candidate via
// `apt-cache policy` (no network — it reads the already-refreshed local index).
// Debian package versions are compared with dpkg's ordering rules rather than
// string inequality: the candidate can be a lower pinned version, and epochs /
// distro revisions are not semantic versions.
func (m *Manager) MinorUpdateAvailable(ctx context.Context, major int) (available bool, target string, err error) {
	if m.runner == nil {
		return false, "", core.InternalError("pg: MinorUpdateAvailable requires a Runner")
	}
	pkg := fmt.Sprintf("postgresql-%d", major)
	res, runErr := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-cache", Args: []string{"policy", pkg}, Timeout: commandTimeout,
	})
	if runErr != nil {
		return false, "", core.ExecError("pg: reading apt policy for %s", pkg).Wrap(runErr)
	}
	installed, candidate := parseAptPolicy(res.Stdout)
	if installed == "" || installed == "(none)" || candidate == "" || candidate == "(none)" {
		return false, "", nil
	}
	res, compareErr := m.runner.Run(ctx, exec.RunSpec{
		Name: "dpkg", Args: []string{"--compare-versions", installed, "lt", candidate}, Timeout: commandTimeout,
	})
	if compareErr != nil {
		// dpkg uses exit 1 for a valid false comparison. Any other failure means
		// the version endpoint could not determine update availability reliably.
		if res.ExitCode == 1 {
			return false, "", nil
		}
		return false, "", core.ExecError("pg: comparing installed and candidate versions for %s", pkg).Wrap(compareErr)
	}
	if res.ExitCode == 1 {
		return false, "", nil
	}
	return true, upstreamVersion(candidate), nil
}

// parseAptPolicy extracts the Installed and Candidate version strings from
// `apt-cache policy <pkg>` output.
func parseAptPolicy(out string) (installed, candidate string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "Installed:"); ok {
			installed = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "Candidate:"); ok {
			candidate = strings.TrimSpace(v)
		}
	}
	return installed, candidate
}

// upstreamVersion reduces a Debian package version ("16.4-1.pgdg120+2") to its
// upstream part ("16.4") for display.
func upstreamVersion(deb string) string {
	if i := strings.IndexByte(deb, ':'); i >= 0 {
		deb = deb[i+1:]
	}
	if i := strings.IndexByte(deb, '-'); i >= 0 {
		return deb[:i]
	}
	return deb
}

// MinorUpgrade performs a minor upgrade of the installed major: apt
// --only-upgrade of the versioned server + contrib, then a service restart (the
// only downtime). It re-reads the version on the way out so the caller can show
// "now on 16.4". A restart that does not come back is surfaced as a clear error.
func (m *Manager) MinorUpgrade(ctx context.Context) (core.Result, error) {
	if m.runner == nil {
		return core.Result{}, core.InternalError("pg: MinorUpgrade requires a Runner")
	}
	major, err := m.MajorVersion(ctx)
	if err != nil {
		return core.Result{}, err
	}
	steps := make([]string, 0, 3)

	// contrib modules ship bundled inside postgresql-<major>; there is no
	// installable postgresql-<major>-contrib on Debian/PGDG (requesting it makes
	// apt-get abort), so upgrading the server package upgrades contrib too.
	pkgs := []string{fmt.Sprintf("postgresql-%d", major)}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-get", Args: []string{"update"},
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
	}); err != nil {
		return core.Result{}, core.ExecError("pg: apt-get update failed").Wrap(err)
	}
	steps = append(steps, "apt-get update")

	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-get", Args: append([]string{"install", "--only-upgrade", "-y"}, pkgs...),
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
	})
	if err != nil {
		return core.Result{}, core.ExecError("pg: minor upgrade of postgresql-%d failed", major).Wrap(err)
	}
	steps = append(steps, aptStep(res, "apt-get install --only-upgrade -y "+strings.Join(pkgs, " ")))

	// Restart to activate the new minor binaries; verify it actually came back.
	if err := m.restartAndConfirm(ctx); err != nil {
		return core.Result{}, core.ExecError("pg: Postgres did not come back up after the minor upgrade restart").
			WithHint("check `systemctl status postgresql` and the cluster log").Wrap(err)
	}
	steps = append(steps, "systemctl restart "+serviceName)

	full, err := m.ServerVersion(ctx)
	if err != nil {
		return core.Result{}, core.InternalError("pg: minor upgrade completed but the running version could not be read").Wrap(err)
	}
	return core.Ok("minor upgrade complete").
		WithData("version", full).
		WithStatements(steps...), nil
}

// UpgradeClusterResult carries the facts the worker needs after a major upgrade:
// where the old cluster ended up (its moved/parked port and unchanged data dir,
// for finalize/rollback) and the recorded steps.
type UpgradeClusterResult struct {
	OldPort    string
	OldDataDir string
	Steps      []string
}

// UpgradeCluster runs Debian's pg_upgradecluster --method=upgrade <fromMajor>
// main: it creates the new cluster, migrates postgresql.conf/pg_hba.conf, swaps
// ports so the new cluster lands live on the original port, and leaves the old
// cluster stopped but intact. It then reads back where the old cluster was
// parked. It does NOT itself take the mandatory backup — the caller does, before
// calling this — and on any failure the old cluster is preserved by design.
func (m *Manager) UpgradeCluster(ctx context.Context, fromMajor, toMajor int) (UpgradeClusterResult, error) {
	if m.runner == nil {
		return UpgradeClusterResult{}, core.InternalError("pg: UpgradeCluster requires a Runner")
	}

	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "pg_upgradecluster",
		Args:    []string{"--method=upgrade", "-v", strconv.Itoa(toMajor), strconv.Itoa(fromMajor), "main"},
		Timeout: majorUpgradeCommandTimeout,
	})
	if err != nil {
		return UpgradeClusterResult{}, core.ExecError("pg: pg_upgradecluster %d main failed", fromMajor).
			WithDetail("stderr", firstLine(res.Stderr)).
			WithHint("the old cluster is preserved and untouched; review the error and retry").Wrap(err)
	}

	out := UpgradeClusterResult{Steps: []string{aptStep(res, fmt.Sprintf("pg_upgradecluster --method=upgrade -v %d %d main", toMajor, fromMajor))}}

	// Read back where the old cluster was parked (its data dir is unchanged; its
	// port was moved off the live port by pg_upgradecluster).
	clusters, lerr := m.listClusters(ctx)
	if lerr != nil {
		return out, core.InternalError("pg: upgrade completed but old-cluster metadata could not be read").Wrap(lerr)
	}
	old, ok := mainClusterFor(clusters, fromMajor)
	if !ok || old.Port == "" || old.DataDir == "" {
		return out, core.InternalError("pg: upgrade completed but PostgreSQL %d/main was not found for rollback", fromMajor)
	}
	out.OldPort = old.Port
	out.OldDataDir = old.DataDir
	return out, nil
}

// ReapplyManagedConfig re-asserts the panel-managed configuration after the
// config migration that pg_upgradecluster performed: the pg_hba socket-auth rule
// for the panel roles, WAL archiving for pgBackRest, and the host-sized tuning.
// Each step is idempotent (a no-op when already in place). A tuning value the
// new postmaster rejects is auto-rolled-back (CodeSafety) and downgraded to a
// warning here rather than failing the whole upgrade.
func (m *Manager) ReapplyManagedConfig(ctx context.Context, stanza string) ([]string, error) {
	steps := make([]string, 0, 3)

	if changed, err := m.EnsureSocketAuth(ctx); err != nil {
		return steps, err
	} else if changed {
		steps = append(steps, "re-applied pg_hba.conf socket auth for panel roles")
	} else {
		steps = append(steps, "pg_hba.conf socket auth already present")
	}

	if changed, err := m.EnsureArchiving(ctx, stanza); err != nil {
		// EnsureArchiving returns CodeSafety when an auto-rollback kept Postgres up;
		// keep the upgrade moving and surface it as a step note rather than aborting.
		if core.CodeOf(err) == core.CodeSafety {
			m.log.Warn("WAL archiving re-apply was rolled back after upgrade", "error", err.Error())
			steps = append(steps, "WAL archiving re-apply rolled back (Postgres running on prior config)")
		} else {
			return steps, err
		}
	} else if changed {
		steps = append(steps, "re-applied WAL archiving config")
	} else {
		steps = append(steps, "WAL archiving config already in place")
	}

	tuning, _ := m.hostTuning(ProfileMixed)
	if changed, err := m.ApplyTuning(ctx, tuning); err != nil {
		if core.CodeOf(err) == core.CodeSafety {
			m.log.Warn("host-sized tuning re-apply was rolled back after upgrade", "error", err.Error())
			steps = append(steps, "host-sized tuning re-apply rolled back (Postgres running on prior config)")
		} else {
			return steps, err
		}
	} else if changed {
		steps = append(steps, "re-applied host-sized tuning")
	} else {
		steps = append(steps, "host-sized tuning already in place")
	}

	return steps, nil
}

// VacuumAnalyzeAll runs `vacuumdb --all --analyze-in-stages` as the postgres OS
// user. pg_upgrade does NOT carry planner statistics, so without this the
// freshly-upgraded database feels slow until autovacuum catches up; the staged
// analyze gets usable stats quickly.
func (m *Manager) VacuumAnalyzeAll(ctx context.Context) (string, error) {
	if m.runner == nil {
		return "", core.InternalError("pg: VacuumAnalyzeAll requires a Runner")
	}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "vacuumdb", AsUser: "postgres",
		Args: []string{"--all", "--analyze-in-stages"}, Timeout: majorUpgradeCommandTimeout,
	}); err != nil {
		return "", core.ExecError("pg: vacuumdb --all --analyze-in-stages failed").Wrap(err)
	}
	return "vacuumdb --all --analyze-in-stages", nil
}

// UpdateAllExtensions runs ALTER EXTENSION … UPDATE for every installed
// extension in every database, moving each to the default version shipped with
// the new major. A per-extension failure (e.g. an extension with no update path)
// is logged and recorded but does not abort the upgrade — the database is
// already live on the new major.
func (m *Manager) UpdateAllExtensions(ctx context.Context) ([]string, error) {
	dbs, err := m.listDatabaseNames(ctx)
	if err != nil {
		return nil, err
	}
	var steps []string
	for _, db := range dbs {
		out, err := m.runPsql(ctx, db, "SELECT extname FROM pg_extension WHERE extname <> 'plpgsql'")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			ext := strings.TrimSpace(line)
			if ext == "" {
				continue
			}
			stmt, berr := admin.AlterExtensionUpdate(ext)
			if berr != nil {
				continue
			}
			if _, perr := m.runPsql(ctx, db, stmt); perr != nil {
				m.log.Warn("ALTER EXTENSION UPDATE failed", "database", db, "extension", ext, "error", perr.Error())
				steps = append(steps, fmt.Sprintf("%s on %q: failed (%s)", stmt, db, firstLine(perr.Error())))
				continue
			}
			steps = append(steps, fmt.Sprintf("%s on %q", stmt, db))
		}
	}
	return steps, nil
}

// SmokeTest is the post-upgrade sanity check: Postgres is up, accepting
// connections, and reporting the expected new major.
func (m *Manager) SmokeTest(ctx context.Context, expectMajor int) error {
	running, _ := m.IsRunning(ctx)
	if !running {
		return core.ExecError("pg: post-upgrade smoke test failed — Postgres is not accepting connections").
			WithHint("the upgrade left the new cluster down; consider rolling back")
	}
	major, err := m.MajorVersion(ctx)
	if err != nil {
		return err
	}
	if major != expectMajor {
		return core.InternalError("pg: post-upgrade smoke test failed — running major is %d, expected %d", major, expectMajor)
	}
	return nil
}

// FinalizeUpgrade is the point of no return: it drops the old cluster
// (pg_dropcluster --stop <oldMajor> main), reclaiming its disk. After this the
// rollback path is gone.
func (m *Manager) FinalizeUpgrade(ctx context.Context, oldMajor int) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: FinalizeUpgrade requires a Runner")
	}
	clusters, err := m.listClusters(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := mainClusterFor(clusters, oldMajor); !ok {
		return []string{fmt.Sprintf("PostgreSQL %d/main already absent", oldMajor)}, nil
	}
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "pg_dropcluster", Args: []string{"--stop", strconv.Itoa(oldMajor), "main"}, Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: pg_dropcluster %d main failed", oldMajor).
			WithDetail("stderr", firstLine(res.Stderr)).Wrap(err)
	}
	return []string{aptStep(res, fmt.Sprintf("pg_dropcluster --stop %d main", oldMajor))}, nil
}

// RollbackUpgrade returns the box to the old major: stop the new cluster, swap
// the old cluster back onto the live port (5432) — parking the new cluster on
// the old cluster's moved port — and start the old cluster. The new cluster is
// left in place (stopped) for inspection. The caller's confirm dialog must warn
// that writes made against the new major during the verification window are
// discarded by this.
func (m *Manager) RollbackUpgrade(ctx context.Context, fromMajor, toMajor int, oldPort string) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: RollbackUpgrade requires a Runner")
	}
	if oldPort == "" {
		return nil, core.InternalError("pg: rollback metadata does not contain the old cluster port")
	}
	steps := make([]string, 0, 4)

	clusters, err := m.listClusters(ctx)
	if err != nil {
		return nil, err
	}
	old, oldOK := mainClusterFor(clusters, fromMajor)
	newCluster, newOK := mainClusterFor(clusters, toMajor)
	if !oldOK || !newOK {
		return nil, core.ConflictError("rollback requires both PostgreSQL %d/main and %d/main clusters", fromMajor, toMajor)
	}
	// Idempotent retry after the port swap and start completed but clearing the
	// durable pending state failed.
	if old.Port == "5432" && old.Status == "online" && newCluster.Port == oldPort && newCluster.Status != "online" {
		return []string{fmt.Sprintf("PostgreSQL %d/main is already restored on port 5432", fromMajor)}, nil
	}
	if old.Port != oldPort || old.Status == "online" || newCluster.Port != "5432" || newCluster.Status != "online" {
		return nil, core.ConflictError(
			"cluster state changed outside indiepg; refusing rollback (old %d/main: port %s status %s; new %d/main: port %s status %s)",
			fromMajor, old.Port, old.Status, toMajor, newCluster.Port, newCluster.Status).
			WithHint("restore the expected two-cluster state before retrying; no files were changed")
	}

	oldConfig, err := snapshotClusterConfig(fromMajor)
	if err != nil {
		return nil, err
	}
	newConfig, err := snapshotClusterConfig(toMajor)
	if err != nil {
		return nil, err
	}
	recoverNew := func(cause error) error {
		// The old start may have succeeded even if its caller observed a timeout.
		// Stop it before restoring the new cluster to the live port.
		_, _ = m.runner.Run(context.WithoutCancel(ctx), exec.RunSpec{
			Name: "pg_ctlcluster", Args: []string{strconv.Itoa(fromMajor), "main", "stop", "--force"}, Timeout: commandTimeout,
		})
		restoreErr := restoreClusterConfig(oldConfig)
		if err := restoreClusterConfig(newConfig); restoreErr == nil {
			restoreErr = err
		}
		_, startErr := m.runner.Run(context.WithoutCancel(ctx), exec.RunSpec{
			Name: "pg_ctlcluster", Args: []string{strconv.Itoa(toMajor), "main", "start"}, Timeout: commandTimeout,
		})
		if startErr != nil {
			if current, listErr := m.listClusters(context.WithoutCancel(ctx)); listErr == nil {
				if c, ok := mainClusterFor(current, toMajor); ok && c.Port == "5432" && c.Status == "online" {
					startErr = nil
				}
			}
		}
		if restoreErr != nil || startErr != nil {
			return core.InternalError("pg: rollback failed and restoring the new cluster also failed; manual recovery is required").
				WithDetail("rollback_error", cause.Error()).
				WithDetail("config_restore_error", errorString(restoreErr)).
				WithDetail("new_cluster_start_error", errorString(startErr))
		}
		return cause
	}

	// 1. Stop the new cluster so it releases the live port.
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "pg_ctlcluster", Args: []string{strconv.Itoa(toMajor), "main", "stop", "--force"}, Timeout: commandTimeout,
	}); err != nil {
		return steps, recoverNew(core.ExecError("pg: stopping the new cluster (%d) for rollback failed", toMajor).Wrap(err))
	}
	steps = append(steps, fmt.Sprintf("pg_ctlcluster %d main stop", toMajor))

	// 2. Park the new cluster on the old cluster's moved port and move the old
	// cluster back onto the live port.
	if err := m.setClusterPort(ctx, toMajor, oldPort); err != nil {
		return steps, recoverNew(err)
	}
	steps = append(steps, fmt.Sprintf("set cluster %d main port = %s", toMajor, oldPort))
	if err := m.setClusterPort(ctx, fromMajor, "5432"); err != nil {
		return steps, recoverNew(err)
	}
	steps = append(steps, fmt.Sprintf("set cluster %d main port = 5432", fromMajor))

	// 3. Start the old cluster on the live port.
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "pg_ctlcluster", Args: []string{strconv.Itoa(fromMajor), "main", "start"}, Timeout: commandTimeout,
	}); err != nil {
		return steps, recoverNew(core.ExecError("pg: starting the old cluster (%d) after rollback failed", fromMajor).Wrap(err))
	}
	steps = append(steps, fmt.Sprintf("pg_ctlcluster %d main start", fromMajor))

	return steps, nil
}

type clusterConfigSnapshot struct {
	path string
	data []byte
	info os.FileInfo
}

func snapshotClusterConfig(major int) (clusterConfigSnapshot, error) {
	path := clusterConfPath(major)
	info, err := os.Stat(path)
	if err != nil {
		return clusterConfigSnapshot{}, core.InternalError("pg: stat %s", path).Wrap(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return clusterConfigSnapshot{}, core.InternalError("pg: read %s", path).Wrap(err)
	}
	return clusterConfigSnapshot{path: path, data: data, info: info}, nil
}

func restoreClusterConfig(s clusterConfigSnapshot) error {
	return writePreserving(s.path, s.data, s.info)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// portLineRe matches the `port = NNNN` directive in a cluster's
// postgresql.conf (with optional surrounding whitespace and a trailing comment).
var portLineRe = regexp.MustCompile(`(?m)^\s*port\s*=\s*\S+`)

// setClusterPort rewrites the `port = …` directive in a Debian cluster's
// postgresql.conf, preserving the file's owner and mode (it is postgres-owned,
// mode 0644). It appends the directive when the file has none. Used only by
// rollback, on a real box; the data dir / conf path is the standard Debian
// layout.
func (m *Manager) setClusterPort(ctx context.Context, major int, port string) error {
	// Defence in depth: `port` originates from pg_lsclusters output. Refuse to
	// write a non-numeric value into postgresql.conf — a corrupt port directive
	// would break the old cluster exactly when a rollback needs it to start.
	if _, err := strconv.Atoi(strings.TrimSpace(port)); err != nil {
		return core.InternalError("pg: refusing to write non-numeric port %q to the cluster config", port)
	}
	path := clusterConfPath(major)
	info, err := os.Stat(path)
	if err != nil {
		return core.InternalError("pg: stat %s", path).Wrap(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return core.InternalError("pg: read %s", path).Wrap(err)
	}
	replacement := "port = " + port
	var updated string
	if portLineRe.Match(data) {
		updated = portLineRe.ReplaceAllLiteralString(string(data), replacement)
	} else {
		updated = strings.TrimRight(string(data), "\n") + "\n" + replacement + "\n"
	}
	if err := writePreserving(path, []byte(updated), info); err != nil {
		return err
	}
	return nil
}
