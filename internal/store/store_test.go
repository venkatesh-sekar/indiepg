package store

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAndPing(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Ping(context.Background()))
}

func TestOpenCreatesPrivateStateFile(t *testing.T) {
	// The state file holds the password hash and session signing secret, so it
	// must be created 0600 (owner-only) and its parent dir 0700, regardless of
	// the process umask.
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "indiepg.db")

	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "state file must be owner read/write only")

	di, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), di.Mode().Perm(), "state dir must be owner-only")
}

func TestOpenTightensExistingStateFile(t *testing.T) {
	// A pre-existing world-readable state file (e.g. created by an older build)
	// must be chmod-ed down to 0600 on Open.
	path := filepath.Join(t.TempDir(), "indiepg.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Chmod(path, 0o644))

	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "existing state file must be tightened to 0600")
}

func TestConnectionPragmasApplyToEveryConnection(t *testing.T) {
	// The connection pragmas (busy_timeout, foreign_keys, ...) must be set on
	// EVERY connection the pool opens, not just the first. database/sql may
	// discard the underlying connection (e.g. after a driver error) and open a
	// fresh one; if the pragmas were applied only once on the pooled *sql.DB,
	// that fresh connection would silently revert busy_timeout to 0 — turning a
	// transient lock into an immediate "database is locked" error. Encoding them
	// in the DSN makes the driver re-apply them on each open. Forcing
	// MaxIdleConns(0) makes the pool open a brand-new connection per query, so
	// reading the pragma back here proves a fresh connection carries it.
	path := filepath.Join(t.TempDir(), "indiepg.db")
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Drop any retained idle connection so the next query opens a fresh one.
	s.DB().SetMaxIdleConns(0)

	ctx := context.Background()
	var busyTimeout int
	require.NoError(t, s.DB().QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout))
	require.Equal(t, 5000, busyTimeout, "busy_timeout must be set on every fresh connection (never get stuck)")

	var foreignKeys int
	require.NoError(t, s.DB().QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys, "foreign_keys must be ON on every fresh connection")

	var journalMode string
	require.NoError(t, s.DB().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode))
	require.Equal(t, "wal", journalMode)
}

func TestBuildDSNEncodesPragmas(t *testing.T) {
	dsn := buildDSN("/var/lib/indiepg/state.db")
	require.True(t, strings.HasPrefix(dsn, "/var/lib/indiepg/state.db?"), "path must be preserved before the query")
	for _, want := range connectionPragmas {
		require.Contains(t, dsn, "_pragma="+url.QueryEscape(want))
	}
	// An empty DSN has no '?' to anchor query params, so it is left unchanged.
	require.Equal(t, "", buildDSN(""))
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	// Re-running migrate must not error.
	require.NoError(t, s.migrate())
	require.NoError(t, s.migrate())
}

func TestInstanceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.GetInstance(ctx)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	inst := Instance{
		InstanceID:   "uuid-1",
		Label:        "web-db-01",
		Hostname:     "host-a",
		PanelVersion: "1.0.0",
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, s.SaveInstance(ctx, inst))

	got, err := s.GetInstance(ctx)
	require.NoError(t, err)
	require.Equal(t, "uuid-1", got.InstanceID)
	require.Equal(t, "web-db-01", got.Label)

	require.NoError(t, s.SetPGSystemID(ctx, "7300000000000000000"))
	got, err = s.GetInstance(ctx)
	require.NoError(t, err)
	require.Equal(t, "7300000000000000000", got.PGSystemID)
}

func TestConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.GetConfig(ctx, "bind_addr")
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	require.NoError(t, s.SetConfig(ctx, "bind_addr", "127.0.0.1:8443"))
	v, err := s.GetConfig(ctx, "bind_addr")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:8443", v)

	require.NoError(t, s.SetConfig(ctx, "bind_addr", "100.64.0.1:8443"))
	all, err := s.AllConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "100.64.0.1:8443", all["bind_addr"])

	require.NoError(t, s.DeleteConfig(ctx, "bind_addr"))
	_, err = s.GetConfig(ctx, "bind_addr")
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestAuthRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.GetAuth(ctx)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	require.NoError(t, s.InitAuth(ctx, "argon2-hash", []byte("secret-bytes")))
	rec, err := s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "argon2-hash", rec.PasswordHash)
	require.Equal(t, []byte("secret-bytes"), rec.SessionSecret)
	require.Equal(t, 0, rec.FailedAttempts)
	require.Nil(t, rec.LockedUntil)

	until := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	require.NoError(t, s.SetLockout(ctx, 3, &until))
	rec, err = s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, rec.FailedAttempts)
	require.NotNil(t, rec.LockedUntil)

	require.NoError(t, s.SetPasswordHash(ctx, "new-hash"))
	rec, err = s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "new-hash", rec.PasswordHash)
	require.Equal(t, 0, rec.FailedAttempts)
	require.Nil(t, rec.LockedUntil)
}

