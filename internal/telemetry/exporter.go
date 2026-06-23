package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Well-known OpenTelemetry resource attribute keys. We set them directly rather
// than depend on a semconv module so the package stays within the declared
// deps. service.instance.id is the load-bearing one: it must be distinct per
// panel so a collector (e.g. SigNoz) never merges two servers' metrics.
const (
	attrServiceName       = "service.name"
	attrServiceInstanceID = "service.instance.id"
	attrServiceNamespace  = "service.namespace"

	// meterName scopes the instruments created by this exporter.
	meterName = "github.com/venkatesh-sekar/indiepg/internal/telemetry"

	// serviceName is the OTel service.name reported for all panels.
	serviceName = "indiepg"
)

// Options configure the Exporter.
type Options struct {
	Config config.Config
	// InstanceID becomes the service.instance.id resource attribute. It must be
	// the panel's stable, unique instance id.
	InstanceID string
	Logger     *core.Logger
}

// Exporter wires an OTLP/HTTP meter provider with a distinct service.instance.id
// resource attribute so collectors never merge two panels. When the configured
// OTLPEndpoint is empty the exporter is a no-op: it still exposes a non-nil
// MeterProvider (with no reader, so nothing is pushed) and Record/Start/Stop
// are harmless, letting callers wire it unconditionally.
type Exporter struct {
	log      *core.Logger
	provider *sdkmetric.MeterProvider
	enabled  bool

	// instruments, created lazily on first use under once.
	once    sync.Once
	instErr error
	gauges  map[string]metric.Float64Gauge
}

// NewExporter builds the meter provider. If cfg.OTLPEndpoint is empty it returns
// a no-op exporter (non-nil, MeterProvider present, nothing exported).
func NewExporter(ctx context.Context, opts Options) (*Exporter, error) {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	res, err := buildResource(ctx, opts.InstanceID)
	if err != nil {
		return nil, err
	}

	endpoint := opts.Config.OTLPEndpoint
	if endpoint == "" {
		// No-op: provider with the resource but no reader. Instruments still
		// work; their measurements simply have nowhere to go.
		log.Debug("telemetry exporter disabled: no OTLP endpoint configured")
		return &Exporter{
			log:      log,
			provider: sdkmetric.NewMeterProvider(sdkmetric.WithResource(res)),
			enabled:  false,
			gauges:   map[string]metric.Float64Gauge{},
		}, nil
	}

	exp, err := newOTLPExporter(ctx, opts.Config)
	if err != nil {
		return nil, core.InternalError("build OTLP metric exporter").Wrap(err)
	}

	reader := sdkmetric.NewPeriodicReader(exp)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	log.Info("telemetry exporter enabled", "endpoint", endpoint, "instance_id", opts.InstanceID)
	return &Exporter{
		log:      log,
		provider: provider,
		enabled:  true,
		gauges:   map[string]metric.Float64Gauge{},
	}, nil
}

// buildResource constructs the OTel resource carrying the distinct
// service.instance.id. It is merged onto resource.Default so standard SDK
// attributes (telemetry.sdk.*) are retained.
func buildResource(ctx context.Context, instanceID string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String(attrServiceName, serviceName),
		attribute.String(attrServiceNamespace, serviceName),
	}
	if instanceID != "" {
		attrs = append(attrs, attribute.String(attrServiceInstanceID, instanceID))
	}

	base, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return nil, core.InternalError("build telemetry resource").Wrap(err)
	}
	merged, err := resource.Merge(resource.Default(), base)
	if err != nil {
		// Schema-URL mismatches are non-fatal: prefer our explicit attributes.
		return base, nil
	}
	return merged, nil
}

// newOTLPExporter builds the OTLP/HTTP metric exporter from config, honoring the
// insecure flag for plaintext local collectors.
func newOTLPExporter(ctx context.Context, cfg config.Config) (*otlpmetrichttp.Exporter, error) {
	o := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint),
	}
	if cfg.OTLPInsecure {
		o = append(o, otlpmetrichttp.WithInsecure())
	}
	return otlpmetrichttp.New(ctx, o...)
}

// MeterProvider returns the underlying SDK meter provider (always non-nil).
func (e *Exporter) MeterProvider() *sdkmetric.MeterProvider { return e.provider }

