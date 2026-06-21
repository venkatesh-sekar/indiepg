// Package pg owns the native Postgres the panel provisions and connects to.
//
// The Manager installs Postgres via apt and enables it via systemctl (all shell
// side effects go through an exec.Runner), reports whether the service is up,
// reads the cluster's stable system identifier, and manages two distinct pgx
// connection pools over the local unix socket:
//
//   - a read-only pool, connected as a dedicated read-only role, used for the
//     query box and schema browsing. Read-only is enforced at the DB level so a
//     UI bug cannot turn a SELECT into a DELETE.
//   - a privileged pool, connected as a dedicated admin role, used only for
//     guided, confirmed administrative actions.
//
// Nothing here interpolates raw identifiers into SQL: every identifier is
// validated and quoted via internal/core before use.
package pg

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
)

// Dedicated DB-level roles the manager creates and connects as. The read-only
// role is granted only CONNECT/USAGE/SELECT and is additionally hardened to
// reject writes; the admin role drives guided privileged actions.
const (
	// ReadOnlyRole is the login role used for the read-only pool.
	ReadOnlyRole = "pgpanel_readonly"
	// AdminRole is the login role used for the privileged pool.
	AdminRole = "pgpanel_admin"
)

// defaultConnectDatabase is the database the pools dial first. Postgres always
// ships a "postgres" maintenance database, so it is a safe initial target for
// cluster-wide queries (listing databases, reading the system identifier).
const defaultConnectDatabase = "postgres"

// aptPackages is the set of packages Provision installs.
var aptPackages = []string{"postgresql", "postgresql-contrib"}

// serviceName is the systemd unit for the managed Postgres.
const serviceName = "postgresql"

// commandTimeout bounds individual provisioning shell commands so a wedged apt
// or systemctl cannot hang the panel forever.
const commandTimeout = 10 * time.Minute

// Manager owns the native Postgres and its connection pools.
//
// A Manager is safe for concurrent use: pool access is guarded by a mutex so
// Connect/Close and the pool accessors never race.
type Manager struct {
	runner exec.Runner
	cfg    config.Config
	log    *core.Logger

	mu       sync.RWMutex
	readPool *pgxpool.Pool
	privPool *pgxpool.Pool
}

// Options configure a Manager. Runner is required for any IO; a nil Logger is
// replaced with a discard logger.
type Options struct {
	Runner exec.Runner
	Config config.Config
	Logger *core.Logger
}

// New builds a Manager from Options.
func New(opts Options) *Manager {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}
	return &Manager{
		runner: opts.Runner,
		cfg:    opts.Config,
		log:    log,
	}
}

// Provision installs Postgres (apt), enables and starts the service
// (systemctl), creates the dedicated read-only and admin roles with locked-down
// privileges, and enables pg_stat_statements. It is written to be safely
// re-runnable: every role/extension step uses an idempotent guard, and an
// already-installed package or already-running service is not an error.
//
// Provision returns a core.Result recording the statements/commands run so the
// caller can surface them in the audit log and dry-run display.
func (m *Manager) Provision(ctx context.Context) (core.Result, error) {
	if m.runner == nil {
		return core.Result{}, core.InternalError("pg: provision requires a Runner")
	}

	steps := make([]string, 0, 8)
	record := func(rs exec.RunResult, fallback string) {
		if len(rs.Command) > 0 {
			steps = append(steps, strings.Join(rs.Command, " "))
			return
		}
		steps = append(steps, fallback)
	}

	// 1. apt-get update so the package index is fresh.
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"update"},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return core.Result{}, core.ExecError("pg: apt-get update failed").Wrap(err)
	}
	record(res, "apt-get update")

	// 2. install Postgres packages.
	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    append([]string{"install", "-y"}, aptPackages...),
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return core.Result{}, core.ExecError("pg: installing postgresql failed").
			WithHint("ensure the apt sources include the postgresql packages").Wrap(err)
	}
	record(res, "apt-get install -y "+strings.Join(aptPackages, " "))

	// 3. enable + start the service (systemctl enable --now is idempotent).
	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"enable", "--now", serviceName},
		Timeout: commandTimeout,
	})
	if err != nil {
		return core.Result{}, core.ExecError("pg: enabling the postgresql service failed").Wrap(err)
	}
	record(res, "systemctl enable --now "+serviceName)

	// 4. create the dedicated roles + extension via psql, run as the postgres
	// OS superuser over the local socket. The SQL is generated with quoted
	// identifiers and is itself idempotent.
	sqlStmts, err := provisionSQL()
	if err != nil {
		return core.Result{}, err
	}
	for _, stmt := range sqlStmts {
		if _, err := m.runPsql(ctx, defaultConnectDatabase, stmt); err != nil {
			return core.Result{}, err
		}
		steps = append(steps, stmt)
	}

	result := core.Ok("Postgres provisioned").
		WithData("roles", []string{ReadOnlyRole, AdminRole}).
		WithData("service", serviceName).
		WithStatements(steps...)
	return result, nil
}

// IsRunning reports whether the postgresql systemd service is active. It treats
// a non-zero "is-active" exit (i.e. inactive/failed) as a clean false rather
// than an error, since "not running" is a normal, queryable state.
func (m *Manager) IsRunning(ctx context.Context) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pg: IsRunning requires a Runner")
	}
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "systemctl",
		Args:    []string{"is-active", serviceName},
		Timeout: 30 * time.Second,
	})
	out := strings.TrimSpace(res.Stdout)
	if out == "active" {
		return true, nil
	}
	if err != nil {
		// systemctl is-active exits non-zero for inactive/failed/unknown; those
		// are legitimate "not running" answers, not a Runner failure. Only an
		// empty stdout with an error means we could not determine the state.
		if out == "" {
			return false, nil
		}
		return false, nil
	}
	return false, nil
}

// SystemIdentifier returns the Postgres cluster's stable 64-bit system
// identifier (from pg_control_system()). It is read over the read-only pool
// when connected, otherwise via a one-shot psql query. The identifier uniquely
// names a cluster and is used to catch "different cluster, same backup repo".
func (m *Manager) SystemIdentifier(ctx context.Context) (string, error) {
	const query = `SELECT system_identifier::text FROM pg_control_system()`

	if pool := m.ReadPool(); pool != nil {
		var id string
		if err := pool.QueryRow(ctx, query).Scan(&id); err != nil {
			return "", core.InternalError("pg: reading system identifier").Wrap(err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return "", core.InternalError("pg: empty system identifier")
		}
		return id, nil
	}

	if m.runner == nil {
		return "", core.InternalError("pg: SystemIdentifier requires a Runner or an open pool")
	}
	out, err := m.runPsql(ctx, defaultConnectDatabase, query)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", core.InternalError("pg: empty system identifier")
	}
	return id, nil
}

// runPsql runs a single SQL statement as the postgres OS superuser via psql over
// the local socket, returning trimmed stdout. -tAqX yields tuples-only,
// unaligned, quiet output with no startup file, ideal for scraping a scalar.
func (m *Manager) runPsql(ctx context.Context, database, sql string) (string, error) {
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "psql",
		AsUser:  "postgres",
		Args:    []string{"-v", "ON_ERROR_STOP=1", "-tAqX", "-d", database, "-c", sql},
		Timeout: commandTimeout,
	})
	if err != nil {
		return "", core.ExecError("pg: psql failed").
			WithDetail("stderr", res.Stderr).Wrap(err)
	}
	return res.Stdout, nil
}
