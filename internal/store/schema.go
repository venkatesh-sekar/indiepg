package store

// schemaStatements are the idempotent DDL statements that define the panel's
// local SQLite state. They are applied in order by migrate(); every statement
// uses IF NOT EXISTS so applying them repeatedly is a no-op.
//
// This mirrors the "Local data model" in the design doc (§8): instance, config,
// auth, audit_log, backup_history, restore_tests, alerts, telemetry_buffer.
var schemaStatements = []string{
	// PRAGMAs are applied per-connection via the DSN in Open (see buildDSN); kept
	// out of the schema list.

	// instance — the panel's stable identity (one row).
	`CREATE TABLE IF NOT EXISTS instance (
		id              INTEGER PRIMARY KEY CHECK (id = 1),
		instance_id     TEXT    NOT NULL,
		label           TEXT    NOT NULL DEFAULT '',
		hostname        TEXT    NOT NULL DEFAULT '',
		pg_system_id    TEXT    NOT NULL DEFAULT '',
		panel_version   TEXT    NOT NULL DEFAULT '',
		created_at      TEXT    NOT NULL
	)`,

	// config — key/value panel configuration (binding, OTLP endpoint, ...).
	`CREATE TABLE IF NOT EXISTS config (
		key         TEXT PRIMARY KEY,
		value       TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`,

	// auth — admin password hash + session secret + lockout counters (one row).
	`CREATE TABLE IF NOT EXISTS auth (
		id               INTEGER PRIMARY KEY CHECK (id = 1),
		password_hash    TEXT    NOT NULL DEFAULT '',
		session_secret   BLOB    NOT NULL,
		failed_attempts  INTEGER NOT NULL DEFAULT 0,
		locked_until     TEXT,
		updated_at       TEXT    NOT NULL
	)`,

	// audit_log — every panel action (append-only in practice).
	`CREATE TABLE IF NOT EXISTS audit_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          TEXT    NOT NULL,
		actor       TEXT    NOT NULL,
		action      TEXT    NOT NULL,
		target      TEXT    NOT NULL DEFAULT '',
		summary     TEXT    NOT NULL DEFAULT '',
		result      TEXT    NOT NULL DEFAULT '',
		detail      TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log (ts)`,

	// backup_history — pgBackRest run records with stats.
	`CREATE TABLE IF NOT EXISTS backup_history (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		label          TEXT    NOT NULL,
		backup_type    TEXT    NOT NULL,
		started_at     TEXT    NOT NULL,
		stopped_at     TEXT,
		size_bytes     INTEGER NOT NULL DEFAULT 0,
		database_bytes INTEGER NOT NULL DEFAULT 0,
		repo_bytes     INTEGER NOT NULL DEFAULT 0,
		wal_start      TEXT    NOT NULL DEFAULT '',
		wal_stop       TEXT    NOT NULL DEFAULT '',
		result         TEXT    NOT NULL DEFAULT '',
		repo_path      TEXT    NOT NULL DEFAULT '',
		error          TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_backup_history_started ON backup_history (started_at)`,

	// restore_tests — periodic "do my backups actually restore?" results.
	`CREATE TABLE IF NOT EXISTS restore_tests (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		tested_at      TEXT    NOT NULL,
		source_label   TEXT    NOT NULL DEFAULT '',
		verified_rows  INTEGER NOT NULL DEFAULT 0,
		result         TEXT    NOT NULL DEFAULT '',
		duration_ms    INTEGER NOT NULL DEFAULT 0,
		detail         TEXT    NOT NULL DEFAULT ''
	)`,

	// alerts — rule definitions, channel config, and cooldown/fire state.
	`CREATE TABLE IF NOT EXISTS alerts (
		id            TEXT PRIMARY KEY,
		name          TEXT    NOT NULL,
		enabled       INTEGER NOT NULL DEFAULT 1,
		definition    TEXT    NOT NULL DEFAULT '{}',
		severity      TEXT    NOT NULL DEFAULT 'warning',
		state         TEXT    NOT NULL DEFAULT 'ok',
		last_fired_at TEXT,
		last_eval_at  TEXT,
		updated_at    TEXT    NOT NULL
	)`,

	// telemetry_buffer — recent metric samples for the in-panel dashboard.
	`CREATE TABLE IF NOT EXISTS telemetry_buffer (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          TEXT    NOT NULL,
		metric      TEXT    NOT NULL,
		value       REAL    NOT NULL,
		labels      TEXT    NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_telemetry_metric_ts ON telemetry_buffer (metric, ts)`,

	// migrations — the source of truth for this panel's database-migration jobs
	// (direct-pull single-db/cluster + ssh-less handshake). The S3 session doc is
	// only the cross-panel channel; status/phase/progress/errors/rowcounts live
	// here so the UI can poll a single local store.
	`CREATE TABLE IF NOT EXISTS migrations (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		mode            TEXT    NOT NULL,
		role            TEXT    NOT NULL DEFAULT '',
		status          TEXT    NOT NULL,
		phase           TEXT    NOT NULL DEFAULT '',
		source_summary  TEXT    NOT NULL DEFAULT '',
		target_database TEXT    NOT NULL DEFAULT '',
		overwrite       INTEGER NOT NULL DEFAULT 0,
		code            TEXT    NOT NULL DEFAULT '',
		progress_done   INTEGER NOT NULL DEFAULT 0,
		progress_total  INTEGER NOT NULL DEFAULT 0,
		bytes_total     INTEGER NOT NULL DEFAULT 0,
		error           TEXT    NOT NULL DEFAULT '',
		row_counts_src  TEXT    NOT NULL DEFAULT '{}',
		row_counts_tgt  TEXT    NOT NULL DEFAULT '{}',
		created_at      TEXT    NOT NULL,
		updated_at      TEXT    NOT NULL,
		finished_at     TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_migrations_created ON migrations (created_at)`,

	// dropoff_sessions — the local source of truth for "drop-off link" migrations:
	// the panel mints two presigned S3 PUT URLs, a source box pushes one database's
	// dump + meta.json to them, and the panel imports from S3. Only the S3 object
	// KEYS are stored here (never the presigned URLs or any password). A dedicated
	// table (not new columns on `migrations`) because the schema runner only issues
	// CREATE TABLE IF NOT EXISTS and SQLite's ADD COLUMN has no IF NOT EXISTS, so
	// re-running ALTERs would crash migrate() on the second startup.
	`CREATE TABLE IF NOT EXISTS dropoff_sessions (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		code            TEXT    NOT NULL UNIQUE,
		migration_id    INTEGER,
		dump_key        TEXT    NOT NULL,
		meta_key        TEXT    NOT NULL,
		target_database TEXT    NOT NULL,
		overwrite       INTEGER NOT NULL DEFAULT 0,
		created_target  INTEGER NOT NULL DEFAULT 0,
		status          TEXT    NOT NULL,
		error           TEXT    NOT NULL DEFAULT '',
		byte_size       INTEGER NOT NULL DEFAULT 0,
		expires_at      TEXT    NOT NULL,
		created_at      TEXT    NOT NULL,
		updated_at      TEXT    NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_dropoff_expires ON dropoff_sessions (expires_at)`,
}