// Enabled reports whether an OTLP endpoint is configured and metrics are pushed.
func (e *Exporter) Enabled() bool { return e.enabled }

// Start initializes the OTLP meters (instruments). It is idempotent and safe to
// call on a no-op exporter. Returns a typed error if instrument creation fails.
func (e *Exporter) Start(ctx context.Context) error {
	e.ensureInstruments()
	return e.instErr
}

// ensureInstruments lazily creates one Float64Gauge per metric key. Done once.
func (e *Exporter) ensureInstruments() {
	e.once.Do(func() {
		meter := e.provider.Meter(meterName)
		for _, key := range MetricKeys {
			g, err := meter.Float64Gauge(key, metric.WithDescription(metricDescriptions[key]), metric.WithUnit(metricUnits[key]))
			if err != nil {
				e.instErr = core.InternalError("create gauge %q", key).Wrap(err)
				return
			}
			e.gauges[key] = g
		}
	})
}

// Record publishes a Snapshot to the OTLP meters. It is a no-op when the
// exporter is disabled or instruments failed to initialize. Instruments are
// created on first use if Start was not called.
func (e *Exporter) Record(ctx context.Context, snap Snapshot) {
	e.ensureInstruments()
	if e.instErr != nil {
		e.log.Warn("telemetry record skipped: instruments unavailable", "error", e.instErr)
		return
	}
	for _, key := range MetricKeys {
		g, ok := e.gauges[key]
		if !ok {
			continue
		}
		v, known := snap.Value(key)
		if !known {
			continue
		}
		g.Record(ctx, v)
	}
}

// Stop flushes and shuts down the meter provider. Safe on a no-op exporter and
// idempotent. Always wraps the cause as a typed error.
func (e *Exporter) Stop(ctx context.Context) error {
	if e.provider == nil {
		return nil
	}
	if err := e.provider.ForceFlush(ctx); err != nil {
		e.log.Warn("telemetry flush on shutdown failed", "error", err)
	}
	if err := e.provider.Shutdown(ctx); err != nil {
		return core.InternalError("shutdown telemetry meter provider").Wrap(err)
	}
	return nil
}

// metricDescriptions and metricUnits annotate the OTLP instruments. Units use
// the UCUM-ish conventions OTel expects ("1" for ratios/percent-of-one,
// "By" for bytes, "s" for seconds).
var metricDescriptions = map[string]string{
	MetricCPUPercent:            "Host CPU utilization percent",
	MetricMemUsedBytes:          "Host memory used in bytes",
	MetricMemTotalBytes:         "Host memory total in bytes",
	MetricMemUsedPercent:        "Host memory utilization percent",
	MetricDiskUsedBytes:         "Host disk used in bytes",
	MetricDiskTotalBytes:        "Host disk total in bytes",
	MetricDiskUsedPercent:       "Host disk utilization percent",
	MetricLoad1:                 "Host 1-minute load average",
	MetricConnections:           "Postgres active connections",
	MetricMaxConnections:        "Postgres max_connections",
	MetricConnectionsPercent:    "Postgres connection saturation percent",
	MetricCacheHitRatio:         "Postgres buffer cache hit ratio",
	MetricTPS:                   "Postgres transactions per second",
	MetricDeadlocks:             "Postgres deadlocks observed",
	MetricReplicationLagSeconds: "Postgres replication lag in seconds",
	MetricLastBackupAgeSeconds:  "Age of the last successful backup in seconds",
	MetricLastBackupFailed:      "1 when the most recent backup attempt failed, else 0",
}

var metricUnits = map[string]string{
	MetricCPUPercent:            "%",
	MetricMemUsedBytes:          "By",
	MetricMemTotalBytes:         "By",
	MetricMemUsedPercent:        "%",
	MetricDiskUsedBytes:         "By",
	MetricDiskTotalBytes:        "By",
	MetricDiskUsedPercent:       "%",
	MetricLoad1:                 "1",
	MetricConnections:           "{connection}",
	MetricMaxConnections:        "{connection}",
	MetricConnectionsPercent:    "%",
	MetricCacheHitRatio:         "1",
	MetricTPS:                   "{transaction}/s",
	MetricDeadlocks:             "{deadlock}",
	MetricReplicationLagSeconds: "s",
	MetricLastBackupAgeSeconds:  "s",
	MetricLastBackupFailed:      "1",
}
