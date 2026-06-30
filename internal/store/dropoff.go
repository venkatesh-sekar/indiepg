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
	overwrite, created_target, status, error, byte_size, expires_at, created_at, updated_at`

// dropoffExpiresLayout is a FIXED-WIDTH UTC timestamp layout for the expires_at
// column. The expiry sweep filters `expires_at <= ?` as a SQLite TEXT comparison,
// which is only correct if the stored and queried strings sort lexicographically
// in time order. time.RFC3339Nano is variable-width — it drops trailing fractional
// zeros, so `...:00:00Z` and `...:00:00.5Z` sort by their differing 7th character
// where '.' (0x2E) < 'Z' (0x5A), inverting order across the whole-second boundary
// and letting a just-expired row be skipped. Pinning nine fractional digits makes
// string order match time order. Values written with this layout remain
// RFC3339Nano-parseable, so scanDropoff/parseTime read them back unchanged.
const dropoffExpiresLayout = "2006-01-02T15:04:05.000000000Z07:00"

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
			(code, migration_id, dump_key, meta_key, target_database, overwrite, created_target,
			 status, error, byte_size, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Code, nullInt64(d.MigrationID), d.DumpKey, d.MetaKey, d.TargetDatabase, boolToInt(d.Overwrite), boolToInt(d.CreatedTarget),
		d.Status, d.Error, d.ByteSize, d.ExpiresAt.UTC().Format(dropoffExpiresLayout), created, updated)
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
// target, overwrite flag, created_target flag and expiry are immutable through this
// method; created_target is set only by MarkDropoffTargetCreated.
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
// 'completed', 'canceled' and 'expired' never transition, so a started/finished/
// cancelled/expired session is left untouched and the caller is told it already
// moved on. 'canceled' is terminal here ON PURPOSE: the presigned PUT URLs cannot
// be revoked, so a cancelled session must never be re-claimable for import even if
// its dump is re-uploaded.
//
// The claim ALSO requires expires_at > now ATOMICALLY: the handler checks expiry
// before calling, but the expiry sweep can reclaim (and delete the dump of) a row
// concurrently, so without the predicate Start could win the claim on a row whose
// objects the sweep is about to remove. The cutoff uses the same fixed-width layout
// as the stored value so the TEXT comparison matches time order (see
// dropoffExpiresLayout); a past-TTL row therefore never claims.
func (s *Store) ClaimDropoffForImport(ctx context.Context, code string) (bool, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET status = 'importing', error = '', updated_at = ?
		WHERE code = ? AND status IN ('waiting_for_upload','uploaded','failed')
		  AND expires_at > ?`,
		now.Format(time.RFC3339Nano), code, now.Format(dropoffExpiresLayout))
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

