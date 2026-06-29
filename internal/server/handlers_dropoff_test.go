package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// fakeServerDrop is an in-memory migrate.DropTransport injected into a test server
// so the drop-off handlers can be exercised end to end without a live S3.
type fakeServerDrop struct {
	mu      sync.Mutex
	objects map[string][]byte
	deletes []string
}

func newFakeServerDrop() *fakeServerDrop {
	return &fakeServerDrop{objects: map[string][]byte{}}
}

func (f *fakeServerDrop) put(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), data...)
}

func (f *fakeServerDrop) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "https://s3.example.test/" + key + "?X-Amz-Signature=stub-secret", nil
}

func (f *fakeServerDrop) StatObject(ctx context.Context, key string) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return 0, false, nil
	}
	return int64(len(data)), true, nil
}

func (f *fakeServerDrop) DownloadToFile(ctx context.Context, key, dest string, max int64) error {
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return core.NotFoundError("object %s not found", key)
	}
	if int64(len(data)) > max {
		return core.ValidationError("object %s exceeds the %d-byte download limit", key, max)
	}
	return os.WriteFile(dest, data, 0o600)
}

func (f *fakeServerDrop) GetObjectLimited(ctx context.Context, key string, max int64) ([]byte, error) {
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

func (f *fakeServerDrop) DeleteObject(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, key)
	delete(f.objects, key)
	return nil
}

var _ migrate.DropTransport = (*fakeServerDrop)(nil)

// withDrops injects a fake drop transport into a freshly built test server.
func withDrops(t *testing.T) (*Server, *fakeServerDrop, string) {
	t.Helper()
	srv, _ := newTestServer(t)
	require.Nil(t, srv.drops, "default test server has no S3 transport")
	fake := newFakeServerDrop()
	srv.drops = fake
	token := login(t, srv, testPassword)
	return srv, fake, token
}

// TestDropoffEndpointsRequireS3 verifies EVERY drop-off endpoint returns the
// honest "requires S3" CodeInternal error when no S3 transport is configured.
func TestDropoffEndpointsRequireS3(t *testing.T) {
	srv, _ := newTestServer(t)
	require.Nil(t, srv.drops)
	token := login(t, srv, testPassword)

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/migrate/drops", map[string]any{"target_database": "appdb"}},
		{http.MethodGet, "/api/migrate/drops/ABCDEF", nil},
		{http.MethodPost, "/api/migrate/drops/ABCDEF/start", nil},
		{http.MethodDelete, "/api/migrate/drops/ABCDEF", nil},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := authedRequest(t, srv, c.method, c.path, token, c.body)
			require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
			var ae apiError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
			require.Equal(t, core.CodeInternal, ae.Code)
			require.Contains(t, strings.ToLower(ae.Message), "requires s3")
		})
	}
}

// TestCreateDropoffMintsCommandAndRecord verifies a successful mint returns the
// paste-able commands (with the presigned URLs) and records a keys-only session.
func TestCreateDropoffMintsCommandAndRecord(t *testing.T) {
	srv, _, token := withDrops(t)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Len(t, env.Data.Code, migrate.CodeLength)
	require.Equal(t, "appdb", env.Data.TargetDatabase)
	require.Contains(t, env.Data.CommandDocker, "migrate-push.sh")
	// The presigned URLs ride in the environment (INDIEPG_DUMP_URL/INDIEPG_META_URL),
	// NOT argv, so they stay out of the source's `ps` listing — never as --dump-url.
	require.Contains(t, env.Data.CommandDocker, "INDIEPG_DUMP_URL=")
	require.Contains(t, env.Data.CommandDocker, "INDIEPG_META_URL=")
	require.NotContains(t, env.Data.CommandDocker, "--dump-url")
	require.Contains(t, env.Data.CommandDocker, "X-Amz-Signature")
	require.Contains(t, env.Data.CommandDocker, "--docker CONTAINER")
	require.Contains(t, env.Data.CommandNative, "--host SOURCE_HOST")
	// Placeholders must NOT be shell metacharacters: a verbatim paste must not
	// trigger shell redirection before migrate-push.sh's own placeholder guard.
	require.NotContains(t, env.Data.CommandDocker, "<")
	require.NotContains(t, env.Data.CommandNative, "<")
	require.False(t, env.Data.ExpiresAt.IsZero())

	// The store record holds only the KEYS — never the presigned URLs.
	drec, err := srv.store.GetDropoffByCode(context.Background(), env.Data.Code)
	require.NoError(t, err)
	require.Equal(t, migrate.DropDumpKey(env.Data.Code), drec.DumpKey)
	require.Equal(t, migrate.DropMetaKey(env.Data.Code), drec.MetaKey)
	require.Equal(t, string(migrate.DropWaiting), drec.Status)
	require.NotContains(t, drec.DumpKey, "X-Amz", "URLs must never be persisted")
}