// additiveColumn describes a column added to a table AFTER its initial CREATE in a
// prior build. The schema runner CREATEs tables with IF NOT EXISTS, which is a no-op
// on an existing table, so a column added only to a CREATE TABLE statement above never
// appears on a database created before the column existed. SQLite's ALTER TABLE ADD
// COLUMN has no IF NOT EXISTS, so a bare ALTER in schemaStatements would crash migrate()
// on the second startup — migrate() instead guards each one on a PRAGMA table_info
// check (columnExists) to stay idempotent across restarts.
type additiveColumn struct {
	table  string
	column string
	ddl    string
}

// additiveColumns are applied (each guarded) after schemaStatements by migrate().
var additiveColumns = []additiveColumn{
	{
		// created_target records whether the import worker CREATED the drop-off target
		// database (vs restoring into a pre-existing one). Startup reconciliation of an
		// interrupted import uses it to drop a partially-restored target it created so a
		// non-overwrite retry is not blocked by leftover tables. Added after the
		// dropoff_sessions table first shipped without it; also present in the CREATE TABLE
		// above so fresh installs get it directly.
		table:  "dropoff_sessions",
		column: "created_target",
		ddl:    `ALTER TABLE dropoff_sessions ADD COLUMN created_target INTEGER NOT NULL DEFAULT 0`,
	},
}

// connectionPragmas are encoded into the DSN by buildDSN so the driver applies
// them to EVERY connection it opens, not just the first. Each value is run as
// "PRAGMA <value>" by the modernc.org/sqlite driver on connection open.
// journal_mode is persistent (file-header), but the rest (busy_timeout,
// foreign_keys, synchronous) are per-connection and would silently revert on a
// reopened connection if applied only once on the pooled *sql.DB.
var connectionPragmas = []string{
	"journal_mode(WAL)",
	"busy_timeout(5000)",
	"foreign_keys(on)",
	"synchronous(NORMAL)",
}
