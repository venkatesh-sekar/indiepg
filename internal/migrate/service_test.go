package migrate

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
)

// fakeObjectStore is an in-memory ObjectStore for tests. Absent keys return a
// *core.Error CodeNotFound, matching the identity.ObjectStore contract.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	getErr  error
	delErr  error
	puts    int
	deletes []string
}

func newFakeStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

func (f *fakeObjectStore) GetObject(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, core.NotFoundError("object %s not found", key)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeObjectStore) PutObject(ctx context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.puts++
	f.objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeObjectStore) DeleteObject(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	f.deletes = append(f.deletes, key)
	delete(f.objects, key)
	return nil
}

func newService(t *testing.T) (*Service, *fakeObjectStore) {
	t.Helper()
	fs := newFakeStore()
	svc := NewService(fs, exec.NewFakeRunner(), core.Discard())
	return svc, fs
}

func TestSessionKeyLayout(t *testing.T) {
	require.Equal(t, "pg-migrations/sessions/ABCDEF/session.json", SessionKey("ABCDEF"))
	require.Equal(t, "pg-migrations/sessions/ABCDEF/dump.bin", DumpKey("ABCDEF"))
}

func TestCreateSession(t *testing.T) {
	svc, fs := newService(t)
	ctx := context.Background()
	fixed := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }

	sess, err := svc.CreateSession(ctx, "appdb", 30*time.Minute)
	require.NoError(t, err)
	require.Len(t, sess.Code, CodeLength)
	require.Equal(t, "appdb", sess.Database)
	require.Equal(t, StatusWaiting, sess.Status)
	require.Equal(t, fixed, sess.CreatedAt)
	require.Equal(t, fixed.Add(30*time.Minute), sess.ExpiresAt)

	// Persisted under the right key with valid JSON.
	raw, ok := fs.objects[SessionKey(sess.Code)]
	require.True(t, ok, "session document must be written")
	var got MigrationSession
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, sess.Code, got.Code)
	require.Equal(t, StatusWaiting, got.Status)
}

func TestCreateSession_defaultTTL(t *testing.T) {
	svc, _ := newService(t)
	fixed := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }

	sess, err := svc.CreateSession(context.Background(), "appdb", 0)
	require.NoError(t, err)
	require.Equal(t, fixed.Add(DefaultTTL), sess.ExpiresAt)
}

func TestCreateSession_validation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	_, err := svc.CreateSession(ctx, "", time.Hour)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	// Reserved/invalid identifier rejected via core.ValidateIdentifier.
	_, err = svc.CreateSession(ctx, "select", time.Hour)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	_, err = svc.CreateSession(ctx, "bad name!", time.Hour)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestCreateSession_putError(t *testing.T) {
	fs := newFakeStore()
	fs.putErr = core.InternalError("s3 down")
	svc := NewService(fs, exec.NewFakeRunner(), core.Discard())

	_, err := svc.CreateSession(context.Background(), "appdb", time.Hour)
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestGetSession_roundTrip(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	fixed := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }

	created, err := svc.CreateSession(ctx, "appdb", time.Hour)
	require.NoError(t, err)

	got, err := svc.GetSession(ctx, created.Code)
	require.NoError(t, err)
	require.Equal(t, created.Code, got.Code)
	require.Equal(t, StatusWaiting, got.Status)
	require.True(t, got.CreatedAt.Equal(created.CreatedAt))
}

func TestGetSession_notFound(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.GetSession(context.Background(), "ZZZZZZ")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestGetSession_emptyCode(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.GetSession(context.Background(), "")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestGetSession_malformed(t *testing.T) {
	fs := newFakeStore()
	fs.objects[SessionKey("ABCDEF")] = []byte("{not json")
	svc := NewService(fs, exec.NewFakeRunner(), core.Discard())

	_, err := svc.GetSession(context.Background(), "ABCDEF")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestGetSession_expiryOnRead(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	create := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return create }
	created, err := svc.CreateSession(ctx, "appdb", 10*time.Minute)
	require.NoError(t, err)

	// Advance past expiry: a live session reads back as expired.
	svc.now = func() time.Time { return create.Add(time.Hour) }
	got, err := svc.GetSession(ctx, created.Code)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, got.Status)
}

func TestGetSession_terminalNotOverriddenByExpiry(t *testing.T) {
	svc, fs := newService(t)
	ctx := context.Background()
	create := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return create }
	created, err := svc.CreateSession(ctx, "appdb", 10*time.Minute)
	require.NoError(t, err)

	// Mark it completed in the store, then read past expiry.
	created.Status = StatusCompleted
	data, err := marshalSession(created)
	require.NoError(t, err)
	fs.objects[SessionKey(created.Code)] = data

	svc.now = func() time.Time { return create.Add(time.Hour) }
	got, err := svc.GetSession(ctx, created.Code)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, got.Status, "terminal status must not be overwritten by expiry")
}

