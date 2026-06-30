package migrate

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// fakeEngine is a programmable PgEngine test double. Each method consults a
// scripted return value (or per-database value for the count maps) and records
// the calls it received so a test can assert ordering and arguments. The default
// zero value behaves like a reachable, empty server.
type fakeEngine struct {
	mu sync.Mutex

	versionErr error
	version    string
	// versionBlocks makes Version block until the context is cancelled, modelling
	// a source that accepts the connection then stalls. It returns ctx.Err().
	versionBlocks bool

	databases   []DatabaseSize
	listErr     error
	exists      bool
	existsErr   error
	nonEmpty    bool
	nonEmptyErr error
	createErr   error
	dropErr     error

	dumpInfo          DumpInfo
	dumpErr           error
	globalsErr        error
	globalsRestoreErr error
	restoreErr        error
	rowCountErr       error
	// rowCounts maps "<conn-tag>:<db>" -> counts so the source and target can be
	// distinguished. connTag is "src" for non-local, "tgt" for local.
	rowCounts map[string]map[string]int64
	// rowCountsByTable is the structured counterpart consulted by RowCountsByTable;
	// when set for a "<conn-tag>:<db>" it takes precedence, letting a test express
	// (schema, name) pairs that would collide if flattened (e.g. "a"/"b.c" vs
	// "a.b"/"c"). When unset, RowCountsByTable derives pairs from rowCounts by
	// splitting each flat key on its first dot (matching the drop-off meta helper).
	rowCountsByTable map[string]map[TableKey]int64

	// recorded calls for assertions.
	dumps          []string // databases dumped
	restores       []string // target databases restored into
	created        []string
	dropped        []string
	globalsDump    int
	globalsRestore int
}

func (f *fakeEngine) connTag(c ConnInfo) string {
	if c.Local() {
		return "tgt"
	}
	return "src"
}

