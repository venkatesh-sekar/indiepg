package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/migrate"
)

// TestMigrateSingleDBStartsWithoutS3 is the central regression test for the bug
// this feature fixes: a DIRECT single-database pull must work with ZERO S3
// configured. The handler must record a job and return its id (HTTP 202), NOT
// the misleading "requires S3" error that the old stub returned for every mode.
func TestMigrateSingleDBStartsWithoutS3(t *testing.T) {
	srv, st := newTestServer(t)
	require.Nil(t, srv.migrate, "test server has no S3 target, so the session Service must be nil")
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source": map[string]any{
			"host":     "db.example.com",
			"port":     "5432",
			"user":     "reader",
			"password": "s3cr3t",
			"database": "appdb",
		},
		"target_database": "appdb",
		"overwrite":       false,
	})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Data migrateStartedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotZero(t, env.Data.ID, "a direct migration must return a record id to poll")

	// The local store is the source of truth: a record must exist with the
	// redacted source summary and NO password anywhere.
	got, err := st.GetMigration(context.Background(), env.Data.ID)
	require.NoError(t, err)
	require.Equal(t, string(migrate.ModeSingleDB), got.Mode)
	require.Equal(t, "appdb", got.TargetDatabase)
	require.Equal(t, "reader@db.example.com:5432/appdb", got.SourceSummary)
	require.NotContains(t, got.SourceSummary, "s3cr3t", "password must never be persisted")

	// The whole response body must never echo the password either.
	require.NotContains(t, rec.Body.String(), "s3cr3t")
}

// TestMigrateClusterStartsWithoutS3 verifies the cluster mode also runs with no
// S3 and requires no source database.
func TestMigrateClusterStartsWithoutS3(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/cluster", token, map[string]any{
		"source": map[string]any{
			"host":     "10.0.0.5",
			"port":     "5432",
			"user":     "postgres",
			"password": "hunter2",
		},
		"overwrite": true,
		// A whole-cluster overwrite requires the typed sentinel confirmation.
		"confirm": clusterOverwriteConfirm,
	})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Data migrateStartedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotZero(t, env.Data.ID)

	got, err := st.GetMigration(context.Background(), env.Data.ID)
	require.NoError(t, err)
	require.Equal(t, string(migrate.ModeCluster), got.Mode)
	require.True(t, got.Overwrite)
	require.NotContains(t, rec.Body.String(), "hunter2")
}

// TestMigrateSingleDBOverwriteRequiresTypedConfirm verifies the destructive
// overwrite gate: sending overwrite=true with NO (or a wrong) confirm value is a
// typed CodeSafety error, so a bare boolean can never authorize dropping a
// non-empty target.
func TestMigrateSingleDBOverwriteRequiresTypedConfirm(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	// overwrite=true, no confirm -> SafetyError, no job recorded.
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"host": "h", "database": "appdb"},
		"target_database": "appdb",
		"overwrite":       true,
	})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)

	// Wrong confirm value is also rejected.
	rec = authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"host": "h", "database": "appdb"},
		"target_database": "appdb",
		"overwrite":       true,
		"confirm":         "notthename",
	})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	// No migration record should have been created by the rejected requests.
	recs, err := st.ListMigrations(context.Background(), 50)
	require.NoError(t, err)
	require.Empty(t, recs, "a rejected overwrite must not start a job")
}

// TestMigrateSingleDBOverwriteWithConfirmStarts verifies that echoing the target
// database name in confirm satisfies the gate and the job starts.
func TestMigrateSingleDBOverwriteWithConfirmStarts(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"host": "h", "database": "appdb"},
		"target_database": "appdb",
		"overwrite":       true,
		"confirm":         "appdb",
	})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Data migrateStartedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	got, err := st.GetMigration(context.Background(), env.Data.ID)
	require.NoError(t, err)
	require.True(t, got.Overwrite)
}

// TestMigrateClusterOverwriteRequiresTypedConfirm verifies the whole-cluster
// overwrite gate: overwrite=true with no sentinel confirm is a CodeSafety error.
func TestMigrateClusterOverwriteRequiresTypedConfirm(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/cluster", token, map[string]any{
		"source":    map[string]any{"host": "10.0.0.5"},
		"overwrite": true,
	})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeSafety, ae.Code)

	recs, err := st.ListMigrations(context.Background(), 50)
	require.NoError(t, err)
	require.Empty(t, recs, "a rejected cluster overwrite must not start a job")
}

