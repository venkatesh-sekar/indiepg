package store

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// AppendAudit inserts an audit entry and returns its assigned id. The TS field
// is set to now if zero.
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) (int64, error) {
	ts := nowRFC3339()
	if !e.TS.IsZero() {
		ts = e.TS.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (ts, actor, action, target, summary, result, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, e.Actor, e.Action, e.Target, e.Summary, e.Result, e.Detail)
	if err != nil {
		return 0, core.InternalError("append audit").Wrap(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, core.InternalError("audit last insert id").Wrap(err)
	}
	return id, nil
}

// ListAudit returns up to limit audit entries, newest first, skipping offset
// rows. A limit <= 0 defaults to 100.
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, actor, action, target, summary, result, detail
		FROM audit_log ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, core.InternalError("list audit").Wrap(err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Summary, &e.Result, &e.Detail); err != nil {
			return nil, core.InternalError("scan audit").Wrap(err)
		}
		if e.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("iterate audit").Wrap(err)
	}
	return out, nil
}
