package migrate

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// fakeDropTransport is an in-memory DropTransport for tests. It models S3 closely
// enough to exercise ImportFromDrop end to end: presign returns a stub URL,
// StatObject reports size+existence, DownloadToFile copies bytes to disk, and
// Delete records the cleanup.
type fakeDropTransport struct {
	mu      sync.Mutex
	objects map[string][]byte

	presignErr  error
	statErr     error
	downloadErr error
	getErr      error

	presigned   []string // keys presigned
	deletes     []string // keys deleted
	downloadMax int64    // the byte ceiling DownloadToFile was last called with
}

func newFakeDrop() *fakeDropTransport {
	return &fakeDropTransport{objects: map[string][]byte{}}
}

func (f *fakeDropTransport) put(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), data...)
}

func (f *fakeDropTransport) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	f.mu.Lock()
	f.presigned = append(f.presigned, key)
	f.mu.Unlock()
	return "https://s3.example.test/" + key + "?X-Amz-Signature=stub", nil
}

func (f *fakeDropTransport) StatObject(ctx context.Context, key string) (int64, bool, error) {
	if f.statErr != nil {
		return 0, false, f.statErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return 0, false, nil
	}
	return int64(len(data)), true, nil
}

func (f *fakeDropTransport) DownloadToFile(ctx context.Context, key, dest string, max int64) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	f.mu.Lock()
	f.downloadMax = max
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return core.NotFoundError("object %s not found", key)
	}
	// Mirror the real S3ObjectStore.DownloadToFile: refuse to write an object that
	// exceeds the byte ceiling (a dump swapped huge after the StatObject pre-check).
	if int64(len(data)) > max {
		return core.ValidationError("object %s exceeds the %d-byte download limit", key, max)
	}
	return os.WriteFile(dest, data, 0o600)
}

func (f *fakeDropTransport) GetObjectLimited(ctx context.Context, key string, max int64) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, core.NotFoundError("object %s not found", key)
	}
	if int64(len(data)) > max {
		return nil, core.ValidationError("object %s exceeds the %d-byte read limit", key, max)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeDropTransport) DeleteObject(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, key)
	delete(f.objects, key)
	return nil
}

// stageDrop uploads a dump + matching meta.json under a code's keys and returns
// the dump bytes. The meta's tables/total are the caller-supplied source counts,
// and sha256/byte_size are computed from the dump so a happy path verifies.
func stageDrop(t *testing.T, tr *fakeDropTransport, code string, dump []byte, srcCounts map[string]int64) {
	t.Helper()
	tr.put(DropDumpKey(code), dump)
	meta := DropMeta{
		SchemaVersion: DropMetaSchemaVersion,
		SourceDB:      "appdb",
		PGVersion:     "16.3",
		SHA256:        sha256Hex(dump),
		ByteSize:      int64(len(dump)),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	var total int64
	for k, n := range srcCounts {
		schema, name := splitKey(k)
		meta.Tables = append(meta.Tables, DropTable{Schema: schema, Name: name, Rows: n})
		total += n
	}
	meta.TotalRows = total
	raw, err := json.Marshal(meta)
	require.NoError(t, err)
	tr.put(DropMetaKey(code), raw)
}

func splitKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '.' {
			return k[:i], k[i+1:]
		}
	}
	return "public", k
}

func dropSpec(code string) DropImportSpec {
	return DropImportSpec{
		Code:           code,
		DumpKey:        DropDumpKey(code),
		MetaKey:        DropMetaKey(code),
		TargetDatabase: "appdb",
		Target:         tgtConn(),
	}
}

func TestDropKeyLayout(t *testing.T) {
	require.Equal(t, "pg-migrations/dropoff/ABCDEF/dump", DropDumpKey("ABCDEF"))
	require.Equal(t, "pg-migrations/dropoff/ABCDEF/meta.json", DropMetaKey("ABCDEF"))
}

// TestDropMeta_SourceRowCountsKeyParity pins the hard contract: meta tables MUST
// key EXACTLY like engine.RowCounts (schema + "." + name), or CompareRowCounts
// false-fails forever.
func TestDropMeta_SourceRowCountsKeyParity(t *testing.T) {
	meta := DropMeta{Tables: []DropTable{
		{Schema: "public", Name: "users", Rows: 3},
		{Schema: "billing", Name: "invoices", Rows: 7},
	}}
	got := meta.SourceRowCounts()
	require.Equal(t, map[string]int64{"public.users": 3, "billing.invoices": 7}, got)
}