// errStatDrop wraps a fake transport but fails every StatObject, modelling S3 with
// wrong credentials or an unreachable bucket. PresignPut is a purely local signing
// op that would still "succeed", so the mint-time HEAD probe is the only thing that
// catches the misconfiguration before a command is handed out.
type errStatDrop struct {
	*fakeServerDrop
	err error
}

func (e *errStatDrop) StatObject(ctx context.Context, key string) (int64, bool, error) {
	return 0, false, e.err
}

// TestCreateDropoffFailsFastOnUnreachableS3 pins the mint-time reachability probe:
// a HEAD that errors (bad creds / unreachable bucket) must fail the mint with a
// clear "not reachable with the configured credentials" message instead of handing
// out a command that would only fail later on the hard-to-reach source as a
// misleading "link may have expired".
func TestCreateDropoffFailsFastOnUnreachableS3(t *testing.T) {
	srv, fake, token := withDrops(t)
	srv.drops = &errStatDrop{fakeServerDrop: fake, err: core.InternalError("backup: stat S3 object: AccessDenied")}

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeInternal, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "not reachable")
}

// TestDropCancelRegistryGenerationGuard pins the owner-guard fix: a retry under the
// same session code registers a NEW worker (new migration id); the previous
// worker's late-running deferred unregister must NOT delete the new worker's entry,
// so a cancel still interrupts the live restore rather than only marking the row.
func TestDropCancelRegistryGenerationGuard(t *testing.T) {
	srv, _, _ := withDrops(t)

	worker1Cancelled := false
	worker2Cancelled := false
	srv.registerDropCancel("CODE01", 1, func() { worker1Cancelled = true })
	// Retry: a new worker (id 2) registers under the same code.
	srv.registerDropCancel("CODE01", 2, func() { worker2Cancelled = true })
	// Worker 1's deferred unregister runs LATE — it must be a no-op (not its entry).
	srv.unregisterDropCancel("CODE01", 1)

	// A cancel now must still reach worker 2 (the live one).
	srv.cancelDropWorker("CODE01")
	require.False(t, worker1Cancelled, "the stale worker's cancel must not fire")
	require.True(t, worker2Cancelled, "the live retry worker must remain interruptible")

	// Worker 2's own unregister cleans the entry; a later cancel is a no-op.
	srv.unregisterDropCancel("CODE01", 2)
	worker2Cancelled = false
	srv.cancelDropWorker("CODE01")
	require.False(t, worker2Cancelled, "after the owner unregisters, no entry remains")
}

// TestCreateDropoffOverwriteRequiresTypedConfirm verifies the destructive
// overwrite gate at mint time.
func TestCreateDropoffOverwriteRequiresTypedConfirm(t *testing.T) {
	srv, _, token := withDrops(t)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
		"overwrite":       true,
	})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)

	// Correct confirm starts it.
	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
		"overwrite":       true,
		"confirm":         "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
}

// TestCreateDropoffRejectsBadIdentifier verifies identifier validation.
func TestCreateDropoffRejectsBadIdentifier(t *testing.T) {
	srv, _, token := withDrops(t)
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "bad name!",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeValidation, ae.Code)
}

