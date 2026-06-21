package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

func nullTimeStr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// InsertBackup records a backup run and returns its id.
func (s *Store) InsertBackup(ctx context.Context, b BackupRecord) (int64, error) {
	started := nowRFC3339()
	if !b.StartedAt.IsZero() {
		started = b.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_history
			(label, backup_type, started_at, stopped_at, size_bytes, database_bytes,
			 repo_bytes, wal_start, wal_stop, result, repo_path, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Label, b.BackupType, started, nullTimeStr(b.StoppedAt), b.SizeBytes, b.DatabaseBytes,
		b.RepoBytes, b.WALStart, b.WALStop, b.Result, b.RepoPath, b.Error)
	if err != nil {
		return 0, core.InternalError("insert backup").Wrap(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, core.InternalError("backup last insert id").Wrap(err)
	}
	return id, nil
}

// ListBackups returns up to limit backup records, newest first. limit <= 0
// defaults to 50.
func (s *Store) ListBackups(ctx context.Context, limit int) ([]BackupRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, backup_type, started_at, stopped_at, size_bytes, database_bytes,
		       repo_bytes, wal_start, wal_stop, result, repo_path, error
		FROM backup_history ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, core.InternalError("list backups").Wrap(err)
	}
	defer rows.Close()

	var out []BackupRecord
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate backups").Wrap(err)
	}
	return out, nil
}

// LatestSuccessfulBackup returns the most recent backup whose result is
// "success", or a CodeNotFound error if there is none.
func (s *Store) LatestSuccessfulBackup(ctx context.Context) (*BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, backup_type, started_at, stopped_at, size_bytes, database_bytes,
		       repo_bytes, wal_start, wal_stop, result, repo_path, error
		FROM backup_history WHERE result = 'success' ORDER BY started_at DESC LIMIT 1`)
	if err != nil {
		return nil, core.InternalError("query latest backup").Wrap(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, core.InternalError("iterate latest backup").Wrap(err)
		}
		return nil, core.NotFoundError("no successful backup recorded")
	}
	b, err := scanBackup(rows)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBackup(rows rowScanner) (BackupRecord, error) {
	var b BackupRecord
	var started string
	var stopped sql.NullString
	if err := rows.Scan(&b.ID, &b.Label, &b.BackupType, &started, &stopped,
		&b.SizeBytes, &b.DatabaseBytes, &b.RepoBytes, &b.WALStart, &b.WALStop,
		&b.Result, &b.RepoPath, &b.Error); err != nil {
		return b, core.InternalError("scan backup").Wrap(err)
	}
	var err error
	if b.StartedAt, err = parseTime(started); err != nil {
		return b, err
	}
	if stopped.Valid && stopped.String != "" {
		t, err := parseTime(stopped.String)
		if err != nil {
			return b, err
		}
		b.StoppedAt = &t
	}
	return b, nil
}

// InsertRestoreTest records a restore-test result and returns its id.
func (s *Store) InsertRestoreTest(ctx context.Context, r RestoreTestRecord) (int64, error) {
	tested := nowRFC3339()
	if !r.TestedAt.IsZero() {
		tested = r.TestedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO restore_tests (tested_at, source_label, verified_rows, result, duration_ms, detail)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tested, r.SourceLabel, r.VerifiedRows, r.Result, r.DurationMS, r.Detail)
	if err != nil {
		return 0, core.InternalError("insert restore test").Wrap(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, core.InternalError("restore test last insert id").Wrap(err)
	}
	return id, nil
}

// ListRestoreTests returns up to limit restore-test records, newest first.
// limit <= 0 defaults to 50.
func (s *Store) ListRestoreTests(ctx context.Context, limit int) ([]RestoreTestRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tested_at, source_label, verified_rows, result, duration_ms, detail
		FROM restore_tests ORDER BY tested_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, core.InternalError("list restore tests").Wrap(err)
	}
	defer rows.Close()

	var out []RestoreTestRecord
	for rows.Next() {
		var r RestoreTestRecord
		var tested string
		if err := rows.Scan(&r.ID, &tested, &r.SourceLabel, &r.VerifiedRows, &r.Result, &r.DurationMS, &r.Detail); err != nil {
			return nil, core.InternalError("scan restore test").Wrap(err)
		}
		if r.TestedAt, err = parseTime(tested); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate restore tests").Wrap(err)
	}
	return out, nil
}