// TestMigrateSingleDBRejectsMissingSourceHost verifies input validation: a direct
// pull with no source host is a CodeValidation error, not a job.
func TestMigrateSingleDBRejectsMissingSourceHost(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"database": "appdb"},
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeValidation, ae.Code)
}

// TestMigrateSessionEndpointsRequireS3 verifies EVERY ssh-less endpoint returns
// the honest "requires S3" CodeInternal error (and only those endpoints do) when
// no S3 target is configured.
func TestMigrateSessionEndpointsRequireS3(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/migrate/sessions", map[string]any{"database": "appdb"}},
		{http.MethodGet, "/api/migrate/sessions/ABC123", nil},
		{http.MethodPost, "/api/migrate/sessions/ABC123/export", map[string]any{
			"source":   map[string]any{"host": "h", "database": "appdb"},
			"database": "appdb",
		}},
		{http.MethodDelete, "/api/migrate/sessions/ABC123", nil},
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

// TestMigrateListAndGet verifies the history list and single-record poll routes
// work over the real in-memory store.
func TestMigrateListAndGet(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	// Start a direct job to create a record.
	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"host": "h", "database": "appdb"},
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())
	var started struct {
		Data migrateStartedResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))

	// List.
	rec = authedRequest(t, srv, http.MethodGet, "/api/migrate", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"migrations":`)
	var list struct {
		Data struct {
			Migrations []migrationResponse `json:"migrations"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.NotEmpty(t, list.Data.Migrations)

	// Get by id.
	rec = authedRequest(t, srv, http.MethodGet, idPath(started.Data.ID), token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var one struct {
		Data migrationResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	require.Equal(t, started.Data.ID, one.Data.ID)
	// Row count maps must serialize as objects, not null.
	require.NotNil(t, one.Data.RowCountsSrc)
	require.NotNil(t, one.Data.RowCountsTgt)
}

// TestMigrateGetUnknownID returns a typed NotFound.
func TestMigrateGetUnknownID(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodGet, "/api/migrate/999999", token, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeNotFound, ae.Code)
}

// TestMigrateSingleDBWritesAudit verifies a successful start writes a success
// audit entry (so the operator log records who pulled what, redacted).
func TestMigrateSingleDBWritesAudit(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/migrate/single-db", token, map[string]any{
		"source":          map[string]any{"host": "src.internal", "user": "rep", "database": "appdb"},
		"target_database": "appdb",
	})
	require.Equal(t, http.StatusAccepted, rec.Code, "body: %s", rec.Body.String())

	entries, err := st.ListAudit(context.Background(), 50, 0)
	require.NoError(t, err)
	var found bool
	for _, e := range entries {
		if e.Action == "migrate_single_db" && e.Result == "success" {
			found = true
			require.NotContains(t, e.Summary, "appdb password", "audit must never leak secrets")
		}
	}
	require.True(t, found, "a migrate_single_db success audit entry must be written")
}

// idPath builds /api/migrate/<id>.
func idPath(id int64) string {
	return "/api/migrate/" + strconv.FormatInt(id, 10)
}