func (f *fakeEngine) Version(ctx context.Context, conn ConnInfo) (string, error) {
	if f.versionBlocks {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.version, f.versionErr
}

func (f *fakeEngine) ListDatabases(ctx context.Context, conn ConnInfo) ([]DatabaseSize, error) {
	return f.databases, f.listErr
}

func (f *fakeEngine) DatabaseExists(ctx context.Context, conn ConnInfo, name string) (bool, error) {
	return f.exists, f.existsErr
}

func (f *fakeEngine) DatabaseNonEmpty(ctx context.Context, conn ConnInfo, name string) (bool, error) {
	return f.nonEmpty, f.nonEmptyErr
}

func (f *fakeEngine) CreateDatabase(ctx context.Context, conn ConnInfo, name, owner string) error {
	f.mu.Lock()
	f.created = append(f.created, name)
	f.mu.Unlock()
	return f.createErr
}

func (f *fakeEngine) DropDatabase(ctx context.Context, conn ConnInfo, name string) error {
	f.mu.Lock()
	f.dropped = append(f.dropped, name)
	f.mu.Unlock()
	return f.dropErr
}

func (f *fakeEngine) Dump(ctx context.Context, conn ConnInfo, database, outPath string, opts DumpOpts) (DumpInfo, error) {
	f.mu.Lock()
	f.dumps = append(f.dumps, database)
	f.mu.Unlock()
	if f.dumpErr != nil {
		return DumpInfo{}, f.dumpErr
	}
	info := f.dumpInfo
	info.Database = database
	info.Path = outPath
	// Write a tiny file so the export path's os.ReadFile succeeds and the
	// checksum is deterministic when the test does not set one explicitly.
	if outPath != "" {
		_ = writeFakeDump(outPath)
	}
	return info, nil
}

func (f *fakeEngine) DumpGlobals(ctx context.Context, conn ConnInfo, outPath string) error {
	f.mu.Lock()
	f.globalsDump++
	f.mu.Unlock()
	if f.globalsErr != nil {
		return f.globalsErr
	}
	// Write a tiny file so RestoreGlobals' real engine os.ReadFile would succeed;
	// the orchestrator only passes the path along so this is belt-and-suspenders.
	if outPath != "" {
		_ = os.WriteFile(outPath, []byte("-- globals"), 0o600)
	}
	return nil
}

func (f *fakeEngine) RestoreGlobals(ctx context.Context, conn ConnInfo, path string) error {
	f.mu.Lock()
	f.globalsRestore++
	f.mu.Unlock()
	return f.globalsRestoreErr
}

func (f *fakeEngine) Restore(ctx context.Context, conn ConnInfo, dumpPath, targetDatabase string, opts RestoreOpts) error {
	f.mu.Lock()
	f.restores = append(f.restores, targetDatabase)
	f.mu.Unlock()
	return f.restoreErr
}

func (f *fakeEngine) RowCounts(ctx context.Context, conn ConnInfo, database string) (map[string]int64, error) {
	if f.rowCountErr != nil {
		return nil, f.rowCountErr
	}
	if f.rowCounts == nil {
		return map[string]int64{}, nil
	}
	key := f.connTag(conn) + ":" + database
	if m, ok := f.rowCounts[key]; ok {
		return m, nil
	}
	return map[string]int64{}, nil
}

func (f *fakeEngine) RowCountsByTable(ctx context.Context, conn ConnInfo, database string) (map[TableKey]int64, error) {
	if f.rowCountErr != nil {
		return nil, f.rowCountErr
	}
	key := f.connTag(conn) + ":" + database
	if f.rowCountsByTable != nil {
		if m, ok := f.rowCountsByTable[key]; ok {
			return m, nil
		}
	}
	// Fall back to deriving structured pairs from the flat rowCounts map, splitting
	// each key on its first dot exactly like the drop-off meta helper (splitKey).
	out := map[TableKey]int64{}
	if f.rowCounts != nil {
		if m, ok := f.rowCounts[key]; ok {
			for k, n := range m {
				schema, name := splitKey(k)
				out[TableKey{Schema: schema, Name: name}] = n
			}
		}
	}
	return out, nil
}

func writeFakeDump(path string) error {
	return os.WriteFile(path, []byte("PGDMP-fake"), 0o600)
}

// fakeRecorder captures the orchestrator's progress sink for assertions.
type fakeRecorder struct {
	mu        sync.Mutex
	stages    []stageCall
	progress  []progressCall
	failed    error
	failCount int
	succeeded bool
	srcCounts map[string]int64
	tgtCounts map[string]int64

	stageErr error
}

type stageCall struct {
	status Status
	phase  Phase
}

type progressCall struct {
	done, total, bytes int64
}

func (r *fakeRecorder) Stage(ctx context.Context, status Status, phase Phase) error {
	r.mu.Lock()
	r.stages = append(r.stages, stageCall{status, phase})
	r.mu.Unlock()
	return r.stageErr
}

func (r *fakeRecorder) Progress(ctx context.Context, done, total, bytesTotal int64) error {
	r.mu.Lock()
	r.progress = append(r.progress, progressCall{done, total, bytesTotal})
	r.mu.Unlock()
	return nil
}

func (r *fakeRecorder) Fail(ctx context.Context, cause error) error {
	r.mu.Lock()
	r.failed = cause
	r.failCount++
	r.mu.Unlock()
	return nil
}

func (r *fakeRecorder) Succeed(ctx context.Context, src, tgt map[string]int64) error {
	r.mu.Lock()
	r.succeeded = true
	r.srcCounts = src
	r.tgtCounts = tgt
	r.mu.Unlock()
	return nil
}

func (r *fakeRecorder) phases() []Phase {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Phase, 0, len(r.stages))
	for _, s := range r.stages {
		out = append(out, s.phase)
	}
	return out
}

// srcConn is a remote (non-local) source.
func srcConn(db string) ConnInfo {
	return ConnInfo{Host: "10.0.0.9", Port: "5432", User: "app", Password: "s3cr3t", Database: db}
}

// tgtConn is the local target.
func tgtConn() ConnInfo {
	return ConnInfo{Host: "/var/run/postgresql", Port: "5432"}
}

func newOrch(t *testing.T, eng PgEngine, svc *Service, os ObjectStore) *Orchestrator {
	t.Helper()
	return NewOrchestrator(eng, svc, os, t.TempDir(), core.Discard())
}

// ---------------------------------------------------------------------------
// Direct single-db
// ---------------------------------------------------------------------------

