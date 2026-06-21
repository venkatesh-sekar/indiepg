package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// DatabaseInfo describes one database in the cluster.
type DatabaseInfo struct {
	Name      string
	Owner     string
	SizeBytes int64
}

// ColumnInfo describes one column of a table.
type ColumnInfo struct {
	Name     string
	DataType string
	NotNull  bool
	Default  string
}

// TableInfo describes one table and (when fully described) its columns.
type TableInfo struct {
	Schema      string
	Name        string
	RowEstimate int64
	SizeBytes   int64
	Columns     []ColumnInfo
}

// listDatabasesSQL lists non-template, connectable databases with owner and
// size. It excludes template databases and any database not marked
// datallowconn. pg_database_size is used for the on-disk size.
const listDatabasesSQL = `
SELECT d.datname,
       pg_catalog.pg_get_userbyid(d.datdba) AS owner,
       pg_catalog.pg_database_size(d.datname)::bigint AS size_bytes
FROM pg_catalog.pg_database d
WHERE d.datistemplate = false
  AND d.datallowconn = true
ORDER BY d.datname`

// ListDatabases returns the cluster's databases using the read-only pool.
func (m *Manager) ListDatabases(ctx context.Context) ([]DatabaseInfo, error) {
	pool := m.ReadPool()
	if pool == nil {
		return nil, core.InternalError("pg: not connected").
			WithHint("call Connect before ListDatabases")
	}

	rows, err := pool.Query(ctx, listDatabasesSQL)
	if err != nil {
		return nil, core.InternalError("pg: listing databases").Wrap(err)
	}
	defer rows.Close()

	var out []DatabaseInfo
	for rows.Next() {
		var d DatabaseInfo
		if err := rows.Scan(&d.Name, &d.Owner, &d.SizeBytes); err != nil {
			return nil, core.InternalError("pg: scanning database row").Wrap(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading databases").Wrap(err)
	}
	return out, nil
}

// listTablesSQL lists ordinary tables (not system catalogs) in user schemas
// with a row estimate (reltuples) and total relation size. It skips the
// internal pg_catalog and information_schema namespaces.
const listTablesSQL = `
SELECT n.nspname AS schema,
       c.relname AS name,
       COALESCE(c.reltuples, 0)::bigint AS row_estimate,
       pg_catalog.pg_total_relation_size(c.oid)::bigint AS size_bytes
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY n.nspname, c.relname`

// ListTables returns the user tables in the given database. The database name is
// validated; because pgx pools are bound to a single database, listing a
// database other than the pool's connect database requires opening a transient
// connection to it over the same socket. To keep the read-only guarantee, that
// transient connection also connects as the read-only role.
func (m *Manager) ListTables(ctx context.Context, database string) ([]TableInfo, error) {
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}
	pool := m.ReadPool()
	if pool == nil {
		return nil, core.InternalError("pg: not connected").
			WithHint("call Connect before ListTables")
	}

	conn, release, err := m.acquireRead(ctx, database)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := conn.Query(ctx, listTablesSQL)
	if err != nil {
		return nil, core.InternalError("pg: listing tables in %s", database).Wrap(err)
	}
	defer rows.Close()

	var out []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Schema, &t.Name, &t.RowEstimate, &t.SizeBytes); err != nil {
			return nil, core.InternalError("pg: scanning table row").Wrap(err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading tables").Wrap(err)
	}
	return out, nil
}

// describeColumnsSQL returns the columns of a specific table, ordered by
// position. It binds schema/table as parameters so user input is never
// interpolated into the catalog query. NotNull and the default expression come
// from pg_attribute / pg_attrdef.
const describeColumnsSQL = `
SELECT a.attname AS name,
       pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
       a.attnotnull AS not_null,
       COALESCE(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid), '') AS default_expr
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE n.nspname = $1
  AND c.relname = $2
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum`

// describeTableMetaSQL returns the row estimate and size for one table.
const describeTableMetaSQL = `
SELECT COALESCE(c.reltuples, 0)::bigint AS row_estimate,
       pg_catalog.pg_total_relation_size(c.oid)::bigint AS size_bytes
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

// DescribeTable returns a single table's columns plus its size and row
// estimate. database/schema/table are all validated; the catalog lookups bind
// schema/table as query parameters. Returns *core.Error CodeNotFound if the
// table does not exist.
func (m *Manager) DescribeTable(ctx context.Context, database, schema, table string) (*TableInfo, error) {
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(schema, "schema"); err != nil {
		return nil, err
	}
	if err := core.ValidateIdentifier(table, "table"); err != nil {
		return nil, err
	}
	if m.ReadPool() == nil {
		return nil, core.InternalError("pg: not connected").
			WithHint("call Connect before DescribeTable")
	}

	conn, release, err := m.acquireRead(ctx, database)
	if err != nil {
		return nil, err
	}
	defer release()

	info := &TableInfo{Schema: schema, Name: table}

	err = conn.QueryRow(ctx, describeTableMetaSQL, schema, table).
		Scan(&info.RowEstimate, &info.SizeBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, core.NotFoundError("pg: table %s.%s not found in %s", schema, table, database)
		}
		return nil, core.InternalError("pg: reading table metadata").Wrap(err)
	}

	rows, err := conn.Query(ctx, describeColumnsSQL, schema, table)
	if err != nil {
		return nil, core.InternalError("pg: describing columns").Wrap(err)
	}
	defer rows.Close()

	for rows.Next() {
		var c ColumnInfo
		if err := rows.Scan(&c.Name, &c.DataType, &c.NotNull, &c.Default); err != nil {
			return nil, core.InternalError("pg: scanning column row").Wrap(err)
		}
		info.Columns = append(info.Columns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading columns").Wrap(err)
	}
	return info, nil
}

// queryier is the read surface used for schema browsing: either the pooled
// connect-database pool or a transient connection to another database. Both the
// pgxpool.Pool and *pgxpool.Conn satisfy it.
type queryier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// acquireRead returns a read-only queryier scoped to database. When database is
// the pool's connect database, the shared read-only pool is returned directly.
// Otherwise a transient pool to that database is opened (as the read-only role)
// and closed by the returned release func, so cross-database browsing still goes
// through the read-only path.
func (m *Manager) acquireRead(ctx context.Context, database string) (queryier, func(), error) {
	if database == defaultConnectDatabase {
		pool := m.ReadPool()
		if pool == nil {
			return nil, nil, core.InternalError("pg: not connected")
		}
		return pool, func() {}, nil
	}

	pool, err := m.openPool(ctx, connConfig{
		SocketDir:        m.socketDir(),
		Database:         database,
		User:             ReadOnlyRole,
		StatementTimeout: m.cfg.StatementTimeout,
	}, readPoolMaxConns)
	if err != nil {
		return nil, nil, core.InternalError("pg: connecting to %s", database).Wrap(err)
	}
	return pool, pool.Close, nil
}
