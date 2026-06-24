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

	// backupOutcome is an optional, store-independent source for the most recent
	// backup result. The newest backup_history row normally drives the immediate
	// backup-failed alert, but a failed backup whose history insert ALSO fails
	// would leave that row at the prior success and silence the alert. When wired,
	// the in-memory outcome wins whenever it is more recent than the newest row.
	backupOutcome BackupOutcomeSource
}

// BackupOutcomeSource reports the most recent backup result observed in memory,
// independent of the history store. *backup.Manager satisfies it structurally.
type BackupOutcomeSource interface {
	LastOutcome() (at time.Time, failed bool, ok bool)
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

// UseBackupOutcome wires a store-independent backup-outcome source so a failed
// backup whose history-row insert ALSO fails still raises the immediate
// backup-failed alert. Optional and nil-safe; pass *backup.Manager. Call it
// during construction, before any goroutine begins calling SampleOnce — the
// field is set without a lock (the server wires it in newServer, before the
// background loop starts).
func (c *Collector) UseBackupOutcome(src BackupOutcomeSource) {
	c.backupOutcome = src
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
	// The most recent attempt of any result drives the immediate failure signal.
	// Two sources can report it: the newest backup_history row (authoritative,
	// covers history across restarts) and the in-memory outcome of this process's
	// last backup (covers the case where a failed backup's history insert ALSO
	// failed, leaving the newest row stale). Whichever is more recent wins.
	storeAt, storeFailed, haveStore := c.latestStoreOutcome(ctx)

	var memAt time.Time
	var memFailed, haveMem bool
	if c.backupOutcome != nil {
		memAt, memFailed, haveMem = c.backupOutcome.LastOutcome()
	}

	// Set unconditionally (not just on failure) so a recovered backup clears the
	// signal even if some earlier stage set it.
	switch {
	case haveStore && haveMem:
		// The in-memory outcome wins only when strictly newer, so a persisted row
		// (which may carry richer detail) stays authoritative on a tie.
		if memAt.After(storeAt) {
			snap.LastBackupFailed = boolToMetric(memFailed)
		} else {
			snap.LastBackupFailed = boolToMetric(storeFailed)
		}
	case haveStore:
		snap.LastBackupFailed = boolToMetric(storeFailed)
	case haveMem:
		snap.LastBackupFailed = boolToMetric(memFailed)
	default:
		// A box with no backups yet is left untouched (failed stays 0) so a fresh
		// install is not reported as "backup failed" before its first run.
		return
	}

	if c.store == nil {
		return
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

// latestStoreOutcome reports the newest backup_history row's completion time and
// whether it failed. ok is false when there is no store, no rows, or a read
// error (logged) — callers then fall back to the in-memory outcome.
func (c *Collector) latestStoreOutcome(ctx context.Context) (at time.Time, failed bool, ok bool) {
	if c.store == nil {
		return time.Time{}, false, false
	}
	// backup_history rows are terminal ("success"/"fail"), so the newest row's
	// result is the latest scheduled backup's outcome.
	recent, err := c.store.ListBackups(ctx, 1)
	if err != nil {
		c.log.Warn("enrich backup telemetry failed", "step", "list_backups", "error", err)
		return time.Time{}, false, false
	}
	if len(recent) == 0 {
		return time.Time{}, false, false
	}
	return backupAtTime(&recent[0]), !backupSucceeded(recent[0].Result), true
}

// boolToMetric maps a failure flag to the 0/1 metric value.
func boolToMetric(failed bool) float64 {
	if failed {
		return 1
	}
	return 0
}

// backupAtTime returns a backup row's completion time (its stop time, falling
// back to its start time) for recency comparison against the in-memory outcome.
func backupAtTime(b *store.BackupRecord) time.Time {
	if b.StoppedAt != nil && !b.StoppedAt.IsZero() {
		return *b.StoppedAt
	}
	return b.StartedAt
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
