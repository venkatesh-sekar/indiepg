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

func TestSaveInstancePreservesCreatedAtOnResave(t *testing.T) {
	// SaveInstance upserts the single (id=1) identity row. Its ON CONFLICT DO
	// UPDATE set deliberately OMITS created_at so the panel's recorded birth time
	// survives every later re-save (a panel_version bump on upgrade, a label edit,
	// a pg_system_id capture). Every OTHER column must still update. If created_at
	// ever crept into the UPDATE set, an install's birth time would silently reset
	// on the next SaveInstance — this test locks that contract, and the "every
	// other field updated" half also proves the upsert is a real UPDATE, not a
	// DO NOTHING that would "preserve" created_at for the wrong reason.
	//
	// The birth time is supplied in a NON-UTC zone on purpose: SaveInstance is
	// documented to store canonical UTC (like every other timestamp column, via
	// nowRFC3339), so the raw stored string must be normalized to a trailing "Z",
	// not the input's "+05:30" offset. Asserting the raw TEXT (not the parsed
	// time — Time.Equal compares instants and is blind to the zone) pins the
	// created.UTC() normalization on the write path.
	ctx := context.Background()
	s := newTestStore(t)

	ist := time.FixedZone("IST", 5*60*60+30*60) // +05:30, deliberately not UTC
	birth := time.Date(2021, 3, 4, 5, 6, 7, 0, ist)
	wantCanonical := birth.UTC().Format(time.RFC3339Nano) // "2021-03-03T23:36:07Z"
	require.NoError(t, s.SaveInstance(ctx, Instance{
		InstanceID:   "uuid-original",
		Label:        "web-db-01",
		Hostname:     "host-a",
		PGSystemID:   "7300000000000000000",
		PanelVersion: "1.0.0",
		CreatedAt:    birth,
	}))

	// The raw stored string must be canonical UTC (trailing Z), not the input
	// offset — dropping created.UTC() on the write path would store "+05:30".
	var raw string
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT created_at FROM instance WHERE id = 1").Scan(&raw))
	require.Equal(t, wantCanonical, raw, "created_at must be stored as canonical UTC")
	require.True(t, strings.HasSuffix(raw, "Z"), "stored created_at must be normalized to UTC (Z); got %q", raw)

	got, err := s.GetInstance(ctx)
	require.NoError(t, err)
	require.True(t, got.CreatedAt.Equal(birth), "birth time must round-trip to the same instant")

	// Re-save the same identity row with EVERY field changed, including a
	// deliberately different CreatedAt.
	later := birth.Add(48 * time.Hour)
	require.NoError(t, s.SaveInstance(ctx, Instance{
		InstanceID:   "uuid-rotated",
		Label:        "web-db-01-renamed",
		Hostname:     "host-b",
		PGSystemID:   "7400000000000000000",
		PanelVersion: "2.5.0",
		CreatedAt:    later,
	}))

	got, err = s.GetInstance(ctx)
	require.NoError(t, err)

	// created_at is the birth time — it MUST NOT move even though a different
	// CreatedAt was supplied on the re-save (the UPDATE set omits it).
	require.True(t, got.CreatedAt.Equal(birth),
		"created_at must be preserved across re-save; got %s want %s", got.CreatedAt, birth)
	require.False(t, got.CreatedAt.Equal(later),
		"created_at must not adopt the re-save's CreatedAt")

	// The raw stored string is unchanged too (still the original canonical value).
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT created_at FROM instance WHERE id = 1").Scan(&raw))
	require.Equal(t, wantCanonical, raw, "created_at TEXT must be untouched by the re-save")

	// Every other column MUST reflect the re-save (proves a real UPDATE, not a
	// no-op/DO NOTHING that would coincidentally leave created_at alone).
	require.Equal(t, "uuid-rotated", got.InstanceID)
	require.Equal(t, "web-db-01-renamed", got.Label)
	require.Equal(t, "host-b", got.Hostname)
	require.Equal(t, "7400000000000000000", got.PGSystemID)
	require.Equal(t, "2.5.0", got.PanelVersion)

	// Still exactly one identity row (the CHECK (id = 1) single-row invariant).
	var n int
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM instance").Scan(&n))
	require.Equal(t, 1, n)
}

