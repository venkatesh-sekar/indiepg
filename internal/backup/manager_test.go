package backup

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
	"github.com/venkatesh-sekar/pgpanel/internal/identity"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
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

func TestManagerBackup_NoOwnerDegrades(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("backup", exec.FakeResponse{Stdout: "ok"})
	runner.On("info", exec.FakeResponse{Stdout: `[{"name":"main","backup":[]}]`})

	st := newTestStore(t)
	m := New(Options{Runner: runner, Store: st, Config: testConfig(), Logger: core.Discard()})

	res, err := m.Backup(ctx, TypeFull)
	require.NoError(t, err)
	require.True(t, res.OK)
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
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})

	// Repo is unclaimed (empty store) -> Verify returns NotFound, which is fine
	// for a read-side restore.
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	res, err := m.Restore(ctx, nil, true, "main")
	require.NoError(t, err)
	require.True(t, res.OK)

	// Verify the restore command actually ran with --delta.
	require.Equal(t, 1, runner.CallCount())
	require.Contains(t, runner.Calls()[0].Args, "--delta")
}

func TestManagerRestore_PITRConfirmed(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewFakeRunner()
	runner.On("restore", exec.FakeResponse{Stdout: "ok"})
	owner := identity.NewOwner(testIdentity(), newFakeObjectStore(), core.Discard())
	m := New(Options{Runner: runner, Config: testConfig(), Owner: owner, Logger: core.Discard()})

	tm := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	res, err := m.Restore(ctx, &RecoveryTarget{Time: &tm}, false, "main")
	require.NoError(t, err)
	require.Equal(t, true, res.Data["pitr"])
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
