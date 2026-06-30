package pg

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// This file is the reusable pre-flight framework: one component that runs a list
// of named checks and returns structured results. Each check is pass / warn /
// fail with a human message and an optional remediation hint. Any fail blocks
// the operation; a warn is shown but proceedable. The install flow and the
// major-upgrade flow each select the checks that apply to them.

// CheckStatus is the outcome of a single pre-flight check.
type CheckStatus string

const (
	// CheckPass means the check is satisfied.
	CheckPass CheckStatus = "pass"
	// CheckWarn means the check found something worth surfacing but the operation
	// may still proceed (with a confirm).
	CheckWarn CheckStatus = "warn"
	// CheckFail means the check is a hard blocker; the operation must not proceed.
	CheckFail CheckStatus = "fail"
)

// Check is a single named pre-flight result. Its JSON shape is the API contract
// in §7: id, title, status, message, and an optional remediation hint.
type Check struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Status      CheckStatus `json:"status"`
	Message     string      `json:"message"`
	Remediation string      `json:"remediation,omitempty"`
}

// CheckSet is an ordered list of checks.
type CheckSet []Check

// HasFail reports whether any check in the set is a hard blocker (status fail).
func (cs CheckSet) HasFail() bool {
	for _, c := range cs {
		if c.Status == CheckFail {
			return true
		}
	}
	return false
}

// pass/warn/fail are small constructors that keep the check-building code terse.
func pass(id, title, msg string) Check {
	return Check{ID: id, Title: title, Status: CheckPass, Message: msg}
}
func warn(id, title, msg, remedy string) Check {
	return Check{ID: id, Title: title, Status: CheckWarn, Message: msg, Remediation: remedy}
}
func fail(id, title, msg, remedy string) Check {
	return Check{ID: id, Title: title, Status: CheckFail, Message: msg, Remediation: remedy}
}

// diskMargin is the headroom multiplier required on the PGDATA filesystem for a
// --copy major upgrade: the new cluster is a full copy of the old, plus a 20%
// cushion. (The "100 GB data needs ~120 GB free" guard.)
const diskMargin = 1.2

// minInstallFreeBytes is the absurdly-low floor below which a fresh install is
// blocked outright (2 GiB). It is a floor, not a sizing estimate.
const minInstallFreeBytes int64 = 2 * 1024 * 1024 * 1024

// InstallPreflight runs the install-time checks for a fresh install of the given
// major: no existing cluster to clobber, port 5432 free, a PGDG-supported OS
// release, no broken/half-installed packages, and a sane minimum of free disk.
// It is read-only (it never installs or changes anything) and is intended for
// the web installer / first-run flow.
func (m *Manager) InstallPreflight(ctx context.Context, major int) (CheckSet, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: InstallPreflight requires a Runner")
	}
	if !IsSupported(major) {
		return nil, core.ValidationError("PostgreSQL %d is not a supported version", major)
	}
	checks := make(CheckSet, 0, 5)

	// 1. Existing cluster present — refuse to clobber data.
	clusters, dataEntries, err := m.existingInstallClusters(ctx)
	if err != nil {
		return nil, err
	}
	if len(clusters) > 0 || len(dataEntries) > 0 {
		names := make([]string, 0, len(clusters))
		for _, c := range clusters {
			names = append(names, fmt.Sprintf("%d/%s", c.Major, c.Name))
		}
		for _, name := range dataEntries {
			names = append(names, "/var/lib/postgresql/"+name)
		}
		checks = append(checks, fail("existing_cluster", "No existing PostgreSQL cluster",
			"found existing cluster(s): "+strings.Join(names, ", "),
			"this panel provisions a fresh cluster; remove the existing PostgreSQL install/data or use a clean host"))
	} else {
		checks = append(checks, pass("existing_cluster", "No existing PostgreSQL cluster", "no existing clusters detected"))
	}

	// 2. Port 5432 free.
	listening, err := m.portListening(ctx, "5432")
	if err != nil {
		return nil, err
	}
	if listening {
		checks = append(checks, fail("port_5432", "Port 5432 available",
			"a process is already listening on port 5432",
			"stop whatever is bound to 5432 (often an existing Postgres) before installing"))
	} else {
		checks = append(checks, pass("port_5432", "Port 5432 available", "port 5432 is free"))
	}

	// 3. OS release supported by PGDG.
	codename := detectOSCodename()
	if codename == "" {
		checks = append(checks, fail("os_supported", "OS supported by PGDG",
			"could not determine the OS release codename",
			"install on a supported Debian/Ubuntu release (see apt.postgresql.org)"))
	} else if !pgdgSupportedCodenames[codename] {
		checks = append(checks, fail("os_supported", "OS supported by PGDG",
			fmt.Sprintf("OS release %q is not on the PGDG-supported list", codename),
			"use a Debian/Ubuntu release published by apt.postgresql.org"))
	} else {
		checks = append(checks, pass("os_supported", "OS supported by PGDG",
			fmt.Sprintf("%q is supported by the PGDG apt repository", codename)))
	}

	// 4. No broken / half-installed packages (apt-get check returns non-zero when
	// the dependency tree is inconsistent).
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-get", Args: []string{"check"},
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
	}); err != nil {
		checks = append(checks, warn("packages_consistent", "Package state consistent",
			"apt reports broken or half-configured packages",
			"run `sudo apt-get -f install` to repair the package state, then retry"))
	} else {
		checks = append(checks, pass("packages_consistent", "Package state consistent", "no broken packages detected"))
	}

	// 5. Minimum free disk for install.
	if free, err := freeBytes("/var/lib"); err != nil {
		checks = append(checks, fail("install_disk", "Sufficient free disk",
			"could not determine free disk under /var/lib", err.Error()))
	} else {
		if free < minInstallFreeBytes {
			checks = append(checks, fail("install_disk", "Sufficient free disk",
				fmt.Sprintf("only %s free under /var/lib (need at least %s)", humanBytes(free), humanBytes(minInstallFreeBytes)),
				"free up disk on the system volume before installing"))
		} else {
			checks = append(checks, pass("install_disk", "Sufficient free disk",
				fmt.Sprintf("%s free under /var/lib", humanBytes(free))))
		}
	}

	return checks, nil
}

