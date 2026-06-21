package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// compile-time assertion: a pgxpool.Pool is a usable read-only queryier.
var _ queryier = (*pgxpool.Pool)(nil)

func TestListDatabases_NotConnected(t *testing.T) {
	m := New(Options{Config: config.Default()})
	_, err := m.ListDatabases(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestListTables_InvalidDatabase(t *testing.T) {
	m := New(Options{Config: config.Default()})
	_, err := m.ListTables(context.Background(), "bad name!")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestListTables_NotConnected(t *testing.T) {
	m := New(Options{Config: config.Default()})
	_, err := m.ListTables(context.Background(), "mydb")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestDescribeTable_ValidationOrder(t *testing.T) {
	m := New(Options{Config: config.Default()})

	tests := []struct {
		name            string
		db, schema, tbl string
		wantCode        core.Code
	}{
		{"bad database", "1bad", "app", "users", core.CodeValidation},
		{"bad schema", "appdb", "pub lic", "users", core.CodeValidation},
		{"reserved schema", "appdb", "public", "users", core.CodeValidation},
		{"bad table", "appdb", "app", "drop;", core.CodeValidation},
		{"reserved table", "appdb", "app", "select", core.CodeValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.DescribeTable(context.Background(), tt.db, tt.schema, tt.tbl)
			require.Error(t, err)
			require.Equal(t, tt.wantCode, core.CodeOf(err))
		})
	}
}

func TestDescribeTable_NotConnected(t *testing.T) {
	m := New(Options{Config: config.Default()})
	// all identifiers valid (non-reserved) so validation passes and we reach the
	// not-connected guard.
	_, err := m.DescribeTable(context.Background(), "appdb", "app", "users")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// TestSchemaSQLConstants guards the catalog queries: they must filter out
// system schemas, bind user input as parameters where applicable, and never
// touch templates/non-connectable databases.
func TestSchemaSQLConstants(t *testing.T) {
	require.Contains(t, listDatabasesSQL, "datistemplate = false")
	require.Contains(t, listDatabasesSQL, "datallowconn = true")
	require.Contains(t, listDatabasesSQL, "pg_database_size")

	require.Contains(t, listTablesSQL, "information_schema")
	require.Contains(t, listTablesSQL, "pg_catalog")
	require.Contains(t, listTablesSQL, "pg_total_relation_size")
	// only ordinary + partitioned tables.
	require.Contains(t, listTablesSQL, "relkind IN ('r', 'p')")

	// describe queries bind schema/table positionally — no interpolation.
	require.Contains(t, describeColumnsSQL, "n.nspname = $1")
	require.Contains(t, describeColumnsSQL, "c.relname = $2")
	require.Contains(t, describeColumnsSQL, "format_type")
	require.Contains(t, describeTableMetaSQL, "n.nspname = $1")
	require.Contains(t, describeTableMetaSQL, "c.relname = $2")

	// no raw user identifier should ever be string-concatenated: the only $1/$2
	// placeholders are the binding mechanism.
	require.False(t, strings.Contains(describeColumnsSQL, "'%s'"))
}
