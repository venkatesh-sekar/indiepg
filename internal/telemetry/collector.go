package telemetry

import (
	"context"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// Collector runs the sampling loop: Sample -> buffer in the local store ->
// Record to OTLP. It ties a Sampler, the local store buffer, and the Exporter
// together. The scheduler drives SampleOnce on the configured cadence.
type Collector struct {
	sampler Sampler
	store   *store.Store
	exp     *Exporter
	log     *core.Logger
}

// NewCollector builds a Collector. A nil store skips buffering; a nil exporter
// skips OTLP recording, so the collector degrades gracefully when either side
// is not wired.
func NewCollector(sampler Sampler, st *store.Store, exp *Exporter, log *core.Logger) *Collector {
	if log == nil {
		log = core.Discard()
	}
	return &Collector{sampler: sampler, store: st, exp: exp, log: log}
}

// SampleOnce performs one sampling cycle: take a snapshot, buffer its samples in
// the store for the in-panel dashboard, and record it to OTLP. A sampling error
// aborts the cycle and is returned. Buffering errors are logged but do not abort
// the OTLP record (the dashboard buffer is best-effort; export should still
// happen). The snapshot is always returned on success.
func (c *Collector) SampleOnce(ctx context.Context) (Snapshot, error) {
	if c.sampler == nil {
		return Snapshot{}, core.InternalError("telemetry collector has no sampler")
	}

	snap, err := c.sampler.Sample(ctx)
	if err != nil {
		return Snapshot{}, core.InternalError("sample telemetry").Wrap(err)
	}

	c.buffer(ctx, snap)

	if c.exp != nil {
		c.exp.Record(ctx, snap)
	}

	return snap, nil
}

// buffer writes the snapshot's samples into the local store, logging and
// continuing on per-row errors so a transient store hiccup never blocks export.
func (c *Collector) buffer(ctx context.Context, snap Snapshot) {
	if c.store == nil {
		return
	}
	for _, s := range snap.Samples() {
		if err := c.store.InsertSample(ctx, s); err != nil {
			c.log.Warn("buffer telemetry sample failed", "metric", s.Metric, "error", err)
		}
	}
}
