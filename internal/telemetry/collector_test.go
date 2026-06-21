package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newNoOpExporter(t *testing.T) *Exporter {
	t.Helper()
	exp, err := NewExporter(context.Background(), Options{
		Config:     config.Default(),
		InstanceID: "test",
		Logger:     core.Discard(),
	})
	require.NoError(t, err)
	return exp
}

func TestCollectorSampleOnceBuffersAndRecords(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	exp := newNoOpExporter(t)

	want := sampleSnapshot()
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return want, nil
	}), st, exp, core.Discard())

	got, err := c.SampleOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, want.CPUPercent, got.CPUPercent)

	// Every metric was buffered into the store.
	for _, key := range MetricKeys {
		rows, err := st.RecentSamples(ctx, key, time.Time{})
		require.NoError(t, err)
		require.Lenf(t, rows, 1, "expected one buffered row for %q", key)
		expVal, _ := want.Value(key)
		require.InDelta(t, expVal, rows[0].Value, 1e-9)
		require.Equal(t, "{}", rows[0].Labels)
	}
}

func TestCollectorSampleOncePropagatesSamplerError(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	sentinel := errors.New("sampler boom")
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return Snapshot{}, sentinel
	}), st, nil, core.Discard())

	_, err := c.SampleOnce(ctx)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.ErrorIs(t, err, sentinel, "underlying cause must be wrapped")

	// Nothing was buffered.
	rows, err := st.RecentSamples(ctx, MetricCPUPercent, time.Time{})
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestCollectorSampleOnceNoSampler(t *testing.T) {
	c := NewCollector(nil, nil, nil, core.Discard())
	_, err := c.SampleOnce(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestCollectorNilStoreSkipsBuffering(t *testing.T) {
	ctx := context.Background()
	exp := newNoOpExporter(t)
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return sampleSnapshot(), nil
	}), nil, exp, core.Discard())

	// No store: must not panic, must still return the snapshot and record.
	got, err := c.SampleOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, sampleSnapshot().TPS, got.TPS)
}

func TestCollectorNilExporterSkipsRecording(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return sampleSnapshot(), nil
	}), st, nil, core.Discard())

	_, err := c.SampleOnce(ctx)
	require.NoError(t, err)

	rows, err := st.RecentSamples(ctx, MetricTPS, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestCollectorNilLoggerIsSafe(t *testing.T) {
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return sampleSnapshot(), nil
	}), nil, nil, nil)
	require.NotNil(t, c.log)
	_, err := c.SampleOnce(context.Background())
	require.NoError(t, err)
}

func TestCollectorMultipleCyclesAccumulate(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := NewCollector(SamplerFunc(func(context.Context) (Snapshot, error) {
		return sampleSnapshot(), nil
	}), st, nil, core.Discard())

	const cycles = 3
	for i := 0; i < cycles; i++ {
		_, err := c.SampleOnce(ctx)
		require.NoError(t, err)
	}

	rows, err := st.RecentSamples(ctx, MetricCPUPercent, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, cycles)
}
