package alert

import (
	"github.com/venkatesh-sekar/pgpanel/internal/store"
	"github.com/venkatesh-sekar/pgpanel/internal/telemetry"
)

// store_AlertRecord builds a minimal AlertRecord for decode tests.
func store_AlertRecord(id, definition string) store.AlertRecord {
	return store.AlertRecord{
		ID:         id,
		Name:       "test rule",
		Enabled:    true,
		Definition: definition,
		Severity:   string(SeverityWarning),
		State:      string(StateOK),
	}
}

// fullSnapshot returns a snapshot with every field populated so metric
// extraction never hits a divide-by-zero.
func fullSnapshot() telemetry.Snapshot {
	return telemetry.Snapshot{
		CPUPercent:            42,
		MemUsedBytes:          4 << 30,
		MemTotalBytes:         8 << 30,
		DiskUsedBytes:         50 << 30,
		DiskTotalBytes:        100 << 30,
		Load1:                 1.5,
		Connections:           20,
		MaxConnections:        100,
		CacheHitRatio:         0.99,
		TPS:                   123.4,
		Deadlocks:             2,
		ReplicationLagSeconds: 1.0,
		LastBackupAgeSeconds:  3600,
	}
}
