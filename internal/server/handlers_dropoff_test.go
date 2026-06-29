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

func (f *fakeServerDrop) DownloadToFile(ctx context.Context, key, dest string) error {
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return core.NotFoundError("object %s not found", key)
	}
	return os.WriteFile(dest, data, 0o600)
}

func (f *fakeServerDrop) GetObject(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, core.NotFoundError("object %s not found", key)
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
	require.Contains(t, env.Data.CommandDocker, "--dump-url")
	require.Contains(t, env.Data.CommandDocker, "X-Amz-Signature")
	require.Contains(t, env.Data.CommandDocker, "--docker <container>")
	require.Contains(t, env.Data.CommandNative, "--host <source-host>")
	require.False(t, env.Data.ExpiresAt.IsZero())

	// The store record holds only the KEYS — never the presigned URLs.
	drec, err := srv.store.GetDropoffByCode(context.Background(), env.Data.Code)
	require.NoError(t, err)
	require.Equal(t, migrate.DropDumpKey(env.Data.Code), drec.DumpKey)
	require.Equal(t, migrate.DropMetaKey(env.Data.Code), drec.MetaKey)
	require.Equal(t, string(migrate.DropWaiting), drec.Status)
	require.NotContains(t, drec.DumpKey, "X-Amz", "URLs must never be persisted")
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

	// The migration row was created in drop-off mode and linked to the session.
	mrec, err := srv.store.GetMigration(context.Background(), started.Data.ID)
	require.NoError(t, err)
	require.Equal(t, string(migrate.ModeDropOff), mrec.Mode)
	require.Equal(t, code, mrec.Code)

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
