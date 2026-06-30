package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
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
		{http.MethodGet, "/api/migrate/drops", nil},
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

// TestListDropoffsReturnsActiveWithoutSecrets verifies the recovery list endpoint
// returns the live, non-terminal sessions as the safe status view (never the
// presigned URLs/command) so a minted-but-not-started link is resumable after a
// browser reload.
func TestListDropoffsReturnsActiveWithoutSecrets(t *testing.T) {
	srv, fake, token := withDrops(t)

	// Mint two sessions; upload one so it flips to 'uploaded'.
	mint := func(db string) string {
		rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
			"target_database": db,
		})
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Data createDropoffResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		return env.Data.Code
	}
	c1 := mint("appone")
	c2 := mint("apptwo")
	fake.put(migrate.DropDumpKey(c2), []byte("PGDMP"))
	fake.put(migrate.DropMetaKey(c2), []byte(`{"sha256":"x"}`))

	rec := authedRequest(t, srv, http.MethodGet, "/api/migrate/drops", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	// The list view must NEVER carry the presigned command/URL.
	require.NotContains(t, rec.Body.String(), "X-Amz-Signature")
	require.NotContains(t, rec.Body.String(), "migrate-push.sh")

	var env struct {
		Data []dropoffStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	byCode := map[string]dropoffStatusResponse{}
	for _, d := range env.Data {
		byCode[d.Code] = d
	}
	require.Contains(t, byCode, c1)
	require.Contains(t, byCode, c2)
	require.Equal(t, string(migrate.DropWaiting), byCode[c1].Status)
	// The list applies the same upload-readiness flip the single-code status does.
	require.Equal(t, string(migrate.DropUploaded), byCode[c2].Status)
}

// errStatDrop wraps a fake transport but fails every StatObject, modelling S3 with
// wrong credentials or an unreachable bucket. PresignPut is a purely local signing
// op that would still "succeed", so the mint-time probe is the only thing that
// catches the misconfiguration before a command is handed out.
type errStatDrop struct {
	*fakeServerDrop
	err error
}

func (e *errStatDrop) StatObject(ctx context.Context, key string) (int64, bool, error) {
	return 0, false, e.err
}

// probeFailDrop models the real S3 store's FULL-lifecycle probe failing: the panel
// could PUT (the source's upload would land) but cannot stat/read/delete the object
// — a PutObject-only policy. The panel itself needs all of those to import the dump
// and reclaim it, so the mint must be REFUSED rather than handing out a command that
// would import-fail or orphan the dump with no cleanup.
type probeFailDrop struct {
	*fakeServerDrop
}

func (p *probeFailDrop) ProbePutReachable(ctx context.Context, key string) error {
	return core.InternalError("backup: the configured S3 credentials cannot delete objects")
}

// TestCreateDropoffFailsWhenProbeLifecycleDenied pins the full-lifecycle probe fix:
// a transport that can PUT but not stat/read/delete (a PutObject-only policy) must
// FAIL the mint, because the panel itself later needs HEAD/GET/DELETE to import and
// clean up — a PUT-only policy would orphan the dump forever.
func TestCreateDropoffFailsWhenProbeLifecycleDenied(t *testing.T) {
	srv, fake, token := withDrops(t)
	srv.drops = &probeFailDrop{fakeServerDrop: fake}

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeInternal, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "not reachable")
}

// failDeleteDrop is a transport whose DeleteObject always errors, modelling an S3
// target the panel can read but not delete from (rotated/over-restricted creds).
type failDeleteDrop struct {
	*fakeServerDrop
}

func (f *failDeleteDrop) DeleteObject(ctx context.Context, key string) error {
	return core.InternalError("backup: delete S3 object: AccessDenied")
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
	// Cancel records the TERMINAL 'canceled' status (not the retryable 'failed'), so
	// the session can never be re-started even though its presigned PUT URLs live on.
	require.Equal(t, string(migrate.DropCanceled), drec.Status)
	require.Equal(t, "cancelled", drec.Error)
}

