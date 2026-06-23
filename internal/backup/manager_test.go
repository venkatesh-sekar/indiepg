package backup

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// fakeObjectStore is an in-memory identity.ObjectStore. To stay decoupled from
// identity's exact prefix/key join, marker reads/writes are matched by the
// MarkerObjectName suffix.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

// seedForeignMarker installs a marker owned by another instance that was last
// seen `age` ago (a small age => non-stale => HARD STOP; a large age => stale =>
// adoptable).
func (f *fakeObjectStore) seedForeignMarker(t *testing.T, age time.Duration) {
	t.Helper()
	m := identity.OwnershipMarker{
		InstanceID: "foreign-instance",
		Hostname:   "10.0.0.5",
		PGSystemID: "7300000000000000000",
		ClaimedAt:  time.Now().UTC().Add(-2 * time.Hour),
		LastSeen:   time.Now().UTC().Add(-age),
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[identity.MarkerObjectName] = data
}

func (f *fakeObjectStore) key(k string) string {
	if strings.HasSuffix(k, identity.MarkerObjectName) {
		return identity.MarkerObjectName
	}
	return k
}

func (f *fakeObjectStore) GetObject(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[f.key(key)]
	if !ok {
		return nil, core.NotFoundError("object %q not found", key)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeObjectStore) PutObject(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[f.key(key)] = append([]byte(nil), data...)
	return nil
}

func (f *fakeObjectStore) PutObjectIfAbsent(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(key)
	if _, exists := f.objects[k]; exists {
		return identity.ErrPreconditionFailed
	}
	f.objects[k] = append([]byte(nil), data...)
	return nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, f.key(key))
	return nil
}

func testIdentity() *identity.Identity {
	return &identity.Identity{
		InstanceID: "me-instance",
		Label:      "web-db-01",
		Hostname:   "host-a",
		PGSystemID: "7300000000000000000",
	}
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Stanza = "main"
	cfg.Backup.Prefix = "panel/me-instance"
	return cfg
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestManagerInfo(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})

	m := New(Options{Runner: runner, Config: testConfig(), Logger: core.Discard()})
	got, err := m.Info(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, TypeIncr, got[0].Type)
}

func TestManagerInfo_RunnerError(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("info", exec.FakeResponse{ExitCode: 1, Err: core.ExecError("boom")})

	m := New(Options{Runner: runner, Config: testConfig(), Logger: core.Discard()})
	_, err := m.Info(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestManagerBackup_SuccessRecordsHistory(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "completed"})
	runner.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})

	st := newTestStore(t)
	os := newFakeObjectStore()
	owner := identity.NewOwner(testIdentity(), os, core.Discard())

	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})
	res, err := m.Backup(ctx, TypeFull)
	require.NoError(t, err)
	require.True(t, res.OK)

	recs, err := st.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "success", recs[0].Result)
	require.Equal(t, "full", recs[0].BackupType)
	// Stats enriched from the info refresh (newest first = the incr backup).
	require.Equal(t, "20260102-030000F_20260102-040000I", recs[0].Label)
	require.Equal(t, int64(65536), recs[0].RepoBytes)

	// The marker must now exist and be claimed by me.
	mk, err := owner.Verify(ctx, testConfig().Backup.Prefix)
	require.NoError(t, err)
	require.Equal(t, "me-instance", mk.InstanceID)
}

func TestManagerBackup_RunFailureRecordsFailRow(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{ExitCode: 50, Err: core.ExecError("pgbackrest failed")})

	st := newTestStore(t)
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())

	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})
	_, err := m.Backup(ctx, TypeIncr)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	recs, err := st.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "fail", recs[0].Result)
	require.NotEmpty(t, recs[0].Error)
}

func TestManagerBackup_ForeignOwnerHardStop(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "should not run"})

	os := newFakeObjectStore()
	os.seedForeignMarker(t, 1*time.Minute) // recent => non-stale => HARD STOP
	owner := identity.NewOwner(testIdentity(), os, core.Discard())

	st := newTestStore(t)
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	_, err := m.Backup(ctx, TypeFull)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	var oe *core.OwnershipError
	require.ErrorAs(t, err, &oe)
	require.Equal(t, "foreign-instance", oe.OwnerID)

	// pgBackRest must never have been invoked, and no history row written.
	for _, c := range runner.Calls() {
		require.NotContains(t, c.Args, "backup")
	}
	recs, err := st.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, recs)
}

