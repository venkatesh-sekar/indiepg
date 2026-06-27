package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// terminalMigrationStatuses are the statuses a migration job can never leave.
// A job in any other status is considered "running" and is swept on restart.
var terminalMigrationStatuses = []string{"completed", "failed", "expired"}

// InsertMigration records a new migration job and returns its assigned id.
// CreatedAt/UpdatedAt are set to now when zero. RowCounts default to "{}".
func (s *Store) InsertMigration(ctx context.Context, m MigrationRecord) (int64, error) {
	now := nowRFC3339()
	created := now
	if !m.CreatedAt.IsZero() {
		created = m.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	updated := now
	if !m.UpdatedAt.IsZero() {
		updated = m.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if m.RowCountsSrc == "" {
		m.RowCountsSrc = "{}"
	}
	if m.RowCountsTgt == "" {
		m.RowCountsTgt = "{}"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO migrations
			(mode, role, status, phase, source_summary, target_database, overwrite,
			 code, progress_done, progress_total, bytes_total, error,
			 row_counts_src, row_counts_tgt, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Mode, m.Role, m.Status, m.Phase, m.SourceSummary, m.TargetDatabase, boolToInt(m.Overwrite),
		m.Code, m.ProgressDone, m.ProgressTotal, m.BytesTotal, m.Error,
		m.RowCountsSrc, m.RowCountsTgt, created, updated, nullTimeStr(m.FinishedAt))
	if err != nil {
		return 0, core.InternalError("insert migration").Wrap(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, core.InternalError("migration last insert id").Wrap(err)
	}
	return id, nil
}

const migrationColumns = `id, mode, role, status, phase, source_summary, target_database,
	overwrite, code, progress_done, progress_total, bytes_total, error,
	row_counts_src, row_counts_tgt, created_at, updated_at, finished_at`

// GetMigration returns the migration with the given id, or a CodeNotFound error
// if none exists.
func (s *Store) GetMigration(ctx context.Context, id int64) (*MigrationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+migrationColumns+`
		FROM migrations WHERE id = ?`, id)
	if err != nil {
		return nil, core.InternalError("query migration").Wrap(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, core.InternalError("iterate migration").Wrap(err)
		}
		return nil, core.NotFoundError("migration %d not found", id)
	}
	m, err := scanMigration(rows)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMigrationByCode returns the most recent migration carrying the given
// handshake code, or a CodeNotFound error if none exists.
func (s *Store) GetMigrationByCode(ctx context.Context, code string) (*MigrationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+migrationColumns+`
		FROM migrations WHERE code = ? ORDER BY id DESC LIMIT 1`, code)
	if err != nil {
		return nil, core.InternalError("query migration by code").Wrap(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, core.InternalError("iterate migration by code").Wrap(err)
		}
		return nil, core.NotFoundError("no migration with code %q", code)
	}
	m, err := scanMigration(rows)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMigrations returns up to limit migration records, newest first. limit <= 0
// defaults to 50.
func (s *Store) ListMigrations(ctx context.Context, limit int) ([]MigrationRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+migrationColumns+`
		FROM migrations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, core.InternalError("list migrations").Wrap(err)
	}
	defer rows.Close()

	var out []MigrationRecord
	for rows.Next() {
		m, err := scanMigration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate migrations").Wrap(err)
	}
	return out, nil
}

// UpdateMigration writes all mutable columns of m back by ID, always bumping
// updated_at to now.
func (s *Store) UpdateMigration(ctx context.Context, m MigrationRecord) error {
	if m.RowCountsSrc == "" {
		m.RowCountsSrc = "{}"
	}
	if m.RowCountsTgt == "" {
		m.RowCountsTgt = "{}"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE migrations SET
			mode = ?, role = ?, status = ?, phase = ?, source_summary = ?,
			target_database = ?, overwrite = ?, code = ?, progress_done = ?,
			progress_total = ?, bytes_total = ?, error = ?, row_counts_src = ?,
			row_counts_tgt = ?, updated_at = ?, finished_at = ?
		WHERE id = ?`,
		m.Mode, m.Role, m.Status, m.Phase, m.SourceSummary,
		m.TargetDatabase, boolToInt(m.Overwrite), m.Code, m.ProgressDone,
		m.ProgressTotal, m.BytesTotal, m.Error, m.RowCountsSrc,
		m.RowCountsTgt, nowRFC3339(), nullTimeStr(m.FinishedAt),
		m.ID)
	if err != nil {
		return core.InternalError("update migration").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return core.InternalError("update migration rows affected").Wrap(err)
	}
	if n == 0 {
		return core.NotFoundError("migration %d not found", m.ID)
	}
	return nil
}

// SweepRunningMigrations marks every non-terminal migration as failed with an
// "interrupted by panel restart" error and a finished timestamp, returning the
// number of rows affected. This is called best-effort on startup so the UI never
// shows a job stuck "running" after the panel was restarted mid-migration.
func (s *Store) SweepRunningMigrations(ctx context.Context) (int, error) {
	now := nowRFC3339()
	res, err := s.db.ExecContext(ctx, `
		UPDATE migrations SET
			status = 'failed', phase = '', error = 'interrupted by panel restart',
			finished_at = ?, updated_at = ?
		WHERE status NOT IN ('completed', 'failed', 'expired')`,
		now, now)
	if err != nil {
		return 0, core.InternalError("sweep running migrations").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, core.InternalError("sweep migrations rows affected").Wrap(err)
	}
	return int(n), nil
}

// scanMigration reads one migrations row into a MigrationRecord.
func scanMigration(rows rowScanner) (MigrationRecord, error) {
	var m MigrationRecord
	var overwrite int
	var created, updated string
	var finished sql.NullString
	if err := rows.Scan(&m.ID, &m.Mode, &m.Role, &m.Status, &m.Phase, &m.SourceSummary,
		&m.TargetDatabase, &overwrite, &m.Code, &m.ProgressDone, &m.ProgressTotal,
		&m.BytesTotal, &m.Error, &m.RowCountsSrc, &m.RowCountsTgt,
		&created, &updated, &finished); err != nil {
		return m, core.InternalError("scan migration").Wrap(err)
	}
	m.Overwrite = overwrite != 0
	var err error
	if m.CreatedAt, err = parseTime(created); err != nil {
		return m, err
	}
	if m.UpdatedAt, err = parseTime(updated); err != nil {
		return m, err
	}
	if finished.Valid && finished.String != "" {
		t, err := parseTime(finished.String)
		if err != nil {
			return m, err
		}
		m.FinishedAt = &t
	}
	return m, nil
}
