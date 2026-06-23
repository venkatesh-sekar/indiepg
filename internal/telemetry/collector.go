package telemetry

import (
	"context"
	"strings"
	"time"

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

	// The host/Postgres sampler does not read the backup tables, so fold backup
	// freshness/failure in from the store. Without this the backup.* metrics are
	// always zero and the "no recent backup" / "backup failed" alerts are silently
	// dead.
	c.enrichBackup(ctx, &snap)

	c.buffer(ctx, snap)

	if c.exp != nil {
		c.exp.Record(ctx, snap)
	}

	return snap, nil
}

// enrichBackup folds backup-history-derived health into the snapshot from the
// store so backup freshness and failure alerts evaluate against real data. It is
// best-effort: a store error is logged and leaves the snapshot's backup fields at
// their sampled values rather than aborting the cycle.
//
// A box with no backups yet is deliberately left untouched (failed stays 0) so a
// fresh install is not reported as "backup failed" before its first run.
func (c *Collector) enrichBackup(ctx context.Context, snap *Snapshot) {
	if c.store == nil {
		return
	}

	// The most recent attempt of any result drives the immediate failure signal.
	// backup_history rows are terminal ("success"/"fail"), so the newest row's
	// result is the latest scheduled backup's outcome.
	recent, err := c.store.ListBackups(ctx, 1)
	if err != nil {
		c.log.Warn("enrich backup telemetry failed", "step", "list_backups", "error", err)
		return
	}
	if len(recent) == 0 {
		return
	}
	// Set unconditionally (not just on failure) so the store stays authoritative:
	// a recovered backup clears the signal even if some earlier stage set it.
	if backupSucceeded(recent[0].Result) {
		snap.LastBackupFailed = 0
	} else {
		snap.LastBackupFailed = 1
	}

	// Age of the last *successful* backup. NotFound is normal ("never succeeded")
	// and is left for the failure/staleness signals to convey; other errors are
	// logged. Only fill when the sampler did not already provide an age.
	last, err := c.store.LatestSuccessfulBackup(ctx)
	if err != nil {
		if core.CodeOf(err) != core.CodeNotFound {
			c.log.Warn("enrich backup telemetry failed", "step", "latest_successful_backup", "error", err)
		}
		return
	}
	if snap.LastBackupAgeSeconds == 0 {
		if age := backupAgeSeconds(last); age > 0 {
			snap.LastBackupAgeSeconds = age
		}
	}
}

// backupSucceeded reports whether a backup_history result denotes success. The
// manager writes the canonical "success"/"fail"; the trim/fold is defensive.
func backupSucceeded(result string) bool {
	return strings.EqualFold(strings.TrimSpace(result), "success")
}

// backupAgeSeconds returns a completed backup's age in seconds, preferring its
// completion time and falling back to its start time (mirrors the dashboard).
func backupAgeSeconds(b *store.BackupRecord) float64 {
	ref := b.StartedAt
	if b.StoppedAt != nil && !b.StoppedAt.IsZero() {
		ref = *b.StoppedAt
	}
	if ref.IsZero() {
		return 0
	}
	return time.Since(ref).Seconds()
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
