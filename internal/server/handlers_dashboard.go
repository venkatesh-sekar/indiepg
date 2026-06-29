package server

import (
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
	"github.com/venkatesh-sekar/indiepg/internal/telemetry"
)

// dashboardDiskFullPercent is the disk utilization at or above which the panel
// considers the host "nearly full" and flags an at-a-glance health warning.
const dashboardDiskFullPercent = 90.0

// dashboardPGStatus is the at-a-glance Postgres status for the dashboard header.
// Version is the running server_version string (omitted when Postgres is
// unreachable); SystemID is best-effort and absent when Postgres is unreachable.
type dashboardPGStatus struct {
	Running  bool   `json:"running"`
	Version  string `json:"version,omitempty"`
	SystemID string `json:"system_id,omitempty"`
}

// dashboardSnapshot mirrors telemetry.Snapshot in snake_case for the SPA. It is
// always populated (a zero snapshot is returned when sampling fails) so the
// dashboard can render without null-guards on every metric.
type dashboardSnapshot struct {
	TakenAt               time.Time `json:"taken_at"`
	CPUPercent            float64   `json:"cpu_percent"`
	MemUsedBytes          int64     `json:"mem_used_bytes"`
	MemTotalBytes         int64     `json:"mem_total_bytes"`
	DiskUsedBytes         int64     `json:"disk_used_bytes"`
	DiskTotalBytes        int64     `json:"disk_total_bytes"`
	Load1                 float64   `json:"load1"`
	Connections           int       `json:"connections"`
	MaxConnections        int       `json:"max_connections"`
	CacheHitRatio         float64   `json:"cache_hit_ratio"`
	TPS                   float64   `json:"tps"`
	Deadlocks             int64     `json:"deadlocks"`
	ReplicationLagSeconds float64   `json:"replication_lag_seconds"`
	LastBackupAgeSeconds  float64   `json:"last_backup_age_seconds"`
}

// dashboardData is the composite payload for GET /dashboard: the at-a-glance PG
// status, the latest telemetry snapshot, the last successful backup (or null),
// and a single green/red health verdict with human-readable failing reasons.
type dashboardData struct {
	PG            dashboardPGStatus   `json:"pg"`
	Snapshot      dashboardSnapshot   `json:"snapshot"`
	LastBackup    *store.BackupRecord `json:"last_backup"`
	HealthOK      bool                `json:"health_ok"`
	HealthReasons []string            `json:"health_reasons,omitempty"`
}

// handleDashboard returns the dashboard summary. It is read-only and never
// audits. Every dependency is best-effort: a down Postgres or a failed sample
// degrades the health verdict rather than failing the request, so the panel
// dashboard keeps rendering even when the managed database is unreachable.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	running, _ := s.pg.IsRunning(ctx)
	sysID, _ := s.pg.SystemIdentifier(ctx)

	// Best-effort running version for the dashboard line; absent (omitted) when
	// Postgres is unreachable.
	var pgVersion string
	if running {
		pgVersion, _ = s.pg.ServerVersion(ctx)
	}

	var reasons []string

	snap, sampleErr := s.sampler.Sample(ctx)
	if sampleErr != nil {
		// Keep a zero snapshot so the SPA renders, but mark telemetry as a
		// failing health check.
		snap = telemetry.Snapshot{}
		reasons = append(reasons, "telemetry sampling failed")
	}

	// Latest successful backup is optional: NotFound means "no backup yet" and
	// is surfaced as a health reason, not an error.
	var lastBackup *store.BackupRecord
	if rec, err := s.store.LatestSuccessfulBackup(ctx); err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			reasons = append(reasons, "no successful backup yet")
		} else {
			writeError(w, err)
			return
		}
	} else {
		lastBackup = rec
	}

	out := dashboardSnapshot{
		TakenAt:               snap.TakenAt,
		CPUPercent:            snap.CPUPercent,
		MemUsedBytes:          snap.MemUsedBytes,
		MemTotalBytes:         snap.MemTotalBytes,
		DiskUsedBytes:         snap.DiskUsedBytes,
		DiskTotalBytes:        snap.DiskTotalBytes,
		Load1:                 snap.Load1,
		Connections:           snap.Connections,
		MaxConnections:        snap.MaxConnections,
		CacheHitRatio:         snap.CacheHitRatio,
		TPS:                   snap.TPS,
		Deadlocks:             snap.Deadlocks,
		ReplicationLagSeconds: snap.ReplicationLagSeconds,
		LastBackupAgeSeconds:  snap.LastBackupAgeSeconds,
	}

	// Derive the backup age from the recorded run if the sampler did not already
	// provide one. Prefer the completion time (StoppedAt) and fall back to the
	// start time so a still-stamped record still yields a sensible age.
	if out.LastBackupAgeSeconds == 0 && lastBackup != nil {
		ref := lastBackup.StartedAt
		if lastBackup.StoppedAt != nil && !lastBackup.StoppedAt.IsZero() {
			ref = *lastBackup.StoppedAt
		}
		if !ref.IsZero() {
			if age := time.Since(ref).Seconds(); age > 0 {
				out.LastBackupAgeSeconds = age
			}
		}
	}

	// Health verdict: Postgres up, disk not nearly full, connections below the
	// configured max. Each failing check contributes a human-readable reason.
	if !running {
		reasons = append(reasons, "postgres not running")
	}
	if pct := snap.DiskUsedPercent(); pct >= dashboardDiskFullPercent {
		reasons = append(reasons, "disk nearly full")
	}
	if snap.MaxConnections > 0 && snap.Connections >= snap.MaxConnections {
		reasons = append(reasons, "connections near max")
	}

	data := dashboardData{
		PG: dashboardPGStatus{
			Running:  running,
			Version:  pgVersion,
			SystemID: sysID,
		},
		Snapshot:      out,
		LastBackup:    lastBackup,
		HealthOK:      len(reasons) == 0,
		HealthReasons: reasons,
	}

	writeData(w, http.StatusOK, data)
}
