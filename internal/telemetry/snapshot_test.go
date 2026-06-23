package telemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		TakenAt:               time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CPUPercent:            42.5,
		MemUsedBytes:          2 << 30,
		MemTotalBytes:         8 << 30,
		DiskUsedBytes:         90 << 30,
		DiskTotalBytes:        100 << 30,
		Load1:                 1.25,
		Connections:           80,
		MaxConnections:        100,
		CacheHitRatio:         0.995,
		TPS:                   1234.5,
		Deadlocks:             3,
		ReplicationLagSeconds: 12.5,
		LastBackupAgeSeconds:  3600,
		LastBackupFailed:      1,
	}
}

func TestSnapshotDerivedPercents(t *testing.T) {
	tests := []struct {
		name  string
		snap  Snapshot
		mem   float64
		disk  float64
		conns float64
	}{
		{
			name:  "normal",
			snap:  sampleSnapshot(),
			mem:   25,
			disk:  90,
			conns: 80,
		},
		{
			name:  "zero totals are safe",
			snap:  Snapshot{MemUsedBytes: 100, DiskUsedBytes: 100, Connections: 5},
			mem:   0,
			disk:  0,
			conns: 0,
		},
		{
			name:  "negative totals are safe",
			snap:  Snapshot{MemTotalBytes: -1, DiskTotalBytes: -1, MaxConnections: -1},
			mem:   0,
			disk:  0,
			conns: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.mem, tc.snap.MemUsedPercent(), 1e-9)
			require.InDelta(t, tc.disk, tc.snap.DiskUsedPercent(), 1e-9)
			require.InDelta(t, tc.conns, tc.snap.ConnectionsPercent(), 1e-9)
		})
	}
}

func TestSnapshotValue(t *testing.T) {
	snap := sampleSnapshot()
	tests := []struct {
		metric string
		want   float64
		ok     bool
	}{
		{MetricCPUPercent, 42.5, true},
		{MetricMemUsedBytes, float64(2 << 30), true},
		{MetricMemTotalBytes, float64(8 << 30), true},
		{MetricMemUsedPercent, 25, true},
		{MetricDiskUsedBytes, float64(90 << 30), true},
		{MetricDiskTotalBytes, float64(100 << 30), true},
		{MetricDiskUsedPercent, 90, true},
		{MetricLoad1, 1.25, true},
		{MetricConnections, 80, true},
		{MetricMaxConnections, 100, true},
		{MetricConnectionsPercent, 80, true},
		{MetricCacheHitRatio, 0.995, true},
		{MetricTPS, 1234.5, true},
		{MetricDeadlocks, 3, true},
		{MetricReplicationLagSeconds, 12.5, true},
		{MetricLastBackupAgeSeconds, 3600, true},
		{MetricLastBackupFailed, 1, true},
		{"does.not.exist", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.metric, func(t *testing.T) {
			got, ok := snap.Value(tc.metric)
			require.Equal(t, tc.ok, ok)
			require.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

func TestSnapshotValueCoversEveryMetricKey(t *testing.T) {
	// Every advertised metric key must resolve via Value, otherwise Samples and
	// the Exporter would silently drop it.
	snap := sampleSnapshot()
	for _, key := range MetricKeys {
		_, ok := snap.Value(key)
		require.Truef(t, ok, "metric key %q not handled by Value", key)
	}
}

func TestSnapshotSamples(t *testing.T) {
	snap := sampleSnapshot()
	samples := snap.Samples()

	require.Len(t, samples, len(MetricKeys))

	byMetric := map[string]float64{}
	for _, s := range samples {
		require.Equal(t, snap.TakenAt.UTC(), s.TS)
		require.Equal(t, "{}", s.Labels, "labels must be a valid empty JSON object")
		require.Zero(t, s.ID, "ID is assigned by the store on insert")
		byMetric[s.Metric] = s.Value
	}

	// Spot-check derived and raw values made it through.
	require.InDelta(t, 25, byMetric[MetricMemUsedPercent], 1e-9)
	require.InDelta(t, 80, byMetric[MetricConnectionsPercent], 1e-9)
	require.InDelta(t, 42.5, byMetric[MetricCPUPercent], 1e-9)
	require.InDelta(t, 3, byMetric[MetricDeadlocks], 1e-9)

	// Every key is present exactly once.
	require.Len(t, byMetric, len(MetricKeys))
	for _, key := range MetricKeys {
		_, ok := byMetric[key]
		require.Truef(t, ok, "sample for %q missing", key)
	}
}

func TestSnapshotSamplesZeroTimestampUsesNow(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	samples := Snapshot{CPUPercent: 1}.Samples()
	after := time.Now().UTC().Add(time.Second)

	require.NotEmpty(t, samples)
	for _, s := range samples {
		require.Equal(t, time.UTC, s.TS.Location())
		require.WithinRange(t, s.TS, before, after)
	}
}

func TestMetricKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range MetricKeys {
		require.Falsef(t, seen[k], "duplicate metric key %q", k)
		seen[k] = true
	}
}