func TestDirect_single_happyPath(t *testing.T) {
	eng := &fakeEngine{
		dumpInfo: DumpInfo{SizeBytes: 1234, Checksum: "abc"},
		rowCounts: map[string]map[string]int64{
			"src:appdb":  {"public.users": 3},
			"tgt:appdb2": {"public.users": 3},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	job := Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb2"}
	require.NoError(t, o.Direct(context.Background(), job, rec))

	require.True(t, rec.succeeded)
	require.Nil(t, rec.failed)
	require.Equal(t, []string{"appdb"}, eng.dumps)
	require.Equal(t, []string{"appdb2"}, eng.restores)
	require.Equal(t, []Phase{PhaseValidating, PhaseDumping, PhaseRestoring, PhaseVerifying}, rec.phases())
	require.Equal(t, map[string]int64{"public.users": 3}, rec.srcCounts)
}

func TestDirect_single_sourceUnreachable(t *testing.T) {
	eng := &fakeEngine{versionErr: core.ExecError("connection refused")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	err := o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Equal(t, 1, rec.failCount)
	require.False(t, rec.succeeded)
	require.Empty(t, eng.dumps, "must not dump when source is unreachable")
	// The redacted source summary must not leak the password.
	require.NotContains(t, err.Error(), "s3cr3t")
}

// A source that accepts the connection then stalls (Version never returns) must
// not hang the orchestrator: the bounded worker context (modelled here by a
// short deadline) expires, the job fails promptly with the context error, and no
// dump is ever taken. Without the worker-level timeout this test would block to
// the package test timeout.
func TestDirect_single_stalledSourceFailsAtDeadline(t *testing.T) {
	eng := &fakeEngine{versionBlocks: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- o.Direct(ctx, Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, 1, rec.failCount)
		require.False(t, rec.succeeded)
		require.Empty(t, eng.dumps, "must not dump a stalled source")
	case <-time.After(5 * time.Second):
		t.Fatal("Direct hung past the source deadline instead of failing")
	}
}

func TestDirect_single_nonEmptyTargetWithoutOverwrite(t *testing.T) {
	eng := &fakeEngine{nonEmpty: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	err := o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb", Overwrite: false}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	se, ok := err.(*core.SafetyError)
	require.True(t, ok, "must be a typed SafetyError so the UI can prompt")
	require.Equal(t, "appdb", se.Expected)
	require.Equal(t, 1, rec.failCount)
	require.Empty(t, eng.dumps)
}

func TestDirect_single_overwriteDropsAndRecreates(t *testing.T) {
	eng := &fakeEngine{
		nonEmpty: true, // ignored because Overwrite short-circuits the gate
		dumpInfo: DumpInfo{SizeBytes: 1},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	job := Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb", Overwrite: true}
	require.NoError(t, o.Direct(context.Background(), job, rec))
	require.Equal(t, []string{"appdb"}, eng.dropped)
	require.Equal(t, []string{"appdb"}, eng.created)
}

func TestDirect_single_dumpFails(t *testing.T) {
	eng := &fakeEngine{dumpErr: core.ExecError("pg_dump boom")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	err := o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Empty(t, eng.restores, "must not restore when the dump failed")
	require.Equal(t, 1, rec.failCount)
}

func TestDirect_single_restoreFatal(t *testing.T) {
	eng := &fakeEngine{dumpInfo: DumpInfo{SizeBytes: 1}, restoreErr: core.ExecError("pg_restore error: relation exists")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	err := o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.False(t, rec.succeeded)
}

func TestDirect_single_rowMismatch(t *testing.T) {
	eng := &fakeEngine{
		dumpInfo: DumpInfo{SizeBytes: 1},
		rowCounts: map[string]map[string]int64{
			"src:appdb": {"public.users": 10},
			"tgt:appdb": {"public.users": 7},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	err := o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.False(t, rec.succeeded)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDirect_unsupportedMode(t *testing.T) {
	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{}, nil, nil)
	err := o.Direct(context.Background(), Job{Mode: Mode("bogus")}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Equal(t, 1, rec.failCount)
}

// ---------------------------------------------------------------------------
// Direct cluster
// ---------------------------------------------------------------------------

func TestDirect_cluster_happyPath(t *testing.T) {
	eng := &fakeEngine{
		databases: []DatabaseSize{{Name: "appdb", SizeBytes: 10}, {Name: "analytics", SizeBytes: 20}},
		dumpInfo:  DumpInfo{SizeBytes: 5},
		rowCounts: map[string]map[string]int64{
			"src:appdb":     {"public.a": 1},
			"tgt:appdb":     {"public.a": 1},
			"src:analytics": {"public.b": 2},
			"tgt:analytics": {"public.b": 2},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	require.NoError(t, o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn()}, rec))
	require.True(t, rec.succeeded)
	require.Equal(t, 1, eng.globalsDump, "globals must be dumped exactly once, first")
	require.Equal(t, 1, eng.globalsRestore, "globals must be replayed into the target exactly once")
	require.Equal(t, []string{"appdb", "analytics"}, eng.dumps)
	// Per-db restores all target the maintenance db with --create.
	require.Equal(t, []string{"postgres", "postgres"}, eng.restores)
	// Merged counts are namespaced by database.
	require.Equal(t, int64(1), rec.srcCounts["appdb.public.a"])
	require.Equal(t, int64(2), rec.tgtCounts["analytics.public.b"])
}

func TestDirect_cluster_overwriteDropsEachDB(t *testing.T) {
	eng := &fakeEngine{
		databases: []DatabaseSize{{Name: "appdb"}, {Name: "analytics"}},
		dumpInfo:  DumpInfo{SizeBytes: 1},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	require.NoError(t, o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn(), Overwrite: true}, rec))
	require.Equal(t, []string{"appdb", "analytics"}, eng.dropped)
}

func TestDirect_cluster_globalsFail(t *testing.T) {
	eng := &fakeEngine{
		databases:  []DatabaseSize{{Name: "appdb"}},
		globalsErr: core.ExecError("pg_dumpall boom"),
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn()}, rec)
	require.Error(t, err)
	require.Empty(t, eng.dumps, "must not dump databases if globals failed")
	require.Equal(t, 1, rec.failCount)
}

func TestDirect_cluster_nonEmptyTargetWithoutOverwrite(t *testing.T) {
	// Without overwrite, a non-empty target database is a typed-name SafetyError
	// before any restore touches it.
	eng := &fakeEngine{
		databases: []DatabaseSize{{Name: "appdb"}},
		nonEmpty:  true,
		dumpInfo:  DumpInfo{SizeBytes: 1},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn(), Overwrite: false}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	require.Empty(t, eng.dumps, "must not dump a database it would refuse to overwrite")
	require.Empty(t, eng.restores)
	require.Equal(t, 1, eng.globalsRestore, "globals are replayed before the per-db safety stop")
}

func TestDirect_cluster_globalsRestoreFail(t *testing.T) {
	eng := &fakeEngine{
		databases:         []DatabaseSize{{Name: "appdb"}},
		globalsRestoreErr: core.ExecError("psql globals error: boom"),
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn()}, rec)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
	require.Empty(t, eng.dumps, "must not dump databases if globals replay failed")
	require.Equal(t, 1, rec.failCount)
}

func TestDirect_cluster_listFail(t *testing.T) {
	eng := &fakeEngine{listErr: core.ExecError("cannot list")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.Direct(context.Background(), Job{Mode: ModeCluster, Source: srcConn(""), Target: tgtConn()}, rec)
	require.Error(t, err)
	require.Equal(t, 0, eng.globalsDump)
}

// ---------------------------------------------------------------------------
// ssh-less ExportToSession (SOURCE side)
// ---------------------------------------------------------------------------

func newSessionSvc(t *testing.T) (*Service, *fakeObjectStore) {
	t.Helper()
	fs := newFakeStore()
	return NewService(fs, exec.NewFakeRunner(), core.Discard()), fs
}

func TestExportToSession_happyPath(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err)

	eng := &fakeEngine{
		dumpInfo: DumpInfo{SizeBytes: 9, Checksum: "deadbeef"},
		rowCounts: map[string]map[string]int64{
			"src:appdb": {"public.users": 4},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)

	require.NoError(t, o.ExportToSession(context.Background(), sess, srcConn("appdb"), rec))

	// Session advanced to exported with dump metadata recorded.
	require.Equal(t, StatusExported, sess.Status)
	require.Equal(t, DumpKey(sess.Code), sess.DumpKey)
	require.NotEmpty(t, sess.DumpChecksum)
	require.Equal(t, map[string]int64{"public.users": 4}, sess.SourceRowCounts)
	// Dump uploaded to S3 under the dump key.
	_, ok := fs.objects[DumpKey(sess.Code)]
	require.True(t, ok, "dump must be uploaded to the object store")
	require.True(t, rec.succeeded)
	require.Contains(t, rec.phases(), PhaseUploading)
}

func TestExportToSession_noS3(t *testing.T) {
	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil) // no svc/os
	err := o.ExportToSession(context.Background(), &MigrationSession{Code: "ABCDEF", Database: "appdb", Status: StatusWaiting, ExpiresAt: time.Now().Add(time.Hour)}, srcConn("appdb"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Equal(t, 1, rec.failCount)
}

func TestExportToSession_notWaiting(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err)
	sess.Status = StatusExported // not waiting -> ValidateForExport rejects

	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err = o.ExportToSession(context.Background(), sess, srcConn("appdb"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	require.Empty(t, eng.dumps)
}

func TestExportToSession_dumpFails(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err)

	eng := &fakeEngine{dumpErr: core.ExecError("dump boom")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err = o.ExportToSession(context.Background(), sess, srcConn("appdb"), rec)
	require.Error(t, err)
	require.Equal(t, 1, rec.failCount)
	// No dump uploaded.
	_, ok := fs.objects[DumpKey(sess.Code)]
	require.False(t, ok)
	// The cross-panel session must be moved to failed (not left stuck in
	// "exporting") so the other panel observes the failure.
	require.Equal(t, StatusFailed, sess.Status)
	require.NotEmpty(t, sess.Error)
	// The session must be readable as failed through the service too.
	reloaded, gerr := svc.GetSession(context.Background(), sess.Code)
	require.NoError(t, gerr)
	require.Equal(t, StatusFailed, reloaded.Status)
}

func TestExportToSession_dumpTooLargeRefusedBeforeUpload(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err)

	// A dump whose size exceeds the in-memory ceiling must be refused BEFORE it is
	// read into RAM and uploaded, so the single binary cannot OOM mid-migration.
	eng := &fakeEngine{dumpInfo: DumpInfo{SizeBytes: 2 * MaxSessionDumpBytes, Checksum: "deadbeef"}}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)

	err = o.ExportToSession(context.Background(), sess, srcConn("appdb"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "direct-pull")
	// Nothing was uploaded — the guard fired before the ReadFile/PutObject.
	_, ok := fs.objects[DumpKey(sess.Code)]
	require.False(t, ok, "an oversized dump must never be uploaded")
	require.NotContains(t, rec.phases(), PhaseUploading, "must not reach the uploading phase")
	// The cross-panel session is moved to failed, not left stuck in "exporting".
	require.Equal(t, StatusFailed, sess.Status)
	require.NotEmpty(t, sess.Error)
}

// ---------------------------------------------------------------------------
// ssh-less ImportFromSession (TARGET side)
// ---------------------------------------------------------------------------

// exportedSession builds a session in the exported state with a dump uploaded to
// fs, returning the session and the source counts that were recorded.
func exportedSession(t *testing.T, svc *Service, fs *fakeObjectStore, srcCounts map[string]int64) *MigrationSession {
	t.Helper()
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err)
	dump := []byte("PGDMP-import")
	require.NoError(t, fs.PutObject(context.Background(), DumpKey(sess.Code), dump))
	sess.DumpKey = DumpKey(sess.Code)
	sess.DumpSize = int64(len(dump))
	sess.DumpChecksum = sha256Hex(dump)
	sess.SourceRowCounts = srcCounts
	require.NoError(t, svc.Transition(context.Background(), sess, StatusExporting))
	require.NoError(t, svc.Transition(context.Background(), sess, StatusExported))
	return sess
}

func TestImportFromSession_happyPath(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess := exportedSession(t, svc, fs, map[string]int64{"public.users": 4})

	eng := &fakeEngine{
		exists: false,
		rowCounts: map[string]map[string]int64{
			"tgt:appdb": {"public.users": 4},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)

	require.NoError(t, o.ImportFromSession(context.Background(), sess, tgtConn(), rec))
	require.Equal(t, StatusCompleted, sess.Status)
	require.Equal(t, map[string]int64{"public.users": 4}, sess.TargetRowCounts)
	require.Equal(t, []string{"appdb"}, eng.created, "fresh db must be created when absent")
	require.Equal(t, []string{"appdb"}, eng.restores)
	require.True(t, rec.succeeded)
	require.Contains(t, rec.phases(), PhaseDownloading)
}

func TestImportFromSession_noS3(t *testing.T) {
	o := newOrch(t, &fakeEngine{}, nil, nil)
	rec := &fakeRecorder{}
	err := o.ImportFromSession(context.Background(), &MigrationSession{Code: "ABCDEF"}, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestImportFromSession_checksumMismatch(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess := exportedSession(t, svc, fs, map[string]int64{"public.users": 4})
	// Corrupt the stored dump after the session recorded the original checksum.
	require.NoError(t, fs.PutObject(context.Background(), sess.DumpKey, []byte("CORRUPT")))

	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err := o.ImportFromSession(context.Background(), sess, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "checksum mismatch")
	require.Empty(t, eng.restores, "must not restore a corrupt dump")
	require.Equal(t, StatusFailed, sess.Status, "a checksum mismatch after import started must fail the session")
}

func TestImportFromSession_nonEmptyTarget(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess := exportedSession(t, svc, fs, map[string]int64{"public.users": 4})

	eng := &fakeEngine{nonEmpty: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err := o.ImportFromSession(context.Background(), sess, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	require.Empty(t, eng.restores)
	// The session must not be left stuck in "importing".
	require.Equal(t, StatusFailed, sess.Status)
	require.NotEmpty(t, sess.Error)
}

func TestImportFromSession_rowMismatch(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess := exportedSession(t, svc, fs, map[string]int64{"public.users": 4})

	eng := &fakeEngine{
		rowCounts: map[string]map[string]int64{
			"tgt:appdb": {"public.users": 1},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err := o.ImportFromSession(context.Background(), sess, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.NotEqual(t, StatusCompleted, sess.Status)
	// A row-count mismatch after the import started must fail the session, not
	// leave it stuck in "importing".
	require.Equal(t, StatusFailed, sess.Status)
	require.NotEmpty(t, sess.Error)
	reloaded, gerr := svc.GetSession(context.Background(), sess.Code)
	require.NoError(t, gerr)
	require.Equal(t, StatusFailed, reloaded.Status)
}

func TestImportFromSession_dumpTooLargeRefusedBeforeDownload(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess := exportedSession(t, svc, fs, map[string]int64{"public.users": 4})
	// The recorded dump size exceeds the in-memory ceiling (e.g. a tampered or
	// foreign upload — the exporter enforces the same limit). The target must
	// refuse before pulling the object into RAM.
	sess.DumpSize = 2 * MaxSessionDumpBytes

	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)

	err := o.ImportFromSession(context.Background(), sess, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "direct-pull")
	require.Empty(t, eng.restores, "an oversized dump must never be downloaded or restored")
	require.NotContains(t, rec.phases(), PhaseDownloading, "must not reach the downloading phase")
	// The cross-panel session is moved to failed, not left stuck in "importing".
	require.Equal(t, StatusFailed, sess.Status)
	require.NotEmpty(t, sess.Error)
}

func TestImportFromSession_notExported(t *testing.T) {
	svc, fs := newSessionSvc(t)
	sess, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.NoError(t, err) // still waiting, not exported

	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, svc, fs)
	err = o.ImportFromSession(context.Background(), sess, tgtConn(), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
}

// ---------------------------------------------------------------------------
// cleanup + misc
// ---------------------------------------------------------------------------

func TestDirect_cleansWorkDir(t *testing.T) {
	dir := t.TempDir()
	eng := &fakeEngine{dumpInfo: DumpInfo{SizeBytes: 1}}
	rec := &fakeRecorder{}
	o := NewOrchestrator(eng, nil, nil, dir, core.Discard())
	require.NoError(t, o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec))
	require.NoDirExists(t, dir, "workDir must be removed on return")
}

func TestRecorderStageError_doesNotMaskOutcome(t *testing.T) {
	eng := &fakeEngine{dumpInfo: DumpInfo{SizeBytes: 1}}
	rec := &fakeRecorder{stageErr: core.InternalError("store write failed")}
	o := newOrch(t, eng, nil, nil)
	// A Stage error is logged, not fatal: the migration still succeeds.
	require.NoError(t, o.Direct(context.Background(), Job{Mode: ModeSingleDB, Source: srcConn("appdb"), Target: tgtConn(), TargetDatabase: "appdb"}, rec))
	require.True(t, rec.succeeded)
}

var _ PgEngine = (*fakeEngine)(nil)
var _ Recorder = (*fakeRecorder)(nil)
