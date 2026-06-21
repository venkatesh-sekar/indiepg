package alert

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/telemetry"
)

func TestMetricValue(t *testing.T) {
	snap := fullSnapshot()

	tests := []struct {
		metric string
		want   float64
		ok     bool
	}{
		{MetricCPUPercent, 42, true},
		{MetricMemPercent, 50, true},  // 4GiB / 8GiB
		{MetricDiskPercent, 50, true}, // 50GiB / 100GiB
		{MetricLoad1, 1.5, true},
		{MetricPGUp, 1, true}, // MaxConnections > 0
		{MetricConnections, 20, true},
		{MetricMaxConnections, 100, true},
		{MetricConnectionsPercent, 20, true},
		{MetricCacheHitRatio, 0.99, true},
		{MetricTPS, 123.4, true},
		{MetricDeadlocks, 2, true},
		{MetricReplicationLagSecs, 1.0, true},
		{MetricLastBackupAgeSecs, 3600, true},
		{"unknown.metric", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			got, ok := metricValue(snap, tt.metric)
			require.Equal(t, tt.ok, ok)
			if ok {
				require.InDelta(t, tt.want, got, 1e-9)
			}
		})
	}
}

func TestMetricValuePGDown(t *testing.T) {
	// A zero snapshot models "no successful sample / pg unreachable":
	// MaxConnections == 0 means pg.up resolves to 0.
	snap := telemetry.Snapshot{}
	v, ok := metricValue(snap, MetricPGUp)
	require.True(t, ok)
	require.Equal(t, 0.0, v)
}

func TestMetricValueDivideByZeroSkips(t *testing.T) {
	snap := telemetry.Snapshot{} // all denominators zero
	for _, m := range []string{MetricMemPercent, MetricDiskPercent, MetricConnectionsPercent} {
		_, ok := metricValue(snap, m)
		require.False(t, ok, "metric %q should be unavailable when denominator is zero", m)
	}
}
