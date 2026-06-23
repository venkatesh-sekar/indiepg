package core

// RedactedMarker is the fixed placeholder that replaces a secret value when a
// secret-bearing struct renders itself for a log line, an error string, or any
// fmt verb. It carries no information about the secret (not even its length), so
// it is safe to surface anywhere.
const RedactedMarker = "REDACTED"

// Redact returns RedactedMarker when secret is non-empty and "" when it is
// empty. Use it inside a String()/LogValue() implementation on a struct that
// holds secrets (S3 keys, session signing secret, notification tokens) so the
// secret itself never reaches logs, error text, or stdout — while still letting
// the rendering distinguish "a secret is set" from "no secret configured".
//
// Empty maps to "" rather than the marker on purpose: an unset optional secret
// is not a secret, and printing the marker for it would be misleading.
func Redact(secret string) string {
	if secret == "" {
		return ""
	}
	return RedactedMarker
}

// RedactBytes is the []byte form of Redact, for secrets stored as raw bytes
// (e.g. the HMAC session signing secret). It never reveals the byte length.
func RedactBytes(secret []byte) string {
	if len(secret) == 0 {
		return ""
	}
	return RedactedMarker
}
