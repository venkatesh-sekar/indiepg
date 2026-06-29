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
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// Dedicated DB-level roles the manager creates and connects as. The read-only
// role is granted only CONNECT/USAGE/SELECT and is additionally hardened to
// reject writes; the admin role drives guided privileged actions.
const (
	// ReadOnlyRole is the login role used for the read-only pool.
	ReadOnlyRole = "indiepg_readonly"
	// AdminRole is the login role used for the privileged pool.
	AdminRole = "indiepg_admin"
)

// defaultConnectDatabase is the database the pools dial first. Postgres always
// ships a "postgres" maintenance database, so it is a safe initial target for
// cluster-wide queries (listing databases, reading the system identifier).
const defaultConnectDatabase = "postgres"

// aptPackages is the set of packages Provision installs. pgbackrest is included
// here (not installed lazily) because the backup feature shells out to the
// `pgbackrest` binary via `sudo -u postgres`; without it the first backup fails
// with "sudo: pgbackrest: command not found". The Debian/Ubuntu package puts the
// binary in /usr/bin, which is on sudo's secure_path.
var aptPackages = []string{"postgresql", "postgresql-contrib", "pgbackrest"}

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

	// detectTuning resolves this host's recommended tuning for a profile. nil
	// means use detectHostTuning (real /proc/meminfo + runtime CPU); tests set
	// it to pin RAM/CPU so the apply decision is deterministic.
	detectTuning func(WorkloadProfile) (TuningRecommendation, bool)
}

// hostTuning returns the host-sized recommendation for a profile, via the
// detectTuning seam when set (tests) or real host detection otherwise.
func (m *Manager) hostTuning(profile WorkloadProfile) (TuningRecommendation, bool) {
	if m.detectTuning != nil {
		return m.detectTuning(profile)
	}
	return detectHostTuning(profile)
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
// profile is the workload profile to host-size Postgres for. It is passed IN by
// the caller (which owns config) rather than read here, so pg stays ignorant of
// "what's chosen": a re-run after an operator picked OLTP/OLAP must re-apply THAT
// profile, not silently restart Postgres back onto a hardcoded Mixed while config
// and the UI still report the chosen one. A fresh install has no persisted choice,
// so the caller passes ProfileMixed (config's default) and behaviour is unchanged.
//
// Provision returns a core.Result recording the statements/commands run so the
// caller can surface them in the audit log and dry-run display.
func (m *Manager) Provision(ctx context.Context, profile WorkloadProfile) (core.Result, error) {
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
		// Record the redacted form: any PASSWORD '...' literal is masked before
		// it is persisted into the audit log / dry-run display so a secret never
		// lands in panel state, even though provision SQL today carries none.
		steps = append(steps, redactPasswordLiteral(stmt))
	}

	// 5. Make the panel's dedicated roles connectable over the local socket.
	// They have no OS user (peer fails) and no password by design, so add a
	// tightly scoped trust rule for them to pg_hba.conf and reload. Without this
	// the panel cannot connect to the Postgres it just provisioned.
	socketAuthChanged, err := m.EnsureSocketAuth(ctx)
	if err != nil {
		return core.Result{}, err
	}
	if socketAuthChanged {
		steps = append(steps, "configured pg_hba.conf socket auth for "+ReadOnlyRole+" and "+AdminRole+"; reloaded config")
	}

	// Re-running Provision on an already-set-up box is safe: every step above is
	// idempotent (apt install / `systemctl enable --now` are no-ops when already
	// done, and provisionSQL guards every statement with DO/IF NOT EXISTS). The
	// one step that mutates on-disk state — pg_hba.conf — reports whether it
	// actually changed anything, so the operator can tell a fresh provision from a
	// re-run that found nothing to do (north star: never be confused). We only
	// claim "already present" for the step we can honestly detect; the message
	// does not assert the whole provision was a no-op.
	socketAuth := "configured"
	if !socketAuthChanged {
		socketAuth = "already-present"
	}
	// Apply the host-sized tuning for the operator's chosen workload profile:
	// shared_buffers / work_mem / effective_cache_size / max_connections sized to
	// detected RAM/CPU per DEFAULTS.md. The profile comes from the caller (the
	// persisted choice, default Mixed) so a re-provision preserves an OLTP/OLAP
	// selection instead of forcing the box back onto Mixed. Restart-requiring
	// settings are activated through restartWithRollback, so a value the postmaster
	// rejects auto-rolls-back to last-known-good and Postgres is never left down.
	// Re-running Provision is a no-op when the settings already match.
	tuning, _ := m.hostTuning(profile)
	tuningStatus := "applied"
	tuningChanged, err := m.ApplyTuning(ctx, tuning)
	switch {
	case err == nil && !tuningChanged:
		tuningStatus = "already-applied"
	case err == nil:
		tuningStatus = "applied"
	case core.CodeOf(err) == core.CodeSafety:
		// A value the postmaster rejected was auto-rolled-back; Postgres is running
		// on the previous good config. Don't fail the whole provision over an
		// oversized tuning default — record it and carry on, on best defaults.
		m.log.Warn("host-sized tuning was rejected and rolled back; Postgres running on prior config",
			"error", err.Error())
		tuningStatus = "rejected (rolled back to last-known-good)"
	default:
		return core.Result{}, err
	}
	steps = append(steps, fmt.Sprintf(
		"host-sized tuning (%dMB RAM, %d CPU, %s profile): %s",
		tuning.MemoryMB, tuning.CPUCount, tuning.Profile, tuningStatus))

	result := core.Ok("Postgres provisioned").
		WithData("roles", []string{ReadOnlyRole, AdminRole}).
		WithData("service", serviceName).
		WithData("socket_auth", socketAuth).
		WithData("recommended_tuning", tuning.SettingsMap()).
		WithData("tuning", tuningStatus).
		WithStatements(steps...)
	return result, nil
}