// TestCancelDropoffImportingIsRefused pins finding #9: a cancel must be REFUSED
// while the import is running — interrupting an overwrite after the original DB was
// dropped, then deleting the S3 recovery dump, would destroy the only copy. The
// recovery dump must be left untouched.
func TestCancelDropoffImportingIsRefused(t *testing.T) {
	srv, fake, token := withDrops(t)
	ctx := context.Background()

	d := store.DropoffRecord{
		Code: "IMPCXL", DumpKey: migrate.DropDumpKey("IMPCXL"), MetaKey: migrate.DropMetaKey("IMPCXL"),
		TargetDatabase: "appdb", Status: string(migrate.DropImporting),
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)
	fake.put(migrate.DropDumpKey("IMPCXL"), []byte("PGDMP-live"))
	fake.put(migrate.DropMetaKey("IMPCXL"), []byte(`{"sha256":"x"}`))

	rec := authedRequest(t, srv, http.MethodDelete, "/api/migrate/drops/IMPCXL", token, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeConflict, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "importing")

	// The recovery dump must NOT have been deleted, and the session stays importing.
	require.NotContains(t, fake.deletes, migrate.DropDumpKey("IMPCXL"))
	got, err := srv.store.GetDropoffByCode(ctx, "IMPCXL")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropImporting), got.Status)
}

// TestStartDropoffOverwriteRequiresConfirm pins finding #3: the Start endpoint must
// require the typed-name confirmation when the session was minted with overwrite, so
// a direct API call cannot bypass the SPA's confirm dialog and drop the database.
func TestStartDropoffOverwriteRequiresConfirm(t *testing.T) {
	srv, fake, token := withDrops(t)

	// Mint an overwrite session (confirm required at mint too).
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops", token, map[string]any{
		"target_database": "appdb",
		"overwrite":       true,
		"confirm":         "appdb",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var env struct {
		Data createDropoffResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	code := env.Data.Code
	fake.put(migrate.DropDumpKey(code), []byte("PGDMP-ovr"))
	fake.put(migrate.DropMetaKey(code), []byte(`{"sha256":"x"}`))

	// Start with NO typed confirm: refused with a typed CodeSafety error.
	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/"+code+"/start", token, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)

	// A WRONG confirm is also refused.
	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/"+code+"/start", token, map[string]any{"confirm": "nope"})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	// The session is still un-started after the refusals.
	drec, err := srv.store.GetDropoffByCode(context.Background(), code)
	require.NoError(t, err)
	require.Nil(t, drec.MigrationID, "a refused overwrite Start must not have linked a migration")

	// The correct typed confirm authorizes the import.
	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/"+code+"/start", token, map[string]any{"confirm": "appdb"})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
}

// TestStartDropoffCancelledIsRefused pins finding #6: a cancelled session must never
// be startable, even though its presigned PUT URLs cannot be revoked and a dump may
// still be present.
func TestStartDropoffCancelledIsRefused(t *testing.T) {
	srv, fake, token := withDrops(t)
	ctx := context.Background()

	d := store.DropoffRecord{
		Code: "CXLSTR", DumpKey: migrate.DropDumpKey("CXLSTR"), MetaKey: migrate.DropMetaKey("CXLSTR"),
		TargetDatabase: "appdb", Status: string(migrate.DropCanceled), Error: "cancelled",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)
	fake.put(migrate.DropDumpKey("CXLSTR"), []byte("PGDMP-reuploaded"))
	fake.put(migrate.DropMetaKey("CXLSTR"), []byte(`{"sha256":"x"}`))

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/drops/CXLSTR/start", token, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeConflict, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "cancel")

	got, err := srv.store.GetDropoffByCode(ctx, "CXLSTR")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropCanceled), got.Status, "a cancelled session stays cancelled")
}

// TestSweepExpiredDropoffsLeavesUnreclaimedOnDeleteFailure pins finding #4: the sweep
// must NOT mark a session 'expired' (which would exclude it from all future sweeps)
// until BOTH idempotent object deletes succeed — a delete failure leaves it eligible
// for a later retry instead of orphaning the dump forever.
func TestSweepExpiredDropoffsLeavesUnreclaimedOnDeleteFailure(t *testing.T) {
	srv, fake, _ := withDrops(t)
	srv.drops = &failDeleteDrop{fakeServerDrop: fake}
	ctx := context.Background()
	past := time.Now().Add(-time.Hour).UTC()

	d := store.DropoffRecord{
		Code: "DELERR", DumpKey: migrate.DropDumpKey("DELERR"), MetaKey: migrate.DropMetaKey("DELERR"),
		TargetDatabase: "appdb", Status: string(migrate.DropFailed), Error: "verification failed: row mismatch",
		ExpiresAt: past,
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)
	fake.put(migrate.DropDumpKey("DELERR"), []byte("PGDMP-still-at-rest"))
	fake.put(migrate.DropMetaKey("DELERR"), []byte(`{"sha256":"x"}`))

	require.NoError(t, srv.sweepExpiredDropoffs(ctx))

	got, err := srv.store.GetDropoffByCode(ctx, "DELERR")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropFailed), got.Status,
		"a delete failure must NOT mark the session expired; it stays eligible for the next sweep")
	require.Contains(t, got.Error, "row mismatch")
}

