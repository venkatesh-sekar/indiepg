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

// ClaimDropoffForImport atomically transitions a startable drop-off session to
// 'importing', returning true ONLY for the single winning caller. It is the
// concurrency guard for POST /migrate/drops/{code}/start: two near-simultaneous
// starts (a double-click, two tabs, a retried request, direct API use) both pass
// the handler's status/stat checks, but only one wins this conditional UPDATE —
// the loser gets false and is rejected, so a single destructive target is never
// dropped+restored by two workers at once.
//
// A session is startable from 'uploaded' (the normal path) or 'failed' (a retry
// from the dump kept on failure). 'waiting_for_upload' is also accepted because
// the handler only reaches this call after confirming meta.json is present (the
// readiness flip may simply not have been persisted yet). 'importing',
// 'completed' and 'expired' never transition, so a started/finished/expired
// session is left untouched and the caller is told it already moved on.
func (s *Store) ClaimDropoffForImport(ctx context.Context, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET status = 'importing', error = '', updated_at = ?
		WHERE code = ? AND status IN ('waiting_for_upload','uploaded','failed')`,
		nowRFC3339(), code)
	if err != nil {
		return false, core.InternalError("claim dropoff for import").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, core.InternalError("claim dropoff rows affected").Wrap(err)
	}
	return n == 1, nil
}

// FinalizeDropoffFromImporting records a drop-off session's terminal outcome
// (status + error) but ONLY when it is still 'importing', returning true when the
// transition was applied. It is the guard against an import worker resurrecting a
// session that a concurrent cancel (or expiry sweep) already moved to a terminal
// state while the worker was mid-restore: a cancelled session must STAY cancelled
// even though the worker's restore may have finished. The worker only reaches its
// finalize after winning ClaimDropoffForImport, so the normal (un-cancelled) case
// always matches 'importing' and is applied.
func (s *Store) FinalizeDropoffFromImporting(ctx context.Context, code, status, errMsg string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET status = ?, error = ?, updated_at = ?
		WHERE code = ? AND status = 'importing'`,
		status, errMsg, nowRFC3339(), code)
	if err != nil {
		return false, core.InternalError("finalize dropoff from importing").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, core.InternalError("finalize dropoff rows affected").Wrap(err)
	}
	return n == 1, nil
}

// MarkDropoffCancelled records a cancel on a drop-off session — status 'failed'
// with the reason 'cancelled' — but ONLY when it is not already terminal
// ('completed'/'expired'), returning true when applied. The condition makes cancel
// race-safe against a completion (or sweep) that landed between the handler's read
// and this write: a genuinely-completed import must not be relabelled cancelled.
func (s *Store) MarkDropoffCancelled(ctx context.Context, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET status = 'failed', error = 'cancelled', updated_at = ?
		WHERE code = ? AND status NOT IN ('completed','expired')`,
		nowRFC3339(), code)
	if err != nil {
		return false, core.InternalError("mark dropoff cancelled").Wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, core.InternalError("mark dropoff cancelled rows affected").Wrap(err)
	}
	return n == 1, nil
}

// ListExpiredDropoffs returns drop-off sessions whose expiry has passed and whose
// S3 objects may still be at rest, oldest first, so the periodic sweep can delete
// the dump+metadata and mark them expired. limit <= 0 defaults to 100.
//
// The status filter deliberately EXCLUDES 'importing': an import that legitimately
// Started shortly before the (short) TTL can still be streaming/restoring past it
// under the worker's longer context, and reclaiming its dump out from under it
// would fail the job spuriously. Interrupted imports are handled separately by
// SweepRunningDropoffs on startup. It INCLUDES 'failed': a failed/abandoned
// session keeps its (up to multi-GiB) dump in S3 for retry, which must still be
// reclaimed once the TTL passes, or a full database lingers at rest indefinitely.
//
// It also INCLUDES 'completed': the import path deletes the dump+metadata on
// success best-effort, but a transient S3 error there would otherwise orphan the
// full database forever (a completed session is never otherwise revisited). Making
// the sweep reclaim completed-and-past-TTL sessions too is the backstop that
// upholds the invariant "a full database at rest must not linger past its TTL".
// The deletes are idempotent, so a completed session whose objects were already
// removed is a cheap no-op.
func (s *Store) ListExpiredDropoffs(ctx context.Context, now time.Time, limit int) ([]DropoffRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+dropoffColumns+`
		FROM dropoff_sessions
		WHERE status NOT IN ('expired','importing') AND expires_at <= ?
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
