// Package telemetry holds the panel's metric snapshot model, the Sampler
// interface that produces snapshots, an OTLP/HTTP meter-provider Exporter that
// publishes each snapshot under a distinct service.instance.id (so collectors
// never merge two panels), and a Collector that runs the sampling loop:
// sample -> buffer in the local store -> record to OTLP.
//
// The Snapshot is the single source of truth for the dashboard metric catalog
// (host + Postgres health + backups). Its Samples method flattens it into
// store.TelemetrySample rows for the in-panel buffer, and the Exporter records
// the same fields to the configured OTLP endpoint. The metric keys are stable,
// exported constants so the alert package can match rules against them.
package telemetry

import (
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// Metric keys are the stable names used both for the buffered samples
// (store.TelemetrySample.Metric) and the OTLP instrument names. They are
// exported so the alert package can reference the exact same identifiers when
// matching rules against a snapshot-derived metric.
const (
	MetricCPUPercent            = "host.cpu.percent"
	MetricMemUsedBytes          = "host.mem.used_bytes"
	MetricMemTotalBytes         = "host.mem.total_bytes"
	MetricMemUsedPercent        = "host.mem.used_percent"
	MetricDiskUsedBytes         = "host.disk.used_bytes"
	MetricDiskTotalBytes        = "host.disk.total_bytes"
	MetricDiskUsedPercent       = "host.disk.used_percent"
	MetricLoad1                 = "host.load1"
	MetricConnections           = "pg.connections"
	MetricMaxConnections        = "pg.max_connections"
	MetricConnectionsPercent    = "pg.connections.percent"
	MetricCacheHitRatio         = "pg.cache_hit_ratio"
	MetricTPS                   = "pg.tps"
	MetricDeadlocks             = "pg.deadlocks"
	MetricReplicationLagSeconds = "pg.replication_lag_seconds"
	MetricLastBackupAgeSeconds  = "backup.last_age_seconds"
	MetricLastBackupFailed      = "backup.last_failed"
)

// MetricKeys is the ordered list of every metric key a Snapshot emits. It is
// the canonical catalog used by Samples and the Exporter so both stay in sync.
var MetricKeys = []string{
	MetricCPUPercent,
	MetricMemUsedBytes,
	MetricMemTotalBytes,
	MetricMemUsedPercent,
	MetricDiskUsedBytes,
	MetricDiskTotalBytes,
	MetricDiskUsedPercent,
	MetricLoad1,
	MetricConnections,
	MetricMaxConnections,
	MetricConnectionsPercent,
	MetricCacheHitRatio,
	MetricTPS,
	MetricDeadlocks,
	MetricReplicationLagSeconds,
	MetricLastBackupAgeSeconds,
	MetricLastBackupFailed,
}

// Snapshot is the dashboard metric model: a point-in-time view of host health,
// Postgres health, and backup freshness. It is produced by a Sampler, buffered
// into the local store via Samples, and exported over OTLP via the Exporter.
type Snapshot struct {
	TakenAt               time.Time
	CPUPercent            float64
	MemUsedBytes          int64
	MemTotalBytes         int64
	DiskUsedBytes         int64
	DiskTotalBytes        int64
	Load1                 float64
	Connections           int
	MaxConnections        int
	CacheHitRatio         float64
	TPS                   float64
	Deadlocks             int64
	ReplicationLagSeconds float64
	LastBackupAgeSeconds  float64
	// LastBackupFailed is 1 when the most recent backup attempt did not succeed,
	// else 0. It is the immediate "a scheduled backup just failed" signal, loud
	// well before LastBackupAgeSeconds crosses the staleness window.
	LastBackupFailed float64
}

// MemUsedPercent returns memory utilization in [0,100]; 0 when total is unknown.
func (s Snapshot) MemUsedPercent() float64 {
	if s.MemTotalBytes <= 0 {
		return 0
	}
	return float64(s.MemUsedBytes) / float64(s.MemTotalBytes) * 100
}

// DiskUsedPercent returns disk utilization in [0,100]; 0 when total is unknown.
func (s Snapshot) DiskUsedPercent() float64 {
	if s.DiskTotalBytes <= 0 {
		return 0
	}
	return float64(s.DiskUsedBytes) / float64(s.DiskTotalBytes) * 100
}

// ConnectionsPercent returns connection saturation in [0,100]; 0 when the max
// is unknown. This is the metric most alert rules compare against ("near max").
func (s Snapshot) ConnectionsPercent() float64 {
	if s.MaxConnections <= 0 {
		return 0
	}
	return float64(s.Connections) / float64(s.MaxConnections) * 100
}

// Value returns the snapshot value for a given metric key, and whether the key
// is known. It is the single mapping used by both Samples and the Exporter so
// the buffered dashboard data and the OTLP stream never diverge.
func (s Snapshot) Value(metric string) (float64, bool) {
	switch metric {
	case MetricCPUPercent:
		return s.CPUPercent, true
	case MetricMemUsedBytes:
		return float64(s.MemUsedBytes), true
	case MetricMemTotalBytes:
		return float64(s.MemTotalBytes), true
	case MetricMemUsedPercent:
		return s.MemUsedPercent(), true
	case MetricDiskUsedBytes:
		return float64(s.DiskUsedBytes), true
	case MetricDiskTotalBytes:
		return float64(s.DiskTotalBytes), true
	case MetricDiskUsedPercent:
		return s.DiskUsedPercent(), true
	case MetricLoad1:
		return s.Load1, true
	case MetricConnections:
		return float64(s.Connections), true
	case MetricMaxConnections:
		return float64(s.MaxConnections), true
	case MetricConnectionsPercent:
		return s.ConnectionsPercent(), true
	case MetricCacheHitRatio:
		return s.CacheHitRatio, true
	case MetricTPS:
		return s.TPS, true
	case MetricDeadlocks:
		return float64(s.Deadlocks), true
	case MetricReplicationLagSeconds:
		return s.ReplicationLagSeconds, true
	case MetricLastBackupAgeSeconds:
		return s.LastBackupAgeSeconds, true
	case MetricLastBackupFailed:
		return s.LastBackupFailed, true
	default:
		return 0, false
	}
}

// Samples flattens a Snapshot into store.TelemetrySample rows for buffering in
// the in-panel dashboard. One row per metric in MetricKeys, all stamped with
// TakenAt (or now if the snapshot has no timestamp) and an empty JSON label set.
func (s Snapshot) Samples() []store.TelemetrySample {
	ts := s.TakenAt
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	out := make([]store.TelemetrySample, 0, len(MetricKeys))
	for _, key := range MetricKeys {
		v, ok := s.Value(key)
		if !ok {
			continue
		}
		out = append(out, store.TelemetrySample{
			TS:     ts,
			Metric: key,
			Value:  v,
			Labels: "{}",
		})
	}
	return out
}
