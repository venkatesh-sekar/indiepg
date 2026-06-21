package auth

import "time"

// LockoutPolicy controls failure-based account lockout. After MaxAttempts
// consecutive failures the account is locked for LockFor. Window bounds how far
// back consecutive failures are counted: a failure older than Window since the
// last update is treated as the start of a fresh streak rather than adding to
// the existing count.
type LockoutPolicy struct {
	MaxAttempts int           // failures allowed before locking (must be >= 1)
	Window      time.Duration // sliding window for counting consecutive failures
	LockFor     time.Duration // how long the account stays locked once tripped
}

// DefaultLockoutPolicy returns a sensible default: 5 failures within 15 minutes
// locks the account for 15 minutes.
func DefaultLockoutPolicy() LockoutPolicy {
	return LockoutPolicy{
		MaxAttempts: 5,
		Window:      15 * time.Minute,
		LockFor:     15 * time.Minute,
	}
}

// normalized returns the policy with any non-positive field replaced by its
// default, so a zero-value policy still behaves safely.
func (p LockoutPolicy) normalized() LockoutPolicy {
	d := DefaultLockoutPolicy()
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = d.MaxAttempts
	}
	if p.Window <= 0 {
		p.Window = d.Window
	}
	if p.LockFor <= 0 {
		p.LockFor = d.LockFor
	}
	return p
}
