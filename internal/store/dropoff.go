package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// dropoffColumns is the canonical column list for dropoff_sessions, kept in the
// exact order scanDropoff reads.
const dropoffColumns = `id, code, migration_id, dump_key, meta_key, target_database,
	overwrite, status, error, byte_size, expires_at, created_at, updated_at`

// nullInt64 renders an optional id as a nullable SQL argument.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// InsertDropoff records a new drop-off session and returns its assigned id.
// CreatedAt/UpdatedAt default to now when zero; ExpiresAt is required.
func (s *Store) InsertDropoff(ctx context.Context, d DropoffRecord) (int64, error) {
	now := nowRFC3339()
	created := now
	if !d.CreatedAt.IsZero() {
		created = d.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	updated := now
	if !d.UpdatedAt.IsZero() {
		updated = d.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO dropoff_sessions
			(code, migration_id, dump_key, meta_key, target_database, overwrite,
			 status, error, byte_size, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Code, nullInt64(d.MigrationID), d.DumpKey, d.MetaKey, d.TargetDatabase, boolToInt(d.Overwrite),
		d.Status, d.Error, d.ByteSize, d.ExpiresAt.UTC().Format(time.RFC3339Nano), created, updated)
	if err != nil {
		return 0, core.InternalError("insert dropoff session").Wrap(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, core.InternalError("dropoff last insert id").Wrap(err)
	}
	return id, nil
}

// GetDropoffByCode returns the drop-off session with the given code, or a
// CodeNotFound error if none exists.
func (s *Store) GetDropoffByCode(ctx context.Context, code string) (*DropoffRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+dropoffColumns+`
		FROM dropoff_sessions WHERE code = ?`, code)
	if err != nil {
		return nil, core.InternalError("query dropoff by code").Wrap(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, core.InternalError("iterate dropoff by code").Wrap(err)
		}
		return nil, core.NotFoundError("no drop-off session with code %q", code)
	}
	d, err := scanDropoff(rows)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateDropoff writes the mutable columns of a drop-off session back by code
// (migration_id, status, error, byte_size), always bumping updated_at. The keys,
// target, overwrite flag and expiry are immutable after insert.
func (s *Store) UpdateDropoff(ctx context.Context, d DropoffRecord) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET
			migration_id = ?, status = ?, error = ?, byte_size = ?, updated_at = ?
		WHERE code = ?`,
		nullInt64(d.MigrationID), d.Status, d.Error, d.ByteSize, nowRFC3339(), d.Code)
	if err != nil {
		return core.InternalError("update dropoff session").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return core.InternalError("update dropoff rows affected").Wrap(err)
	}
	if n == 0 {
		return core.NotFoundError("no drop-off session with code %q", d.Code)
	}
	return nil
}

// ListExpiredDropoffs returns non-terminal drop-off sessions whose expiry has
// passed, oldest first, so the periodic sweep can delete their S3 objects and
// mark them expired. limit <= 0 defaults to 100.
func (s *Store) ListExpiredDropoffs(ctx context.Context, now time.Time, limit int) ([]DropoffRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+dropoffColumns+`
		FROM dropoff_sessions
		WHERE status NOT IN ('completed','failed','expired') AND expires_at <= ?
		ORDER BY id LIMIT ?`,
		now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, core.InternalError("list expired dropoffs").Wrap(err)
	}
	defer rows.Close()

	var out []DropoffRecord
	for rows.Next() {
		d, err := scanDropoff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate expired dropoffs").Wrap(err)
	}
	return out, nil
}

// SweepRunningDropoffs marks every drop-off session left "importing" by a panel
// restart as failed (its worker goroutine is gone), returning the rows affected.
// Mirrors SweepRunningMigrations; called best-effort on startup.
func (s *Store) SweepRunningDropoffs(ctx context.Context) (int, error) {
	now := nowRFC3339()
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET
			status = 'failed', error = 'interrupted by panel restart', updated_at = ?
		WHERE status = 'importing'`, now)
	if err != nil {
		return 0, core.InternalError("sweep running dropoffs").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, core.InternalError("sweep dropoffs rows affected").Wrap(err)
	}
	return int(n), nil
}

// scanDropoff reads one dropoff_sessions row into a DropoffRecord.
func scanDropoff(rows rowScanner) (DropoffRecord, error) {
	var d DropoffRecord
	var overwrite int
	var migrationID sql.NullInt64
	var expires, created, updated string
	if err := rows.Scan(&d.ID, &d.Code, &migrationID, &d.DumpKey, &d.MetaKey, &d.TargetDatabase,
		&overwrite, &d.Status, &d.Error, &d.ByteSize, &expires, &created, &updated); err != nil {
		return d, core.InternalError("scan dropoff").Wrap(err)
	}
	d.Overwrite = overwrite != 0
	if migrationID.Valid {
		v := migrationID.Int64
		d.MigrationID = &v
	}
	var err error
	if d.ExpiresAt, err = parseTime(expires); err != nil {
		return d, err
	}
	if d.CreatedAt, err = parseTime(created); err != nil {
		return d, err
	}
	if d.UpdatedAt, err = parseTime(updated); err != nil {
		return d, err
	}
	return d, nil
}
