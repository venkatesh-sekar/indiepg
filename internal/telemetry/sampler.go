package telemetry

import "context"

// Sampler produces a Snapshot when polled. The real implementation lives in the
// pg/host layers (reading pg_stat_* and host counters); tests use a fake.
type Sampler interface {
	Sample(ctx context.Context) (Snapshot, error)
}

// SamplerFunc adapts a plain function to the Sampler interface.
type SamplerFunc func(ctx context.Context) (Snapshot, error)

// Sample implements Sampler.
func (f SamplerFunc) Sample(ctx context.Context) (Snapshot, error) { return f(ctx) }