func TestUpdateSession(t *testing.T) {
	svc, fs := newService(t)
	ctx := context.Background()
	created, err := svc.CreateSession(ctx, "appdb", time.Hour)
	require.NoError(t, err)

	created.Status = StatusExporting
	created.SourceHost = "10.0.0.9"
	require.NoError(t, svc.UpdateSession(ctx, created))

	var got MigrationSession
	require.NoError(t, json.Unmarshal(fs.objects[SessionKey(created.Code)], &got))
	require.Equal(t, StatusExporting, got.Status)
	require.Equal(t, "10.0.0.9", got.SourceHost)
}

func TestUpdateSession_validation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	require.Error(t, svc.UpdateSession(ctx, nil))
	require.Equal(t, core.CodeValidation, core.CodeOf(svc.UpdateSession(ctx, nil)))

	err := svc.UpdateSession(ctx, &MigrationSession{})
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestTransition(t *testing.T) {
	svc, fs := newService(t)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, "appdb", time.Hour)
	require.NoError(t, err)

	// Legal walk through the happy path persists each step.
	require.NoError(t, svc.Transition(ctx, sess, StatusExporting))
	require.Equal(t, StatusExporting, sess.Status)
	require.NoError(t, svc.Transition(ctx, sess, StatusExported))
	require.NoError(t, svc.Transition(ctx, sess, StatusImporting))
	require.NoError(t, svc.Transition(ctx, sess, StatusCompleted))

	var got MigrationSession
	require.NoError(t, json.Unmarshal(fs.objects[SessionKey(sess.Code)], &got))
	require.Equal(t, StatusCompleted, got.Status)
}

func TestTransition_illegal(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, "appdb", time.Hour)
	require.NoError(t, err)

	// Skipping exporting is illegal and must not mutate the session.
	err = svc.Transition(ctx, sess, StatusCompleted)
	require.Error(t, err)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))
	require.Equal(t, StatusWaiting, sess.Status, "illegal transition must not change status")
}

func TestTransition_nil(t *testing.T) {
	svc, _ := newService(t)
	err := svc.Transition(context.Background(), nil, StatusExporting)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestCleanupSession(t *testing.T) {
	svc, fs := newService(t)
	ctx := context.Background()
	sess, err := svc.CreateSession(ctx, "appdb", time.Hour)
	require.NoError(t, err)
	fs.objects[DumpKey(sess.Code)] = []byte("dump")

	require.NoError(t, svc.CleanupSession(ctx, sess.Code))
	require.NotContains(t, fs.objects, SessionKey(sess.Code))
	require.NotContains(t, fs.objects, DumpKey(sess.Code))
	require.Contains(t, fs.deletes, SessionKey(sess.Code))
	require.Contains(t, fs.deletes, DumpKey(sess.Code))
}

func TestCleanupSession_idempotentOnMissing(t *testing.T) {
	// A delete that reports CodeNotFound is swallowed; cleanup stays idempotent.
	fs := newFakeStore()
	fs.delErr = core.NotFoundError("gone")
	svc := NewService(fs, exec.NewFakeRunner(), core.Discard())
	require.NoError(t, svc.CleanupSession(context.Background(), "ABCDEF"))
}

func TestCleanupSession_realError(t *testing.T) {
	fs := newFakeStore()
	fs.delErr = core.InternalError("s3 down")
	svc := NewService(fs, exec.NewFakeRunner(), core.Discard())
	err := svc.CleanupSession(context.Background(), "ABCDEF")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestCleanupSession_emptyCode(t *testing.T) {
	svc, _ := newService(t)
	err := svc.CleanupSession(context.Background(), "")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestMarshalSession_utcNormalized(t *testing.T) {
	loc := time.FixedZone("PST", -8*3600)
	sess := &MigrationSession{
		Code:      "ABCDEF",
		CreatedAt: time.Date(2026, 6, 21, 9, 0, 0, 0, loc),
		ExpiresAt: time.Date(2026, 6, 21, 11, 0, 0, 0, loc),
	}
	data, err := marshalSession(sess)
	require.NoError(t, err)
	var got MigrationSession
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, time.UTC, got.CreatedAt.Location())
	require.Equal(t, time.UTC, got.ExpiresAt.Location())
	require.True(t, got.CreatedAt.Equal(sess.CreatedAt))
}

func TestNewService_nilLogger(t *testing.T) {
	require.NotPanics(t, func() {
		svc := NewService(newFakeStore(), exec.NewFakeRunner(), nil)
		_, _ = svc.CreateSession(context.Background(), "appdb", time.Hour)
	})
}
