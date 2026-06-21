package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestNewExporterNoOpWhenEndpointEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default() // OTLPEndpoint empty

	exp, err := NewExporter(ctx, Options{
		Config:     cfg,
		InstanceID: "inst-noop",
		Logger:     core.Discard(),
	})
	require.NoError(t, err)
	require.NotNil(t, exp)
	require.False(t, exp.Enabled())
	require.NotNil(t, exp.MeterProvider(), "no-op exporter must still expose a provider")

	// All lifecycle methods are harmless on a no-op exporter.
	require.NoError(t, exp.Start(ctx))
	exp.Record(ctx, sampleSnapshot()) // must not panic with no reader
	require.NoError(t, exp.Stop(ctx))
}

func TestNewExporterEnabledWithEndpoint(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.OTLPEndpoint = "localhost:4318"
	cfg.OTLPInsecure = true

	exp, err := NewExporter(ctx, Options{
		Config:     cfg,
		InstanceID: "inst-enabled",
		Logger:     core.Discard(),
	})
	require.NoError(t, err)
	require.True(t, exp.Enabled())
	require.NotNil(t, exp.MeterProvider())

	// Start creates instruments; Record must not error or block even though no
	// collector is listening (the periodic reader pushes asynchronously).
	require.NoError(t, exp.Start(ctx))
	require.Len(t, exp.gauges, len(MetricKeys))
	exp.Record(ctx, sampleSnapshot())

	// Stop force-flushes and shuts the provider down. With no live collector the
	// flush export fails, which Stop surfaces as a typed error -- that is the
	// intended behavior (callers learn the export did not land). We only require
	// that Stop returns cleanly typed and does not hang.
	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := exp.Stop(stopCtx); err != nil {
		require.Equal(t, core.CodeInternal, core.CodeOf(err))
	}
}

func TestExporterResourceHasDistinctInstanceID(t *testing.T) {
	ctx := context.Background()

	res, err := buildResource(ctx, "panel-web-02")
	require.NoError(t, err)

	set := res.Set()

	v, ok := set.Value(attribute.Key(attrServiceInstanceID))
	require.True(t, ok, "service.instance.id attribute must be set")
	require.Equal(t, "panel-web-02", v.AsString())

	name, ok := set.Value(attribute.Key(attrServiceName))
	require.True(t, ok)
	require.Equal(t, serviceName, name.AsString())
}

func TestExporterResourceDistinctPerInstance(t *testing.T) {
	ctx := context.Background()

	a, err := buildResource(ctx, "panel-a")
	require.NoError(t, err)
	b, err := buildResource(ctx, "panel-b")
	require.NoError(t, err)

	av, _ := a.Set().Value(attribute.Key(attrServiceInstanceID))
	bv, _ := b.Set().Value(attribute.Key(attrServiceInstanceID))
	require.NotEqual(t, av.AsString(), bv.AsString(),
		"two panels must export distinct service.instance.id so a collector never merges them")
}

func TestExporterResourceOmitsInstanceIDWhenEmpty(t *testing.T) {
	ctx := context.Background()
	res, err := buildResource(ctx, "")
	require.NoError(t, err)
	_, ok := res.Set().Value(attribute.Key(attrServiceInstanceID))
	require.False(t, ok, "instance id must be omitted (not blank) when not provided")
}

func TestExporterStartIsIdempotent(t *testing.T) {
	ctx := context.Background()
	exp, err := NewExporter(ctx, Options{Config: config.Default(), InstanceID: "x", Logger: core.Discard()})
	require.NoError(t, err)
	require.NoError(t, exp.Start(ctx))
	require.NoError(t, exp.Start(ctx)) // second call is a no-op
	require.Len(t, exp.gauges, len(MetricKeys), "one gauge per metric key")
	require.NoError(t, exp.Stop(ctx))
}

func TestExporterRecordWithoutStartCreatesInstruments(t *testing.T) {
	ctx := context.Background()
	exp, err := NewExporter(ctx, Options{Config: config.Default(), InstanceID: "x", Logger: core.Discard()})
	require.NoError(t, err)
	// Record before Start: instruments are lazily created.
	exp.Record(ctx, sampleSnapshot())
	require.Len(t, exp.gauges, len(MetricKeys))
	require.NoError(t, exp.Stop(ctx))
}

func TestExporterNilLoggerIsSafe(t *testing.T) {
	ctx := context.Background()
	exp, err := NewExporter(ctx, Options{Config: config.Default(), InstanceID: "x"})
	require.NoError(t, err)
	require.NotNil(t, exp.log)
	require.NoError(t, exp.Stop(ctx))
}

func TestMetricMetadataCoversEveryKey(t *testing.T) {
	for _, key := range MetricKeys {
		_, hasDesc := metricDescriptions[key]
		_, hasUnit := metricUnits[key]
		require.Truef(t, hasDesc, "missing description for %q", key)
		require.Truef(t, hasUnit, "missing unit for %q", key)
	}
}
