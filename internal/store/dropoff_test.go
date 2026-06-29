package store

import (
	"context"
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
	//   - waiting + live     -> NOT swept (still inside its TTL)
	//   - importing+ expired -> NOT swept (a live import owns its dump)
	//   - completed+ expired -> NOT swept (objects already deleted on success)
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
	insert("EXPDEX", "expired", now.Add(-time.Hour))

	expired, err := s.ListExpiredDropoffs(ctx, now, 100)
	require.NoError(t, err)
	codes := make([]string, len(expired))
	for i, e := range expired {
		codes[i] = e.Code
	}
	require.ElementsMatch(t, []string{"WAITEX", "FAILEX"}, codes,
		"only past-TTL waiting/uploaded/failed (never importing/completed/expired) are swept")
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

	// 'completed' and 'expired' are terminal — never startable.
	mk("DONEXX", "completed")
	won, err = s.ClaimDropoffForImport(ctx, "DONEXX")
	require.NoError(t, err)
	require.False(t, won)

	mk("EXPIRX", "expired")
	won, err = s.ClaimDropoffForImport(ctx, "EXPIRX")
	require.NoError(t, err)
	require.False(t, won)

	// Unknown code is a benign miss, not an error.
	won, err = s.ClaimDropoffForImport(ctx, "NOPENO")
	require.NoError(t, err)
	require.False(t, won)
}

func TestSweepRunningDropoffs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	importing := sampleDropoff("IMPORT", time.Now().Add(time.Hour))
	importing.Status = "importing"
	_, err := s.InsertDropoff(ctx, importing)
	require.NoError(t, err)
	_, err = s.InsertDropoff(ctx, sampleDropoff("WAITAA", time.Now().Add(time.Hour)))
	require.NoError(t, err)

	n, err := s.SweepRunningDropoffs(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.GetDropoffByCode(ctx, "IMPORT")
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Contains(t, got.Error, "interrupted")

	// A waiting session is untouched (its worker never ran).
	wait, err := s.GetDropoffByCode(ctx, "WAITAA")
	require.NoError(t, err)
	require.Equal(t, "waiting_for_upload", wait.Status)
}