// TestValidateDirectSource pins the fail-fast input validation for a
// user-supplied source connection. Port/User/SSLMode flow into libpq argv/env
// (-p / -U / PGSSLMODE) in os/exec value position with no shell, so this is not
// an injection guard — it turns an opaque mid-connect libpq failure into a clear
// up-front "what's wrong" error on a user-facing migration path.
func TestValidateDirectSource(t *testing.T) {
	base := migrate.ConnInfo{Host: "db.example.com", Database: "appdb"}
	with := func(mut func(*migrate.ConnInfo)) migrate.ConnInfo {
		c := base
		mut(&c)
		return c
	}

	valid := []struct {
		name string
		in   migrate.ConnInfo
	}{
		{"bare host", base},
		{"empty port defaults later", with(func(c *migrate.ConnInfo) { c.Port = "" })},
		{"numeric port", with(func(c *migrate.ConnInfo) { c.Port = "5432" })},
		{"max port", with(func(c *migrate.ConnInfo) { c.Port = "65535" })},
		{"min port", with(func(c *migrate.ConnInfo) { c.Port = "1" })},
		{"empty user", with(func(c *migrate.ConnInfo) { c.User = "" })},
		{"ordinary user", with(func(c *migrate.ConnInfo) { c.User = "reader" })},
		{"mixed-case symbol user", with(func(c *migrate.ConnInfo) { c.User = "App_Reader-1" })},
		{"empty sslmode", with(func(c *migrate.ConnInfo) { c.SSLMode = "" })},
		{"sslmode require", with(func(c *migrate.ConnInfo) { c.SSLMode = "require" })},
		{"sslmode verify-full", with(func(c *migrate.ConnInfo) { c.SSLMode = "verify-full" })},
		{"sslmode disable", with(func(c *migrate.ConnInfo) { c.SSLMode = "disable" })},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			require.NoError(t, validateDirectSource(tc.in, true))
		})
	}

	invalid := []struct {
		name string
		in   migrate.ConnInfo
	}{
		{"non-numeric port", with(func(c *migrate.ConnInfo) { c.Port = "abc" })},
		{"port with space", with(func(c *migrate.ConnInfo) { c.Port = "54 32" })},
		{"negative port", with(func(c *migrate.ConnInfo) { c.Port = "-5" })},
		{"zero port", with(func(c *migrate.ConnInfo) { c.Port = "0" })},
		{"port too high", with(func(c *migrate.ConnInfo) { c.Port = "65536" })},
		{"port overflow", with(func(c *migrate.ConnInfo) { c.Port = "99999999999999999999" })},
		{"user with newline", with(func(c *migrate.ConnInfo) { c.User = "reader\nadmin" })},
		{"user with null", with(func(c *migrate.ConnInfo) { c.User = "reader\x00" })},
		{"user too long", with(func(c *migrate.ConnInfo) { c.User = strings.Repeat("u", 64) })},
		{"unknown sslmode", with(func(c *migrate.ConnInfo) { c.SSLMode = "yes" })},
		{"sslmode wrong case", with(func(c *migrate.ConnInfo) { c.SSLMode = "Require" })},
		{"sslmode with space", with(func(c *migrate.ConnInfo) { c.SSLMode = "require " })},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			err := validateDirectSource(tc.in, true)
			require.Error(t, err)
			var ce *core.Error
			require.ErrorAs(t, err, &ce)
			require.Equal(t, core.CodeValidation, ce.Code)
		})
	}
}

// TestClaimImportTarget_clusterConflictsWithPerDatabase pins finding #1: a
// whole-cluster import drops/restores EVERY database, so the admission gate must
// treat it as conflicting with ANY in-flight per-database import (and vice-versa),
// not just with another cluster import. Two DIFFERENT per-database targets may still
// run concurrently.
func TestClaimImportTarget_clusterConflictsWithPerDatabase(t *testing.T) {
	t.Run("per-db then cluster is refused", func(t *testing.T) {
		s := &Server{}
		require.True(t, s.claimImportTarget("appdb"), "a per-database target is free")
		require.False(t, s.claimImportTarget(clusterImportTarget),
			"a cluster import must conflict with an in-flight per-database import")
		// Releasing the per-database target frees the cluster import.
		s.releaseImportTarget("appdb")
		require.True(t, s.claimImportTarget(clusterImportTarget),
			"with nothing in flight, the cluster import may proceed")
	})

	t.Run("cluster then per-db is refused", func(t *testing.T) {
		s := &Server{}
		require.True(t, s.claimImportTarget(clusterImportTarget), "a cluster import is free")
		require.False(t, s.claimImportTarget("appdb"),
			"a per-database import must conflict with an in-flight whole-cluster import")
		require.False(t, s.claimImportTarget(clusterImportTarget),
			"a second cluster import conflicts with the first")
		// Releasing the cluster sentinel admits the per-database import.
		s.releaseImportTarget(clusterImportTarget)
		require.True(t, s.claimImportTarget("appdb"))
	})

	t.Run("distinct per-db targets run concurrently", func(t *testing.T) {
		s := &Server{}
		require.True(t, s.claimImportTarget("appdb"))
		require.True(t, s.claimImportTarget("otherdb"),
			"two different databases never touch each other and may import at once")
		require.False(t, s.claimImportTarget("appdb"), "the same database is still excluded")
	})
}