// TestManagerBackup_ConcurrentSkipsWithoutFailRow proves the process-local
// single-flight guard: a backup attempted while another is in flight is a clean,
// typed skip (CodeConflict) — pgBackRest is never invoked and NO failure row is
// recorded, so an overlap (e.g. a manual backup during a scheduled one) can never
// raise a false backup-failed alert.
func TestManagerBackup_ConcurrentSkipsWithoutFailRow(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "should not run"})

	st := newTestStore(t)
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	// Simulate a backup already in flight by holding the single-flight guard.
	m.backupMu.Lock()
	defer m.backupMu.Unlock()

	_, err := m.Backup(ctx, TypeIncr)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	// No pgBackRest invocation and no history row — the overlap left no trace.
	for _, c := range runner.Calls() {
		require.NotContains(t, c.Args, "backup")
	}
	recs, lerr := st.ListBackups(ctx, 10)
	require.NoError(t, lerr)
	require.Empty(t, recs)
}

func TestManagerBackup_NoOwnerDegrades(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "ok"})
	runner.On("info", exec.FakeResponse{Stdout: `[{"name":"main","backup":[]}]`})

	st := newTestStore(t)
	// testConfig configures no remote target (no bucket/endpoint), so the repo is
	// explicitly local-only and a nil Owner is allowed to proceed.
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Logger: core.Discard()})

	res, err := m.Backup(ctx, TypeFull)
	require.NoError(t, err)
	require.True(t, res.OK)
}

// TestManagerBackup_NilOwnerWithRemoteTargetFailsClosed proves the fail-closed
// guard: when a remote/S3 target is configured but no Owner is wired, Backup
// refuses (ownership error) and never invokes pgBackRest, rather than silently
// writing a shared repo without single-writer protection.
func TestManagerBackup_NilOwnerWithRemoteTargetFailsClosed(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "should not run"})

	cfg := testConfig()
	cfg.Backup.Bucket = "my-bucket" // remote target configured

	st := newTestStore(t)
	m := New(Options{Runner: runner, Store: st, Config: cfg, Logger: core.Discard()})

	_, err := m.Backup(ctx, TypeFull)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	// pgBackRest must never have been invoked, and no history row written.
	require.Zero(t, runner.CallCount())
	recs, err := st.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, recs)
}

// TestManagerBackup_NilOwnerWithEndpointOnlyFailsClosed proves the guard also
// trips when only an endpoint (no bucket) names a remote target.
func TestManagerBackup_NilOwnerWithEndpointOnlyFailsClosed(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "should not run"})

	cfg := testConfig()
	cfg.Backup.Endpoint = "s3.us-west-002.backblazeb2.com"

	m := New(Options{Runner: runner, Config: cfg, Logger: core.Discard()})

	_, err := m.Backup(ctx, TypeFull)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	require.Zero(t, runner.CallCount())
}

// TestManagerBackup_FailureRowPersistedOnCancelledCtx proves the terminal
// failure history row is written even when the operation ctx is already
// cancelled (e.g. a shutdown cancelled the backup): the record uses a detached
// context so the failure is not lost.
func TestManagerBackup_FailureRowPersistedOnCancelledCtx(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{ExitCode: 50, Err: core.ExecError("pgbackrest failed")})

	st := newTestStore(t)
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	// Cancel the ctx before the run resolves; the terminal record must still land.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Backup(ctx, TypeIncr)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))

	// The fail row is persisted despite the cancelled operation ctx, because the
	// store write runs on a detached context.
	recs, err := st.ListBackups(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "fail", recs[0].Result)
	require.NotEmpty(t, recs[0].Error)
}

func TestManagerBackup_InvalidStanza(t *testing.T) {
	runner := exec.NewFakeRunner()
	cfg := testConfig()
	cfg.Stanza = "Invalid Stanza"
	m := New(Options{Runner: runner, Config: cfg, Logger: core.Discard()})
	_, err := m.Backup(context.Background(), TypeFull)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Zero(t, runner.CallCount())
}