// MarkDropoffCancelled records a cancel on a drop-off session — the TERMINAL
// 'canceled' status with the reason 'cancelled' — but ONLY when it is not already
// terminal ('completed'/'expired') AND not actively 'importing', returning true
// when applied.
//
// 'canceled' (not the retryable 'failed') is deliberate: presigned PUT URLs cannot
// be revoked once minted, so a cancel recorded as 'failed' would leave the session
// re-startable (ClaimDropoffForImport claims 'failed' for retry) and a holder of
// the URL could re-upload and restart it. 'canceled' is excluded from the claim, so
// the cancel is truly terminal.
//
// Excluding 'importing' is the atomic guard behind the handler's "cannot cancel a
// running import" refusal: even if a concurrent Start flipped the row to 'importing'
// between the handler's read and this write, the conditional UPDATE no-ops (returns
// false) so the handler does NOT then delete the dump out from under a live restore
// (which could interrupt an overwrite after the original was dropped, then delete
// the only recovery copy). The condition also keeps cancel race-safe against a
// completion/sweep: a genuinely-completed import must not be relabelled cancelled.
func (s *Store) MarkDropoffCancelled(ctx context.Context, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET status = 'canceled', error = 'cancelled', updated_at = ?
		WHERE code = ? AND status NOT IN ('completed','expired','importing')`,
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
// SweepRunningDropoffs on startup. It INCLUDES 'failed' and 'canceled': a failed/
// abandoned session keeps its (up to multi-GiB) dump in S3 for retry, and a cancel
// deletes the objects best-effort but may have hit a transient S3 error — both must
// still be reclaimed once the TTL passes, or a full database lingers at rest
// indefinitely. The deletes are idempotent, so an already-cleaned session is a
// cheap no-op.
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
		now.UTC().Format(dropoffExpiresLayout), limit)
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

// ListActiveDropoffs returns the resumable, not-yet-expired drop-off sessions
// (waiting_for_upload / uploaded / importing / failed), newest first, so the panel
// can re-discover a session whose minted code was lost to a browser reload, tab
// discard or close — the operator returns to a status list and can resume
// Start/Cancel (or, for a failed import, Retry/Cancel) instead of being stranded
// until the expiry sweep.
//
// It returns only the safe, re-servable columns (the presigned URLs and the push
// command were NEVER stored, only the keys), so this can never re-leak the upload
// secret. It INCLUDES unexpired 'failed' sessions: a failed import keeps its dump in
// S3 for retry (ClaimDropoffForImport re-claims 'failed'), so after a reload the UI
// must still offer a Retry/Cancel path while the artifact lingers — otherwise the
// dump sits orphaned until expiry with no way to act on it. It EXCLUDES the truly
// terminal states 'canceled', 'completed' and 'expired' (their dump is gone or being
// reclaimed) so a finished/cancelled session never resurfaces as resumable.
// limit <= 0 defaults to 50.
func (s *Store) ListActiveDropoffs(ctx context.Context, now time.Time, limit int) ([]DropoffRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+dropoffColumns+`
		FROM dropoff_sessions
		WHERE status IN ('waiting_for_upload','uploaded','importing','failed') AND expires_at > ?
		ORDER BY id DESC LIMIT ?`,
		now.UTC().Format(dropoffExpiresLayout), limit)
	if err != nil {
		return nil, core.InternalError("list active dropoffs").Wrap(err)
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
		return nil, core.InternalError("iterate active dropoffs").Wrap(err)
	}
	return out, nil
}

// MarkDropoffTargetCreated records that THIS import created the session's target
// database (as opposed to restoring into a pre-existing one), so startup
// reconciliation of an interrupted import can drop a partially-restored target it
// created and thus unblock a non-overwrite retry. The worker sets it right after the
// target is created, BEFORE the (potentially long) restore, so a crash mid-restore
// still finds the flag set. Narrow (touches only created_target) and idempotent:
// re-running it, or running it after the row moved on, is a harmless no-op.
func (s *Store) MarkDropoffTargetCreated(ctx context.Context, code string) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE dropoff_sessions SET created_target = 1, updated_at = ?
		WHERE code = ?`, nowRFC3339(), code); err != nil {
		return core.InternalError("mark dropoff target created").Wrap(err)
	}
	return nil
}

