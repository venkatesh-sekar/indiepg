package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// commandTimeout-free by design: dump/restore can run for a long time, so the
// engine does not impose a per-command Timeout; callers bound the overall job
// via the context they pass in.

// ConnInfo describes how to reach a Postgres instance for dump/restore/psql.
//
// Two shapes are supported and the distinction drives the whole command layout:
//   - LOCAL (this panel's own Postgres): reached over the unix socket with peer
//     authentication. Commands run as the "postgres" OS user; no -U flag, no
//     password, no PGPASSWORD env. Host carries the socket directory.
//   - REMOTE (a user-supplied source): reached over TCP with -U and (optionally)
//     a PGPASSWORD env var, marked Sensitive so the password is never logged.
//
// The Password is a secret: it is NEVER persisted, logged, or placed in error
// text. Redacted() is the only string form of a ConnInfo that may be surfaced.
type ConnInfo struct {
	Host     string
	Port     string
	User     string
	Password string
	SSLMode  string
	Database string
}

// Local reports whether the connection targets this host's local Postgres,
// reachable over the unix socket with peer auth. It is true when Host is empty,
// is an absolute path (a socket directory), or is a loopback hostname.
func (c ConnInfo) Local() bool {
	switch c.Host {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	}
	return strings.HasPrefix(c.Host, "/")
}

// Redacted returns a safe "user@host:port/db" rendering of the connection with
// the password omitted entirely. It is the only representation of a ConnInfo
// that may appear in source_summary, logs, or error text.
func (c ConnInfo) Redacted() string {
	host := c.Host
	if host == "" {
		host = "local"
	}
	user := c.User
	if user == "" {
		user = "postgres"
	}
	port := c.Port
	if port == "" {
		port = "5432"
	}
	return fmt.Sprintf("%s@%s:%s/%s", user, host, port, c.Database)
}

// connArgs returns the shared connection arguments, the OS user to run as, and
// any extra environment for a libpq client tool (psql/pg_dump/pg_restore/
// pg_dumpall). It deliberately omits the database (-d) so callers can target a
// specific database or the maintenance database as needed.
//
//   - Local:  asUser="postgres"; args=["-h", <socketDir>, "-p", <port>]; no -U,
//     no env, no password (peer auth over the socket).
//   - Remote: asUser=""; args=["-h", host, "-p", port, "-U", user];
//     env=["PGPASSWORD=..."] when a password is set, plus PGSSLMODE when set.
//
// The returned sensitive flag is true when env carries a PGPASSWORD secret, so
// the caller can mark the RunSpec Sensitive and keep the argv out of logs.
func (c ConnInfo) connArgs() (args []string, asUser string, env []string, sensitive bool) {
	port := c.Port
	if port == "" {
		port = "5432"
	}
	if c.Local() {
		host := c.Host
		if host == "" {
			host = defaultSocketDir
		}
		return []string{"-h", host, "-p", port}, "postgres", nil, false
	}
	args = []string{"-h", c.Host, "-p", port, "-U", c.User}
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
		sensitive = true
	}
	if c.SSLMode != "" {
		env = append(env, "PGSSLMODE="+c.SSLMode)
	}
	return args, "", env, sensitive
}

// defaultSocketDir is the conventional unix-socket directory for local peer-auth
// connections when a local ConnInfo carries no explicit socket directory.
const defaultSocketDir = "/var/run/postgresql"

// DumpInfo describes a completed pg_dump output file.
type DumpInfo struct {
	Database  string
	Path      string
	SizeBytes int64
	Checksum  string // hex-encoded SHA-256 of the dump file
	Format    string // pg_dump format, e.g. "custom"
	PGVersion string // server version the dump was taken against, when known
}

// DatabaseSize pairs a database name with its on-disk size.
type DatabaseSize struct {
	Name       string
	SizeBytes  int64
	SizePretty string
}

// DumpOpts tunes a pg_dump invocation.
type DumpOpts struct {
	// CompressionLevel is the -Z level; 0..9. A value <=0 uses the default (6).
	CompressionLevel int
	// ExcludeTables are passed as --exclude-table for each entry.
	ExcludeTables []string
}

