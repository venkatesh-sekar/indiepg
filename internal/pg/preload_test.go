package pg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergePreloadLibraries(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		lib         string
		wantValue   string
		wantChanged bool
	}{
		{
			name:        "empty value gets the lib",
			current:     "",
			lib:         "pg_cron",
			wantValue:   "pg_cron",
			wantChanged: true,
		},
		{
			name:        "whitespace-only value is treated as empty",
			current:     "   ",
			lib:         "pg_cron",
			wantValue:   "pg_cron",
			wantChanged: true,
		},
		{
			name:        "append to a single existing entry",
			current:     "pg_stat_statements",
			lib:         "pg_cron",
			wantValue:   "pg_stat_statements, pg_cron",
			wantChanged: true,
		},
		{
			name:        "append preserves order of multiple entries",
			current:     "auto_explain, pg_stat_statements",
			lib:         "pg_cron",
			wantValue:   "auto_explain, pg_stat_statements, pg_cron",
			wantChanged: true,
		},
		{
			name:        "already present is a no-op",
			current:     "pg_cron",
			lib:         "pg_cron",
			wantValue:   "pg_cron",
			wantChanged: false,
		},
		{
			name:        "already present among many is a no-op",
			current:     "pg_stat_statements, pg_cron, auto_explain",
			lib:         "pg_cron",
			wantValue:   "pg_stat_statements, pg_cron, auto_explain",
			wantChanged: false,
		},
		{
			name:        "present with surrounding whitespace is a no-op",
			current:     "pg_stat_statements ,  pg_cron ",
			lib:         "pg_cron",
			wantValue:   "pg_stat_statements ,  pg_cron ",
			wantChanged: false,
		},
		{
			name:        "present but double-quoted is a no-op",
			current:     `pg_stat_statements, "pg_cron"`,
			lib:         "pg_cron",
			wantValue:   `pg_stat_statements, "pg_cron"`,
			wantChanged: false,
		},
		{
			name:        "outer whitespace is trimmed when appending",
			current:     "  pg_stat_statements  ",
			lib:         "pg_cron",
			wantValue:   "pg_stat_statements, pg_cron",
			wantChanged: true,
		},
		{
			name:        "lib with surrounding whitespace is trimmed",
			current:     "pg_stat_statements",
			lib:         "  pg_cron  ",
			wantValue:   "pg_stat_statements, pg_cron",
			wantChanged: true,
		},
		{
			name:        "empty lib is a no-op",
			current:     "pg_stat_statements",
			lib:         "",
			wantValue:   "pg_stat_statements",
			wantChanged: false,
		},
		{
			name:        "whitespace-only lib is a no-op",
			current:     "pg_stat_statements",
			lib:         "   ",
			wantValue:   "pg_stat_statements",
			wantChanged: false,
		},
		{
			name:        "comparison is a substring-safe exact match",
			current:     "pg_cron_extra",
			lib:         "pg_cron",
			wantValue:   "pg_cron_extra, pg_cron",
			wantChanged: true,
		},
		{
			name:        "stray commas and blanks are tolerated for presence check",
			current:     "pg_cron, , ,",
			lib:         "pg_cron",
			wantValue:   "pg_cron, , ,",
			wantChanged: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotChanged := MergePreloadLibraries(tc.current, tc.lib)
			require.Equal(t, tc.wantValue, gotValue)
			require.Equal(t, tc.wantChanged, gotChanged)
		})
	}
}

// Adding two libraries in sequence must be order-preserving and idempotent on
// the second pass.
func TestMergePreloadLibrariesSequential(t *testing.T) {
	v, changed := MergePreloadLibraries("", "pg_stat_statements")
	require.True(t, changed)
	require.Equal(t, "pg_stat_statements", v)

	v, changed = MergePreloadLibraries(v, "pg_cron")
	require.True(t, changed)
	require.Equal(t, "pg_stat_statements, pg_cron", v)

	// Re-adding either is a no-op.
	_, changed = MergePreloadLibraries(v, "pg_cron")
	require.False(t, changed)
	_, changed = MergePreloadLibraries(v, "pg_stat_statements")
	require.False(t, changed)
}