// ReconcileImportingDropoffs reconciles drop-off sessions left 'importing' by a panel
// restart (their import worker goroutine is gone). Unlike a blind sweep-to-failed, it
// consults each session's linked migration row so the persisted outcome matches what
// actually happened before the crash:
//
//   - If the linked migration COMPLETED (the import verified the restore and recorded
//     success in the small window before the dropoff row itself was finalized), the
//     session is marked 'completed'. Any S3 objects whose success-path delete had not
//     run are reclaimed later by the expiry sweep — so a crash after S3 deletion no
//     longer strands a 'failed' session with no retry artifact (it reads as completed).
//   - Otherwise the session is marked 'failed'. When THIS import had created the target
//     database in a NON-overwrite restore (created_target set, overwrite false), the
//     session is returned in toDrop so the caller can drop that partially-restored
//     target — otherwise its leftover tables read as non-empty and block every future
//     non-overwrite retry from the kept-in-S3 dump forever. A pre-existing target (the
//     operator declined a destructive overwrite) and an overwrite target (a retry
//     re-drops it anyway) are NEVER returned for dropping.
//
// The store cannot drop a Postgres database, so the actual DROP is performed by the
// server caller, which owns the engine. Best-effort: called on startup.
func (s *Store) ReconcileImportingDropoffs(ctx context.Context) ([]DropoffRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+dropoffColumns+`
		FROM dropoff_sessions WHERE status = 'importing' ORDER BY id`)
	if err != nil {
		return nil, core.InternalError("list importing dropoffs").Wrap(err)
	}
	var importing []DropoffRecord
	for rows.Next() {
		d, serr := scanDropoff(rows)
		if serr != nil {
			rows.Close()
			return nil, serr
		}
		importing = append(importing, d)
	}
	if cerr := rows.Err(); cerr != nil {
		rows.Close()
		return nil, core.InternalError("iterate importing dropoffs").Wrap(cerr)
	}
	// Close BEFORE the per-row migration reads/updates below: they run on the same
	// pooled connection and must not interleave with an open cursor.
	rows.Close()

	now := nowRFC3339()
	var toDrop []DropoffRecord
	for _, d := range importing {
		completed, cerr := s.migrationCompleted(ctx, d.MigrationID)
		if cerr != nil {
			return nil, cerr
		}
		status, errMsg := "failed", "interrupted by panel restart"
		if completed {
			status, errMsg = "completed", ""
		}
		// Conditional on still 'importing' for idempotency, matching
		// FinalizeDropoffFromImporting (there is no concurrent worker at startup).
		if _, uerr := s.db.ExecContext(ctx, `
			UPDATE dropoff_sessions SET status = ?, error = ?, updated_at = ?
			WHERE code = ? AND status = 'importing'`, status, errMsg, now, d.Code); uerr != nil {
			return nil, core.InternalError("reconcile importing dropoff %s", d.Code).Wrap(uerr)
		}
		if !completed && d.CreatedTarget && !d.Overwrite {
			toDrop = append(toDrop, d)
		}
	}
	return toDrop, nil
}

// migrationCompleted reports whether the migration with id (if any) is in the terminal
// 'completed' state. A nil id, a missing row, or any non-completed status yields false.
// 'completed' is terminal and never swept, so this read is reliable regardless of
// whether SweepRunningMigrations has already run earlier in the same startup.
func (s *Store) migrationCompleted(ctx context.Context, id *int64) (bool, error) {
	if id == nil {
		return false, nil
	}
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM migrations WHERE id = ?`, *id).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, core.InternalError("read linked migration %d", *id).Wrap(err)
	}
	return status == "completed", nil
}

// CountUncleanedDropoffs returns how many drop-off sessions may still own S3 objects
// in the currently-configured bucket — every session whose status is NOT terminally
// 'expired'. The expiry sweep flips a row to 'expired' ONLY after both object deletes
// succeed, so 'expired' is the only status that PROVES the dump+metadata are gone: a
// 'failed' session keeps its dump for retry, and a 'completed'/'canceled' session may
// still hold objects whose best-effort cleanup delete failed transiently.
//
// It gates an S3-target change (handleUpdateConfig): re-pointing the panel at a new
// bucket/credentials while any non-'expired' session exists would orphan that
// session's dump in the OLD bucket and break its import/retry/cleanup, so the change
// is refused until every session has drained to 'expired'.
func (s *Store) CountUncleanedDropoffs(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dropoff_sessions WHERE status <> 'expired'`).Scan(&n); err != nil {
		return 0, core.InternalError("count uncleaned dropoffs").Wrap(err)
	}
	return n, nil
}

// scanDropoff reads one dropoff_sessions row into a DropoffRecord.
func scanDropoff(rows rowScanner) (DropoffRecord, error) {
	var d DropoffRecord
	var overwrite, createdTarget int
	var migrationID sql.NullInt64
	var expires, created, updated string
	if err := rows.Scan(&d.ID, &d.Code, &migrationID, &d.DumpKey, &d.MetaKey, &d.TargetDatabase,
		&overwrite, &createdTarget, &d.Status, &d.Error, &d.ByteSize, &expires, &created, &updated); err != nil {
		return d, core.InternalError("scan dropoff").Wrap(err)
	}
	d.Overwrite = overwrite != 0
	d.CreatedTarget = createdTarget != 0
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
