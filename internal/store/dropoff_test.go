package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func sampleDropoff(code string, expiresAt time.Time) DropoffRecord {
	return DropoffRecord{
		Code:           code,
		DumpKey:        "pg-migrations/dropoff/" + code + "/dump",
		MetaKey:        "pg-migrations/dropoff/" + code + "/meta.json",
		TargetDatabase: "appdb",
		Overwrite:      true,
		Status:         "waiting_for_upload",
		ExpiresAt:      expiresAt,
	}
}

func TestInsertAndGetDropoff(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	id, err := s.InsertDropoff(ctx, sampleDropoff("ABCDEF", exp))
	require.NoError(t, err)
	require.Greater(t, id, int64(0))

	got, err := s.GetDropoffByCode(ctx, "ABCDEF")
	require.NoError(t, err)
	require.Equal(t, "ABCDEF", got.Code)
	require.Equal(t, "appdb", got.TargetDatabase)
	require.True(t, got.Overwrite)
	require.Equal(t, "waiting_for_upload", got.Status)
	require.Equal(t, "pg-migrations/dropoff/ABCDEF/dump", got.DumpKey)
	require.Equal(t, "pg-migrations/dropoff/ABCDEF/meta.json", got.MetaKey)
	require.Nil(t, got.MigrationID)
	require.True(t, got.ExpiresAt.Equal(exp))
	require.False(t, got.CreatedAt.IsZero())
}

func TestGetDropoffByCode_notFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetDropoffByCode(context.Background(), "ZZZZZZ")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestInsertDropoff_uniqueCode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	_, err := s.InsertDropoff(ctx, sampleDropoff("DUP123", exp))
	require.NoError(t, err)
	_, err = s.InsertDropoff(ctx, sampleDropoff("DUP123", exp))
	require.Error(t, err, "code is UNIQUE")
}

func TestUpdateDropoff_linksMigrationAndStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.InsertDropoff(ctx, sampleDropoff("ABCDEF", time.Now().Add(time.Hour)))
	require.NoError(t, err)

	rec, err := s.GetDropoffByCode(ctx, "ABCDEF")
	require.NoError(t, err)
	mid := int64(42)
	rec.MigrationID = &mid
	rec.Status = "importing"
	rec.ByteSize = 9999
	require.NoError(t, s.UpdateDropoff(ctx, *rec))

	got, err := s.GetDropoffByCode(ctx, "ABCDEF")
	require.NoError(t, err)
	require.NotNil(t, got.MigrationID)
	require.Equal(t, int64(42), *got.MigrationID)
	require.Equal(t, "importing", got.Status)
	require.Equal(t, int64(9999), got.ByteSize)
}

func TestUpdateDropoff_unknownCode(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateDropoff(context.Background(), sampleDropoff("NOPENO", time.Now().Add(time.Hour)))
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestListExpiredDropoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert one of every status, expired vs live, to pin the sweep set exactly:
	//   - waiting + expired  -> swept (objects reclaimed, then marked expired)
	//   - failed  + expired  -> swept (its kept-for-retry dump must not linger)
	//   - completed+ expired -> swept (backstop: a success-path delete that failed
	//                                  would otherwise orphan the dump forever)
	//   - waiting + live     -> NOT swept (still inside its TTL)
	//   - importing+ expired -> NOT swept (a live import owns its dump)
	//   - expired  + expired -> NOT swept (already terminal; drained)
	insert := func(code, status string, exp time.Time) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}
	insert("WAITEX", "waiting_for_upload", now.Add(-time.Hour))
	insert("FAILEX", "failed", now.Add(-time.Hour))
	insert("WAITLV", "waiting_for_upload", now.Add(time.Hour))
	insert("IMPREX", "importing", now.Add(-time.Hour))
	insert("DONEEX", "completed", now.Add(-time.Hour))
	insert("CANCEX", "canceled", now.Add(-time.Hour))
	insert("EXPDEX", "expired", now.Add(-time.Hour))

	expired, err := s.ListExpiredDropoffs(ctx, now, 100)
	require.NoError(t, err)
	codes := make([]string, len(expired))
	for i, e := range expired {
		codes[i] = e.Code
	}
	require.ElementsMatch(t, []string{"WAITEX", "FAILEX", "DONEEX", "CANCEX"}, codes,
		"past-TTL waiting/uploaded/failed/completed/canceled are swept; never importing/expired")
}

