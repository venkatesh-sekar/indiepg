package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestInsertAndGetMigration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.InsertMigration(ctx, MigrationRecord{
		Mode:           "single-db",
		Role:           "target",
		Status:         "validating",
		Phase:          "validating",
		SourceSummary:  "admin@db.example.com:5432/app",
		TargetDatabase: "app",
		Overwrite:      true,
		Code:           "ABC123",
		ProgressDone:   1,
		ProgressTotal:  3,
		BytesTotal:     42,
	})
	require.NoError(t, err)
	require.Greater(t, id, int64(0))

	got, err := s.GetMigration(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.ID)
	require.Equal(t, "single-db", got.Mode)
	require.Equal(t, "target", got.Role)
	require.Equal(t, "validating", got.Status)
	require.Equal(t, "validating", got.Phase)
	require.Equal(t, "admin@db.example.com:5432/app", got.SourceSummary)
	require.Equal(t, "app", got.TargetDatabase)
	require.True(t, got.Overwrite)
	require.Equal(t, "ABC123", got.Code)
	require.Equal(t, int64(1), got.ProgressDone)
	require.Equal(t, int64(3), got.ProgressTotal)
	require.Equal(t, int64(42), got.BytesTotal)
	// Row counts default to "{}" when not supplied.
	require.Equal(t, "{}", got.RowCountsSrc)
	require.Equal(t, "{}", got.RowCountsTgt)
	// Timestamps default to now; finished is nil for a live job.
	require.False(t, got.CreatedAt.IsZero())
	require.False(t, got.UpdatedAt.IsZero())
	require.Nil(t, got.FinishedAt)
}

func TestInsertMigrationHonorsExplicitTimestamps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	id, err := s.InsertMigration(ctx, MigrationRecord{
		Mode:      "cluster",
		Status:    "validating",
		CreatedAt: created,
		UpdatedAt: updated,
	})
	require.NoError(t, err)

	got, err := s.GetMigration(ctx, id)
	require.NoError(t, err)
	require.True(t, created.Equal(got.CreatedAt))
	require.True(t, updated.Equal(got.UpdatedAt))
}

func TestGetMigrationNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetMigration(context.Background(), 999)
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestGetMigrationByCode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two records sharing a code; GetMigrationByCode returns the most recent.
	_, err := s.InsertMigration(ctx, MigrationRecord{Mode: "ssh-less", Status: "exporting", Code: "ZZZ999"})
	require.NoError(t, err)
	id2, err := s.InsertMigration(ctx, MigrationRecord{Mode: "ssh-less", Status: "importing", Code: "ZZZ999"})
	require.NoError(t, err)

	got, err := s.GetMigrationByCode(ctx, "ZZZ999")
	require.NoError(t, err)
	require.Equal(t, id2, got.ID)
	require.Equal(t, "importing", got.Status)
}

func TestGetMigrationByCodeNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetMigrationByCode(context.Background(), "NOPE00")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestListMigrationsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id1, err := s.InsertMigration(ctx, MigrationRecord{Mode: "single-db", Status: "completed"})
	require.NoError(t, err)
	id2, err := s.InsertMigration(ctx, MigrationRecord{Mode: "cluster", Status: "failed"})
	require.NoError(t, err)
	id3, err := s.InsertMigration(ctx, MigrationRecord{Mode: "ssh-less", Status: "importing"})
	require.NoError(t, err)

	list, err := s.ListMigrations(ctx, 0) // 0 => default limit
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, id3, list[0].ID)
	require.Equal(t, id2, list[1].ID)
	require.Equal(t, id1, list[2].ID)

	// limit caps the result set.
	limited, err := s.ListMigrations(ctx, 2)
	require.NoError(t, err)
	require.Len(t, limited, 2)
	require.Equal(t, id3, limited[0].ID)
}

func TestListMigrationsEmpty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListMigrations(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestUpdateMigration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.InsertMigration(ctx, MigrationRecord{
		Mode:   "single-db",
		Status: "validating",
		Phase:  "validating",
	})
	require.NoError(t, err)

	original, err := s.GetMigration(ctx, id)
	require.NoError(t, err)

	// Advance the job to completed with row counts and a finished timestamp.
	finished := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	rec := *original
	rec.Status = "completed"
	rec.Phase = "verifying"
	rec.ProgressDone = 5
	rec.ProgressTotal = 5
	rec.RowCountsSrc = `{"public.users":10}`
	rec.RowCountsTgt = `{"public.users":10}`
	rec.FinishedAt = &finished
	require.NoError(t, s.UpdateMigration(ctx, rec))

	got, err := s.GetMigration(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	require.Equal(t, "verifying", got.Phase)
	require.Equal(t, int64(5), got.ProgressDone)
	require.Equal(t, `{"public.users":10}`, got.RowCountsSrc)
	require.Equal(t, `{"public.users":10}`, got.RowCountsTgt)
	require.NotNil(t, got.FinishedAt)
	require.True(t, finished.Equal(*got.FinishedAt))
	// updated_at is always bumped past the original.
	require.False(t, got.UpdatedAt.Before(original.UpdatedAt))
}

func TestUpdateMigrationEmptyRowCountsDefaultToJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.InsertMigration(ctx, MigrationRecord{Mode: "single-db", Status: "validating"})
	require.NoError(t, err)

	got, err := s.GetMigration(ctx, id)
	require.NoError(t, err)
	got.RowCountsSrc = ""
	got.RowCountsTgt = ""
	require.NoError(t, s.UpdateMigration(ctx, *got))

	reloaded, err := s.GetMigration(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "{}", reloaded.RowCountsSrc)
	require.Equal(t, "{}", reloaded.RowCountsTgt)
}

func TestSweepRunningMigrations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two running jobs and one already-terminal job.
	running1, err := s.InsertMigration(ctx, MigrationRecord{Mode: "single-db", Status: "dumping"})
	require.NoError(t, err)
	running2, err := s.InsertMigration(ctx, MigrationRecord{Mode: "cluster", Status: "validating"})
	require.NoError(t, err)
	done, err := s.InsertMigration(ctx, MigrationRecord{Mode: "single-db", Status: "completed"})
	require.NoError(t, err)

	n, err := s.SweepRunningMigrations(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	r1, err := s.GetMigration(ctx, running1)
	require.NoError(t, err)
	require.Equal(t, "failed", r1.Status)
	require.Equal(t, "", r1.Phase)
	require.Equal(t, "interrupted by panel restart", r1.Error)
	require.NotNil(t, r1.FinishedAt)

	r2, err := s.GetMigration(ctx, running2)
	require.NoError(t, err)
	require.Equal(t, "failed", r2.Status)

	// The already-completed job is untouched.
	d, err := s.GetMigration(ctx, done)
	require.NoError(t, err)
	require.Equal(t, "completed", d.Status)
	require.Empty(t, d.Error)
	require.Nil(t, d.FinishedAt)
}

func TestSweepRunningMigrationsNoOpWhenNoneRunning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.InsertMigration(ctx, MigrationRecord{Mode: "single-db", Status: "completed"})
	require.NoError(t, err)
	_, err = s.InsertMigration(ctx, MigrationRecord{Mode: "cluster", Status: "expired"})
	require.NoError(t, err)

	n, err := s.SweepRunningMigrations(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