// IsRunning reports whether Postgres is actually up and accepting connections.
// It probes the postmaster directly with a real `SELECT 1` over the local socket
// (confirmAcceptingConnections), NOT `systemctl is-active postgresql`: on
// Debian/Ubuntu — exactly the platform we provision via apt — that unit is a
// oneshot wrapper that reports "active" even when the real
// postgresql@<ver>-main.service failed to start, so is-active can claim "running"
// while the cluster is down. A probe failure (Postgres down or unreachable) is a
// normal, queryable "not running" answer, returned as a clean (false, nil)
// rather than an error.
func (m *Manager) IsRunning(ctx context.Context) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pg: IsRunning requires a Runner")
	}
	if err := m.confirmAcceptingConnections(ctx); err != nil {
		return false, nil
	}
	return true, nil
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
//
// The statement travels in the -c argument, which is the resolved argv the
// Runner would otherwise log; when the SQL carries a secret (a PASSWORD '...'
// literal, e.g. CREATE/ALTER ROLE ... PASSWORD) the spec is marked Sensitive so
// the Runner never logs the cleartext password and RunResult.Command is elided.
// Connecting as the postgres OS user over the unix socket is peer-authenticated,
// so no PGPASSWORD/connection secret is inlined here; the only secret risk is a
// PASSWORD literal inside the statement itself, which redaction handles.
func (m *Manager) runPsql(ctx context.Context, database, sql string) (string, error) {
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:      "psql",
		AsUser:    "postgres",
		Args:      []string{"-v", "ON_ERROR_STOP=1", "-tAqX", "-d", database, "-c", sql},
		Timeout:   commandTimeout,
		Sensitive: statementHasSecret(sql),
	})
	if err != nil {
		// Redact the stderr too: psql can echo the failing statement (and thus a
		// PASSWORD literal) back in its error text.
		return "", core.ExecError("pg: psql failed").
			WithDetail("stderr", redactPasswordLiteral(res.Stderr)).Wrap(err)
	}
	return res.Stdout, nil
}

// passwordLiteralRe matches a SQL PASSWORD literal: the PASSWORD keyword (any
// case) followed by an optional E string-escape prefix and a single-quoted
// literal whose body may contain doubled ” escapes. It deliberately does not
// span newlines so it only consumes the literal itself.
var passwordLiteralRe = regexp.MustCompile(`(?i)(PASSWORD\s+)E?'(?:[^']|'')*'`)

// statementHasSecret reports whether sql contains a PASSWORD literal, i.e. a
// value that must not be logged or persisted in cleartext.
func statementHasSecret(sql string) bool {
	return passwordLiteralRe.MatchString(sql)
}

// redactPasswordLiteral rewrites every PASSWORD '...' literal in sql to
// PASSWORD <redacted> so the statement is safe to echo or persist. The PASSWORD
// keyword (with its original spacing) is preserved; only the secret value is
// masked.
func redactPasswordLiteral(sql string) string {
	return passwordLiteralRe.ReplaceAllString(sql, "${1}<redacted>")
}