// TestCompareRowCountsByTable_dotNamesDoNotCollide pins finding #5 at the unit level:
// two DISTINCT (schema, name) pairs that flatten to the same "a.b.c" string must be
// compared independently, so a real mismatch on one is never masked by the other.
func TestCompareRowCountsByTable_dotNamesDoNotCollide(t *testing.T) {
	src := map[TableKey]int64{{Schema: "a", Name: "b.c"}: 100, {Schema: "a.b", Name: "c"}: 200}
	tgt := map[TableKey]int64{{Schema: "a", Name: "b.c"}: 50, {Schema: "a.b", Name: "c"}: 200}
	diffs := compareRowCountsByTable(src, tgt)
	require.Len(t, diffs, 1, "the per-pair comparison must catch the a/b.c mismatch")
	require.Equal(t, "a.b.c", diffs[0].Table)
	require.Equal(t, int64(100), diffs[0].Source)
	require.Equal(t, int64(50), diffs[0].Target)
}

// TestImportFromDrop_dotNameCollisionDoesNotFalsePass pins finding #5 end to end: the
// source claims two tables whose (schema, name) pairs flatten to the same string but
// hold different counts; the restored target under-restores the first. A flattened-key
// comparison would collapse them and could falsely report success — the structured
// (schema, name) comparison must instead FAIL verification.
func TestImportFromDrop_dotNameCollisionDoesNotFalsePass(t *testing.T) {
	tr := newFakeDrop()
	dump := []byte("PGDMP-collision")
	meta := DropMeta{
		SchemaVersion: DropMetaSchemaVersion,
		SourceDB:      "appdb",
		SHA256:        sha256Hex(dump),
		ByteSize:      int64(len(dump)),
		Tables: []DropTable{
			{Schema: "a", Name: "b.c", Rows: 100},
			{Schema: "a.b", Name: "c", Rows: 200},
		},
	}
	raw, err := json.Marshal(meta)
	require.NoError(t, err)
	tr.put(DropDumpKey("ABCDEF"), dump)
	tr.put(DropMetaKey("ABCDEF"), raw)

	// The target under-restored a/b.c (50 != claimed 100); a.b/c matches.
	eng := &fakeEngine{
		exists: true,
		rowCountsByTable: map[string]map[TableKey]int64{
			"tgt:appdb": {
				{Schema: "a", Name: "b.c"}: 50,
				{Schema: "a.b", Name: "c"}: 200,
			},
		},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err = o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err, "a real per-table mismatch hidden by a dot-name collision must NOT pass")
	require.Contains(t, err.Error(), "verification failed")
	require.False(t, rec.succeeded)
	require.Empty(t, tr.deletes, "a verification failure keeps the S3 objects")
}

// TestImportFromDrop_duplicateTablePairRejected pins the second half of finding #5:
// metadata that lists the SAME (schema, name) pair twice is rejected up front, so one
// claimed count can never silently overwrite the other and hide a mismatch.
func TestImportFromDrop_duplicateTablePairRejected(t *testing.T) {
	tr := newFakeDrop()
	dump := []byte("PGDMP-dup")
	meta := DropMeta{
		SchemaVersion: DropMetaSchemaVersion,
		SHA256:        sha256Hex(dump),
		ByteSize:      int64(len(dump)),
		Tables: []DropTable{
			{Schema: "public", Name: "users", Rows: 1},
			{Schema: "public", Name: "users", Rows: 2}, // same pair listed twice
		},
	}
	raw, err := json.Marshal(meta)
	require.NoError(t, err)
	tr.put(DropDumpKey("ABCDEF"), dump)
	tr.put(DropMetaKey("ABCDEF"), raw)

	eng := &fakeEngine{exists: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err = o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "more than once")
	require.Empty(t, eng.restores, "must not restore metadata with a duplicate table pair")
	require.Empty(t, tr.deletes)
}

