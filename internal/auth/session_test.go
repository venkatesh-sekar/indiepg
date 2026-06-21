package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func mustSecret(t *testing.T) []byte {
	t.Helper()
	s, err := NewSessionSecret()
	require.NoError(t, err)
	return s
}

func TestSignAndVerifySessionRoundTrip(t *testing.T) {
	secret := mustSecret(t)
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	s := Session{Subject: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}

	token, err := SignSession(secret, s)
	require.NoError(t, err)
	require.Contains(t, token, ".")

	got, err := verifySessionAt(secret, token, now.Add(30*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "admin", got.Subject)
	require.True(t, got.ExpiresAt.Equal(s.ExpiresAt))
	require.True(t, got.IssuedAt.Equal(s.IssuedAt))
}

func TestSignSessionEmptySecret(t *testing.T) {
	_, err := SignSession(nil, Session{Subject: "x"})
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestVerifySessionExpired(t *testing.T) {
	secret := mustSecret(t)
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	s := Session{Subject: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	token, err := SignSession(secret, s)
	require.NoError(t, err)

	// Exactly at expiry is already expired (now.Before(exp) is false).
	_, err = verifySessionAt(secret, token, s.ExpiresAt)
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))

	// Well past expiry.
	_, err = verifySessionAt(secret, token, now.Add(2*time.Minute))
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}

func TestVerifySessionWrongSecret(t *testing.T) {
	now := time.Now()
	s := Session{Subject: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	token, err := SignSession(mustSecret(t), s)
	require.NoError(t, err)

	_, err = verifySessionAt(mustSecret(t), token, now)
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}

func TestVerifySessionTampered(t *testing.T) {
	secret := mustSecret(t)
	now := time.Now()
	s := Session{Subject: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	token, err := SignSession(secret, s)
	require.NoError(t, err)

	payload, sig, ok := strings.Cut(token, ".")
	require.True(t, ok)

	// Tamper the payload, keep the old signature.
	tampered := payload + "x" + "." + sig
	_, err = verifySessionAt(secret, tampered, now)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}

func TestVerifySessionMalformedTokens(t *testing.T) {
	secret := mustSecret(t)
	now := time.Now()
	cases := []string{
		"",
		"nodot",
		".",
		"onlypayload.",
		".onlysig",
		"@@@.@@@",
	}
	for _, tok := range cases {
		t.Run(tok, func(t *testing.T) {
			_, err := verifySessionAt(secret, tok, now)
			require.Error(t, err)
			require.Equal(t, core.CodeAuth, core.CodeOf(err))
		})
	}
}

func TestVerifySessionEmptySecret(t *testing.T) {
	_, err := verifySessionAt(nil, "a.b", time.Now())
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}

func TestVerifySessionZeroExpiryRejected(t *testing.T) {
	secret := mustSecret(t)
	s := Session{Subject: "admin", IssuedAt: time.Now()} // zero ExpiresAt
	token, err := SignSession(secret, s)
	require.NoError(t, err)

	// Fail closed: an unset (zero) expiry must be rejected regardless of the
	// current time, even when "now" is at the zero time itself.
	_, err = verifySessionAt(secret, token, time.Now())
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))

	_, err = verifySessionAt(secret, token, time.Time{})
	require.Error(t, err)
	require.Equal(t, core.CodeAuth, core.CodeOf(err))
}