// TestUpdateConfigBlocksS3ChangeWhileUncleanedDropoffExists pins finding #2: an
// S3-target change must be REFUSED while ANY drop-off session may still own objects
// in the current bucket — including a 'failed' session whose dump is kept for retry —
// because re-pointing the panel at a new bucket would orphan that dump and break its
// retry/cleanup. Only a terminally-'expired' session (objects provably reclaimed)
// stops blocking the change.
func TestUpdateConfigBlocksS3ChangeWhileUncleanedDropoffExists(t *testing.T) {
	srv, _, token := withDrops(t)
	ctx := context.Background()

	// Persist an initial S3 target so a later bucket change counts as a re-point.
	cfg := config.Default()
	cfg.Backup.Endpoint = "s3.example.com"
	cfg.Backup.Bucket = "old-bucket"
	cfg.Backup.AccessKey = "AK"
	cfg.Backup.SecretKey = "SK"
	require.NoError(t, config.Save(ctx, srv.store, cfg))

	// A 'failed' session retains its dump in the OLD bucket for retry: uncleaned.
	d := store.DropoffRecord{
		Code: "FAILUN", DumpKey: migrate.DropDumpKey("FAILUN"), MetaKey: migrate.DropMetaKey("FAILUN"),
		TargetDatabase: "appdb", Status: string(migrate.DropFailed), Error: "verification failed",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)

	changeBucket := func() *httptest.ResponseRecorder {
		return authedRequest(t, srv, http.MethodPut, "/api/config", token, map[string]any{
			"backup": map[string]any{"bucket": "new-bucket"},
		})
	}

	rec := changeBucket()
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeConflict, ae.Code)
	require.Contains(t, strings.ToLower(ae.Message), "s3 target")

	// The change must NOT have been applied while it was blocked.
	saved, err := config.Load(ctx, srv.store)
	require.NoError(t, err)
	require.Equal(t, "old-bucket", saved.Backup.Bucket, "a blocked S3 change must not persist")

	// Draining the session to terminal 'expired' (objects reclaimed) lifts the gate.
	exp, err := srv.store.GetDropoffByCode(ctx, "FAILUN")
	require.NoError(t, err)
	exp.Status = string(migrate.DropExpired)
	require.NoError(t, srv.store.UpdateDropoff(ctx, *exp))

	rec = changeBucket()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	saved, err = config.Load(ctx, srv.store)
	require.NoError(t, err)
	require.Equal(t, "new-bucket", saved.Backup.Bucket, "the change applies once no uncleaned session remains")
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
		TargetDatabase: "appdb", Status: string(migrate.DropCanceled), Error: "cancelled",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	_, err := srv.store.InsertDropoff(ctx, d)
	require.NoError(t, err)

	srv.finishDropoff(ctx, "CANC01", nil)

	got, err := srv.store.GetDropoffByCode(ctx, "CANC01")
	require.NoError(t, err)
	require.Equal(t, string(migrate.DropCanceled), got.Status, "a cancelled session must not be resurrected to completed")
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

// TestFailMigrationRowMarksImportingRowFailed verifies the drop-off start error
// path that aborts AFTER InsertMigration but BEFORE the worker is spawned (a failed
// UpdateDropoff link) does not leave a phantom 'importing' migration row: it must be
// flipped to 'failed' with the cause and a finished timestamp, so the History view
// shows no orphaned in-flight job lingering until a restart sweep reclaims it.
func TestFailMigrationRowMarksImportingRowFailed(t *testing.T) {
	srv, _, _ := withDrops(t)
	ctx := context.Background()

	// dropoffMigrationRecord inserts in status 'importing' / phase 'validating' —
	// exactly the orphaned state the start handler would leave behind.
	id, err := srv.store.InsertMigration(ctx, dropoffMigrationRecord("LINK01", "appdb", false))
	require.NoError(t, err)
	pre, err := srv.store.GetMigration(ctx, id)
	require.NoError(t, err)
	require.Equal(t, string(migrate.StatusImporting), pre.Status)

	srv.failMigrationRow(ctx, id, core.InternalError("could not link migration"))

	mrec, err := srv.store.GetMigration(ctx, id)
	require.NoError(t, err)
	require.Equal(t, string(migrate.StatusFailed), mrec.Status)
	require.Empty(t, mrec.Phase)
	require.Contains(t, mrec.Error, "could not link migration")
	require.NotNil(t, mrec.FinishedAt)
}
