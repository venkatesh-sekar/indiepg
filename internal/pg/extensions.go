package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/pg/admin"
)

// This file orchestrates per-database PostgreSQL extension management on top of
// the existing machinery: the read-only pool (acquireRead) for listing, the apt
// install pattern (mirroring Provision), the peer-auth superuser psql path
// (runPsql) for the extension DDL and ALTER SYSTEM, and the self-healing restart
// (snapshotAutoConf → restartWithRollback). Extensions are installed into ONE
// database, so every operation targets a caller-chosen database rather than
// always the maintenance database.

// ExtensionTier classifies how much work installing an available extension
// takes, so the UI can badge each candidate and warn before a restart.
type ExtensionTier string

const (
	// TierReady means the extension's files are on disk: a plain CREATE
	// EXTENSION is all that is needed.
	TierReady ExtensionTier = "ready"
	// TierNeedsPackage means a curated extension whose files are not yet on
	// disk: the panel will apt-get install its OS package, then CREATE.
	TierNeedsPackage ExtensionTier = "needs_package"
	// TierNeedsRestart means the extension must be listed in
	// shared_preload_libraries, which requires a Postgres restart before
	// CREATE EXTENSION will succeed.
	TierNeedsRestart ExtensionTier = "needs_restart"
)

// InstalledExtension describes an extension already present in a database, with
// the version installed and the default version available on disk so the UI can
// nudge an ALTER EXTENSION ... UPDATE when they differ.
type InstalledExtension struct {
	Name             string
	InstalledVersion string
	DefaultVersion   string
	UpdateAvailable  bool
}

// AvailableExtension describes an extension a database does not yet have but
// could add, tagged with the tier/badge that says how much work it takes.
type AvailableExtension struct {
	Name            string
	Description     string
	DefaultVersion  string
	Tier            ExtensionTier
	RequiresPreload bool
	InCatalog       bool
	// Package is the resolved Debian/Ubuntu package that ships this extension's
	// files, with the cluster's PG major version already filled in (e.g.
	// "postgresql-17-pgvector"). It is set only for catalog entries that may need
	// an apt install (needs_package / needs_restart) so the Add dialog can preview
	// the real command rather than a placeholder; empty otherwise.
	Package string
}

// ExtensionList is the full per-database extension picture: what is installed
// and what is available to add.
type ExtensionList struct {
	Database  string
	Installed []InstalledExtension
	Available []AvailableExtension
}

// listAvailableExtensionsSQL reads every extension whose control file is on
// disk in the connected database, with its default and (when present) installed
// version and the catalog comment. pg_available_extensions only lists
// extensions whose files are actually present, so absence here means the OS
// package has not been installed yet.
const listAvailableExtensionsSQL = `
SELECT name,
       COALESCE(default_version, '')   AS default_version,
       COALESCE(installed_version, '') AS installed_version,
       COALESCE(comment, '')           AS comment
FROM pg_available_extensions
ORDER BY name`

