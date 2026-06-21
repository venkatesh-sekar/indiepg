package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

// fakeClock is a controllable monotonic-ish clock for deterministic lockout tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newAuthWithClock builds an Authenticator backed by a fresh in-memory store,
// with a controllable clock, an admin password set, and the given policy.
func newAuthWithClock(t *testing.T, policy LockoutPolicy, password string) (*Authenticator, *store.Store, *fakeClock) {
	t.Helper()
	st := newTestStore(t)
	// Anchor the clock to real wall-clock time so it agrees with the timestamps
	// the store stamps on writes (the store uses time.Now() internally). Tests
	// then advance this clock to simulate the passage of time deterministically.
	clk := &fakeClock{t: time.Now().UTC()}
	a := New(st, policy, time.Hour)
	a.now = clk.Now
	// Use cheap argon2 params so the table-driven tests stay fast.
	a.hashParams = HashParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8}
	require.NoError(t, a.SetPassword(context.Background(), password))
	return a, st, clk
}

func TestAuthenticateSuccess(t *testing.T) {
	a, _, clk := newAuthWithClock(t, DefaultLockoutPolicy(), "s3cret-pass")
	ctx := context.Background()

	token, err := a.Authenticate(ctx, "s3cret-pass")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	sess, err := a.VerifyToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, sessionSubject, sess.Subject)
	require.True(t, sess.ExpiresAt.After(clk.Now()))
}

func TestAuthenticateWrongPasswordIncrementsAttempts(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 3, Window: 15 * time.Minute, LockFor: 10 * time.Minute}
	a, st, _ := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	_, err := a.Authenticate(ctx, "wrong")
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))

	rec, err := st.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, rec.FailedAttempts)
	require.Nil(t, rec.LockedUntil)
}

func TestAuthenticateLocksAfterMaxAttempts(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 3, Window: 15 * time.Minute, LockFor: 10 * time.Minute}
	a, st, clk := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	// Two failures, not yet locked.
	for i := 0; i < 2; i++ {
		_, err := a.Authenticate(ctx, "wrong")
		require.Equal(t, core.CodeAuth, core.CodeOf(err))
	}
	// Third failure trips the lockout.
	_, err := a.Authenticate(ctx, "wrong")
	require.Equal(t, core.CodeLocked, core.CodeOf(err))

	locked, until, err := a.IsLocked(ctx)
	require.NoError(t, err)
	require.True(t, locked)
	require.True(t, until.Equal(clk.Now().Add(policy.LockFor)))

	// Even the correct password is refused while locked.
	_, err = a.Authenticate(ctx, "right")
	require.Equal(t, core.CodeLocked, core.CodeOf(err))

	rec, err := st.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, rec.FailedAttempts)
	require.NotNil(t, rec.LockedUntil)
}

func TestAuthenticateUnlocksAfterLockFor(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 2, Window: 15 * time.Minute, LockFor: 10 * time.Minute}
	a, _, clk := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, _ = a.Authenticate(ctx, "wrong")
	}
	locked, _, err := a.IsLocked(ctx)
	require.NoError(t, err)
	require.True(t, locked)

	// Advance past the lockout window.
	clk.Advance(policy.LockFor + time.Second)

	locked, _, err = a.IsLocked(ctx)
	require.NoError(t, err)
	require.False(t, locked)

	// Correct password now succeeds and clears state.
	token, err := a.Authenticate(ctx, "right")
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

func TestSuccessResetsFailedAttempts(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 5, Window: 15 * time.Minute, LockFor: 10 * time.Minute}
	a, st, _ := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	_, _ = a.Authenticate(ctx, "wrong")
	_, _ = a.Authenticate(ctx, "wrong")
	rec, _ := st.GetAuth(ctx)
	require.Equal(t, 2, rec.FailedAttempts)

	_, err := a.Authenticate(ctx, "right")
	require.NoError(t, err)

	rec, _ = st.GetAuth(ctx)
	require.Equal(t, 0, rec.FailedAttempts)
	require.Nil(t, rec.LockedUntil)
}

