package store

// schemaStatements are the idempotent DDL statements that define the panel's
// local SQLite state. They are applied in order by migrate(); every statement
// uses IF NOT EXISTS so applying them repeatedly is a no-op.
//
// This mirrors the "Local data model" in the design doc (§8): instance, config,
// auth, audit_log, backup_history, restore_tests, alerts, telemetry_buffer.
var schemaStatements = []string{
	// PRAGMAs are set on the connection in Open; kept out of the schema list.

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
}

// connectionPragmas are PRAGMA statements applied to the connection on Open.
var connectionPragmas = []string{
	`PRAGMA journal_mode = WAL`,
	`PRAGMA busy_timeout = 5000`,
	`PRAGMA foreign_keys = ON`,
	`PRAGMA synchronous = NORMAL`,
}