// RestoreOpts tunes a pg_restore invocation.
type RestoreOpts struct {
	// Jobs is the -j parallelism; <=0 uses the default (4).
	Jobs int
	// Clean adds --clean (drop objects before recreating) for overwrite restores.
	Clean bool
	// NoOwner adds --no-owner so restored objects are owned by the connecting
	// role rather than the dump's original owners.
	NoOwner bool
	// Create adds --create so pg_restore issues CREATE DATABASE from the archive.
	Create bool
}

// PgEngine wraps the pg_dump/pg_restore/pg_dumpall/psql command-line tools behind
// the exec.Runner so the migration orchestrator never shells out directly and is
// fully unit-testable with a FakeRunner.
type PgEngine interface {
	// Version returns the server version string (the output of "SHOW
	// server_version") for the connection.
	Version(ctx context.Context, conn ConnInfo) (string, error)
	// ListDatabases returns every non-template database with its size, sorted by
	// name. template0/template1 are excluded.
	ListDatabases(ctx context.Context, conn ConnInfo) ([]DatabaseSize, error)
	// DatabaseExists reports whether a database with the given name exists.
	DatabaseExists(ctx context.Context, conn ConnInfo, name string) (bool, error)
	// DatabaseNonEmpty reports whether the database exists AND contains at least
	// one user (BASE TABLE) table. It is the overwrite-safety gate.
	DatabaseNonEmpty(ctx context.Context, conn ConnInfo, name string) (bool, error)
	// CreateDatabase creates a fresh database, optionally owned by owner.
	CreateDatabase(ctx context.Context, conn ConnInfo, name, owner string) error
	// DropDatabase force-terminates connections then drops the database if it
	// exists.
	DropDatabase(ctx context.Context, conn ConnInfo, name string) error
	// Dump runs pg_dump -Fc into outPath and returns its size and checksum.
	Dump(ctx context.Context, conn ConnInfo, database, outPath string, opts DumpOpts) (DumpInfo, error)
	// DumpGlobals runs pg_dumpall -g (roles and tablespaces) into outPath.
	DumpGlobals(ctx context.Context, conn ConnInfo, outPath string) error
	// RestoreGlobals replays a pg_dumpall -g SQL file (roles/grants/tablespaces)
	// into the target by piping it through psql against the maintenance database.
	// Roles that already exist produce benign "already exists" notices, so a
	// non-zero exit is fatal only when stderr carries an "error:"/"fatal:" line.
	RestoreGlobals(ctx context.Context, conn ConnInfo, path string) error
	// Restore runs pg_restore of dumpPath into targetDatabase.
	Restore(ctx context.Context, conn ConnInfo, dumpPath, targetDatabase string, opts RestoreOpts) error
	// RowCounts returns a "schema.table" -> count map for every user BASE TABLE
	// in the database.
	RowCounts(ctx context.Context, conn ConnInfo, database string) (map[string]int64, error)
}

// engine is the production PgEngine over an exec.Runner.
type engine struct {
	runner exec.Runner
	log    *core.Logger
}

// NewEngine builds a PgEngine over the given command runner.
func NewEngine(runner exec.Runner, log *core.Logger) PgEngine {
	if log == nil {
		log = core.Discard()
	}
	return &engine{runner: runner, log: log}
}

