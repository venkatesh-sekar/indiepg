package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// defaultSocketDir is used when the config does not specify a socket directory.
const defaultSocketDir = "/var/run/postgresql"

// poolMaxConns caps each pool. The read-only pool serves the query box and
// browsing; the privileged pool serves rare guided actions, so both stay small.
const (
	readPoolMaxConns = int32(8)
	privPoolMaxConns = int32(4)
)

// connConfig captures the inputs to a DSN. It is split out so DSN construction
// is a pure, unit-testable function with no IO.
type connConfig struct {
	SocketDir        string
	Database         string
	User             string
	StatementTimeout time.Duration
}

// socketDir resolves the unix-socket directory, falling back to the default.
func (m *Manager) socketDir() string {
	if m.cfg.PGSocketDir != "" {
		return m.cfg.PGSocketDir
	}
	return defaultSocketDir
}

// buildDSN constructs a libpq key/value connection string for a local
// unix-socket connection. The socket directory is passed as host (pgx treats an
// absolute host path as a socket directory). A statement_timeout runtime param
// bounds every query on the connection.
//
// Values are escaped per libpq rules (single quotes wrap any value containing a
// space, and embedded quotes/backslashes are escaped) so a path with spaces or
// an unusual identifier cannot break out of its field.
func buildDSN(c connConfig) (string, error) {
	if c.SocketDir == "" {
		return "", core.ValidationError("pg: socket directory is required")
	}
	if c.User == "" {
		return "", core.ValidationError("pg: connection user is required")
	}
	if c.Database == "" {
		return "", core.ValidationError("pg: connection database is required")
	}

	pairs := []struct{ k, v string }{
		{"host", c.SocketDir},
		{"user", c.User},
		{"dbname", c.Database},
		{"sslmode", "disable"}, // local unix socket: TLS is neither needed nor available
		{"application_name", "pgpanel"},
	}

	out := ""
	for i, p := range pairs {
		if i > 0 {
			out += " "
		}
		out += p.k + "=" + libpqEscape(p.v)
	}

	if c.StatementTimeout > 0 {
		ms := c.StatementTimeout.Milliseconds()
		out += fmt.Sprintf(" statement_timeout=%d", ms)
	}
	return out, nil
}

// libpqEscape quotes a libpq connection-string value when it contains
// whitespace, a single quote, a backslash, or is empty, escaping embedded
// quotes and backslashes.
func libpqEscape(v string) string {
	needsQuote := v == ""
	for _, r := range v {
		if r == ' ' || r == '\t' || r == '\n' || r == '\'' || r == '\\' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return v
	}
	escaped := make([]rune, 0, len(v)+2)
	escaped = append(escaped, '\'')
	for _, r := range v {
		if r == '\'' || r == '\\' {
			escaped = append(escaped, '\\')
		}
		escaped = append(escaped, r)
	}
	escaped = append(escaped, '\'')
	return string(escaped)
}

// Connect opens both pgxpool pools over the unix socket: a read-only pool
// connected as ReadOnlyRole and a privileged pool connected as AdminRole. It is
// idempotent — calling it again while connected is a no-op. Both pools are
// pinged before Connect returns so a bad socket/role surfaces immediately.
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readPool != nil && m.privPool != nil {
		return nil
	}

	socket := m.socketDir()

	readPool, err := m.openPool(ctx, connConfig{
		SocketDir:        socket,
		Database:         defaultConnectDatabase,
		User:             ReadOnlyRole,
		StatementTimeout: m.cfg.StatementTimeout,
	}, readPoolMaxConns)
	if err != nil {
		return core.InternalError("pg: opening read-only pool").Wrap(err)
	}

	privPool, err := m.openPool(ctx, connConfig{
		SocketDir: socket,
		Database:  defaultConnectDatabase,
		User:      AdminRole,
		// the privileged pool intentionally has no forced statement timeout so
		// long-running guided maintenance (e.g. CREATE INDEX) is not killed.
	}, privPoolMaxConns)
	if err != nil {
		readPool.Close()
		return core.InternalError("pg: opening privileged pool").Wrap(err)
	}

	m.readPool = readPool
	m.privPool = privPool
	return nil
}

// openPool builds a pgxpool.Pool for the given connConfig and pings it.
func (m *Manager) openPool(ctx context.Context, c connConfig, maxConns int32) (*pgxpool.Pool, error) {
	dsn, err := buildDSN(c)
	if err != nil {
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, core.InternalError("pg: parsing pool config").Wrap(err)
	}
	poolCfg.MaxConns = maxConns
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, core.InternalError("pg: creating pool").Wrap(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, core.ExecError("pg: pinging %s as %s", c.Database, c.User).Wrap(err)
	}
	return pool, nil
}

// Close releases both pools. It is safe to call when not connected and safe to
// call multiple times.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readPool != nil {
		m.readPool.Close()
		m.readPool = nil
	}
	if m.privPool != nil {
		m.privPool.Close()
		m.privPool = nil
	}
}

// ReadPool returns the read-only pool, or nil if not connected.
func (m *Manager) ReadPool() *pgxpool.Pool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.readPool
}

// PrivPool returns the privileged pool, or nil if not connected.
func (m *Manager) PrivPool() *pgxpool.Pool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.privPool
}
