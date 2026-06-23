package core

import "testing"

func TestRedact(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Errorf("Redact(empty) = %q, want empty", got)
	}
	if got := Redact("super-secret-key"); got != RedactedMarker {
		t.Errorf("Redact(secret) = %q, want %q", got, RedactedMarker)
	}
	// The marker must not echo any part of the secret, including its length.
	if got := Redact("a"); got != Redact("a-much-longer-secret-value") {
		t.Errorf("Redact must not vary with secret length: %q vs %q",
			got, Redact("a-much-longer-secret-value"))
	}
}

func TestRedactBytes(t *testing.T) {
	if got := RedactBytes(nil); got != "" {
		t.Errorf("RedactBytes(nil) = %q, want empty", got)
	}
	if got := RedactBytes([]byte{}); got != "" {
		t.Errorf("RedactBytes(empty) = %q, want empty", got)
	}
	if got := RedactBytes([]byte("session-signing-secret")); got != RedactedMarker {
		t.Errorf("RedactBytes(secret) = %q, want %q", got, RedactedMarker)
	}
}
