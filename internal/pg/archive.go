package pg

import (
	"context"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// EnsureArchiving makes Postgres ship WAL to pgBackRest's archive — the
// prerequisite for `pgbackrest backup`, which otherwise fails with
// "archive_mode must be enabled". It mirrors the archive setup the predecessor
// (server-management) applied at install time: archive_mode=on,
// archive_command=pgbackrest archive-push, wal_level>=replica, wal_compression=on.
//
// Settings are written with ALTER SYSTEM as the postgres superuser (peer-auth via
// psql — the panel's pool roles are NOSUPERUSER and cannot ALTER SYSTEM), then
// activated: a full service restart when a postmaster-only setting
// (archive_mode/wal_level) changed, otherwise a config reload.
//
// It is idempotent and conservative: already-satisfied settings are left alone
// (returns false, no restart), and stronger existing values are preserved —
// wal_level=logical and archive_mode=always are never downgraded. Returns whether
// anything changed.
func (m *Manager) EnsureArchiving(ctx context.Context, stanza string) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pg: EnsureArchiving requires a Runner")
	}

	// Read the four settings in one shot. current_setting (not SHOW) so an empty
	// archive_command comes back as an empty field rather than tripping the
	// empty-value guard in showSetting.
	out, err := m.runPsql(ctx, defaultConnectDatabase,
		"SELECT current_setting('archive_mode'), current_setting('archive_command'), "+
			"current_setting('wal_level'), current_setting('wal_compression')")
	if err != nil {
		return false, err
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 4 {
		return false, core.InternalError("pg: unexpected archive settings output %q", out)
	}
	archiveMode := strings.TrimSpace(fields[0])
	archiveCommand := strings.TrimSpace(fields[1])
	walLevel := strings.TrimSpace(fields[2])
	walCompression := strings.TrimSpace(fields[3])

	var stmts []string
	needRestart := false

	// archive_mode must ship WAL. "always" (archive on a standby too) also
	// satisfies pgBackRest, so it is not downgraded. PGC_POSTMASTER → restart.
	if archiveMode != "on" && archiveMode != "always" {
		stmts = append(stmts, "ALTER SYSTEM SET archive_mode = 'on'")
		needRestart = true
	}

	// archive_command points WAL archiving at pgBackRest. Reloadable (SIGHUP).
	wantCmd := "pgbackrest --stanza=" + stanza + " archive-push %p"
	if archiveCommand != wantCmd {
		stmts = append(stmts, "ALTER SYSTEM SET archive_command = "+core.QuoteLiteral(wantCmd))
	}

	// pgBackRest needs wal_level >= replica. Only raise it from minimal; leave
	// replica/logical as-is. PGC_POSTMASTER → restart.
	if walLevel == "minimal" {
		stmts = append(stmts, "ALTER SYSTEM SET wal_level = 'replica'")
		needRestart = true
	}

	// wal_compression shrinks the WAL pgBackRest archives. Any non-off value
	// (on/lz4/zstd/pglz) is fine; only enable it when explicitly off. Reloadable.
	if walCompression == "off" {
		stmts = append(stmts, "ALTER SYSTEM SET wal_compression = 'on'")
	}

	if len(stmts) == 0 {
		return false, nil
	}

	// A postmaster-only change (archive_mode/wal_level) needs a restart, which
	// could fail to come back up. Snapshot postgresql.auto.conf BEFORE writing
	// anything so restartWithRollback can revert to last-known-good if it does.
	var snap autoConfSnapshot
	if needRestart {
		var err error
		if snap, err = m.snapshotAutoConf(ctx); err != nil {
			return false, err
		}
	}

	for _, s := range stmts {
		if _, err := m.runPsql(ctx, defaultConnectDatabase, s); err != nil {
			return false, core.ExecError("pg: enabling WAL archiving failed").Wrap(err)
		}
	}

	if needRestart {
		if err := m.restartWithRollback(ctx, snap, "WAL archiving config"); err != nil {
			return false, err
		}
		m.log.InfoCtx(ctx, "enabled WAL archiving for pgBackRest (restarted Postgres)", "stanza", stanza)
		return true, nil
	}

	if _, err := m.runPsql(ctx, defaultConnectDatabase, "SELECT pg_reload_conf()"); err != nil {
		return false, err
	}
	m.log.InfoCtx(ctx, "updated WAL archiving for pgBackRest (reloaded Postgres)", "stanza", stanza)
	return true, nil
}