// TestGetDropoffFlipsToUploadedAndHidesCommand verifies the readiness flip and
// that the status endpoint never re-serves the command/URLs.
func TestGetDropoffFlipsToUploadedAndHidesCommand(t *testing.T) {
	srv, fake, token := withDrops(t)

	// Mint a session.
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	code := env.Data.Code

	// Before upload: still waiting.
	rec = authedRequest(t, srv, http.MethodGet, "/api/migrate/drops/"+code, token, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var st struct {
		Data dropoffStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(t, string(migrate.DropWaiting), st.Data.Status)
	// The safe status view must NOT leak the presigned command/URL.
	require.NotContains(t, rec.Body.String(), "X-Amz-Signature")

	// Simulate the source push: dump + meta uploaded.
	dump := []byte("PGDMP-uploaded-bytes")
	fake.put(migrate.DropDumpKey(code), dump)
	fake.put(migrate.DropMetaKey(code), []byte(`{"sha256":"x"}`))

	rec = authedRequest(t, srv, http.MethodGet, "/api/migrate/drops/"+code, token, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(t, string(migrate.DropUploaded), st.Data.Status)
	require.Equal(t, int64(len(dump)), st.Data.ByteSize)
}

// TestStartDropoffNotReadyIsConflict verifies Start refuses when meta.json (the
// "upload complete" marker) is not present.
func TestStartDropoffNotReadyIsConflict(t *testing.T) {
	srv, _, token := withDrops(t)
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))

	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/"+env.Data.Code+"/start", token, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeConflict, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "not uploaded")
}

// TestStartDropoffWhenUploadedRecordsMigration verifies Start links a migration
// row and flips the session to importing once the upload is present.
func TestStartDropoffWhenUploadedRecordsMigration(t *testing.T) {
	srv, fake, token := withDrops(t)
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	code := env.Data.Code

	dump := []byte("PGDMP-start")
	fake.put(migrate.DropDumpKey(code), dump)
	fake.put(migrate.DropMetaKey(code), []byte(`{"sha256":"x"}`))

	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/"+code+"/start", token, nil)
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	var started struct {
		Data migrateStartedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.NotZero(t, started.Data.ID)

	// The migration row was created in drop-off mode and linked to the session,
	// with a self-describing source summary so the shared History "Source" column
	// is not blank for drop-off jobs.
	mrec, err := srv.store.GetMigration(context.Background(), started.Data.ID)
	require.NoError(t, err)
	require.Equal(t, string(migrate.ModeDropOff), mrec.Mode)
	require.Equal(t, code, mrec.Code)
	require.Equal(t, "drop-off "+code, mrec.SourceSummary)

	drec, err := srv.store.GetDropoffByCode(context.Background(), code)
	require.NoError(t, err)
	require.NotNil(t, drec.MigrationID)
	require.Equal(t, started.Data.ID, *drec.MigrationID)
	require.Equal(t, int64(len(dump)), drec.ByteSize)
}

// TestCancelDropoffDeletesObjects verifies cancel removes the data at rest and
// marks the session failed.
func TestCancelDropoffDeletesObjects(t *testing.T) {
	srv, fake, token := withDrops(t)
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	code := env.Data.Code
	fake.put(migrate.DropDumpKey(code), []byte("PGDMP"))
	fake.put(migrate.DropMetaKey(code), []byte(`{"sha256":"x"}`))

	rec = authedRequest(t, srv, http.MethodDelete, "/api/migrate/drops/"+code, token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	require.Contains(t, fake.deletes, migrate.DropDumpKey(code))
	require.Contains(t, fake.deletes, migrate.DropMetaKey(code))

	drec, err := srv.store.GetDropoffByCode(context.Background(), code)
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropFailed), drec.Status)
	require.Equal(t, "cancelled", drec.Error)
}

// TestGetDropoffExpiredInMemoryNotPersisted verifies the expiry-on-read fix: a
// GET on a past-TTL session reports 'expired' to the operator but does NOT persist
// it, so the sweep (the single authority that ALSO deletes the dump at rest) still
// owns the terminal transition — otherwise a read would orphan the dump forever.
func TestGetDropoffExpiredInMemoryNotPersisted(t *testing.T) {
	srv, _, token := withDrops(t)
	ctx := context.Background()
	d := store.DropoffRecord{
		Code: "EXPRD1", DumpKey: migrate.DropDumpKey("EXPRD1"), MetaKey: migrate.DropMetaKey("EXPRD1"),
		TargetDatabase: "appdb", Status: string(migrate.DropWaiting),
		ExpiresAt: time.Now().Add(-time.Minute).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)

	rec := authedRequest(t, srv, http.MethodGet, "/api/migrate/drops/EXPRD1", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var st struct {
		Data dropoffStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(t, string(migrate.DropExpired), st.Data.Status, "GET reports expired in-memory")

	got, err := srv.store.GetDropoffByCode(ctx, "EXPRD1")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropWaiting), got.Status, "expiry is NOT persisted on read; the sweep owns it")
}

// TestSweepExpiredDropoffsReclaimsFailedAndSkipsImporting verifies the sweep
// reclaims a failed/abandoned session's dump at rest (and moves it to terminal
// 'expired' while preserving its failure reason) yet leaves an actively-importing
// session's objects and status untouched.
func TestSweepExpiredDropoffsReclaimsFailedAndSkipsImporting(t *testing.T) {
	srv, fake, _ := withDrops(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour).UTC()

	failed := store.DropoffRecord{
		Code: "FAILED1", DumpKey: migrate.DropDumpKey("FAILED1"), MetaKey: migrate.DropMetaKey("FAILED1"),
		TargetDatabase: "appdb", Status: string(migrate.DropFailed),
		Error: "verification failed: row mismatch", ExpiresAt: past,
	}
	_, err := srv.store.InsertDropoff(ctx, failed)
	require.NoError(t, err)
	fake.put(migrate.DropDumpKey("FAILED1"), []byte("PGDMP-failed-but-at-rest"))
	fake.put(migrate.DropMetaKey("FAILED1"), []byte(`{"sha256":"x"}`))

	importing := store.DropoffRecord{
		Code: "IMPORT1", DumpKey: migrate.DropDumpKey("IMPORT1"), MetaKey: migrate.DropMetaKey("IMPORT1"),
		TargetDatabase: "appdb", Status: string(migrate.DropImporting), ExpiresAt: past,
	}
	_, err = srv.store.InsertDropoff(ctx, importing)
	require.NoError(t, err)
	fake.put(migrate.DropDumpKey("IMPORT1"), []byte("PGDMP-live-import"))
	fake.put(migrate.DropMetaKey("IMPORT1"), []byte(`{"sha256":"y"}`))

	require.NoError(t, srv.sweepExpiredDropoffs(ctx))

	// Failed-and-past-TTL: dump reclaimed, moved to terminal expired, reason kept.
	require.Contains(t, fake.deletes, migrate.DropDumpKey("FAILED1"))
	require.Contains(t, fake.deletes, migrate.DropMetaKey("FAILED1"))
	got, err := srv.store.GetDropoffByCode(ctx, "FAILED1")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropExpired), got.Status)
	require.Contains(t, got.Error, "row mismatch")

	// Importing-and-past-TTL: never reclaimed mid-run.
	require.NotContains(t, fake.deletes, migrate.DropDumpKey("IMPORT1"))
	imp, err := srv.store.GetDropoffByCode(ctx, "IMPORT1")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropImporting), imp.Status)
}

