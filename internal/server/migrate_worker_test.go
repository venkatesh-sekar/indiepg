package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
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

// TestStoreRecorder_FailPersistsSanitizedDiagnostic pins round-5 finding #3: the
// actionable pg_restore reason carried in Error.Details["stderr"] must be persisted
// into the migration error text the operator reads (not just a generic exit failure),
// while the round-4 DDL-body stripping is kept so an echoed "Command was:" body — which
// can embed a secret — never lands in the persisted error.
func TestStoreRecorder_FailPersistsSanitizedDiagnostic(t *testing.T) {
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := st.InsertMigration(context.Background(), store.MigrationRecord{
		Mode:   string(migrate.ModeDropOff),
		Role:   "target",
		Status: string(migrate.StatusImporting),
		Phase:  string(migrate.PhaseRestoring),
	})
	require.NoError(t, err)

	// A pg_restore-style failure: the actionable reason is in the "stderr" detail, and
	// the echoed DDL body after "Command was:" can embed a secret.
	stderr := "pg_restore: error: could not execute query: ERROR:  relation \"users\" already exists\n" +
		"Command was: CREATE FUNCTION secret() RETURNS void AS $$ PASSWORD 'hunter2' $$;"
	cause := core.ExecError("pg_restore into \"appdb\" failed").
		WithDetail("stderr", stderr).Wrap(errors.New("exit status 1"))

	rec := newStoreRecorder(st, id)
	require.NoError(t, rec.Fail(context.Background(), cause))

	got, err := st.GetMigration(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, string(migrate.StatusFailed), got.Status)
	// The operator now sees the actionable PostgreSQL reason...
	require.Contains(t, got.Error, "relation \"users\" already exists")
	// ...but never the echoed DDL body or the secret inside it (round-4 stripping kept).
	require.NotContains(t, got.Error, "hunter2")
	require.NotContains(t, got.Error, "CREATE FUNCTION secret")
}
