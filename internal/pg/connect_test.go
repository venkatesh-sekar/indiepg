package pg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestLibpqEscape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"postgres", "postgres"},
		{"/var/run/postgresql", "/var/run/postgresql"},
		{"", "''"},
		{"with space", "'with space'"},
		{"tab\there", "'tab\there'"},
		{"it's", `'it\'s'`},
		{`back\slash`, `'back\\slash'`},
		{"quote'and\\slash", `'quote\'and\\slash'`},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, libpqEscape(tt.in), "input %q", tt.in)
	}
}

func TestBuildDSN(t *testing.T) {
	dsn, err := buildDSN(connConfig{
		SocketDir:        "/var/run/postgresql",
		Database:         "postgres",
		User:             ReadOnlyRole,
		StatementTimeout: 30 * time.Second,
	})
	require.NoError(t, err)

	require.Contains(t, dsn, "host=/var/run/postgresql")
	require.Contains(t, dsn, "user="+ReadOnlyRole)
	require.Contains(t, dsn, "dbname=postgres")
	require.Contains(t, dsn, "sslmode=disable")
	require.Contains(t, dsn, "application_name=indiepg")
	require.Contains(t, dsn, "statement_timeout=30000")
}

func TestBuildDSN_NoStatementTimeout(t *testing.T) {
	dsn, err := buildDSN(connConfig{
		SocketDir: "/tmp/sock",
		Database:  "postgres",
		User:      AdminRole,
	})
	require.NoError(t, err)
	require.NotContains(t, dsn, "statement_timeout=")
}

func TestBuildDSN_SocketDirWithSpace(t *testing.T) {
	dsn, err := buildDSN(connConfig{
		SocketDir: "/var/run/pg socket",
		Database:  "postgres",
		User:      ReadOnlyRole,
	})
	require.NoError(t, err)
	require.Contains(t, dsn, "host='/var/run/pg socket'")
}

func TestBuildDSN_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  connConfig
	}{
		{"missing socket", connConfig{Database: "postgres", User: ReadOnlyRole}},
		{"missing user", connConfig{SocketDir: "/tmp", Database: "postgres"}},
		{"missing db", connConfig{SocketDir: "/tmp", User: ReadOnlyRole}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildDSN(tt.cfg)
			require.Error(t, err)
			require.Equal(t, core.CodeValidation, core.CodeOf(err))
		})
	}
}

func TestSocketDir_DefaultAndOverride(t *testing.T) {
	m := New(Options{Config: config.Config{}})
	require.Equal(t, defaultSocketDir, m.socketDir())

	cfg := config.Default()
	cfg.PGSocketDir = "/custom/socket"
	m2 := New(Options{Config: cfg})
	require.Equal(t, "/custom/socket", m2.socketDir())
}

func TestPoolsNilWhenNotConnected(t *testing.T) {
	m := New(Options{Config: config.Default()})
	require.Nil(t, m.ReadPool())
	require.Nil(t, m.PrivPool())
	// Close on an unconnected manager is a safe no-op.
	require.NotPanics(t, m.Close)
}
