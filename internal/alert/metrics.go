package alert

import "github.com/venkatesh-sekar/indiepg/internal/telemetry"

// Metric keys identify the scalar values a Rule can be evaluated against. They
// are derived from a telemetry.Snapshot: the raw fields, plus a few computed
// ratios that are far more useful for alerting than the raw byte/count fields
// (e.g. "disk 92% full" instead of "disk 480000000000 bytes used"). The string
// values are stable and also used as the metric column for any sample buffered
// from a snapshot, so they double as the keys produced by telemetry.Snapshot.
const (
	// Host metrics.
	MetricCPUPercent  = "host.cpu_percent"
	MetricMemPercent  = "host.mem_percent"  // computed: MemUsed/MemTotal*100
	MetricDiskPercent = "host.disk_percent" // computed: DiskUsed/DiskTotal*100
	MetricLoad1       = "host.load1"

	// Postgres health metrics.
	MetricPGUp               = "pg.up" // computed: 1 when MaxConnections>0 else 0
	MetricConnections        = "pg.connections"
	MetricMaxConnections     = "pg.max_connections"
	MetricConnectionsPercent = "pg.connections_percent" // computed: Connections/Max*100
	MetricCacheHitRatio      = "pg.cache_hit_ratio"
	MetricTPS                = "pg.tps"
	MetricDeadlocks          = "pg.deadlocks"
	MetricReplicationLagSecs = "pg.replication_lag_seconds"

	// Backup metrics.
	MetricLastBackupAgeSecs = "backup.last_age_seconds"
	MetricLastBackupFailed  = "backup.last_failed" // 1 when the most recent backup attempt failed
)

// metricValue extracts the named metric from a snapshot. The second return is
// false when the metric is unknown or cannot be computed (e.g. a percentage
// whose denominator is zero), in which case the rule is skipped rather than
// firing on a meaningless value.
func metricValue(snap telemetry.Snapshot, metric string) (float64, bool) {
	switch metric {
	case MetricCPUPercent:
		return snap.CPUPercent, true
	case MetricMemPercent:
		return percent(snap.MemUsedBytes, snap.MemTotalBytes)
	case MetricDiskPercent:
		return percent(snap.DiskUsedBytes, snap.DiskTotalBytes)
	case MetricLoad1:
		return snap.Load1, true
	case MetricPGUp:
		if snap.MaxConnections > 0 {
			return 1, true
		}
		return 0, true
	case MetricConnections:
		return float64(snap.Connections), true
	case MetricMaxConnections:
		return float64(snap.MaxConnections), true
	case MetricConnectionsPercent:
		return percent(int64(snap.Connections), int64(snap.MaxConnections))
	case MetricCacheHitRatio:
		return snap.CacheHitRatio, true
	case MetricTPS:
		return snap.TPS, true
	case MetricDeadlocks:
		return float64(snap.Deadlocks), true
	case MetricReplicationLagSecs:
		return snap.ReplicationLagSeconds, true
	case MetricLastBackupAgeSecs:
		return snap.LastBackupAgeSeconds, true
	case MetricLastBackupFailed:
		return snap.LastBackupFailed, true
	default:
		return 0, false
	}
}

// knownMetrics is the set of metric keys metricValue can resolve. It is the
// single source of truth for which metrics a rule may target, used to reject a
// rule whose metric the engine would silently skip (see MetricKnown). Keep in
// sync with the metricValue switch above.
var knownMetrics = map[string]struct{}{
	MetricCPUPercent:         {},
	MetricMemPercent:         {},
	MetricDiskPercent:        {},
	MetricLoad1:              {},
	MetricPGUp:               {},
	MetricConnections:        {},
	MetricMaxConnections:     {},
	MetricConnectionsPercent: {},
	MetricCacheHitRatio:      {},
	MetricTPS:                {},
	MetricDeadlocks:          {},
	MetricReplicationLagSecs: {},
	MetricLastBackupAgeSecs:  {},
	MetricLastBackupFailed:   {},
}

// MetricKnown reports whether metric is a recognized metric key the engine can
// evaluate. A rule targeting an unknown metric is skipped every eval cycle and
// silently never fires, so callers accepting user input should reject one early.
func MetricKnown(metric string) bool {
	_, ok := knownMetrics[metric]
	return ok
}

// percent returns used/total*100, or (0,false) when total is non-positive so a
// rule never fires on a divide-by-zero.
func percent(used, total int64) (float64, bool) {
	if total <= 0 {
		return 0, false
	}
	return float64(used) / float64(total) * 100, true
}