// existingInstallClusters works before postgresql-common is installed. A fresh
// host has no pg_lsclusters binary yet, so treating "command not found" as a
// preflight error would make every first install impossible. When the tool is
// unavailable, non-empty entries under the conventional data root still fail
// closed instead of assuming the host is clean.
func (m *Manager) existingInstallClusters(ctx context.Context) ([]pgCluster, []string, error) {
	if fileExists("/usr/bin/pg_lsclusters") {
		clusters, err := m.listClusters(ctx)
		return clusters, nil, err
	}
	entries, err := os.ReadDir("/var/lib/postgresql")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, core.InternalError("pg: reading /var/lib/postgresql during install preflight").Wrap(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return nil, names, nil
}

// MajorPreflight is the result of a major-upgrade pre-flight: the structured
// checks plus the preview data the wizard renders (extensions carried over and
// the disk required/free figures).
type MajorPreflight struct {
	Checks            CheckSet
	Extensions        []string
	DiskRequiredBytes int64
	DiskFreeBytes     int64
}

// MajorUpgradePreflight performs §5 Phase A: it installs the target-major
// packages (server + contrib, plus a postgresql-<new>-<ext> for every installed
// catalog extension) — installing is non-destructive — and then runs the
// major-upgrade checklist against the running old cluster: target binaries
// present, disk headroom, extension parity (the control files now resolve for
// the new major), and no blockers pg_upgrade refuses (prepared transactions,
// logical replication slots). It returns the checks plus the preview figures.
func (m *Manager) MajorUpgradePreflight(ctx context.Context, fromMajor, toMajor int) (MajorPreflight, error) {
	if m.runner == nil {
		return MajorPreflight{}, core.InternalError("pg: MajorUpgradePreflight requires a Runner")
	}

	out := MajorPreflight{Checks: make(CheckSet, 0, 6)}

	// Phase A install: refresh the index, then the new server + contrib.
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-get", Args: []string{"update"},
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
	}); err != nil {
		out.Checks = append(out.Checks, fail("target_binaries", "Target version installed",
			"apt-get update failed while preparing the target packages", err.Error()))
	}
	// contrib modules ship bundled inside postgresql-<major>; there is no
	// installable postgresql-<major>-contrib on Debian/PGDG (requesting it makes
	// apt-get abort), so the server package alone lays down server + contrib.
	corePkgs := []string{fmt.Sprintf("postgresql-%d", toMajor)}
	_, coreErr := m.runner.Run(ctx, exec.RunSpec{
		Name: "apt-get", Args: append([]string{"install", "-y"}, corePkgs...),
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
	})

	// Discover installed extensions across all databases for parity + the preview.
	exts, err := m.installedExtensions(ctx)
	if err != nil {
		return MajorPreflight{}, err
	}
	out.Extensions = exts

	// Best-effort install of each extension's versioned package for the new major.
	// Parity is judged below by the on-disk control file, not by this exit code,
	// so a single missing package surfaces as a precise, named blocker.
	for _, ext := range exts {
		if pkg, templated := requiredExtensionPackage(ext, toMajor); templated {
			_, _ = m.runner.Run(ctx, exec.RunSpec{
				Name: "apt-get", Args: []string{"install", "-y", pkg},
				Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Timeout: commandTimeout,
			})
		}
	}

	// Check: target binaries present.
	pgUpgradeBin := pgUpgradeBinDir(toMajor) + "/pg_upgrade"
	if fileExists(pgUpgradeBin) {
		out.Checks = append(out.Checks, pass("target_binaries", "Target version installed",
			fmt.Sprintf("PostgreSQL %d binaries are installed", toMajor)))
	} else {
		msg := fmt.Sprintf("PostgreSQL %d binaries were not found at %s", toMajor, pgUpgradeBin)
		if coreErr != nil {
			msg += " (package install failed: " + coreErr.Error() + ")"
		}
		out.Checks = append(out.Checks, fail("target_binaries", "Target version installed", msg,
			fmt.Sprintf("ensure %s installs from PGDG for this OS release", corePkgs[0])))
	}

	// Check: disk headroom on the PGDATA filesystem (copy + 20%).
	dataDir, ddErr := m.DataDirectory(ctx)
	if ddErr != nil {
		return MajorPreflight{}, ddErr
	}
	{
		size, sizeErr := m.DirSizeBytes(ctx, dataDir)
		if sizeErr != nil {
			return MajorPreflight{}, sizeErr
		}
		free, freeErr := freeBytes(dataDir)
		required := int64(float64(size) * diskMargin)
		out.DiskRequiredBytes = required
		out.DiskFreeBytes = free
		switch {
		case freeErr != nil:
			out.Checks = append(out.Checks, fail("disk", "Sufficient free disk",
				"could not read free space on the data directory filesystem", freeErr.Error()))
		case free >= required:
			out.Checks = append(out.Checks, pass("disk", "Sufficient free disk",
				fmt.Sprintf("%s free, need ~%s (data %s + 20%%)", humanBytes(free), humanBytes(required), humanBytes(size))))
		default:
			out.Checks = append(out.Checks, fail("disk", "Sufficient free disk",
				fmt.Sprintf("need ~%s (data %s + 20%%) but only %s is free", humanBytes(required), humanBytes(size), humanBytes(free)),
				"free disk on the data directory volume, or use --link mode (future) when on the same filesystem"))
		}
	}

	// pg_upgradecluster's own check catches missing target packages and package
	// parity that a control-file scan cannot see. Pin the requested target: when
	// -v is omitted, pg_upgradecluster silently chooses the newest installed
	// major, which may differ from the operator's selection.
	if res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "pg_upgradecluster",
		Args:    []string{"--check", "-v", strconv.Itoa(toMajor), strconv.Itoa(fromMajor), "main"},
		Timeout: commandTimeout,
	}); err != nil {
		out.Checks = append(out.Checks, fail("pg_upgrade_check", "pg_upgrade compatibility check",
			"pg_upgradecluster found an upgrade blocker", firstLine(res.Stderr)))
	} else {
		out.Checks = append(out.Checks, pass("pg_upgrade_check", "pg_upgrade compatibility check",
			"pg_upgradecluster found all required packages"))
	}

	// Check: extension parity — every installed extension must have a control file
	// for the new major.
	var missing []string
	for _, ext := range exts {
		if ext == "plpgsql" {
			continue // ships with the server for every major
		}
		control := fmt.Sprintf("/usr/share/postgresql/%d/extension/%s.control", toMajor, ext)
		if !fileExists(control) {
			pkg, _ := requiredExtensionPackage(ext, toMajor)
			if pkg == "" {
				pkg = fmt.Sprintf("a PostgreSQL %d build of %q", toMajor, ext)
			}
			missing = append(missing, fmt.Sprintf("%s (install %s)", ext, pkg))
		}
	}
	if len(missing) > 0 {
		out.Checks = append(out.Checks, fail("extensions", "Extension parity",
			"these extensions have no build for PostgreSQL "+strconv.Itoa(toMajor)+": "+strings.Join(missing, ", "),
			"install the named package(s) for the target major, then re-run preflight"))
	} else {
		out.Checks = append(out.Checks, pass("extensions", "Extension parity",
			"every installed extension has a build for the target major"))
	}

	// Check: no prepared transactions (pg_upgrade refuses them).
	if n, err := m.scalarCount(ctx, "SELECT count(*) FROM pg_prepared_xacts"); err != nil {
		out.Checks = append(out.Checks, fail("prepared_xacts", "No prepared transactions",
			"could not verify whether prepared transactions are open", err.Error()))
	} else if n > 0 {
		out.Checks = append(out.Checks, fail("prepared_xacts", "No prepared transactions",
			fmt.Sprintf("%d prepared transaction(s) are open; pg_upgrade cannot proceed", n),
			"COMMIT or ROLLBACK PREPARED every entry in pg_prepared_xacts, then retry"))
	} else {
		out.Checks = append(out.Checks, pass("prepared_xacts", "No prepared transactions", "none open"))
	}

	// Check: no logical replication slots (not carried by pg_upgrade).
	if n, err := m.scalarCount(ctx, "SELECT count(*) FROM pg_replication_slots WHERE slot_type = 'logical'"); err != nil {
		out.Checks = append(out.Checks, fail("replication_slots", "No logical replication slots",
			"could not verify whether logical replication slots exist", err.Error()))
	} else if n > 0 {
		out.Checks = append(out.Checks, fail("replication_slots", "No logical replication slots",
			fmt.Sprintf("%d logical replication slot(s) exist; pg_upgrade does not carry them", n),
			"drop the logical slots (pg_drop_replication_slot) before upgrading, then re-create after"))
	} else {
		out.Checks = append(out.Checks, pass("replication_slots", "No logical replication slots", "none present"))
	}

	return out, nil
}