func TestFailureWindowResetsStreak(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 3, Window: 10 * time.Minute, LockFor: 10 * time.Minute}
	a, st, clk := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	// Two failures.
	_, _ = a.Authenticate(ctx, "wrong")
	_, _ = a.Authenticate(ctx, "wrong")
	rec, _ := st.GetAuth(ctx)
	require.Equal(t, 2, rec.FailedAttempts)

	// Wait longer than the window, then fail again: streak restarts at 1, not 3.
	clk.Advance(11 * time.Minute)
	_, err := a.Authenticate(ctx, "wrong")
	require.Equal(t, core.CodeAuth, core.CodeOf(err), "should not be locked yet")
	rec, _ = st.GetAuth(ctx)
	require.Equal(t, 1, rec.FailedAttempts)
	require.Nil(t, rec.LockedUntil)
}

func TestAuthenticateUninitialized(t *testing.T) {
	st := newTestStore(t)
	a := New(st, DefaultLockoutPolicy(), time.Hour)
	_, err := a.Authenticate(context.Background(), "anything")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestSetPasswordInitThenUpdate(t *testing.T) {
	st := newTestStore(t)
	a := New(st, DefaultLockoutPolicy(), time.Hour)
	a.hashParams = HashParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8}
	ctx := context.Background()

	// First call initializes auth and generates a session secret.
	require.NoError(t, a.SetPassword(ctx, "first-pass"))
	rec, err := st.GetAuth(ctx)
	require.NoError(t, err)
	require.Len(t, rec.SessionSecret, 32)
	firstSecret := append([]byte(nil), rec.SessionSecret...)

	tok, err := a.Authenticate(ctx, "first-pass")
	require.NoError(t, err)

	// Second call updates the hash but preserves the session secret, so an
	// already-issued token still validates.
	require.NoError(t, a.SetPassword(ctx, "second-pass"))
	rec2, err := st.GetAuth(ctx)
	require.NoError(t, err)
	require.Equal(t, firstSecret, rec2.SessionSecret, "session secret must be preserved across password change")

	sess, err := a.VerifyToken(ctx, tok)
	require.NoError(t, err)
	require.Equal(t, sessionSubject, sess.Subject)

	// Old password no longer works; new one does.
	_, err = a.Authenticate(ctx, "first-pass")
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
	_, err = a.Authenticate(ctx, "second-pass")
	require.NoError(t, err)
}

func TestSetPasswordEmptyRejected(t *testing.T) {
	st := newTestStore(t)
	a := New(st, DefaultLockoutPolicy(), time.Hour)
	err := a.SetPassword(context.Background(), "")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestSetPasswordResetsLockout(t *testing.T) {
	policy := LockoutPolicy{MaxAttempts: 2, Window: 15 * time.Minute, LockFor: time.Hour}
	a, _, _ := newAuthWithClock(t, policy, "right")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, _ = a.Authenticate(ctx, "wrong")
	}
	locked, _, err := a.IsLocked(ctx)
	require.NoError(t, err)
	require.True(t, locked)

	// Setting a new password (the reset-password escape hatch) clears lockout.
	require.NoError(t, a.SetPassword(ctx, "brand-new"))
	locked, _, err = a.IsLocked(ctx)
	require.NoError(t, err)
	require.False(t, locked)

	_, err = a.Authenticate(ctx, "brand-new")
	require.NoError(t, err)
}

func TestVerifyTokenRejectsForeignToken(t *testing.T) {
	a, _, clk := newAuthWithClock(t, DefaultLockoutPolicy(), "pw")
	ctx := context.Background()

	// A token signed with an unrelated secret must be rejected.
	foreign, err := SignSession(mustSecret(t), Session{
		Subject:   "admin",
		IssuedAt:  clk.Now(),
		ExpiresAt: clk.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	_, err = a.VerifyToken(ctx, foreign)
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}

func TestNewNormalizesTTLAndPolicy(t *testing.T) {
	st := newTestStore(t)
	a := New(st, LockoutPolicy{}, 0)
	require.Equal(t, DefaultSessionTTL, a.sessionTTL)
	require.Equal(t, DefaultLockoutPolicy(), a.policy)
}

func TestIsLockedUninitialized(t *testing.T) {
	st := newTestStore(t)
	a := New(st, DefaultLockoutPolicy(), time.Hour)
	_, _, err := a.IsLocked(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestDefaultLockoutPolicy(t *testing.T) {
	p := DefaultLockoutPolicy()
	require.Equal(t, 5, p.MaxAttempts)
	require.Equal(t, 15*time.Minute, p.Window)
	require.Equal(t, 15*time.Minute, p.LockFor)
}
