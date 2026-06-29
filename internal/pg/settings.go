package pg

import (
	"context"
	"strconv"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// DataDirectory returns the cluster's data directory (SHOW data_directory),
// used as pgBackRest's pg1-path. data_directory is a superuser-only GUC, so the
// panel's non-superuser pool role cannot read it; showSetting falls back to a
// psql query as the postgres OS user (peer-auth superuser) in that case.
func (m *Manager) DataDirectory(ctx context.Context) (string, error) {
	return m.showSetting(ctx, "data_directory")
}

// Port returns the cluster's TCP port (SHOW port), used as pgBackRest's pg1-port.
func (m *Manager) Port(ctx context.Context) (string, error) {
	return m.showSetting(ctx, "port")
}

// MajorVersion returns the cluster's PostgreSQL major version, read from the
// server_version_num GUC (e.g. 170004 → 17). It is used to fill the "%d" in
// catalog package templates (postgresql-17-pgvector). server_version_num is a
// plain integer GUC readable by any role, so it normally comes straight from
// the read-only pool, but showSetting transparently falls back to a privileged
// psql query when no pool is connected.
func (m *Manager) MajorVersion(ctx context.Context) (int, error) {
	raw, err := m.showSetting(ctx, "server_version_num")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, core.InternalError("pg: parsing server_version_num %q", raw).Wrap(err)
	}
	if n <= 0 {
		return 0, core.InternalError("pg: invalid server_version_num %d", n)
	}
	return n / 10000, nil
}

// showSetting reads a single Postgres GUC via SHOW. The setting name is a fixed
// caller-provided constant (never user input); it is validated against an
// identifier charset as defense-in-depth before being placed in the statement.
func (m *Manager) showSetting(ctx context.Context, name string) (string, error) {
	if !isGUCName(name) {
		return "", core.InternalError("pg: invalid setting name %q", name)
	}

	const queryFmt = "SHOW "

	// Prefer the read-only pool when connected. But a superuser-only GUC (e.g.
	// data_directory) is NOT readable by the panel's non-superuser pool role — the
	// SHOW fails with "must be superuser or have privileges of
	// pg_read_all_settings". On any pool read error (or an empty value) fall back
	// to a one-shot psql query as the postgres OS user (peer-auth superuser), which
	// can read every setting. Without a runner there is no fallback, so the pool
	// error is returned as-is.
	if pool := m.ReadPool(); pool != nil {
		var v string
		err := pool.QueryRow(ctx, queryFmt+name).Scan(&v)
		if err == nil {
			if v = strings.TrimSpace(v); v != "" {
				return v, nil
			}
		}
		if m.runner == nil {
			if err != nil {
				return "", core.InternalError("pg: reading setting %q", name).Wrap(err)
			}
			return "", core.InternalError("pg: empty value for setting %q", name)
		}
		// Pool could not read it; fall through to the privileged psql path below.
	} else if m.runner == nil {
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
