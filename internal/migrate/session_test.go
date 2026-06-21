package migrate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

func TestCanTransition(t *testing.T) {
	all := []Status{
		StatusWaiting, StatusExporting, StatusExported, StatusImporting,
		StatusCompleted, StatusFailed, StatusExpired,
	}

	// legal is the exact set of allowed edges; everything else must be false.
	legal := map[Status]map[Status]bool{
		StatusWaiting:   {StatusExporting: true, StatusFailed: true, StatusExpired: true},
		StatusExporting: {StatusExported: true, StatusFailed: true, StatusExpired: true},
		StatusExported:  {StatusImporting: true, StatusFailed: true, StatusExpired: true},
		StatusImporting: {StatusCompleted: true, StatusFailed: true, StatusExpired: true},
		StatusCompleted: {},
		StatusFailed:    {},
		StatusExpired:   {},
	}

	for _, from := range all {
		for _, to := range all {
			want := legal[from][to]
			require.Equalf(t, want, CanTransition(from, to),
				"CanTransition(%q,%q) want %v", from, to, want)
		}
	}
}

func TestCanTransition_edges(t *testing.T) {
	tests := []struct {
		name     string
		from, to Status
		want     bool
	}{
		{"happy export start", StatusWaiting, StatusExporting, true},
		{"happy exported", StatusExporting, StatusExported, true},
		{"happy import start", StatusExported, StatusImporting, true},
		{"happy completed", StatusImporting, StatusCompleted, true},
		{"fail from waiting", StatusWaiting, StatusFailed, true},
		{"expire from importing", StatusImporting, StatusExpired, true},
		{"no self-loop", StatusWaiting, StatusWaiting, false},
		{"no skipping exporting", StatusWaiting, StatusExported, false},
		{"no skipping to completed", StatusWaiting, StatusCompleted, false},
		{"no resurrect completed", StatusCompleted, StatusImporting, false},
		{"no resurrect failed", StatusFailed, StatusWaiting, false},
		{"no resurrect expired", StatusExpired, StatusExporting, false},
		{"no backwards", StatusExported, StatusExporting, false},
		{"unknown from", Status("bogus"), StatusExporting, false},
		{"unknown to", StatusWaiting, Status("bogus"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, CanTransition(tt.from, tt.to))
		})
	}
}

func TestStatusIsTerminal(t *testing.T) {
	tests := []struct {
		s    Status
		want bool
	}{
		{StatusWaiting, false},
		{StatusExporting, false},
		{StatusExported, false},
		{StatusImporting, false},
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusExpired, true},
	}
	for _, tt := range tests {
		require.Equalf(t, tt.want, tt.s.IsTerminal(), "IsTerminal(%q)", tt.s)
	}
}

func TestIsExpiredAndTimeRemaining(t *testing.T) {
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	sess := MigrationSession{ExpiresAt: base.Add(10 * time.Minute)}

	tests := []struct {
		name      string
		now       time.Time
		expired   bool
		remaining time.Duration
	}{
		{"well before", base, false, 10 * time.Minute},
		{"just before", base.Add(10*time.Minute - time.Nanosecond), false, time.Nanosecond},
		{"exactly at expiry", base.Add(10 * time.Minute), true, 0},
		{"after expiry", base.Add(20 * time.Minute), true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expired, sess.IsExpired(tt.now))
			require.Equal(t, tt.remaining, sess.TimeRemaining(tt.now))
		})
	}
}

