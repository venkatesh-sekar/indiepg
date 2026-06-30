//go:build e2e

package harness

import (
	"testing"
	"time"
)

// Await polls cond every interval until it returns (true, nil) or timeout
// elapses, then fails the test with desc and the last error. It is the ONLY
// sanctioned wait primitive — never use a fixed time.Sleep to "let things
// settle", because that is exactly the nondeterminism this suite exists to avoid.
//
// cond returning an error does NOT abort the poll (transient "not ready yet"
// errors — connection refused, psql not up — are expected while a unit boots);
// the last error is surfaced only if the deadline is hit.
func Await(t *testing.T, timeout, interval time.Duration, desc string, cond func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := cond()
		if ok {
			return
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				t.Fatalf("Await timed out after %s waiting for %s: last error: %v", timeout, desc, lastErr)
			}
			t.Fatalf("Await timed out after %s waiting for %s", timeout, desc)
		}
		time.Sleep(interval)
	}
}

// Poll is the non-fatal form of Await: it returns nil once cond is true, or the
// last error (or a timeout error) otherwise. Use it when a scenario wants to
// branch on the outcome rather than fail outright.
func Poll(timeout, interval time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := cond()
		if ok {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return errTimeout
		}
		time.Sleep(interval)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "poll timed out" }

var errTimeout = timeoutError{}