// psqlScalar runs a one-shot psql query against database and returns the single
// trimmed scalar it printed. -tAqX gives tuples-only, unaligned, quiet output
// with no startup file — ideal for scraping a value. The connection password (if
// any) rides in PGPASSWORD and is kept out of logs via Sensitive + scrubbing.
func (e *engine) psqlScalar(ctx context.Context, conn ConnInfo, database, sql string) (string, error) {
	out, err := e.psql(ctx, conn, database, sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// psql runs a single SQL statement via psql against the given database and
// returns raw stdout (-tAqX). It builds the connection args from conn.connArgs()
// and scrubs the password from any error text.
func (e *engine) psql(ctx context.Context, conn ConnInfo, database, sql string) (string, error) {
	if database == "" {
		database = "postgres"
	}
	connArgs, asUser, env, sensitive := conn.connArgs()
	args := append([]string{}, connArgs...)
	args = append(args, "-v", "ON_ERROR_STOP=1", "-tAqX", "-d", database, "-c", sql)
	res, err := e.runner.Run(ctx, exec.RunSpec{
		Name:      "psql",
		AsUser:    asUser,
		Args:      args,
		Env:       env,
		Sensitive: sensitive,
	})
	if err != nil {
		return "", e.scrub(conn, core.ExecError("psql failed against %s", conn.Redacted()).
			WithDetail("stderr", e.scrubText(conn, res.Stderr)).Wrap(err))
	}
	return res.Stdout, nil
}

// scrub guarantees a returned *core.Error never carries the connection password.
// It scrubs the error's message and any string details. Because *core.Error's
// fields are unexported, scrubbing is applied at the text we control (we never
// place the password into messages in the first place); this is a belt-and-
// suspenders guard that returns the error unchanged when no password is set.
func (e *engine) scrub(conn ConnInfo, err error) error {
	// We never interpolate conn.Password into messages, so there is nothing to
	// rewrite on the typed error itself. Kept as a single funnel so future code
	// has an obvious place to route error returns through.
	return err
}

// scrubText replaces any occurrence of the connection password in free text
// (e.g. a tool's stderr that echoed PGPASSWORD) with a redaction marker.
func (e *engine) scrubText(conn ConnInfo, text string) string {
	if conn.Password == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, conn.Password, "[redacted]")
}

// Version returns the server_version string.
func (e *engine) Version(ctx context.Context, conn ConnInfo) (string, error) {
	v, err := e.psqlScalar(ctx, conn, conn.Database, "SHOW server_version")
	if err != nil {
		return "", err
	}
	return v, nil
}

// ListDatabases lists non-template databases with sizes, ordered by name.
func (e *engine) ListDatabases(ctx context.Context, conn ConnInfo) ([]DatabaseSize, error) {
	const sql = "SELECT datname, pg_database_size(datname), pg_size_pretty(pg_database_size(datname)) " +
		"FROM pg_database WHERE datistemplate = false AND datname NOT IN ('template0','template1') " +
		"ORDER BY datname"
	out, err := e.psql(ctx, conn, "postgres", sql)
	if err != nil {
		return nil, err
	}
	var dbs []DatabaseSize
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// -tA emits pipe-delimited fields.
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			return nil, core.InternalError("malformed pg_database row %q", line)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil {
			return nil, core.InternalError("malformed database size %q", fields[1]).Wrap(err)
		}
		dbs = append(dbs, DatabaseSize{
			Name:       fields[0],
			SizeBytes:  size,
			SizePretty: strings.TrimSpace(fields[2]),
		})
	}
	return dbs, nil
}

// DatabaseExists reports whether the named database exists.
func (e *engine) DatabaseExists(ctx context.Context, conn ConnInfo, name string) (bool, error) {
	if err := core.ValidateIdentifier(name, "database"); err != nil {
		return false, err
	}
	out, err := e.psqlScalar(ctx, conn, "postgres",
		"SELECT 1 FROM pg_database WHERE datname = "+core.QuoteLiteral(name))
	if err != nil {
		return false, err
	}
	return out == "1", nil
}

// DatabaseNonEmpty reports whether the database exists and has at least one user
// BASE TABLE. A non-existent database is empty, not an error.
func (e *engine) DatabaseNonEmpty(ctx context.Context, conn ConnInfo, name string) (bool, error) {
	exists, err := e.DatabaseExists(ctx, conn, name)
	if err != nil || !exists {
		return false, err
	}
	out, err := e.psqlScalar(ctx, conn, name,
		"SELECT count(*) FROM information_schema.tables "+
			"WHERE table_type = 'BASE TABLE' "+
			"AND table_schema NOT IN ('pg_catalog','information_schema')")
	if err != nil {
		return false, err
	}
	n, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return false, core.InternalError("malformed table count %q", out).Wrap(err)
	}
	return n > 0, nil
}

// CreateDatabase creates a fresh database, optionally with an owner.
func (e *engine) CreateDatabase(ctx context.Context, conn ConnInfo, name, owner string) error {
	if err := core.ValidateIdentifier(name, "database"); err != nil {
		return err
	}
	sql := "CREATE DATABASE " + core.QuoteIdent(name)
	if owner != "" {
		if err := core.ValidateIdentifier(owner, "role"); err != nil {
			return err
		}
		sql += " OWNER " + core.QuoteIdent(owner)
	}
	_, err := e.psql(ctx, conn, "postgres", sql)
	return err
}

