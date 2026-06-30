//go:build e2e

package harness

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PG is Postgres ground truth: it runs psql INSIDE the panel container as the
// postgres OS user over the local socket (peer auth), bypassing the panel API so
// assertions read the database directly. The default database is "postgres"; the
// *DB variants target a named database (e.g. for per-database extension checks).
type PG struct {
	env *Env
}

const defaultDB = "postgres"

// psql runs a single statement via `psql -tAqX` (tuples-only, unaligned, quiet,
// no startup file) and returns trimmed stdout.
func (pg *PG) psql(db, sql string) (string, error) {
	out, stderr, err := pg.env.execPsql(db, sql)
	if err != nil {
		return "", fmt.Errorf("psql failed: %w\nstderr: %s", err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(out), nil
}

// Scalar runs query against the default database and returns the first column of
// the first row as a string.
func (pg *PG) Scalar(query string) (string, error) { return pg.psql(defaultDB, query) }

// ScalarDB is Scalar against a named database.
func (pg *PG) ScalarDB(db, query string) (string, error) { return pg.psql(db, query) }

// Exec runs a statement (DDL/DML) against the default database, discarding output.
func (pg *PG) Exec(sql string) error {
	_, err := pg.psql(defaultDB, sql)
	return err
}

// ExecDB runs a statement against a named database.
func (pg *PG) ExecDB(db, sql string) error {
	_, err := pg.psql(db, sql)
	return err
}

// CountRows returns the row count of a table (or any FROM-able relation) in the
// default database. The table identifier is interpolated verbatim — pass a
// trusted/literal name from the scenario.
func (pg *PG) CountRows(table string) (int, error) { return pg.CountRowsDB(defaultDB, table) }

// CountRowsDB is CountRows against a named database.
func (pg *PG) CountRowsDB(db, table string) (int, error) {
	out, err := pg.psql(db, "SELECT count(*) FROM "+table)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// XID returns the current transaction id (txid_current) as a string — a stable,
// wall-clock-free marker for PITR scenarios.
func (pg *PG) XID() (string, error) { return pg.Scalar("SELECT txid_current()::text") }

// Now returns the database's current UTC timestamp (second precision), parsed
// from a server-side to_char so there is no client-clock dependency.
func (pg *PG) Now() (time.Time, error) {
	out, err := pg.Scalar("SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS')")
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02T15:04:05", strings.TrimSpace(out))
}

// ServerVersion returns the integer major version (e.g. 17) of the running cluster.
func (pg *PG) ServerVersion() (int, error) {
	out, err := pg.Scalar("SHOW server_version_num")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, err
	}
	return n / 10000, nil
}

// execPsql is the Env seam that runs psql inside the panel container as postgres.
func (e *Env) execPsql(db, sql string) (stdout, stderr string, err error) {
	ctx, cancel := shortCtx()
	defer cancel()
	return dockerExec(ctx, e.panelContainer, "postgres",
		"psql", "-v", "ON_ERROR_STOP=1", "-tAqX", "-d", db, "-c", sql)
}
