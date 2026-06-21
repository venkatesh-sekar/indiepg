package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// GetInstance returns the panel identity row, or a CodeNotFound error if the
// panel has not been installed yet.
func (s *Store) GetInstance(ctx context.Context) (*Instance, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT instance_id, label, hostname, pg_system_id, panel_version, created_at
		FROM instance WHERE id = 1`)

	var inst Instance
	var createdAt string
	err := row.Scan(&inst.InstanceID, &inst.Label, &inst.Hostname,
		&inst.PGSystemID, &inst.PanelVersion, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.NotFoundError("panel not installed (no instance identity)")
	}
	if err != nil {
		return nil, core.InternalError("read instance").Wrap(err)
	}
	if inst.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &inst, nil
}

// SaveInstance upserts the single instance identity row.
func (s *Store) SaveInstance(ctx context.Context, inst Instance) error {
	created := inst.CreatedAt
	createdStr := nowRFC3339()
	if !created.IsZero() {
		createdStr = created.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO instance (id, instance_id, label, hostname, pg_system_id, panel_version, created_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			instance_id   = excluded.instance_id,
			label         = excluded.label,
			hostname      = excluded.hostname,
			pg_system_id  = excluded.pg_system_id,
			panel_version = excluded.panel_version`,
		inst.InstanceID, inst.Label, inst.Hostname, inst.PGSystemID, inst.PanelVersion, createdStr)
	if err != nil {
		return core.InternalError("save instance").Wrap(err)
	}
	return nil
}

// SetPGSystemID updates only the recorded Postgres system identifier.
func (s *Store) SetPGSystemID(ctx context.Context, systemID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE instance SET pg_system_id = ? WHERE id = 1`, systemID)
	if err != nil {
		return core.InternalError("update pg_system_id").Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.NotFoundError("panel not installed (no instance identity)")
	}
	return nil
}
