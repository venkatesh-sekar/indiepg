package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// GetAuth returns the admin auth record, or a CodeNotFound error if it has not
// been initialized.
func (s *Store) GetAuth(ctx context.Context) (*AuthRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT password_hash, session_secret, failed_attempts, locked_until, updated_at
		FROM auth WHERE id = 1`)

	var rec AuthRecord
	var lockedUntil sql.NullString
	var updatedAt string
	err := row.Scan(&rec.PasswordHash, &rec.SessionSecret, &rec.FailedAttempts, &lockedUntil, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.NotFoundError("admin auth not initialized")
	}
	if err != nil {
		return nil, core.InternalError("read auth").Wrap(err)
	}
	if rec.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	if lockedUntil.Valid && lockedUntil.String != "" {
		t, err := parseTime(lockedUntil.String)
		if err != nil {
			return nil, err
		}
		rec.LockedUntil = &t
	}
	return &rec, nil
}

// InitAuth creates the single auth row with a password hash and session secret.
// It overwrites any existing row (used by install and reset-password).
func (s *Store) InitAuth(ctx context.Context, passwordHash string, sessionSecret []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth (id, password_hash, session_secret, failed_attempts, locked_until, updated_at)
		VALUES (1, ?, ?, 0, NULL, ?)
		ON CONFLICT(id) DO UPDATE SET
			password_hash   = excluded.password_hash,
			session_secret  = excluded.session_secret,
			failed_attempts = 0,
			locked_until    = NULL,
			updated_at      = excluded.updated_at`,
		passwordHash, sessionSecret, nowRFC3339())
	if err != nil {
		return core.InternalError("init auth").Wrap(err)
	}
	return nil
}

// SetPasswordHash updates only the password hash and resets lockout state.
func (s *Store) SetPasswordHash(ctx context.Context, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE auth SET password_hash = ?, failed_attempts = 0, locked_until = NULL, updated_at = ?
		WHERE id = 1`, passwordHash, nowRFC3339())
	if err != nil {
		return core.InternalError("set password hash").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.NotFoundError("admin auth not initialized")
	}
	return nil
}

// SetLockout records the failed-attempt counter and optional lockout deadline.
// A nil lockedUntil clears the lockout.
func (s *Store) SetLockout(ctx context.Context, failedAttempts int, lockedUntil *time.Time) error {
	var lu any
	if lockedUntil != nil {
		lu = lockedUntil.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE auth SET failed_attempts = ?, locked_until = ?, updated_at = ? WHERE id = 1`,
		failedAttempts, lu, nowRFC3339())
	if err != nil {
		return core.InternalError("set lockout").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.NotFoundError("admin auth not initialized")
	}
	return nil
}