func TestSaveInstanceStampsCreatedAtWhenZero(t *testing.T) {
	// When a caller supplies a zero CreatedAt, SaveInstance falls back to
	// nowRFC3339() so the birth-time column is never persisted as the zero time
	// (0001-01-01). This guards the `if !created.IsZero()` fallback: dropping it
	// would let a zero input land 0001-01-01 in created_at (a NOT NULL column that
	// GetInstance/Dashboard treat as a real timestamp).
	ctx := context.Background()
	s := newTestStore(t)

	before := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, s.SaveInstance(ctx, Instance{InstanceID: "uuid-zero"})) // CreatedAt left zero

	var raw string
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT created_at FROM instance WHERE id = 1").Scan(&raw))
	require.NotEmpty(t, raw, "created_at must be stamped, never left empty")

	got, err := s.GetInstance(ctx)
	require.NoError(t, err)
	require.False(t, got.CreatedAt.IsZero(), "a zero CreatedAt must be stamped ~now, not stored as the zero time")
	require.True(t, got.CreatedAt.After(before), "stamped created_at must be a recent, real timestamp")
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

func TestInitAuthOverwritesExistingRowAndResetsLockout(t *testing.T) {
	// InitAuth documents that it "overwrites any existing row (used by install and
	// reset-password)" and its ON CONFLICT resets failed_attempts=0 / locked_until
	// =NULL. TestAuthRoundTrip only exercises the first-time INSERT; this drives the
	// reset path: a second InitAuth on a locked-out account must (1) overwrite the
	// hash so the old password stops working, (2) ROTATE the session secret so any
	// token issued under the old secret can no longer be replayed after a reset —
	// the security point of a reset-password — and (3) clear the lockout so the
	// operator isn't locked out of the account they just reset. And it must update
	// the single row in place, never insert a second.
	ctx := context.Background()
	s := newTestStore(t)

	require.NoError(t, s.InitAuth(ctx, "argon2-hash-v1", []byte("secret-v1")))

	// Lock the account, as a burst of failed logins would.
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, s.SetLockout(ctx, 5, &until))
	rec, err := s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, 5, rec.FailedAttempts)
	require.NotNil(t, rec.LockedUntil)
	before := rec.UpdatedAt // the timestamp SetLockout wrote

	// Reset-password: re-init with a new hash AND a new session secret.
	require.NoError(t, s.InitAuth(ctx, "argon2-hash-v2-reset", []byte("secret-v2-rotated")))

	rec, err = s.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, "argon2-hash-v2-reset", rec.PasswordHash, "reset must overwrite the password hash")
	require.Equal(t, []byte("secret-v2-rotated"), rec.SessionSecret,
		"reset must rotate the session secret so tokens issued under the old secret cannot be replayed")
	require.Equal(t, 0, rec.FailedAttempts, "reset must clear the failed-attempt counter")
	require.Nil(t, rec.LockedUntil, "reset must clear the lockout deadline")
	// The reset bumps updated_at to now (excluded.updated_at, not the old row
	// value) so the audit/"last changed" surface reflects the reset. The full
	// GetAuth round-trip + assertions above run between the two writes, so the
	// wall clock has advanced and strict After reliably separates a fresh stamp
	// from a kept one.
	require.True(t, rec.UpdatedAt.After(before), "reset must bump updated_at to now, not keep the old timestamp")

	// Overwrite in place — the single-row invariant holds, no second row appears.
	var count int
	require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM auth").Scan(&count))
	require.Equal(t, 1, count, "re-init must overwrite the existing row, not insert a second")
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

