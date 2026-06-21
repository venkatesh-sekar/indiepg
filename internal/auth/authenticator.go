package auth

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// DefaultSessionTTL is the lifetime of an issued session token when New is
// given a non-positive TTL.
const DefaultSessionTTL = 12 * time.Hour

// sessionSubject is the constant subject for the single-admin session.
const sessionSubject = "admin"

// Authenticator ties password verification and failure lockout to the panel
// store, and mints/validates signed session tokens. It is safe for concurrent
// use to the extent the underlying *store.Store is; the lockout read-modify-
// write is not strictly serialized, which is acceptable for a single-admin
// panel (worst case: an extra attempt or two near the threshold).
type Authenticator struct {
	st         *store.Store
	policy     LockoutPolicy
	sessionTTL time.Duration
	hashParams HashParams
	now        func() time.Time
}

// New constructs an Authenticator over st with the given lockout policy and
// session token TTL. A non-positive TTL falls back to DefaultSessionTTL; a
// zero-value policy falls back to DefaultLockoutPolicy.
func New(st *store.Store, policy LockoutPolicy, sessionTTL time.Duration) *Authenticator {
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}
	return &Authenticator{
		st:         st,
		policy:     policy.normalized(),
		sessionTTL: sessionTTL,
		hashParams: DefaultHashParams(),
		now:        time.Now,
	}
}

// Authenticate verifies password against the stored admin credential, applying
// the lockout policy. Behavior:
//
//   - Locked account            -> *core.Error CodeLocked (with until detail).
//   - Wrong password            -> failure recorded; *core.Error CodeAuth,
//     or CodeLocked if this failure tripped the lockout threshold.
//   - Correct password          -> lockout reset; returns a freshly signed
//     session token.
//
// A missing auth row surfaces as the store's CodeNotFound error.
func (a *Authenticator) Authenticate(ctx context.Context, password string) (string, error) {
	rec, err := a.st.GetAuth(ctx)
	if err != nil {
		return "", err
	}
	now := a.now().UTC()

	// Hard stop if currently locked.
	if locked, until := lockState(rec, now); locked {
		return "", core.LockedError("account locked").
			WithHint("too many failed attempts; try again later").
			WithDetail("locked_until", until.Format(time.RFC3339Nano))
	}

	ok, verr := VerifyPassword(password, rec.PasswordHash)
	if verr != nil {
		// A malformed stored hash is an internal/auth integrity problem, not a
		// user-correctable bad password. Surface as auth failure without
		// incrementing lockout (the credential store itself is broken).
		return "", core.AuthError("cannot verify password").Wrap(verr)
	}
	if !ok {
		return "", a.recordFailure(ctx, rec, now)
	}

	// Success: clear any failure state.
	if rec.FailedAttempts != 0 || rec.LockedUntil != nil {
		if err := a.st.SetLockout(ctx, 0, nil); err != nil {
			return "", err
		}
	}

	token, err := a.issueToken(rec.SessionSecret, now)
	if err != nil {
		return "", err
	}
	return token, nil
}

// recordFailure increments the consecutive-failure counter (respecting the
// sliding Window) and locks the account if the threshold is reached. It always
// returns a typed error describing the outcome.
func (a *Authenticator) recordFailure(ctx context.Context, rec *store.AuthRecord, now time.Time) error {
	attempts := rec.FailedAttempts
	// Reset the streak if the previous failure is older than the window.
	if attempts > 0 && now.Sub(rec.UpdatedAt.UTC()) > a.policy.Window {
		attempts = 0
	}
	attempts++

	if attempts >= a.policy.MaxAttempts {
		until := now.Add(a.policy.LockFor)
		if err := a.st.SetLockout(ctx, attempts, &until); err != nil {
			return err
		}
		return core.LockedError("account locked after %d failed attempts", attempts).
			WithHint("try again later").
			WithDetail("locked_until", until.Format(time.RFC3339Nano))
	}

	if err := a.st.SetLockout(ctx, attempts, nil); err != nil {
		return err
	}
	return core.AuthError("invalid password").
		WithDetail("failed_attempts", attempts).
		WithDetail("remaining", a.policy.MaxAttempts-attempts)
}

// issueToken signs a session valid for the configured TTL starting at now.
func (a *Authenticator) issueToken(secret []byte, now time.Time) (string, error) {
	s := Session{
		Subject:   sessionSubject,
		IssuedAt:  now,
		ExpiresAt: now.Add(a.sessionTTL),
	}
	token, err := SignSession(secret, s)
	if err != nil {
		return "", err
	}
	return token, nil
}

// VerifyToken validates token against the stored session secret and returns the
// decoded Session. Returns *core.Error CodeAuth on tamper/expiry, or whatever
// the store returns if auth is uninitialized.
func (a *Authenticator) VerifyToken(ctx context.Context, token string) (*Session, error) {
	rec, err := a.st.GetAuth(ctx)
	if err != nil {
		return nil, err
	}
	return verifySessionAt(rec.SessionSecret, token, a.now())
}

// SetPassword hashes password with the configured Argon2id parameters and
// stores it, resetting lockout. If auth has never been initialized it creates
// the row with a freshly generated session secret; otherwise it updates only
// the hash and preserves the existing session secret (so already-issued tokens
// continue to validate). An empty password is rejected.
func (a *Authenticator) SetPassword(ctx context.Context, password string) error {
	if password == "" {
		return core.ValidationError("password must not be empty")
	}
	hash, err := HashPassword(password, a.hashParams)
	if err != nil {
		return err
	}

	if _, err := a.st.GetAuth(ctx); err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			// First-time setup: create the auth row with a fresh session secret.
			secret, serr := NewSessionSecret()
			if serr != nil {
				return serr
			}
			return a.st.InitAuth(ctx, hash, secret)
		}
		return err
	}
	// Existing account: rotate only the hash so the session secret (and thus
	// already-issued tokens) is preserved. SetPasswordHash also clears lockout.
	return a.st.SetPasswordHash(ctx, hash)
}

// IsLocked reports whether the account is currently locked and, if so, until
// when (UTC). A nil/expired lockout returns (false, zero, nil).
func (a *Authenticator) IsLocked(ctx context.Context) (bool, time.Time, error) {
	rec, err := a.st.GetAuth(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	locked, until := lockState(rec, a.now().UTC())
	if !locked {
		return false, time.Time{}, nil
	}
	return true, until, nil
}

// lockState reports whether rec is locked at now and the (UTC) deadline. A
// lockout whose deadline has passed is not considered locked.
func lockState(rec *store.AuthRecord, now time.Time) (bool, time.Time) {
	if rec == nil || rec.LockedUntil == nil {
		return false, time.Time{}
	}
	until := rec.LockedUntil.UTC()
	if now.Before(until) {
		return true, until
	}
	return false, time.Time{}
}