func TestRotateSessionSecret(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Rotating before init must report the missing auth row, not silently no-op.
	require.Equal(t, core.CodeNotFound, core.CodeOf(s.RotateSessionSecret(ctx, []byte("x"))))

	require.NoError(t, s.InitAuth(ctx, "argon2-hash", []byte("original-secret")))
	require.NoError(t, s.SetLockout(ctx, 2, nil))

	require.NoError(t, s.RotateSessionSecret(ctx, []byte("rotated-secret")))
	rec, err := s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("rotated-secret"), rec.SessionSecret)
	// Password hash and failure counters must be preserved by a rotation.
	require.Equal(t, "argon2-hash", rec.PasswordHash)
	require.Equal(t, 2, rec.FailedAttempts)

	// An empty secret is rejected so signing/verification cannot degrade.
	require.Equal(t, core.CodeValidation, core.CodeOf(s.RotateSessionSecret(ctx, nil)))
	rec, err = s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("rotated-secret"), rec.SessionSecret)
}

func TestAuditAppendAndList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		_, err := s.AppendAudit(ctx, AuditEntry{Actor: "admin", Action: "login", Result: "ok"})
		require.NoError(t, err)
	}
	entries, err := s.ListAudit(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, "admin", entries[0].Actor)
}

func TestBackupHistory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.LatestSuccessfulBackup(ctx)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	stopped := time.Now().UTC().Truncate(time.Second)
	_, err = s.InsertBackup(ctx, BackupRecord{
		Label: "20260621-full", BackupType: "full", StartedAt: stopped.Add(-time.Minute),
		StoppedAt: &stopped, SizeBytes: 1000, RepoBytes: 300, Result: "success",
	})
	require.NoError(t, err)
	_, err = s.InsertBackup(ctx, BackupRecord{Label: "20260621-incr", BackupType: "incr", Result: "failed", Error: "boom"})
	require.NoError(t, err)

	all, err := s.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, all, 2)

	latest, err := s.LatestSuccessfulBackup(ctx)
	require.NoError(t, err)
	require.Equal(t, "20260621-full", latest.Label)
	require.NotNil(t, latest.StoppedAt)
}

func TestRestoreTests(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.InsertRestoreTest(ctx, RestoreTestRecord{SourceLabel: "20260621-full", VerifiedRows: 42, Result: "pass"})
	require.NoError(t, err)
	list, err := s.ListRestoreTests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, int64(42), list[0].VerifiedRows)
}

func TestAlerts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	require.NoError(t, s.UpsertAlert(ctx, AlertRecord{
		ID: "pg-down", Name: "Postgres down", Enabled: true,
		Definition: `{"threshold":1}`, Severity: "critical", State: "ok",
	}))
	got, err := s.GetAlert(ctx, "pg-down")
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, "critical", got.Severity)

	fired := time.Now().UTC().Truncate(time.Second)
	got.State = "firing"
	got.LastFiredAt = &fired
	require.NoError(t, s.UpsertAlert(ctx, *got))

	got, err = s.GetAlert(ctx, "pg-down")
	require.NoError(t, err)
	require.Equal(t, "firing", got.State)
	require.NotNil(t, got.LastFiredAt)

	list, err := s.ListAlerts(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, s.DeleteAlert(ctx, "pg-down"))
	_, err = s.GetAlert(ctx, "pg-down")
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestTelemetryBuffer(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		require.NoError(t, s.InsertSample(ctx, TelemetrySample{
			TS: base.Add(time.Duration(i) * time.Minute), Metric: "pg.connections", Value: float64(i),
		}))
	}
	samples, err := s.RecentSamples(ctx, "pg.connections", time.Time{})
	require.NoError(t, err)
	require.Len(t, samples, 5)
	require.Equal(t, float64(0), samples[0].Value)

	cutoff := base.Add(3 * time.Minute)
	n, err := s.PruneTelemetry(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
}
