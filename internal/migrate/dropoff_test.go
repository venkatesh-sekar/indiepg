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

	presigned []string // keys presigned
	deletes   []string // keys deleted
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

func (f *fakeDropTransport) DownloadToFile(ctx context.Context, key, dest string) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return core.NotFoundError("object %s not found", key)
	}
	return os.WriteFile(dest, data, 0o600)
}

func (f *fakeDropTransport) GetObject(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, core.NotFoundError("object %s not found", key)
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
	// On success the full DB at rest is removed.
	require.Contains(t, tr.deletes, DropDumpKey("ABCDEF"))
	require.Contains(t, tr.deletes, DropMetaKey("ABCDEF"))
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
	meta := DropMeta{SHA256: sha256Hex(dump), ByteSize: int64(len(dump)) + 100}
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

var _ DropTransport = (*fakeDropTransport)(nil)
var _ DropTransport = (*oversizeDrop)(nil)
