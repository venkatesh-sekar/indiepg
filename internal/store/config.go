package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// GetConfig returns the value for a config key, or a CodeNotFound error if the
// key is unset.
func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.NotFoundError("config key %q not set", key)
	}
	if err != nil {
		return "", core.InternalError("read config %q", key).Wrap(err)
	}
	return value, nil
}

// SetConfig upserts a config key/value pair.
func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowRFC3339())
	if err != nil {
		return core.InternalError("write config %q", key).Wrap(err)
	}
	return nil
}

// AllConfig returns every config key/value pair as a map.
func (s *Store) AllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM config`)
	if err != nil {
		return nil, core.InternalError("list config").Wrap(err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, core.InternalError("scan config").Wrap(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate config").Wrap(err)
	}
	return out, nil
}

// DeleteConfig removes a config key. Removing a missing key is not an error.
func (s *Store) DeleteConfig(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key); err != nil {
		return core.InternalError("delete config %q", key).Wrap(err)
	}
	return nil
}