// TestListActiveDropoffs pins the recovery-list set: the resumable, not-yet-expired
// sessions (waiting/uploaded/importing/failed) are returned, newest first. A 'failed'
// session is INCLUDED (finding #5) because its dump lingers in S3 for retry, so after
// a reload the UI must still offer a Retry/Cancel path. A 'canceled'/'completed'/
// 'expired' or past-TTL session must never resurface as resumable.
func TestListActiveDropoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insert := func(code, status string, exp time.Time) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}
	insert("WAITLV", "waiting_for_upload", now.Add(time.Hour))
	insert("UPLDLV", "uploaded", now.Add(time.Hour))
	insert("IMPRLV", "importing", now.Add(time.Hour))
	insert("FAILLV", "failed", now.Add(time.Hour))              // failed import: resumable for retry/cancel
	insert("CANCLV", "canceled", now.Add(time.Hour))            // cancelled: excluded
	insert("DONELV", "completed", now.Add(time.Hour))           // terminal: excluded
	insert("FAILEX", "failed", now.Add(-time.Hour))             // failed but past TTL: excluded
	insert("WAITEX", "waiting_for_upload", now.Add(-time.Hour)) // past TTL: excluded

	active, err := s.ListActiveDropoffs(ctx, now, 100)
	require.NoError(t, err)
	codes := make([]string, len(active))
	for i, a := range active {
		codes[i] = a.Code
	}
	require.ElementsMatch(t, []string{"WAITLV", "UPLDLV", "IMPRLV", "FAILLV"}, codes,
		"live non-terminal sessions AND unexpired failed (retryable) sessions are resumable")
	// Newest-first ordering (by id desc): FAILLV was inserted last among the live set.
	require.Equal(t, "FAILLV", codes[0], "newest session first")
}

// TestListExpiredDropoffs_subSecondBoundary pins the fixed-width expires_at
// comparison. The headline failure of a variable-width RFC3339Nano comparison is a
// stored whole-second timestamp ("...:00:00Z", zero nanoseconds) vs a query `now`
// in the same second but with a fraction ("...:00:00.5Z"): lexicographically the
// stored value sorts AFTER the query because '.' (0x2E) < 'Z' (0x5A), so the
// just-expired row is wrongly skipped. With the fixed-width layout string order
// matches time order and the row is swept.
func TestListExpiredDropoffs_subSecondBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	exp := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) // exact whole second
	_, err := s.InsertDropoff(ctx, sampleDropoff("BNDARY", exp))
	require.NoError(t, err)

	now := exp.Add(500 * time.Millisecond) // 0.5s past expiry, same whole second
	expired, err := s.ListExpiredDropoffs(ctx, now, 100)
	require.NoError(t, err)
	codes := make([]string, len(expired))
	for i, e := range expired {
		codes[i] = e.Code
	}
	require.Contains(t, codes, "BNDARY", "a row expired within the current whole second must be swept")
}

func TestClaimDropoffForImport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	mk := func(code, status string) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	// 'uploaded' is startable, and the claim is single-winner: a second claim on
	// the now-'importing' row loses.
	mk("UPLOAD", "uploaded")
	won, err := s.ClaimDropoffForImport(ctx, "UPLOAD")
	require.NoError(t, err)
	require.True(t, won)
	got, err := s.GetDropoffByCode(ctx, "UPLOAD")
	require.NoError(t, err)
	require.Equal(t, "importing", got.Status)
	won, err = s.ClaimDropoffForImport(ctx, "UPLOAD")
	require.NoError(t, err)
	require.False(t, won, "a second concurrent start must lose")

	// 'failed' is startable (retry from the kept dump).
	mk("FAILED", "failed")
	won, err = s.ClaimDropoffForImport(ctx, "FAILED")
	require.NoError(t, err)
	require.True(t, won)

	// 'completed', 'canceled' and 'expired' are terminal — never startable. A
	// cancelled session is terminal ON PURPOSE: its presigned PUT URLs can't be
	// revoked, so re-claiming it would let a re-upload restart a cancelled import.
	mk("DONEXX", "completed")
	won, err = s.ClaimDropoffForImport(ctx, "DONEXX")
	require.NoError(t, err)
	require.False(t, won)

	mk("CANCLD", "canceled")
	won, err = s.ClaimDropoffForImport(ctx, "CANCLD")
	require.NoError(t, err)
	require.False(t, won, "a cancelled session must never be re-startable")

	mk("EXPIRX", "expired")
	won, err = s.ClaimDropoffForImport(ctx, "EXPIRX")
	require.NoError(t, err)
	require.False(t, won)

	// Unknown code is a benign miss, not an error.
	won, err = s.ClaimDropoffForImport(ctx, "NOPENO")
	require.NoError(t, err)
	require.False(t, won)
}

