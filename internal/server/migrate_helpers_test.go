package server

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// These pure helpers sit on the operator-facing migrate path: failErrorText builds
// the error every failed migration persists, boundDiagnostic caps it, and
// unmarshalCounts/toMigrationResponse decode the row-count blobs read back on every
// GET /migrations. They take adversarial input — an external pg_restore's stderr and
// JSON blobs round-tripped through the DB — so their branches (non-core cause, no
// stderr, no-double-append, multi-byte truncation, malformed blob) must be pinned,
// not just the one happy path the storeRecorder.Fail integration test exercises.

func TestFailErrorText(t *testing.T) {
	t.Run("plain non-core error is returned verbatim", func(t *testing.T) {
		got := failErrorText(errors.New("exit status 1"))
		require.Equal(t, "exit status 1", got)
	})

	t.Run("core error without a stderr detail is returned as-is", func(t *testing.T) {
		cause := core.ExecError("pg_restore into %q failed", "appdb").Wrap(errors.New("exit status 1"))
		got := failErrorText(cause)
		require.Equal(t, cause.Error(), got)
		require.NotContains(t, got, "\n", "nothing to append when there is no diagnostic")
	})

	t.Run("actionable stderr is appended, echoed DDL body redacted", func(t *testing.T) {
		stderr := "pg_restore: error: could not execute query: ERROR:  relation \"users\" already exists\n" +
			"Command was: CREATE FUNCTION secret() RETURNS void AS $$ PASSWORD 'hunter2' $$;"
		cause := core.ExecError("pg_restore into %q failed", "appdb").
			WithDetail("stderr", stderr).Wrap(errors.New("exit status 1"))

		got := failErrorText(cause)
		// The base cause leads, and the diagnostic is separated by a newline (so the
		// operator reads the reason on its own line, not run together with the cause).
		require.True(t, strings.HasPrefix(got, cause.Error()+"\n"), "diagnostic must follow the cause on a new line")
		require.Contains(t, got, "relation \"users\" already exists", "actionable reason surfaced")
		// The round-4 DDL-body stripping must survive: an echoed body can embed a secret.
		require.NotContains(t, got, "hunter2")
		require.NotContains(t, got, "CREATE FUNCTION secret")
	})

	t.Run("diagnostic already in the message is not appended twice", func(t *testing.T) {
		reason := "pg_restore: error: relation \"users\" already exists"
		// The cause message already carries the exact (sanitized) reason, as it would
		// when the Orchestrator folds the stderr line into its own error text.
		cause := core.ExecError("restore failed: %s", reason).
			WithDetail("stderr", reason).Wrap(errors.New("exit status 1"))

		got := failErrorText(cause)
		require.Equal(t, cause.Error(), got, "no diagnostic re-appended")
		require.Equal(t, 1, strings.Count(got, reason), "reason must appear exactly once")
	})

	t.Run("non-string stderr detail cannot panic and is skipped", func(t *testing.T) {
		cause := core.ExecError("boom").WithDetail("stderr", 42).Wrap(errors.New("x"))
		got := failErrorText(cause)
		require.Equal(t, cause.Error(), got)
	})

	t.Run("stderr that sanitizes to empty appends nothing", func(t *testing.T) {
		cause := core.ExecError("boom").WithDetail("stderr", "   \n  ").Wrap(errors.New("x"))
		got := failErrorText(cause)
		require.Equal(t, cause.Error(), got)
	})
}

func TestBoundDiagnostic(t *testing.T) {
	t.Run("short strings pass through unchanged", func(t *testing.T) {
		require.Equal(t, "short reason", boundDiagnostic("short reason"))
		require.Equal(t, "", boundDiagnostic(""))
	})

	t.Run("a string exactly at the cap is not truncated", func(t *testing.T) {
		exact := strings.Repeat("a", maxPersistedDiagnostic)
		require.Equal(t, exact, boundDiagnostic(exact))
	})

	t.Run("a multi-byte string within the RUNE cap is not truncated", func(t *testing.T) {
		// 2000 runes but 6000 bytes: the byte-length exceeds the cap, but the rune
		// count does not, so the second guard must return it whole.
		mb := strings.Repeat("世", maxPersistedDiagnostic)
		require.Equal(t, mb, boundDiagnostic(mb))
	})

	t.Run("long ASCII is truncated with the marker", func(t *testing.T) {
		long := strings.Repeat("a", maxPersistedDiagnostic+500)
		got := boundDiagnostic(long)
		require.True(t, strings.HasSuffix(got, "… (truncated)"))
		require.Less(t, len([]rune(got)), len([]rune(long)))
		require.True(t, utf8.ValidString(got))
	})

	t.Run("long multi-byte is truncated on a rune boundary, never mid-rune", func(t *testing.T) {
		// "世" is 3 bytes; a byte-slice cut at offset 2000 would land mid-rune and
		// produce invalid UTF-8. The rune-boundary cut must not.
		long := strings.Repeat("世", maxPersistedDiagnostic+1)
		got := boundDiagnostic(long)
		require.True(t, utf8.ValidString(got), "must never split a multi-byte rune")
		require.True(t, strings.HasSuffix(got, "… (truncated)"))
		require.LessOrEqual(t, len([]rune(got)), maxPersistedDiagnostic+len([]rune(" … (truncated)")))
	})

	t.Run("whitespace at the cut boundary is trimmed, no double space before the marker", func(t *testing.T) {
		// The kept prefix ends in spaces (runs 1998-1999 are spaces); TrimSpace must
		// remove them so the result reads "…a … (truncated)", not "…a   … (truncated)".
		long := strings.Repeat("a", maxPersistedDiagnostic-2) + "  " + strings.Repeat("b", 100)
		got := boundDiagnostic(long)
		require.True(t, strings.HasSuffix(got, "a … (truncated)"), "trailing boundary whitespace must be trimmed")
		require.NotContains(t, got, "  … (truncated)", "no doubled space before the marker")
	})
}