// TestSweepExpiredDropoffsReclaimsCompletedWithLingeringObjects covers the
// orphaned-dump fix: the import success path deletes the dump+meta best-effort, so
// a transient S3 error there would otherwise strand a full database at rest forever
// (a completed session is never otherwise revisited). The sweep now also reclaims
// completed-and-past-TTL sessions, deleting any lingering objects and draining the
// row to terminal 'expired'.
func TestSweepExpiredDropoffsReclaimsCompletedWithLingeringObjects(t *testing.T) {
	srv, fake, _ := withDrops(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour).UTC()

	completed := store.DropoffRecord{
		Code: "DONE01", DumpKey: migrate.DropDumpKey("DONE01"), MetaKey: migrate.DropMetaKey("DONE01"),
		TargetDatabase: "appdb", Status: string(migrate.DropCompleted), ExpiresAt: past,
	}
	_, err := srv.store.InsertDropoff(ctx, completed)
	require.NoError(t, err)
	// Simulate the success-path delete having failed: the objects are still at rest.
	fake.put(migrate.DropDumpKey("DONE01"), []byte("PGDMP-orphaned-after-success"))
	fake.put(migrate.DropMetaKey("DONE01"), []byte(`{"sha256":"x"}`))

	require.NoError(t, srv.sweepExpiredDropoffs(ctx))

	require.Contains(t, fake.deletes, migrate.DropDumpKey("DONE01"))
	require.Contains(t, fake.deletes, migrate.DropMetaKey("DONE01"))
	got, err := srv.store.GetDropoffByCode(ctx, "DONE01")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropExpired), got.Status)
}

// TestFinishDropoffDoesNotResurrectCancelled pins the cancel-vs-completion guard: a
// session a cancel moved to 'failed' while the worker was mid-restore must STAY
// failed/cancelled — finishDropoff(nil) (a restore that happened to finish) must
// not flip it back to 'completed' and silently report a cancelled import as a
// success.
func TestFinishDropoffDoesNotResurrectCancelled(t *testing.T) {
	srv, _, _ := withDrops(t)
	ctx := context.Background()

	d := store.DropoffRecord{
		Code: "CANC01", DumpKey: migrate.DropDumpKey("CANC01"), MetaKey: migrate.DropMetaKey("CANC01"),
		TargetDatabase: "appdb", Status: string(migrate.DropFailed), Error: "cancelled",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)

	srv.finishDropoff(ctx, "CANC01", nil)

	got, err := srv.store.GetDropoffByCode(ctx, "CANC01")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropFailed), got.Status, "a cancelled session must not be resurrected to completed")
	require.Equal(t, "cancelled", got.Error)
}

// TestFinishDropoffCompletesFromImporting is the positive case: the normal worker
// finalize flips an actively-importing session to its terminal outcome.
func TestFinishDropoffCompletesFromImporting(t *testing.T) {
	srv, _, _ := withDrops(t)
	ctx := context.Background()

	d := store.DropoffRecord{
		Code: "IMP02", DumpKey: migrate.DropDumpKey("IMP02"), MetaKey: migrate.DropMetaKey("IMP02"),
		TargetDatabase: "appdb", Status: string(migrate.DropImporting),
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)

	srv.finishDropoff(ctx, "IMP02", nil)

	got, err := srv.store.GetDropoffByCode(ctx, "IMP02")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropCompleted), got.Status)
}