func TestValidateForExport(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	good := MigrationSession{
		Code:      "ABCDEF",
		Database:  "appdb",
		Status:    StatusWaiting,
		ExpiresAt: now.Add(time.Hour),
	}

	tests := []struct {
		name   string
		mutate func(s *MigrationSession)
		now    time.Time
		wantOK bool
		code   core.Code
	}{
		{"ready", nil, now, true, ""},
		{"no code", func(s *MigrationSession) { s.Code = "" }, now, false, core.CodeValidation},
		{"no database", func(s *MigrationSession) { s.Database = "" }, now, false, core.CodeValidation},
		{"expired", nil, now.Add(2 * time.Hour), false, core.CodeValidation},
		{"already exporting", func(s *MigrationSession) { s.Status = StatusExporting }, now, false, core.CodeConflict},
		{"already exported", func(s *MigrationSession) { s.Status = StatusExported }, now, false, core.CodeConflict},
		{"completed", func(s *MigrationSession) { s.Status = StatusCompleted }, now, false, core.CodeConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			err := s.ValidateForExport(tt.now)
			if tt.wantOK {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, tt.code, core.CodeOf(err))
		})
	}
}

func TestValidateForImport(t *testing.T) {
	good := MigrationSession{
		Code:         "ABCDEF",
		Status:       StatusExported,
		DumpKey:      "pg-migrations/sessions/ABCDEF/dump.bin",
		DumpChecksum: "sha256:deadbeef",
	}

	tests := []struct {
		name   string
		mutate func(s *MigrationSession)
		wantOK bool
		code   core.Code
	}{
		{"ready", nil, true, ""},
		{"no code", func(s *MigrationSession) { s.Code = "" }, false, core.CodeValidation},
		{"still waiting", func(s *MigrationSession) { s.Status = StatusWaiting }, false, core.CodeConflict},
		{"still exporting", func(s *MigrationSession) { s.Status = StatusExporting }, false, core.CodeConflict},
		{"importing already", func(s *MigrationSession) { s.Status = StatusImporting }, false, core.CodeConflict},
		{"missing dump key", func(s *MigrationSession) { s.DumpKey = "" }, false, core.CodeValidation},
		{"missing checksum", func(s *MigrationSession) { s.DumpChecksum = "" }, false, core.CodeValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			err := s.ValidateForImport()
			if tt.wantOK {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, tt.code, core.CodeOf(err))
		})
	}
}

func TestCompareRowCounts(t *testing.T) {
	tests := []struct {
		name           string
		source, target map[string]int64
		want           []RowCountDiff
	}{
		{
			name:   "all match",
			source: map[string]int64{"users": 10, "orders": 5},
			target: map[string]int64{"users": 10, "orders": 5},
			want:   nil,
		},
		{
			name:   "both empty",
			source: map[string]int64{},
			target: map[string]int64{},
			want:   nil,
		},
		{
			name:   "single mismatch",
			source: map[string]int64{"users": 10, "orders": 5},
			target: map[string]int64{"users": 9, "orders": 5},
			want:   []RowCountDiff{{Table: "users", Source: 10, Target: 9}},
		},
		{
			name:   "missing in target",
			source: map[string]int64{"users": 10, "orders": 5},
			target: map[string]int64{"users": 10},
			want:   []RowCountDiff{{Table: "orders", Source: 5, Target: 0}},
		},
		{
			name:   "missing in source",
			source: map[string]int64{"users": 10},
			target: map[string]int64{"users": 10, "audit": 3},
			want:   []RowCountDiff{{Table: "audit", Source: 0, Target: 3}},
		},
		{
			name:   "sorted output",
			source: map[string]int64{"zeta": 1, "alpha": 1, "mid": 2},
			target: map[string]int64{"zeta": 0, "alpha": 0, "mid": 0},
			want: []RowCountDiff{
				{Table: "alpha", Source: 1, Target: 0},
				{Table: "mid", Source: 2, Target: 0},
				{Table: "zeta", Source: 1, Target: 0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareRowCounts(tt.source, tt.target)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCompareRowCounts_verifiedMeansEmpty(t *testing.T) {
	// The design's "verified: N rows matched" badge is driven by an empty diff.
	src := map[string]int64{"a": 100, "b": 200, "c": 300}
	tgt := map[string]int64{"a": 100, "b": 200, "c": 300}
	require.Empty(t, CompareRowCounts(src, tgt))
}
