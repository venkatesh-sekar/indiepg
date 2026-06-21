package pg

import (
	"context"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// DataDirectory returns the cluster's data directory (SHOW data_directory),
// used as pgBackRest's pg1-path. It reads over the read-only pool when connected,
// otherwise via a one-shot psql query as the postgres OS user.
func (m *Manager) DataDirectory(ctx context.Context) (string, error) {
	return m.showSetting(ctx, "data_directory")
}

// Port returns the cluster's TCP port (SHOW port), used as pgBackRest's pg1-port.
func (m *Manager) Port(ctx context.Context) (string, error) {
	return m.showSetting(ctx, "port")
}

// showSetting reads a single Postgres GUC via SHOW. The setting name is a fixed
// caller-provided constant (never user input); it is validated against an
// identifier charset as defense-in-depth before being placed in the statement.
func (m *Manager) showSetting(ctx context.Context, name string) (string, error) {
	if !isGUCName(name) {
		return "", core.InternalError("pg: invalid setting name %q", name)
	}

	const queryFmt = "SHOW "
	if pool := m.ReadPool(); pool != nil {
		var v string
		if err := pool.QueryRow(ctx, queryFmt+name).Scan(&v); err != nil {
			return "", core.InternalError("pg: reading setting %q", name).Wrap(err)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", core.InternalError("pg: empty value for setting %q", name)
		}
		return v, nil
	}

	if m.runner == nil {
		return "", core.InternalError("pg: reading %q requires a Runner or an open pool", name)
	}
	out, err := m.runPsql(ctx, defaultConnectDatabase, queryFmt+name)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return "", core.InternalError("pg: empty value for setting %q", name)
	}
	return v, nil
}

// isGUCName reports whether name is a safe Postgres setting identifier
// (lowercase letters, digits, underscores). The callers pass only fixed
// constants; this guards against a future caller introducing interpolation.
func isGUCName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}
