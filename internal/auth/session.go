package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Session is the payload carried inside a signed token. Timestamps are encoded
// as RFC3339Nano UTC via the standard time JSON marshaling.
type Session struct {
	Subject   string    `json:"sub"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`
}

// tokenSep separates the base64url payload from its base64url signature.
const tokenSep = "."

// SignSession serializes s to JSON and returns a URL-safe token
// "<base64url(payload)>.<base64url(HMAC-SHA256(payload))>". The secret must be
// non-empty.
func SignSession(secret []byte, s Session) (string, error) {
	if len(secret) == 0 {
		return "", core.InternalError("session secret is empty")
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", core.InternalError("encode session").Wrap(err)
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, encPayload)
	return encPayload + tokenSep + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifySession validates a token's HMAC signature against secret and its
// expiry against the current time, returning the decoded Session. Any tamper,
// malformed token, or expiry yields a *core.Error with CodeAuth so the
// signature and the reason for failure are not distinguishable to a caller (no
// oracle), beyond the broad "auth" code.
func VerifySession(secret []byte, token string) (*Session, error) {
	return verifySessionAt(secret, token, time.Now())
}

// verifySessionAt is the time-injectable core of VerifySession (for tests).
func verifySessionAt(secret []byte, token string, now time.Time) (*Session, error) {
	if len(secret) == 0 {
		return nil, core.AuthError("session secret is empty")
	}
	encPayload, encSig, ok := cut(token, tokenSep)
	if !ok || encPayload == "" || encSig == "" {
		return nil, core.AuthError("invalid session token")
	}

	gotSig, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil {
		return nil, core.AuthError("invalid session token")
	}
	wantSig := sign(secret, encPayload)
	if !hmac.Equal(gotSig, wantSig) {
		return nil, core.AuthError("invalid session signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return nil, core.AuthError("invalid session token")
	}
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, core.AuthError("invalid session payload")
	}
	// Fail closed: a missing (zero) expiry must be rejected rather than treated
	// as "never expires". Only a token whose expiry is set and strictly in the
	// future is accepted.
	if s.ExpiresAt.IsZero() {
		return nil, core.AuthError("session missing expiry")
	}
	if !now.Before(s.ExpiresAt) {
		return nil, core.AuthError("session expired").WithDetail("expired_at", s.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	return &s, nil
}

// sign returns the raw HMAC-SHA256 of encPayload under secret.
func sign(secret []byte, encPayload string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encPayload))
	return mac.Sum(nil)
}

// cut splits s around the first instance of sep. (strings.Cut equivalent, kept
// local to avoid version assumptions.)
func cut(s, sep string) (before, after string, found bool) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}