// ListExtensions returns the installed and available extensions for a database,
// reading over the read-only path (acquireRead) against the TARGET database —
// extensions are per-database, so the maintenance database is not necessarily
// the one the operator cares about. The available list merges the curated
// catalog with any other on-disk extension, each tagged with its tier.
func (m *Manager) ListExtensions(ctx context.Context, database string) (*ExtensionList, error) {
	if database == "" {
		database = defaultConnectDatabase
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}

	conn, release, err := m.acquireRead(ctx, database)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := conn.Query(ctx, listAvailableExtensionsSQL)
	if err != nil {
		return nil, core.InternalError("pg: listing extensions in %s", database).Wrap(err)
	}
	defer rows.Close()

	// diskRow is what pg_available_extensions told us about an on-disk extension.
	type diskRow struct {
		defaultVersion   string
		installedVersion string
		comment          string
	}
	onDisk := make(map[string]diskRow)
	var installed []InstalledExtension
	for rows.Next() {
		var name, def, inst, comment string
		if err := rows.Scan(&name, &def, &inst, &comment); err != nil {
			return nil, core.InternalError("pg: scanning extension row").Wrap(err)
		}
		onDisk[name] = diskRow{defaultVersion: def, installedVersion: inst, comment: comment}
		if inst != "" {
			installed = append(installed, InstalledExtension{
				Name:             name,
				InstalledVersion: inst,
				DefaultVersion:   def,
				UpdateAvailable:  def != "" && def != inst,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading extensions").Wrap(err)
	}

	available := make([]AvailableExtension, 0, len(Catalog)+len(onDisk))
	catalogNames := make(map[string]bool, len(Catalog))

	// Resolve the PG major version once so catalog rows can carry their real
	// package name (postgresql-17-pgvector) for the Add dialog's command preview.
	// Best-effort: a read failure just leaves the templated package name unset
	// rather than failing the whole list — the authoritative command is still
	// recorded in the install Result afterward.
	major, _ := m.MajorVersion(ctx)

	// 1. Curated catalog first, in catalog order, skipping anything already
	// installed. The tier comes from catalog metadata plus on-disk presence:
	// a preload extension is always needs_restart (even when its files ship with
	// contrib and are on disk), otherwise on-disk is ready and absent is
	// needs_package.
	for _, e := range Catalog {
		catalogNames[e.Name] = true
		d, present := onDisk[e.Name]
		if present && d.installedVersion != "" {
			continue
		}
		tier := TierReady
		switch {
		case e.RequiresPreload:
			tier = TierNeedsRestart
		case !present:
			tier = TierNeedsPackage
		}
		available = append(available, AvailableExtension{
			Name:            e.Name,
			Description:     e.Description,
			DefaultVersion:  d.defaultVersion,
			Tier:            tier,
			RequiresPreload: e.RequiresPreload,
			InCatalog:       true,
			Package:         catalogPackageName(e, major),
		})
	}

	// 2. Any other on-disk extension not in the catalog and not yet installed.
	// These are always ready (their files are present) and carry Postgres's own
	// comment as the description.
	others := make([]AvailableExtension, 0)
	for name, d := range onDisk {
		if catalogNames[name] || d.installedVersion != "" {
			continue
		}
		others = append(others, AvailableExtension{
			Name:           name,
			Description:    d.comment,
			DefaultVersion: d.defaultVersion,
			Tier:           TierReady,
			InCatalog:      false,
		})
	}
	sort.Slice(others, func(i, j int) bool { return others[i].Name < others[j].Name })
	available = append(available, others...)

	return &ExtensionList{
		Database:  database,
		Installed: installed,
		Available: available,
	}, nil
}

// InstallExtension installs an extension into a database, choosing the tier from
// catalog metadata plus on-disk presence and returning a core.Result recording
// every command/statement that ran so the UI can show exactly what happened:
//
//   - Tier 1 (ready / free-form): CREATE EXTENSION via the superuser psql path.
//   - Tier 2 (needs_package): apt-get update + install the catalog package, then
//     CREATE.
//   - Tier 3 (needs_restart): install the package if needed, read-modify-write
//     shared_preload_libraries, ALTER SYSTEM + restartWithRollback, then CREATE.
//     Gated by a typed-name confirmation because it restarts Postgres.
//
// freeform marks a call that originated from the "add by name" field rather than
// a catalog Add button. A free-form install is SQL-only: it never triggers an
// apt install or a shared_preload_libraries change off a typed name. The files
// must already be on disk; otherwise it is refused with an actionable message.
// Catalog Add buttons pass freeform=false and get the full tiered orchestration.
func (m *Manager) InstallExtension(ctx context.Context, database, name, confirm string, freeform bool) (core.Result, error) {
	if database == "" {
		database = defaultConnectDatabase
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return core.Result{}, err
	}
	// Validate the name up front, before any side effect: a Tier 2/3 install
	// would otherwise apt-install and restart only to fail at CREATE EXTENSION.
	if err := core.ValidateExtensionName(name); err != nil {
		return core.Result{}, err
	}

	// Every tier ends in CREATE EXTENSION run as the postgres OS superuser via psql
	// (the pool roles are NOSUPERUSER and cannot create untrusted extensions or
	// ones owned by postgres), so a Runner is required throughout — not just for
	// the apt/preload tiers.
	if m.runner == nil {
		return core.Result{}, core.InternalError("pg: installing an extension requires a Runner")
	}

	// pg_cron can only be CREATEd in the database named by cron.database_name
	// (effective default "postgres"). Creating it elsewhere fails only AFTER the
	// package install + restart have happened, so the operator would pay the full
	// cluster-wide downtime for a CREATE that cannot succeed. Pre-flight it here,
	// before any side effect, honouring an operator-set cron.database_name.
	if name == "pg_cron" {
		cronDB, err := m.cronDatabase(ctx)
		if err != nil {
			return core.Result{}, err
		}
		if database != cronDB {
			return core.Result{}, core.ValidationError(
				"pg_cron can only be created in the %q database (its cron.database_name)", cronDB).
				WithHint("select the " + cronDB + " database, then add pg_cron — or set cron.database_name to this database first")
		}
	}

	entry, inCatalog := LookupCatalog(name)
	onDisk, err := m.extensionOnDisk(ctx, database, name)
	if err != nil {
		return core.Result{}, err
	}

	// Free-form ("add by name") is SQL-only: the files must already be on disk.
	// We never apt-install or edit shared_preload_libraries off a typed name —
	// that orchestration is reserved for the curated catalog buttons.
	if freeform && !onDisk {
		return core.Result{}, core.NotFoundError("extension %q is not installed on disk", name).
			WithHint("install its OS package and retry, or add it from the catalog for a one-click install")
	}
	// A non-catalog name with no files on disk has no package we could install.
	if !inCatalog && !onDisk {
		return core.Result{}, core.NotFoundError("extension %q is not installed on disk", name).
			WithHint("install its OS package and retry, or add it to the catalog for one-click install")
	}

	// Tier selection. A free-form install is always plain Tier 1 (it reached here
	// only because its files are on disk); the apt/preload tiers belong to the
	// catalog buttons.
	tier := TierReady
	if !freeform {
		switch {
		case inCatalog && entry.RequiresPreload:
			tier = TierNeedsRestart
		case inCatalog && !onDisk:
			tier = TierNeedsPackage
		}
	}

	// Tier 3 restarts Postgres, so it requires the strong typed-name confirmation.
	if tier == TierNeedsRestart {
		if serr := core.RequireConfirmation("install extension "+name, name, confirm); serr != nil {
			return core.Result{}, serr
		}
	}

	steps := make([]string, 0, 6)

	// Install the OS package for Tier 2, and for Tier 3 when the files are not
	// already present (a contrib-shipped preload extension is already on disk).
	if tier == TierNeedsPackage || (tier == TierNeedsRestart && !onDisk) {
		pkgSteps, err := m.installExtensionPackage(ctx, entry)
		if err != nil {
			return core.Result{}, err
		}
		steps = append(steps, pkgSteps...)
	}

	// Tier 3: ensure the library is in shared_preload_libraries and restart.
	if tier == TierNeedsRestart {
		preloadSteps, err := m.ensurePreloadLibrary(ctx, entry)
		if err != nil {
			return core.Result{}, err
		}
		steps = append(steps, preloadSteps...)
	}

	// All tiers: CREATE EXTENSION inside the target database as the postgres OS
	// superuser via psql — the same peer-auth path provisioning and ALTER SYSTEM
	// use. The privileged pool role is NOSUPERUSER and lacks CREATE on the
	// database, so it cannot create untrusted extensions (e.g. PostGIS) or ones
	// owned by postgres; the superuser can. IF NOT EXISTS makes re-adding an
	// already-present extension a no-op.
	createStmt, err := admin.CreateExtension(name)
	if err != nil {
		return core.Result{}, err
	}
	if _, err := m.runPsql(ctx, database, createStmt); err != nil {
		return core.Result{}, core.ExecError("pg: creating extension %q in %q failed", name, database).Wrap(err)
	}
	steps = append(steps, createStmt)

	return core.Ok(fmt.Sprintf("extension %q installed on %q", name, database)).
		WithData("tier", string(tier)).
		WithData("database", database).
		WithStatements(steps...), nil
}

// DropExtension drops an extension from a database after a typed-name
// confirmation, run as the postgres OS superuser via psql (peer auth) against the
// target database — the same path CREATE EXTENSION uses, since the pool roles are
// NOSUPERUSER and cannot drop extensions owned by postgres. No CASCADE is emitted
// (admin.DropExtension): a dependency error from Postgres is surfaced for the
// operator to resolve explicitly.
func (m *Manager) DropExtension(ctx context.Context, database, name, confirm string) (core.Result, error) {
	if database == "" {
		database = defaultConnectDatabase
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return core.Result{}, err
	}
	stmt, err := admin.DropExtension(name, confirm)
	if err != nil {
		return core.Result{}, err
	}
	if m.runner == nil {
		return core.Result{}, core.InternalError("pg: dropping an extension requires a Runner")
	}
	if _, err := m.runPsql(ctx, database, stmt); err != nil {
		return core.Result{}, core.ExecError("pg: dropping extension %q from %q failed", name, database).Wrap(err)
	}
	return core.Ok(fmt.Sprintf("extension %q dropped from %q", name, database)).
		WithData("database", database).
		WithStatements(stmt), nil
}

// UpdateExtension upgrades an installed extension to its default available
// version with ALTER EXTENSION ... UPDATE, run as the postgres OS superuser via
// psql against the target database. It returns a core.Result recording the
// statement that ran. No confirmation is required: an update is non-destructive
// and never restarts Postgres.
func (m *Manager) UpdateExtension(ctx context.Context, database, name string) (core.Result, error) {
	if database == "" {
		database = defaultConnectDatabase
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return core.Result{}, err
	}
	stmt, err := admin.AlterExtensionUpdate(name)
	if err != nil {
		return core.Result{}, err
	}
	if m.runner == nil {
		return core.Result{}, core.InternalError("pg: updating an extension requires a Runner")
	}
	if _, err := m.runPsql(ctx, database, stmt); err != nil {
		return core.Result{}, core.ExecError("pg: updating extension %q on %q failed", name, database).Wrap(err)
	}
	return core.Ok(fmt.Sprintf("extension %q updated on %q", name, database)).
		WithData("database", database).
		WithStatements(stmt), nil
}

// extensionOnDisk reports whether name's control file is present in database
// (i.e. it appears in pg_available_extensions). The name is bound as a query
// parameter, never interpolated.
func (m *Manager) extensionOnDisk(ctx context.Context, database, name string) (bool, error) {
	conn, release, err := m.acquireRead(ctx, database)
	if err != nil {
		return false, err
	}
	defer release()

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_available_extensions WHERE name = $1)`, name).
		Scan(&exists); err != nil {
		return false, core.InternalError("pg: checking extension availability").Wrap(err)
	}
	return exists, nil
}

// cronDatabase returns the effective pg_cron worker database — the value of the
// cron.database_name GUC, read as the postgres superuser via psql. The second
// current_setting argument (missing_ok) makes it return empty rather than error
// when pg_cron is not loaded yet; an empty/unset value means pg_cron's built-in
// default, "postgres".
func (m *Manager) cronDatabase(ctx context.Context) (string, error) {
	out, err := m.runPsql(ctx, defaultConnectDatabase,
		"SELECT current_setting('cron.database_name', true)")
	if err != nil {
		return "", err
	}
	cronDB := strings.TrimSpace(out)
	if cronDB == "" {
		cronDB = defaultConnectDatabase
	}
	return cronDB, nil
}

// installExtensionPackage installs the OS package that ships an extension's
// files, mirroring the apt pattern in Provision: apt-get update then apt-get
// install -y <pkg>, with the package name resolved from the catalog template and
// the detected PG major version and passed as an exec arg vector (never a shell
// string). It returns the commands run for the Result record.
func (m *Manager) installExtensionPackage(ctx context.Context, entry CatalogEntry) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: installing an extension package requires a Runner")
	}
	major, err := m.MajorVersion(ctx)
	if err != nil {
		return nil, err
	}
	pkg := entry.PackageName(major)

	steps := make([]string, 0, 2)

	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"update"},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: apt-get update failed").Wrap(err)
	}
	steps = append(steps, aptStep(res, "apt-get update"))

	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"install", "-y", pkg},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: installing package %s failed", pkg).
			WithHint("ensure the apt sources include the PostgreSQL (PGDG) repository that ships this extension").Wrap(err)
	}
	steps = append(steps, aptStep(res, "apt-get install -y "+pkg))

	return steps, nil
}

// ensurePreloadLibrary makes sure entry's library is present in
// shared_preload_libraries and, when it had to change it, restarts Postgres
// safely. It mirrors EnsureArchiving: read the current value with
// current_setting (so an empty list is an empty field, not an error),
// MergePreloadLibraries (idempotent read-modify-write), and — only when the
// value changed — snapshotAutoConf BEFORE writing so restartWithRollback can
// revert to last-known-good if the postmaster will not come back up. When the
// library is already loaded it is a no-op: no ALTER SYSTEM, no restart.
func (m *Manager) ensurePreloadLibrary(ctx context.Context, entry CatalogEntry) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: enabling a preload library requires a Runner")
	}
	lib := entry.PreloadLib
	if lib == "" {
		lib = entry.Name
	}

	out, err := m.runPsql(ctx, defaultConnectDatabase,
		"SELECT current_setting('shared_preload_libraries')")
	if err != nil {
		return nil, err
	}
	current := strings.TrimSpace(out)

	merged, changed := MergePreloadLibraries(current, lib)
	if !changed {
		// Already loaded — re-adding is a no-op, no needless restart.
		return []string{fmt.Sprintf("shared_preload_libraries already contains %q; no restart needed", lib)}, nil
	}

	// Snapshot postgresql.auto.conf BEFORE writing so a postmaster that rejects
	// the new value can be rolled back to last-known-good.
	snap, err := m.snapshotAutoConf(ctx)
	if err != nil {
		return nil, err
	}

	stmt := "ALTER SYSTEM SET shared_preload_libraries = " + core.QuoteLiteral(merged)
	if _, err := m.runPsql(ctx, defaultConnectDatabase, stmt); err != nil {
		return nil, core.ExecError("pg: setting shared_preload_libraries failed").Wrap(err)
	}

	if err := m.restartWithRollback(ctx, snap, "shared_preload_libraries change for "+lib); err != nil {
		return nil, err
	}

	return []string{stmt, "systemctl restart " + serviceName}, nil
}

// catalogPackageName resolves a catalog entry's package for display in the Add
// dialog. A version-templated package (postgresql-%d-…) is only returned when a
// valid major version was read; an unresolved major leaves it empty rather than
// rendering a nonsense "postgresql-0-…". Version-agnostic (contrib) packages are
// returned regardless.
func catalogPackageName(e CatalogEntry, major int) string {
	if strings.Contains(e.PackageTemplate, "%d") && major <= 0 {
		return ""
	}
	return e.PackageName(major)
}

// aptStep renders a run command for the Result record: the resolved argv when
// the Runner reported one (real or dry-run), otherwise a readable fallback. It
// mirrors the record helper in Provision.
func aptStep(res exec.RunResult, fallback string) string {
	if len(res.Command) > 0 {
		return strings.Join(res.Command, " ")
	}
	return fallback
}
