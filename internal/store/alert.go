package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// UpsertAlert inserts or updates an alert rule + state row by ID.
func (s *Store) UpsertAlert(ctx context.Context, a AlertRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alerts (id, name, enabled, definition, severity, state, last_fired_at, last_eval_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name          = excluded.name,
			enabled       = excluded.enabled,
			definition    = excluded.definition,
			severity      = excluded.severity,
			state         = excluded.state,
			last_fired_at = excluded.last_fired_at,
			last_eval_at  = excluded.last_eval_at,
			updated_at    = excluded.updated_at`,
		a.ID, a.Name, boolToInt(a.Enabled), a.Definition, a.Severity, a.State,
		nullTimeStr(a.LastFiredAt), nullTimeStr(a.LastEvalAt), nowRFC3339())
	if err != nil {
		return core.InternalError("upsert alert %q", a.ID).Wrap(err)
	}
	return nil
}

// GetAlert returns one alert by ID, or a CodeNotFound error.
func (s *Store) GetAlert(ctx context.Context, id string) (*AlertRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, definition, severity, state, last_fired_at, last_eval_at, updated_at
		FROM alerts WHERE id = ?`, id)
	if err != nil {
		return nil, core.InternalError("get alert %q", id).Wrap(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, core.InternalError("iterate alert").Wrap(err)
		}
		return nil, core.NotFoundError("alert %q not found", id)
	}
	a, err := scanAlert(rows)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAlerts returns all alert rows, ordered by name.
func (s *Store) ListAlerts(ctx context.Context) ([]AlertRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, definition, severity, state, last_fired_at, last_eval_at, updated_at
		FROM alerts ORDER BY name`)
	if err != nil {
		return nil, core.InternalError("list alerts").Wrap(err)
	}
	defer rows.Close()

	var out []AlertRecord
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate alerts").Wrap(err)
	}
	return out, nil
}

// DeleteAlert removes an alert rule. Deleting a missing rule is not an error.
func (s *Store) DeleteAlert(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE id = ?`, id); err != nil {
		return core.InternalError("delete alert %q", id).Wrap(err)
	}
	return nil
}

func scanAlert(rows rowScanner) (AlertRecord, error) {
	var a AlertRecord
	var enabled int
	var lastFired, lastEval sql.NullString
	var updated string
	if err := rows.Scan(&a.ID, &a.Name, &enabled, &a.Definition, &a.Severity, &a.State,
		&lastFired, &lastEval, &updated); err != nil {
		return a, core.InternalError("scan alert").Wrap(err)
	}
	a.Enabled = enabled != 0
	var err error
	if a.UpdatedAt, err = parseTime(updated); err != nil {
		return a, err
	}
	if a.LastFiredAt, err = optTime(lastFired); err != nil {
		return a, err
	}
	if a.LastEvalAt, err = optTime(lastEval); err != nil {
		return a, err
	}
	return a, nil
}

func optTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