func TestManagerRestore_RequiresConfirmation(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())

	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	// Wrong confirmation -> safety error, restore never runs.
	_, err := m.Restore(context.Background(), nil, false, "wrong")
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	require.Zero(t, runner.CallCount())
}

func TestManagerRestore_Confirmed(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	// A safety backup is taken before the destructive restore, so the runner must
	// answer the pre-restore backup + its info refresh as well as the restore.
	runner.On("backup", exec.FakeResponse{Stdout: "ok"})
	runner.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})

	// Repo is unclaimed (empty store) -> Verify returns NotFound, which is fine
	// for a read-side restore.
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	res, err := m.Restore(ctx, nil, true, "main")
	require.NoError(t, err)
	require.True(t, res.OK)

	// The safety backup label is surfaced as the operator's recovery point.
	require.NotEmpty(t, res.Data["safety_backup_label"])

	// Verify the restore command actually ran with --delta. It is the last call,
	// after the safety backup + info refresh.
	calls := runner.Calls()
	restoreCall := calls[len(calls)-1]
	require.Contains(t, restoreCall.Args, "restore")
	require.Contains(t, restoreCall.Args, "--delta")
}

func TestManagerRestore_PITRConfirmed(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "ok"})
	runner.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	tm := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	res, err := m.Restore(ctx, &RecoveryTarget{Time: &tm}, false, "main")
	require.NoError(t, err)
	require.Equal(t, true, res.Data["pitr"])
}

// TestManagerRestore_TakesSafetyBackupFirst proves the Safety DNA: before the
// destructive restore overwrites the live cluster, a full safety backup runs and
// is recorded in history, and the restore command runs strictly after it.
func TestManagerRestore_TakesSafetyBackupFirst(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "ok"})
	runner.On("info", exec.FakeResponse{Stdout: sampleInfoJSON})
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})

	st := newTestStore(t)
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	res, err := m.Restore(ctx, nil, false, "main")
	require.NoError(t, err)
	require.True(t, res.OK)
	require.NotEmpty(t, res.Data["safety_backup_label"])

	// The safety backup must have been written to history as a full backup.
	recs, err := st.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "success", recs[0].Result)
	require.Equal(t, "full", recs[0].BackupType)

	// The backup command must precede the restore command in call order.
	var backupIdx, restoreIdx = -1, -1
	for i, c := range runner.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "backup") && backupIdx == -1 {
			backupIdx = i
		}
		if strings.Contains(joined, "restore") {
			restoreIdx = i
		}
	}
	require.GreaterOrEqual(t, backupIdx, 0)
	require.GreaterOrEqual(t, restoreIdx, 0)
	require.Less(t, backupIdx, restoreIdx, "safety backup must run before the restore")
}

// TestManagerRestore_SafetyBackupFailureHardStops proves that if the pre-restore
// safety backup fails, the restore is refused (HARD STOP) and pgBackRest restore
// is never invoked — the live cluster is left untouched.
func TestManagerRestore_SafetyBackupFailureHardStops(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{ExitCode: 50, Err: core.ExecError("pgbackrest backup failed")})
	runner.On("restore", exec.FakeResponse{Stdout: "should not run"})

	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	_, err := m.Restore(ctx, nil, false, "main")
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))

	// Restore must never have been invoked.
	for _, c := range runner.Calls() {
		require.NotContains(t, c.Args, "restore")
	}
}

func TestManagerRestore_ForeignOwnerHardStop(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("restore", exec.FakeResponse{Stdout: "should not run"})

	os := newFakeObjectStore()
	os.seedForeignMarker(t, 1*time.Minute) // non-stale foreign owner
	owner := identity.NewOwner(testIdentity(), os, core.Discard())

	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})
	_, err := m.Restore(ctx, nil, false, "main")
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	require.Zero(t, runner.CallCount())
}

func TestManagerRestore_InvalidTarget(t *testing.T) {
	tm := time.Now()
	m := New(Options{Runner: exec.NewFakeRunner(), Config: testConfig(), Logger: core.Discard()})
	_, err := m.Restore(context.Background(), &RecoveryTarget{Time: &tm, XID: "1"}, false, "main")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}