// TestClaimDropoffForImport_refusesExpired pins finding #4: the claim must require
// expires_at > now ATOMICALLY, so a row whose TTL has passed can never be claimed for
// import even if the caller's earlier expiry check raced the expiry sweep. A row
// expiring within the current whole second (the fixed-width-comparison boundary) is
// still refused.
func TestClaimDropoffForImport_refusesExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(code, status string, exp time.Time) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	// A startable 'uploaded' row that has already expired must NOT be claimable.
	mk("EXPCLM", "uploaded", time.Now().Add(-time.Hour).UTC())
	won, err := s.ClaimDropoffForImport(ctx, "EXPCLM")
	require.NoError(t, err)
	require.False(t, won, "a past-TTL row must never be claimed for import")
	got, err := s.GetDropoffByCode(ctx, "EXPCLM")
	require.NoError(t, err)
	require.Equal(t, "uploaded", got.Status, "an expired claim attempt must not flip the row to importing")

	// A row expiring within the current whole second (zero fractional digits) must
	// also be refused once now is past it, matching the fixed-width comparison.
	exp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) // safely in the past
	mk("BNDCLM", "failed", exp)
	won, err = s.ClaimDropoffForImport(ctx, "BNDCLM")
	require.NoError(t, err)
	require.False(t, won)

	// Control: an unexpired startable row still claims, proving the predicate didn't
	// over-reject.
	mk("LIVCLM", "uploaded", time.Now().Add(time.Hour).UTC())
	won, err = s.ClaimDropoffForImport(ctx, "LIVCLM")
	require.NoError(t, err)
	require.True(t, won, "an unexpired row must still be claimable")
}

// TestCountUncleanedDropoffs pins finding #2's gate: every session that is NOT
// terminally 'expired' may still own S3 objects in the current bucket and so counts
// as uncleaned. Only the sweep's 'expired' (objects provably reclaimed) drops out.
func TestCountUncleanedDropoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour).UTC()

	mk := func(code, status string) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	// Empty: nothing to clean.
	n, err := s.CountUncleanedDropoffs(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// One of each non-'expired' status owns (or may own) objects -> all count.
	mk("WAIT01", "waiting_for_upload")
	mk("UPLD01", "uploaded")
	mk("IMPR01", "importing")
	mk("FAIL01", "failed")    // keeps its dump for retry
	mk("CANC01", "canceled")  // best-effort delete may have failed
	mk("DONE01", "completed") // best-effort delete may have failed
	mk("EXPR01", "expired")   // sweep proved both deletes succeeded -> NOT counted

	n, err = s.CountUncleanedDropoffs(ctx)
	require.NoError(t, err)
	require.Equal(t, 6, n, "only terminally-expired sessions are provably cleaned")
}

func TestFinalizeDropoffFromImporting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	mk := func(code, status string) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	// From 'importing' the finalize wins and records the terminal outcome.
	mk("IMPGO", "importing")
	won, err := s.FinalizeDropoffFromImporting(ctx, "IMPGO", "completed", "")
	require.NoError(t, err)
	require.True(t, won)
	got, err := s.GetDropoffByCode(ctx, "IMPGO")
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)

	// From a non-'importing' state (a cancel already moved it to 'failed') the
	// finalize is a no-op: the terminal cancel decision is authoritative.
	mk("CANCL", "failed")
	won, err = s.FinalizeDropoffFromImporting(ctx, "CANCL", "completed", "")
	require.NoError(t, err)
	require.False(t, won, "must not resurrect a cancelled/failed session to completed")
	got, err = s.GetDropoffByCode(ctx, "CANCL")
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
}

func TestMarkDropoffCancelled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	mk := func(code, status string) {
		d := sampleDropoff(code, exp)
		d.Status = status
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	// A waiting/uploaded session is cancellable -> the TERMINAL 'canceled' status
	// (not the retryable 'failed'), with reason 'cancelled'.
	mk("CXLUP", "uploaded")
	won, err := s.MarkDropoffCancelled(ctx, "CXLUP")
	require.NoError(t, err)
	require.True(t, won)
	got, err := s.GetDropoffByCode(ctx, "CXLUP")
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
	require.Equal(t, "cancelled", got.Error)

	// A failed (post-failed-import) session is cancellable too, to clean up its
	// kept-for-retry dump.
	mk("CXLFAIL", "failed")
	won, err = s.MarkDropoffCancelled(ctx, "CXLFAIL")
	require.NoError(t, err)
	require.True(t, won)

	// An actively-importing session must NOT be cancellable at the store level:
	// cancelling mid-import could delete the recovery dump out from under a live
	// restore (e.g. an overwrite that already dropped the original).
	mk("CXLIMP", "importing")
	won, err = s.MarkDropoffCancelled(ctx, "CXLIMP")
	require.NoError(t, err)
	require.False(t, won, "importing must not be cancellable")
	got, err = s.GetDropoffByCode(ctx, "CXLIMP")
	require.NoError(t, err)
	require.Equal(t, "importing", got.Status)

	// A completed session must NOT be relabelled cancelled (a completion that landed
	// just before the cancel is authoritative).
	mk("CXLDONE", "completed")
	won, err = s.MarkDropoffCancelled(ctx, "CXLDONE")
	require.NoError(t, err)
	require.False(t, won)
	got, err = s.GetDropoffByCode(ctx, "CXLDONE")
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
}