func TestImportFromDrop_happyPath(t *testing.T) {
	tr := newFakeDrop()
	dump := []byte("PGDMP-dropoff-happy")
	stageDrop(t, tr, "ABCDEF", dump, map[string]int64{"public.users": 4})

	eng := &fakeEngine{
		exists:    true,
		rowCounts: map[string]map[string]int64{"tgt:appdb": {"public.users": 4}},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)

	require.NoError(t, o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec))
	require.True(t, rec.succeeded)
	require.Equal(t, []string{"appdb"}, eng.restores)
	require.Equal(t, map[string]int64{"public.users": 4}, rec.srcCounts)
	require.Contains(t, rec.phases(), PhaseDownloading)
	require.Equal(t, []Phase{PhaseValidating, PhaseDownloading, PhaseRestoring, PhaseVerifying}, rec.phases())
	// The dump download is hard-capped at the single-PUT ceiling so a dump swapped
	// huge after the StatObject pre-check cannot exhaust the disk.
	require.Equal(t, MaxDropBytes, tr.downloadMax)
	// On success the full DB at rest is removed.
	require.Contains(t, tr.deletes, DropDumpKey("ABCDEF"))
	require.Contains(t, tr.deletes, DropMetaKey("ABCDEF"))
}

// TestImportFromDrop_nonOverwriteRestoreFailureDropsCreatedTarget pins the
// retry-from-S3 salvage path: a NON-overwrite restore that fails into a database
// THIS import created (the target was absent) leaves partial tables behind; those
// poison the next retry (they read as non-empty and the overwrite gate would
// refuse forever, but the dump can't be re-pushed from the unreachable source).
// Because we created the database, dropping it is safe and lets a retry from the
// kept-in-S3 dump start clean.
func TestImportFromDrop_nonOverwriteRestoreFailureDropsCreatedTarget(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP"), map[string]int64{})

	// exists:false => prepareTarget creates the DB, so this import owns it.
	eng := &fakeEngine{exists: false, restoreErr: core.ExecError("pg_restore boom")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, []string{"appdb"}, eng.created, "we created the fresh target")
	// The target we created is dropped so a retry starts from a clean slate.
	require.Equal(t, []string{"appdb"}, eng.dropped)
	// The dump is KEPT in S3 for that retry — only the local target was reset.
	require.Empty(t, tr.deletes)
}

// TestImportFromDrop_nonOverwriteRestoreFailurePreservesExistingTarget pins the
// safety invariant: a NON-overwrite restore that fails into a PRE-EXISTING
// database must NEVER drop it. The empty-base-tables gate it passed does not prove
// the database is otherwise empty (it may carry extensions, schemas, functions,
// sequences, or a non-default owner the operator created), and the operator
// explicitly declined a destructive overwrite. The failed restore leaves the
// database untouched (matching directSingle) and surfaces a clear retry hint; the
// dump stays in S3.
func TestImportFromDrop_nonOverwriteRestoreFailurePreservesExistingTarget(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP"), map[string]int64{})

	// exists:true => the DB pre-existed; prepareTarget creates nothing, so the
	// failed restore must not drop it.
	eng := &fakeEngine{exists: true, restoreErr: core.ExecError("pg_restore boom")}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Empty(t, eng.dropped, "a pre-existing target the operator declined to overwrite is never dropped")
	require.Empty(t, eng.created, "the pre-existing target is not (re)created")
	require.Contains(t, err.Error(), "existed before this import")
	require.Empty(t, tr.deletes, "the dump is kept in S3 for retry")
}

func TestImportFromDrop_createsTargetWhenAbsent(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP"), map[string]int64{})

	eng := &fakeEngine{exists: false}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	require.NoError(t, o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec))
	require.Equal(t, []string{"appdb"}, eng.created, "absent target must be created")
}

func TestImportFromDrop_nilTransport(t *testing.T) {
	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{}, nil, nil)
	err := o.ImportFromDrop(context.Background(), nil, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestImportFromDrop_metaAbsentNotReady(t *testing.T) {
	tr := newFakeDrop()
	tr.put(DropDumpKey("ABCDEF"), []byte("PGDMP")) // dump only, no meta yet
	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{}, nil, nil)

	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	require.Contains(t, err.Error(), "not uploaded")
	require.Empty(t, tr.deletes, "nothing imported, nothing deleted")
}