// requiredExtensionPackage resolves the Debian/Ubuntu package that ships an
// extension's files for the target major. Catalog entries with a versioned
// template (postgresql-%d-…) return their concrete name and templated=true;
// contrib-family entries resolve to the bundled server package
// postgresql-<major> (contrib ships inside it — there is no installable
// postgresql-<major>-contrib) and templated=false, so the caller does not
// demand a non-existent package. An unknown extension returns ("", false).
func requiredExtensionPackage(extName string, major int) (pkg string, templated bool) {
	e, ok := LookupCatalog(extName)
	if !ok {
		return "", false
	}
	if strings.Contains(e.PackageTemplate, "%d") {
		return e.PackageName(major), true
	}
	return fmt.Sprintf("postgresql-%d", major), false
}

// scalarCount runs a SELECT count(*) style query via the peer-auth superuser psql
// path and parses the single integer result.
func (m *Manager) scalarCount(ctx context.Context, sql string) (int, error) {
	out, err := m.runPsql(ctx, defaultConnectDatabase, sql)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, core.InternalError("pg: parsing count from %q", out).Wrap(err)
	}
	return n, nil
}

// listDatabaseNames returns the names of every non-template, connectable
// database via the peer-auth superuser psql path (so it works without the pgx
// pools — e.g. mid-upgrade when they are being reconnected).
func (m *Manager) listDatabaseNames(ctx context.Context) ([]string, error) {
	out, err := m.runPsql(ctx, defaultConnectDatabase,
		"SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn = true ORDER BY datname")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// installedExtensions returns the sorted, de-duplicated set of extensions
// installed across every database (excluding the always-present plpgsql), used
// for extension-parity checks and the upgrade preview.
func (m *Manager) installedExtensions(ctx context.Context) ([]string, error) {
	dbs, err := m.listDatabaseNames(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, db := range dbs {
		out, err := m.runPsql(ctx, db, "SELECT extname FROM pg_extension WHERE extname <> 'plpgsql'")
		if err != nil {
			return nil, core.InternalError("pg: listing extensions in database %q", db).Wrap(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[line] = true
			}
		}
	}
	exts := make([]string, 0, len(seen))
	for e := range seen {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return exts, nil
}

// portListening reports whether anything is listening on the given TCP port via
// `ss`. Probe failures are returned so install preflight fails closed rather
// than declaring an unverified port safe.
func (m *Manager) portListening(ctx context.Context, port string) (bool, error) {
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "ss", Args: []string{"-H", "-ltn"}, Timeout: commandTimeout,
	})
	if err != nil {
		return false, core.ExecError("pg: checking whether port %s is in use", port).Wrap(err)
	}
	needle := ":" + port
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			if strings.HasSuffix(f, needle) {
				return true, nil
			}
		}
	}
	return false, nil
}

// DirSizeBytes returns the on-disk size of a directory via `du -sb`, run as the
// postgres OS user (the data directory is postgres-owned and mode 0700). It is
// used by the major-upgrade preflight (disk headroom) and by the worker to size
// the old cluster for the "reclaimable" figure after an upgrade.
func (m *Manager) DirSizeBytes(ctx context.Context, path string) (int64, error) {
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name: "du", AsUser: "postgres", Args: []string{"-sb", "--", path}, Timeout: commandTimeout,
	})
	if err != nil {
		return 0, core.ExecError("pg: sizing %s failed", path).Wrap(err)
	}
	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(fields) == 0 {
		return 0, core.InternalError("pg: empty du output for %s", path)
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, core.InternalError("pg: parsing du output %q", fields[0]).Wrap(err)
	}
	return n, nil
}

// freeBytes returns the available bytes on the filesystem backing path.
func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, core.InternalError("pg: statfs %s", path).Wrap(err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// fileExists reports whether a path exists (file or directory).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// humanBytes renders a byte count in friendly units for check messages.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
