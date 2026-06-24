package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/migrate"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// The detached migration workers must run on a BOUNDED context: a source that
// accepts the connection then stalls can otherwise wedge a worker (and its
// "importing" job) forever on the old context.Background(). workerContext is the
// single seam every worker derives its context from, so proving it carries a
// deadline ~migrationJobTimeout out proves the bound is wired in.
func TestWorkerContext_isBounded(t *testing.T) {
	ctx, cancel := workerContext()
	defer cancel()

	dl, ok := ctx.Deadline()
	require.True(t, ok, "worker context must carry a deadline, never run unbounded")
	remaining := time.Until(dl)
	require.Greater(t, remaining, migrationJobTimeout-time.Minute,
		"deadline should be ~migrationJobTimeout out")
	require.LessOrEqual(t, remaining, migrationJobTimeout)
}

// Cancelling the worker context releases it promptly (the worker bodies select
// on ctx.Done()), and a shrunk timeout fires on its own — the mechanism that
// turns a stalled source into a timely job failure instead of a hang.
func TestWorkerContext_timeoutFires(t *testing.T) {
	orig := migrationJobTimeout
	migrationJobTimeout = 30 * time.Millisecond
	defer func() { migrationJobTimeout = orig }()

	ctx, cancel := workerContext()
	defer cancel()

	select {
	case <-ctx.Done():
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("worker context did not expire at its timeout")
	}
}

// The terminal failure write must land even when the worker context has ALREADY
// expired — that is the headline scenario (a stalled source blows the worker
// deadline). If Fail used the expired context the store write would be a no-op
// and the job would stay wedged in "importing" forever, defeating the timeout.
func TestStoreRecorder_FailPersistsWithExpiredContext(t *testing.T) {
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := st.InsertMigration(context.Background(), store.MigrationRecord{
		Mode:   string(migrate.ModeSingleDB),
		Role:   "direct",
		Status: string(migrate.StatusImporting),
		Phase:  string(migrate.PhaseDumping),
	})
	require.NoError(t, err)

	// An already-cancelled context, as the worker hands to rec.Fail on timeout.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	rec := newStoreRecorder(st, id)
	require.NoError(t, rec.Fail(dead, context.DeadlineExceeded),
		"Fail must persist despite the expired worker context")

	got, err := st.GetMigration(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, string(migrate.StatusFailed), got.Status,
		"failed status must be persisted, not silently dropped")
	require.NotNil(t, got.FinishedAt)
	require.Contains(t, got.Error, "deadline exceeded")
}