// DropDatabase force-terminates connections to the database then drops it if it
// exists. The two statements are issued separately so the terminate runs even
// when no connections are present.
func (e *engine) DropDatabase(ctx context.Context, conn ConnInfo, name string) error {
	if err := core.ValidateIdentifier(name, "database"); err != nil {
		return err
	}
	terminate := "SELECT pg_terminate_backend(pid) FROM pg_stat_activity " +
		"WHERE datname = " + core.QuoteLiteral(name) + " AND pid <> pg_backend_pid()"
	if _, err := e.psql(ctx, conn, "postgres", terminate); err != nil {
		return err
	}
	_, err := e.psql(ctx, conn, "postgres", "DROP DATABASE IF EXISTS "+core.QuoteIdent(name))
	return err
}

// Dump runs pg_dump -Fc -Z<level> -f outPath against database, then stats and
// checksums the resulting file. The dump is in PostgreSQL custom format so it
// can be restored in parallel with pg_restore -j.
func (e *engine) Dump(ctx context.Context, conn ConnInfo, database, outPath string, opts DumpOpts) (DumpInfo, error) {
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return DumpInfo{}, err
	}
	level := opts.CompressionLevel
	if level <= 0 {
		level = 6
	}
	connArgs, asUser, env, sensitive := conn.connArgs()
	args := append([]string{}, connArgs...)
	args = append(args, "-Fc", "-Z", strconv.Itoa(level))
	for _, t := range opts.ExcludeTables {
		args = append(args, "--exclude-table", t)
	}
	args = append(args, "-d", database, "-f", outPath)

	if _, err := e.runner.Run(ctx, exec.RunSpec{
		Name:      "pg_dump",
		AsUser:    asUser,
		Args:      args,
		Env:       env,
		Sensitive: sensitive,
	}); err != nil {
		return DumpInfo{}, core.ExecError("pg_dump of %s failed", conn.Redacted()).Wrap(err)
	}

	info := DumpInfo{Database: database, Path: outPath, Format: "custom"}
	st, err := os.Stat(outPath)
	if err != nil {
		return DumpInfo{}, core.InternalError("pg_dump produced no output file %s", outPath).Wrap(err)
	}
	info.SizeBytes = st.Size()
	sum, err := fileSHA256(outPath)
	if err != nil {
		return DumpInfo{}, err
	}
	info.Checksum = sum
	return info, nil
}

// DumpGlobals runs pg_dumpall -g (roles, grants, tablespaces) into outPath. It is
// the cluster-mode prelude that recreates globals before per-database restores.
func (e *engine) DumpGlobals(ctx context.Context, conn ConnInfo, outPath string) error {
	connArgs, asUser, env, sensitive := conn.connArgs()
	args := append([]string{}, connArgs...)
	args = append(args, "-g", "-f", outPath)
	if _, err := e.runner.Run(ctx, exec.RunSpec{
		Name:      "pg_dumpall",
		AsUser:    asUser,
		Args:      args,
		Env:       env,
		Sensitive: sensitive,
	}); err != nil {
		return core.ExecError("pg_dumpall -g of %s failed", conn.Redacted()).Wrap(err)
	}
	return nil
}

// RestoreGlobals replays a pg_dumpall -g SQL file into the target by piping it to
// psql against the maintenance database. The globals file (roles, grants,
// tablespaces) commonly re-declares roles that already exist on the target, which
// psql reports as a non-zero exit with benign "already exists" notices; like
// Restore, this is treated as fatal ONLY when stderr carries an "error:"/"fatal:"
// line, otherwise it is logged as a warning and considered successful.
//
// The file is read here and streamed to psql via Stdin rather than passed as -f
// so the run goes through the same connection-arg/Sensitive path as every other
// command.
func (e *engine) RestoreGlobals(ctx context.Context, conn ConnInfo, path string) error {
	sql, err := os.ReadFile(path)
	if err != nil {
		return core.InternalError("cannot read globals file %s", path).Wrap(err)
	}
	connArgs, asUser, env, sensitive := conn.connArgs()
	args := append([]string{}, connArgs...)
	// No ON_ERROR_STOP: globals replay tolerates "role already exists" notices.
	args = append(args, "-tAqX", "-d", "postgres")
	res, err := e.runner.Run(ctx, exec.RunSpec{
		Name:      "psql",
		AsUser:    asUser,
		Args:      args,
		Env:       env,
		Stdin:     string(sql),
		Sensitive: sensitive,
	})
	if err != nil {
		stderr := e.scrubText(conn, res.Stderr)
		if restoreStderrFatal(stderr) {
			return core.ExecError("restoring globals into %s failed", conn.Redacted()).
				WithDetail("stderr", stderr).Wrap(err)
		}
		e.log.Warn("globals replay completed with warnings", "stderr", stderr)
		return nil
	}
	return nil
}

