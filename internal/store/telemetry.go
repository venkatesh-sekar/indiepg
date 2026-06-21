package store

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// InsertSample buffers a single telemetry sample for the in-panel dashboard.
func (s *Store) InsertSample(ctx context.Context, sample TelemetrySample) error {
	ts := nowRFC3339()
	if !sample.TS.IsZero() {
		ts = sample.TS.UTC().Format(time.RFC3339Nano)
	}
	labels := sample.Labels
	if labels == "" {
		labels = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telemetry_buffer (ts, metric, value, labels) VALUES (?, ?, ?, ?)`,
		ts, sample.Metric, sample.Value, labels)
	if err != nil {
		return core.InternalError("insert telemetry sample").Wrap(err)
	}
	return nil
}

// RecentSamples returns buffered samples for a metric since the given time,
// oldest first. A zero since returns all buffered samples for the metric.
func (s *Store) RecentSamples(ctx context.Context, metric string, since time.Time) ([]TelemetrySample, error) {
	query := `SELECT id, ts, metric, value, labels FROM telemetry_buffer
		WHERE metric = ? AND ts >= ? ORDER BY ts ASC`
	sinceArg := "0000-01-01T00:00:00Z"
	if !since.IsZero() {
		sinceArg = since.UTC().Format(time.RFC3339Nano)
	}

	r, err := s.db.QueryContext(ctx, query, metric, sinceArg)
	if err != nil {
		return nil, core.InternalError("query telemetry").Wrap(err)
	}
	defer r.Close()

	var out []TelemetrySample
	for r.Next() {
		var sample TelemetrySample
		var ts string
		if err := r.Scan(&sample.ID, &ts, &sample.Metric, &sample.Value, &sample.Labels); err != nil {
			return nil, core.InternalError("scan telemetry").Wrap(err)
		}
		if sample.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		out = append(out, sample)
	}
	if err := r.Err(); err != nil {
		return nil, core.InternalError("iterate telemetry").Wrap(err)
	}
	return out, nil
}

// PruneTelemetry deletes buffered samples older than the cutoff and returns the
// number of rows removed. Used by the scheduler to bound buffer growth.
func (s *Store) PruneTelemetry(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM telemetry_buffer WHERE ts < ?`,
		olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, core.InternalError("prune telemetry").Wrap(err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