func TestUpdateBackup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a "running" row, then resolve it in place to success — the async path.
	id, err := s.InsertBackup(ctx, BackupRecord{BackupType: "incr", Result: "running"})
	require.NoError(t, err)

	stopped := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.UpdateBackup(ctx, BackupRecord{
		ID: id, Label: "20260624-incr", BackupType: "incr", StoppedAt: &stopped,
		SizeBytes: 2048, RepoBytes: 512, Result: "success",
	}))

	all, err := s.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, all, 1) // updated in place, not a second row
	require.Equal(t, "success", all[0].Result)
	require.Equal(t, "20260624-incr", all[0].Label)
	require.NotNil(t, all[0].StoppedAt)

	// Updating a missing id is a NotFound, not a silent no-op.
	err = s.UpdateBackup(ctx, BackupRecord{ID: 99999, Result: "success"})
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestSweepRunningBackups(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	running, err := s.InsertBackup(ctx, BackupRecord{BackupType: "full", Result: "running"})
	require.NoError(t, err)
	_, err = s.InsertBackup(ctx, BackupRecord{BackupType: "incr", Result: "success"})
	require.NoError(t, err)

	n, err := s.SweepRunningBackups(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n) // only the running row is swept

	all, err := s.ListBackups(ctx, 10)
	require.NoError(t, err)
	require.Len(t, all, 2)
	for _, b := range all {
		if b.ID == running {
			require.Equal(t, "fail", b.Result)
			require.Contains(t, b.Error, "interrupted by panel restart")
			require.NotNil(t, b.StoppedAt)
		}
	}

	// A second sweep finds nothing left to do.
	n, err = s.SweepRunningBackups(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
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

func TestSingleRowCheckRejectsSecondIdentityRow(t *testing.T) {
	// instance and auth are the panel's two singleton tables: every accessor
	// hardcodes `WHERE id = 1` (instance.go:17,60; auth.go:17,65,86,105), so a
	// second row would silently diverge — GetInstance/GetAuth would keep reading
	// id=1 while an id=2 row lived on unseen. The `CHECK (id = 1)` on the PRIMARY
	// KEY (schema.go:15,33) is the DB-level guard that makes a second row
	// impossible. Nothing tested that guard: the existing COUNT(*)=1 assertions
	// all write through id=1 accessors, so they would still pass with the CHECK
	// dropped. Drive a RAW insert of a non-1 id and assert the DB itself refuses.
	ctx := context.Background()

	// Each case supplies an otherwise-fully-valid row (all NOT NULL columns
	// populated) parameterized only by id, so the ONLY thing that can reject an
	// id!=1 insert is the CHECK — not an incidental NOT NULL / type failure.
	cases := []struct {
		table  string
		insert string // one ? bind: the id
	}{
		{
			table:  "instance",
			insert: `INSERT INTO instance (id, instance_id, created_at) VALUES (?, 'uuid-x', '2026-01-01T00:00:00Z')`,
		},
		{
			table:  "auth",
			insert: `INSERT INTO auth (id, session_secret, updated_at) VALUES (?, x'0011deadbeef', '2026-01-01T00:00:00Z')`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			s := newTestStore(t)

			// Positive control: id=1 is accepted — proves the row is otherwise
			// valid, so a rejection of id=2/id=0 below can only be the CHECK
			// firing (guards against a false green from a stray NOT NULL failure).
			_, err := s.DB().ExecContext(ctx, tc.insert, 1)
			require.NoError(t, err, "id=1 row must be accepted (row is otherwise valid)")

			// A second identity row (id=2) must be rejected by CHECK (id = 1),
			// NOT by the PRIMARY KEY (id differs from the existing 1).
			_, err = s.DB().ExecContext(ctx, tc.insert, 2)
			require.Error(t, err, "a second identity row (id=2) must be refused")
			require.ErrorContains(t, err, "CHECK constraint failed",
				"id=2 must be rejected by the single-row CHECK, not some other error")

			// Any non-1 id is rejected, not merely "not the existing row": id=0
			// and id=-1, each on a fresh empty table, prove the constraint pins
			// the value to exactly 1. The negative id is deliberate: a `CHECK
			// (id = 1)` weakened to `CHECK (id * id = 1)` (or `abs(id) = 1`) still
			// rejects 0 and 2 but would silently ADMIT id=-1 — a second, diverging
			// identity row the `WHERE id = 1` accessors never see. Probing -1
			// closes that blind spot.
			for _, badID := range []int{0, -1} {
				s2 := newTestStore(t)
				_, err = s2.DB().ExecContext(ctx, tc.insert, badID)
				require.Error(t, err, "id=%d must be refused even on an empty table", badID)
				require.ErrorContains(t, err, "CHECK constraint failed",
					"id=%d must be rejected by the single-row CHECK", badID)
			}

			// The guarded table still holds exactly the one accepted row.
			var n int
			require.NoError(t, s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tc.table).Scan(&n))
			require.Equal(t, 1, n, "only the id=1 row survives; the id=2 insert never landed")
		})
	}
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
