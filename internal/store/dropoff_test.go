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

	// One expired+waiting, one live, one expired+completed (terminal, excluded).
	_, err := s.InsertDropoff(ctx, sampleDropoff("EXPIRE", now.Add(-time.Hour)))
	require.NoError(t, err)
	_, err = s.InsertDropoff(ctx, sampleDropoff("LIVEAA", now.Add(time.Hour)))
	require.NoError(t, err)
	done := sampleDropoff("DONEAA", now.Add(-time.Hour))
	done.Status = "completed"
	_, err = s.InsertDropoff(ctx, done)
	require.NoError(t, err)

	expired, err := s.ListExpiredDropoffs(ctx, now, 100)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, "EXPIRE", expired[0].Code)
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