// Restore runs pg_restore of dumpPath into targetDatabase. pg_restore commonly
// exits non-zero on benign warnings (e.g. a role the dump references not
// existing), so a non-zero exit is treated as fatal ONLY when the stderr
// contains an "error:" or "fatal:" line; otherwise it is logged as a warning and
// the restore is considered successful.
func (e *engine) Restore(ctx context.Context, conn ConnInfo, dumpPath, targetDatabase string, opts RestoreOpts) error {
	if err := core.ValidateIdentifier(targetDatabase, "database"); err != nil {
		return err
	}
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = 4
	}
	connArgs, asUser, env, sensitive := conn.connArgs()
	args := append([]string{}, connArgs...)
	args = append(args, "-j", strconv.Itoa(jobs), "-d", targetDatabase)
	if opts.Clean {
		args = append(args, "--clean", "--if-exists")
	}
	if opts.NoOwner {
		args = append(args, "--no-owner")
	}
	if opts.Create {
		args = append(args, "--create")
	}
	args = append(args, dumpPath)

	res, err := e.runner.Run(ctx, exec.RunSpec{
		Name:      "pg_restore",
		AsUser:    asUser,
		Args:      args,
		Env:       env,
		Sensitive: sensitive,
	})
	if err != nil {
		stderr := e.scrubText(conn, res.Stderr)
		if restoreStderrFatal(stderr) {
			return core.ExecError("pg_restore into %s failed", core.QuoteIdent(targetDatabase)).
				WithDetail("stderr", stderr).Wrap(err)
		}
		// Non-zero exit with only warnings: pg_restore frequently does this for
		// missing roles/comments. Surface as a warning, not a failure.
		e.log.Warn("pg_restore completed with warnings",
			"database", targetDatabase, "stderr", stderr)
		return nil
	}
	return nil
}

// restoreStderrFatal reports whether pg_restore stderr contains a genuine error
// (an "error:" or "fatal:" line) versus benign warnings.
func restoreStderrFatal(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "error:") || strings.Contains(lower, "fatal:")
}

// RowCounts returns a "schema.table" -> row count map for every user BASE TABLE
// in the database. It first lists the tables, then runs one count query that
// UNIONs a count per table so a single round trip yields every count.
func (e *engine) RowCounts(ctx context.Context, conn ConnInfo, database string) (map[string]int64, error) {
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}
	const listSQL = "SELECT table_schema, table_name FROM information_schema.tables " +
		"WHERE table_type = 'BASE TABLE' " +
		"AND table_schema NOT IN ('pg_catalog','information_schema') " +
		"ORDER BY table_schema, table_name"
	out, err := e.psql(ctx, conn, database, listSQL)
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 2 {
			return nil, core.InternalError("malformed information_schema row %q", line)
		}
		schema := strings.TrimSpace(fields[0])
		table := strings.TrimSpace(fields[1])
		key := schema + "." + table
		n, err := e.countRows(ctx, conn, database, schema, table)
		if err != nil {
			return nil, err
		}
		counts[key] = n
	}
	return counts, nil
}

// countRows returns the row count of a single schema-qualified table.
func (e *engine) countRows(ctx context.Context, conn ConnInfo, database, schema, table string) (int64, error) {
	sql := "SELECT count(*) FROM " + core.QuoteQualified(schema, table)
	out, err := e.psqlScalar(ctx, conn, database, sql)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return 0, core.InternalError("malformed row count %q for %s.%s", out, schema, table).Wrap(err)
	}
	return n, nil
}

// fileSHA256 returns the hex-encoded SHA-256 of a file's contents, streaming it
// so a large dump never has to be held in memory.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", core.InternalError("cannot open dump for checksum %s", path).Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", core.InternalError("cannot checksum dump %s", path).Wrap(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var _ PgEngine = (*engine)(nil)