func TestUnmarshalCounts(t *testing.T) {
	t.Run("empty input degrades to a non-nil empty map", func(t *testing.T) {
		got := unmarshalCounts("")
		require.NotNil(t, got, "must serialize as {} not null")
		require.Equal(t, map[string]int64{}, got)
	})

	t.Run("valid JSON decodes", func(t *testing.T) {
		require.Equal(t,
			map[string]int64{"public.users": 42, "public.orders": 7},
			unmarshalCounts(`{"public.users":42,"public.orders":7}`))
	})

	t.Run("malformed blobs degrade to a non-nil empty map", func(t *testing.T) {
		for _, bad := range []string{"not json", "{", `{"a":`, "[1,2,3]", "12345", `{"a":"x"}`} {
			got := unmarshalCounts(bad)
			require.NotNilf(t, got, "malformed %q must not return nil", bad)
			require.Equalf(t, map[string]int64{}, got, "malformed %q must degrade to {}", bad)
		}
	})

	t.Run("JSON null degrades to a non-nil empty map", func(t *testing.T) {
		got := unmarshalCounts("null")
		require.NotNil(t, got)
		require.Equal(t, map[string]int64{}, got)
	})
}

func TestToMigrationResponse(t *testing.T) {
	// Every field is given a DISTINCT value (distinct strings, distinct ints,
	// distinct timestamps) so a cross-wired mapping — e.g. Phase:=Status,
	// CreatedAt:=UpdatedAt, ProgressDone<->ProgressTotal — is caught, not masked by
	// two fields that happen to share a value.
	created := time.Now().UTC().Truncate(time.Second)
	updated := created.Add(time.Minute)
	finished := created.Add(2 * time.Minute)
	rec := store.MigrationRecord{
		ID: 7, Mode: "single_db", Role: "direct", Status: "completed", Phase: "verifying",
		SourceSummary: "db@redacted", TargetDatabase: "appdb", Overwrite: true,
		Code: "AB12", ProgressDone: 3, ProgressTotal: 9, BytesTotal: 4096, Error: "prior boom",
		RowCountsSrc: `{"public.users":42}`, // valid
		RowCountsTgt: "corrupt{",            // malformed -> {}
		CreatedAt:    created, UpdatedAt: updated, FinishedAt: &finished,
	}

	resp := toMigrationResponse(rec)

	// Every scalar field is carried across verbatim, to the RIGHT wire field.
	require.Equal(t, int64(7), resp.ID)
	require.Equal(t, "single_db", resp.Mode)
	require.Equal(t, "direct", resp.Role)
	require.Equal(t, "completed", resp.Status)
	require.Equal(t, "verifying", resp.Phase)
	require.Equal(t, "db@redacted", resp.SourceSummary)
	require.Equal(t, "appdb", resp.TargetDatabase)
	require.True(t, resp.Overwrite)
	require.Equal(t, "AB12", resp.Code)
	require.Equal(t, int64(3), resp.ProgressDone)
	require.Equal(t, int64(9), resp.ProgressTotal)
	require.Equal(t, int64(4096), resp.BytesTotal)
	require.Equal(t, "prior boom", resp.Error)
	require.Equal(t, created, resp.CreatedAt)
	require.Equal(t, updated, resp.UpdatedAt)
	require.Equal(t, &finished, resp.FinishedAt)

	// The valid blob decodes; the malformed one degrades to a non-nil empty map
	// (never null) rather than failing the whole read.
	require.Equal(t, map[string]int64{"public.users": 42}, resp.RowCountsSrc)
	require.NotNil(t, resp.RowCountsTgt)
	require.Equal(t, map[string]int64{}, resp.RowCountsTgt)
}