func TestImportFromDrop_checksumMismatch(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP-original"), map[string]int64{"public.users": 1})
	// Replace the dump with different bytes AFTER meta recorded the original sha.
	tr.put(DropDumpKey("ABCDEF"), []byte("PGDMP-tampered-different-length"))

	eng := &fakeEngine{exists: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	// byte_size mismatch is caught first (the tampered dump is a different length),
	// before any restore — either way nothing is restored or deleted.
	require.Empty(t, eng.restores)
	require.Empty(t, tr.deletes)
}

func TestImportFromDrop_checksumMismatchSameLength(t *testing.T) {
	tr := newFakeDrop()
	orig := []byte("PGDMP-aaaa")
	stageDrop(t, tr, "ABCDEF", orig, map[string]int64{"public.users": 1})
	// Same length, different content: passes the byte_size gate, fails the sha gate.
	tr.put(DropDumpKey("ABCDEF"), []byte("PGDMP-bbbb"))

	eng := &fakeEngine{exists: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "checksum mismatch")
	require.Empty(t, eng.restores, "must not restore a corrupt dump")
	require.Empty(t, tr.deletes, "a failed import keeps the S3 objects for retry")
}

func TestImportFromDrop_rowMismatch(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP-rows"), map[string]int64{"public.users": 10})

	eng := &fakeEngine{
		exists:    true,
		rowCounts: map[string]map[string]int64{"tgt:appdb": {"public.users": 7}},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
	require.Contains(t, err.Error(), "verification failed")
	require.False(t, rec.succeeded)
	require.Empty(t, tr.deletes, "a verification failure keeps the S3 objects")
	// The target PRE-EXISTED (exists:true), so a verification failure must never drop
	// it (the operator may have created it with extensions/schemas).
	require.Empty(t, eng.dropped, "a pre-existing target is never dropped on verification failure")
}

// TestImportFromDrop_verificationFailureDropsCreatedTarget pins finding #11: a
// post-restore row-count mismatch into a database THIS import created leaves it
// non-empty, which would poison the next non-overwrite retry's emptiness gate. Like
// the restore-failure path, drop the target we created so the kept-in-S3 dump can be
// retried clean.
func TestImportFromDrop_verificationFailureDropsCreatedTarget(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP-rows"), map[string]int64{"public.users": 10})

	eng := &fakeEngine{
		exists:    false, // absent -> this import creates the fresh target
		rowCounts: map[string]map[string]int64{"tgt:appdb": {"public.users": 7}},
	}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
	require.Equal(t, []string{"appdb"}, eng.created, "we created the fresh target")
	require.Equal(t, []string{"appdb"}, eng.dropped, "a verification failure drops the target we created")
	require.Empty(t, tr.deletes, "the dump is kept in S3 for retry")
}

func TestImportFromDrop_nonEmptyTargetWithoutOverwrite(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP"), map[string]int64{})

	eng := &fakeEngine{nonEmpty: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
	require.Empty(t, eng.restores)
}

func TestImportFromDrop_overwriteDropsAndRecreates(t *testing.T) {
	tr := newFakeDrop()
	stageDrop(t, tr, "ABCDEF", []byte("PGDMP"), map[string]int64{})

	eng := &fakeEngine{nonEmpty: true} // ignored: Overwrite short-circuits the gate
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	spec := dropSpec("ABCDEF")
	spec.Overwrite = true
	require.NoError(t, o.ImportFromDrop(context.Background(), tr, spec, rec))
	require.Equal(t, []string{"appdb"}, eng.dropped)
	require.Equal(t, []string{"appdb"}, eng.created)
}

func TestImportFromDrop_oversizeRefused(t *testing.T) {
	tr := newFakeDrop()
	// Stat reports a size over the single-PUT cap; the import must refuse before
	// downloading. We model an oversized object via a statSizeOverride.
	big := &oversizeDrop{fakeDropTransport: newFakeDrop(), size: MaxDropBytes + 1}
	big.put(DropMetaKey("ABCDEF"), []byte(`{"sha256":"x"}`))
	big.put(DropDumpKey("ABCDEF"), []byte("small-but-stat-lies"))

	_ = tr
	eng := &fakeEngine{}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err := o.ImportFromDrop(context.Background(), big, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "direct-pull")
	require.NotContains(t, rec.phases(), PhaseDownloading)
}

func TestImportFromDrop_byteSizeMismatch(t *testing.T) {
	tr := newFakeDrop()
	dump := []byte("PGDMP-size")
	tr.put(DropDumpKey("ABCDEF"), dump)
	// meta claims a different byte_size than the actual object.
	meta := DropMeta{SchemaVersion: DropMetaSchemaVersion, SHA256: sha256Hex(dump), ByteSize: int64(len(dump)) + 100}
	raw, _ := json.Marshal(meta)
	tr.put(DropMetaKey("ABCDEF"), raw)

	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{exists: true}, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "size mismatch")
}

func TestImportFromDrop_malformedMeta(t *testing.T) {
	tr := newFakeDrop()
	tr.put(DropDumpKey("ABCDEF"), []byte("PGDMP"))
	tr.put(DropMetaKey("ABCDEF"), []byte("{not json"))

	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{exists: true}, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

// TestImportFromDrop_unsupportedSchemaVersion pins that the schema_version field
// actually gates compatibility: a manifest from a future push script (or a
// pre-v1 producer with no version) is rejected with a clear error instead of
// being silently mis-parsed and restored.
func TestImportFromDrop_unsupportedSchemaVersion(t *testing.T) {
	tr := newFakeDrop()
	dump := []byte("PGDMP-ver")
	tr.put(DropDumpKey("ABCDEF"), dump)
	meta := DropMeta{SchemaVersion: DropMetaSchemaVersion + 1, SHA256: sha256Hex(dump), ByteSize: int64(len(dump))}
	raw, err := json.Marshal(meta)
	require.NoError(t, err)
	tr.put(DropMetaKey("ABCDEF"), raw)

	eng := &fakeEngine{exists: true}
	rec := &fakeRecorder{}
	o := newOrch(t, eng, nil, nil)
	err = o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Contains(t, err.Error(), "schema version")
	require.Empty(t, eng.restores, "must not restore an unsupported manifest")
	require.Empty(t, tr.deletes)
}

// TestImportFromDrop_metaReadBounded covers the meta-read TOCTOU: a holder of the
// meta-key presigned PUT swaps in an oversized manifest AFTER the StatObject
// pre-check, so the bounded GetObjectLimited read — not the stat — must catch it
// before the panel buffers it into memory.
func TestImportFromDrop_metaReadBounded(t *testing.T) {
	base := newFakeDrop()
	base.put(DropDumpKey("ABCDEF"), []byte("PGDMP"))
	base.put(DropMetaKey("ABCDEF"), make([]byte, MaxDropMetaBytes+1)) // oversized manifest
	tr := &metaTOCTOUDrop{fakeDropTransport: base}

	rec := &fakeRecorder{}
	o := newOrch(t, &fakeEngine{exists: true}, nil, nil)
	err := o.ImportFromDrop(context.Background(), tr, dropSpec("ABCDEF"), rec)
	require.Error(t, err)
	require.NotContains(t, rec.phases(), PhaseRestoring, "must reject the oversized manifest before restoring")
	require.Empty(t, tr.deletes)
}

// oversizeDrop overrides StatObject to report an over-cap size while still holding
// real (small) bytes, modelling a presigned PUT that uploaded more than meta
// claims — the panel must trust its own StatObject, not the source.
type oversizeDrop struct {
	*fakeDropTransport
	size int64
}

func (o *oversizeDrop) StatObject(ctx context.Context, key string) (int64, bool, error) {
	_, exists, err := o.fakeDropTransport.StatObject(ctx, key)
	if err != nil || !exists {
		return 0, exists, err
	}
	if key == DropDumpKey("ABCDEF") {
		return o.size, true, nil
	}
	return 1, true, nil
}

// metaTOCTOUDrop reports a small meta size from StatObject while holding an
// oversized meta object, modelling a presigned-PUT holder who swaps in a huge
// manifest after the panel's StatObject pre-check. The dump key falls through to
// the real (small) size, so only the meta read is forced over the limit.
type metaTOCTOUDrop struct {
	*fakeDropTransport
}

func (m *metaTOCTOUDrop) StatObject(ctx context.Context, key string) (int64, bool, error) {
	if key == DropMetaKey("ABCDEF") {
		return 16, true, nil // lie: report a plausibly-small manifest
	}
	return m.fakeDropTransport.StatObject(ctx, key)
}

var _ DropTransport = (*fakeDropTransport)(nil)
var _ DropTransport = (*oversizeDrop)(nil)
var _ DropTransport = (*metaTOCTOUDrop)(nil)