// TestMigrateAddsCreatedTargetToLegacyTable proves the additive-column migration is
// safe under the schema constraint (CREATE ... IF NOT EXISTS is a no-op on an existing
// table, and SQLite's ADD COLUMN has no IF NOT EXISTS): a database whose
// dropoff_sessions predates created_target gets the column back-filled on Open, and a
// SECOND Open (a second startup) re-applies the guarded ALTER as a harmless no-op
// rather than crashing.
func TestMigrateAddsCreatedTargetToLegacyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a LEGACY dropoff_sessions (no created_target column) directly.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE dropoff_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, code TEXT NOT NULL UNIQUE,
		migration_id INTEGER, dump_key TEXT NOT NULL, meta_key TEXT NOT NULL,
		target_database TEXT NOT NULL, overwrite INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', byte_size INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO dropoff_sessions
		(code, dump_key, meta_key, target_database, status, expires_at, created_at, updated_at)
		VALUES ('LEGAC1','d','m','appdb','failed','2999-01-01T00:00:00.000000000Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// First Open: migrate() must ADD created_target to the legacy table.
	s, err := Open(path)
	require.NoError(t, err)
	got, err := s.GetDropoffByCode(context.Background(), "LEGAC1")
	require.NoError(t, err)
	require.False(t, got.CreatedTarget, "the back-filled column defaults to false")
	require.NoError(t, s.Close())

	// Second Open (a second startup): re-applying the guarded ALTER must be a no-op.
	s2, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

func TestMarkDropoffTargetCreated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.InsertDropoff(ctx, sampleDropoff("TGTCRT", time.Now().Add(time.Hour)))
	require.NoError(t, err)

	got, err := s.GetDropoffByCode(ctx, "TGTCRT")
	require.NoError(t, err)
	require.False(t, got.CreatedTarget, "created_target defaults false")

	require.NoError(t, s.MarkDropoffTargetCreated(ctx, "TGTCRT"))
	got, err = s.GetDropoffByCode(ctx, "TGTCRT")
	require.NoError(t, err)
	require.True(t, got.CreatedTarget, "the flag persists")

	// Idempotent, and a no-op for an unknown code (never errors).
	require.NoError(t, s.MarkDropoffTargetCreated(ctx, "TGTCRT"))
	require.NoError(t, s.MarkDropoffTargetCreated(ctx, "NOSUCH"))
}

// TestReconcileImportingDropoffs pins finding #2: startup reconciliation of sessions
// left 'importing' by a crash must consult the linked migration (completed -> the
// session is completed, not blindly failed) and must drop ONLY a target THIS import
// created in a NON-overwrite restore — never a pre-existing or an overwrite target.
func TestReconcileImportingDropoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour).UTC()

	mk := func(code string, createdTarget, overwrite bool, migStatus string) {
		var mid *int64
		if migStatus != "" {
			id, err := s.InsertMigration(ctx, MigrationRecord{
				Mode: "drop-off", Role: "target", Status: migStatus, TargetDatabase: "appdb", Code: code,
			})
			require.NoError(t, err)
			mid = &id
		}
		d := sampleDropoff(code, exp)
		d.Overwrite = overwrite
		d.CreatedTarget = createdTarget
		d.Status = "importing"
		d.MigrationID = mid
		_, err := s.InsertDropoff(ctx, d)
		require.NoError(t, err)
	}

	mk("DONELK", false, false, "completed") // linked migration completed -> completed
	mk("FAILCR", true, false, "failed")     // created + non-overwrite -> failed + DROP
	mk("FAILOW", true, true, "failed")      // created + overwrite     -> failed, NOT dropped
	mk("FAILEX", false, false, "")          // pre-existing (not created) -> failed, NOT dropped

	toDrop, err := s.ReconcileImportingDropoffs(ctx)
	require.NoError(t, err)

	var codes []string
	for _, d := range toDrop {
		codes = append(codes, d.Code)
	}
	require.Equal(t, []string{"FAILCR"}, codes, "only a created, non-overwrite target is returned for dropping")
	require.Equal(t, "appdb", toDrop[0].TargetDatabase)

	statusOf := func(code string) string {
		d, gerr := s.GetDropoffByCode(ctx, code)
		require.NoError(t, gerr)
		return d.Status
	}
	require.Equal(t, "completed", statusOf("DONELK"), "a completed linked migration reconciles to completed")
	require.Equal(t, "failed", statusOf("FAILCR"))
	require.Equal(t, "failed", statusOf("FAILOW"))
	require.Equal(t, "failed", statusOf("FAILEX"))
}
