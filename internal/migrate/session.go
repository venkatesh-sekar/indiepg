package migrate

import (
	"sort"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Status is a state in the migration session state machine. The happy path is
//
//	waiting_for_export -> exporting -> exported -> importing -> completed
//
// with failed/expired reachable as terminal off-ramps from the live states.
type Status string

const (
	// StatusWaiting is the initial state after the target creates the session
	// and is waiting for a source to join with the code and begin exporting.
	StatusWaiting Status = "waiting_for_export"
	// StatusExporting means the source is running pg_dump and uploading.
	StatusExporting Status = "exporting"
	// StatusExported means the dump is uploaded and verified; the target may
	// now import.
	StatusExported Status = "exported"
	// StatusImporting means the target is running pg_restore.
	StatusImporting Status = "importing"
	// StatusCompleted is the terminal success state (rows verified).
	StatusCompleted Status = "completed"
	// StatusFailed is the terminal failure state.
	StatusFailed Status = "failed"
	// StatusExpired is the terminal state for a session that timed out.
	StatusExpired Status = "expired"
)

// validTransitions is the legal edge set of the state machine. Terminal states
// (completed, failed, expired) have no outgoing edges.
var validTransitions = map[Status]map[Status]struct{}{
	StatusWaiting: {
		StatusExporting: {},
		StatusFailed:    {},
		StatusExpired:   {},
	},
	StatusExporting: {
		StatusExported: {},
		StatusFailed:   {},
		StatusExpired:  {},
	},
	StatusExported: {
		StatusImporting: {},
		StatusFailed:    {},
		StatusExpired:   {},
	},
	StatusImporting: {
		StatusCompleted: {},
		StatusFailed:    {},
		StatusExpired:   {},
	},
	StatusCompleted: {},
	StatusFailed:    {},
	StatusExpired:   {},
}

// IsTerminal reports whether the status has no outgoing transitions.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusExpired:
		return true
	default:
		return false
	}
}

// CanTransition reports whether moving from one status to another is a legal
// edge of the state machine. A self-transition (from == to) is not a legal
// edge. Unknown statuses never transition.
func CanTransition(from, to Status) bool {
	edges, ok := validTransitions[from]
	if !ok {
		return false
	}
	_, ok = edges[to]
	return ok
}

// Mode is the migration transport/scope mode.
type Mode string

const (
	// ModeSingleDB pulls a single database from another host.
	ModeSingleDB Mode = "single-db"
	// ModeCluster moves all databases plus globals (roles/grants).
	ModeCluster Mode = "cluster"
	// ModeSession is the SSH-less, code-coordinated session wizard where S3 is
	// the only channel between source and target.
	ModeSession Mode = "ssh-less"
)

// MigrationSession is the JSON document coordinated between source and target
// panels via S3. The target creates it; both sides read and update it as the
// migration progresses.
type MigrationSession struct {
	Code            string           `json:"code"`
	Database        string           `json:"database"`
	Status          Status           `json:"status"`
	TargetHost      string           `json:"target_host"`
	SourceHost      string           `json:"source_host,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
	DumpKey         string           `json:"dump_key,omitempty"`
	DumpSize        int64            `json:"dump_size,omitempty"`
	DumpChecksum    string           `json:"dump_checksum,omitempty"`
	SourceRowCounts map[string]int64 `json:"source_row_counts,omitempty"`
	TargetRowCounts map[string]int64 `json:"target_row_counts,omitempty"`
	Error           string           `json:"error,omitempty"`
}

// IsExpired reports whether the session has passed its expiry instant. A
// session already in a terminal expired/failed/completed state is not
// re-reported as expired by wall clock here — callers use Status for that; this
// strictly answers "is now past ExpiresAt".
func (s MigrationSession) IsExpired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// TimeRemaining returns how long until the session expires. It is zero (never
// negative) once the deadline has passed.
func (s MigrationSession) TimeRemaining(now time.Time) time.Duration {
	d := s.ExpiresAt.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// ValidateForExport returns a *core.Error if the session is not ready for the
// SOURCE role to begin exporting. It must be in the waiting state, not expired,
// and carry a database name.
func (s MigrationSession) ValidateForExport(now time.Time) error {
	if s.Code == "" {
		return core.ValidationError("session has no code")
	}
	if s.Database == "" {
		return core.ValidationError("session %s has no database to export", s.Code)
	}
	if s.IsExpired(now) {
		return core.ValidationError("session %s expired at %s", s.Code, s.ExpiresAt.UTC().Format(time.RFC3339Nano)).
			WithHint("the target must create a new session")
	}
	if s.Status != StatusWaiting {
		return core.ConflictError("session %s is %q, expected %q to export", s.Code, s.Status, StatusWaiting).
			WithHint("a source may have already joined this session")
	}
	return nil
}

// ValidateForImport returns a *core.Error if the session is not ready for the
// TARGET role to import. The dump must be uploaded (status exported) with a key
// and checksum present so the target can verify it before restoring.
func (s MigrationSession) ValidateForImport() error {
	if s.Code == "" {
		return core.ValidationError("session has no code")
	}
	if s.Status != StatusExported {
		return core.ConflictError("session %s is %q, expected %q to import", s.Code, s.Status, StatusExported).
			WithHint("wait for the source to finish exporting")
	}
	if s.DumpKey == "" {
		return core.ValidationError("session %s has no dump_key", s.Code).
			WithHint("the source did not record where it uploaded the dump")
	}
	if s.DumpChecksum == "" {
		return core.ValidationError("session %s has no dump_checksum", s.Code).
			WithHint("the dump cannot be verified before import")
	}
	return nil
}

// RowCountDiff is a single per-table mismatch found by CompareRowCounts.
type RowCountDiff struct {
	Table  string
	Source int64
	Target int64
}

// CompareRowCounts returns every table whose source and target row counts do
// not match, sorted by table name for stable output. A table present in only
// one side is reported with the missing side counted as zero. An empty slice
// means the migration verified: every table matched.
func CompareRowCounts(source, target map[string]int64) []RowCountDiff {
	seen := make(map[string]struct{}, len(source)+len(target))
	for t := range source {
		seen[t] = struct{}{}
	}
	for t := range target {
		seen[t] = struct{}{}
	}

	var diffs []RowCountDiff
	for table := range seen {
		s := source[table]
		t := target[table]
		if s != t {
			diffs = append(diffs, RowCountDiff{Table: table, Source: s, Target: t})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Table < diffs[j].Table })
	return diffs
}
