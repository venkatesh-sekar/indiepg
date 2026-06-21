package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestHashPasswordAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple", DefaultHashParams())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"), "got %q", hash)
	require.Equal(t, 6, len(strings.Split(hash, "$")))

	ok, err := VerifyPassword("correct horse battery staple", hash)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = VerifyPassword("wrong password", hash)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestHashPasswordRandomSalt(t *testing.T) {
	h1, err := HashPassword("samepass", DefaultHashParams())
	require.NoError(t, err)
	h2, err := HashPassword("samepass", DefaultHashParams())
	require.NoError(t, err)
	require.NotEqual(t, h1, h2, "salt should randomize the encoded hash")

	// Both must still verify.
	for _, h := range []string{h1, h2} {
		ok, err := VerifyPassword("samepass", h)
		require.NoError(t, err)
		require.True(t, ok)
	}
}

func TestHashPasswordEmptyRejected(t *testing.T) {
	_, err := HashPassword("", DefaultHashParams())
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestHashPasswordZeroParamsNormalized(t *testing.T) {
	// A zero-value HashParams must still produce a usable, secure hash.
	hash, err := HashPassword("pw", HashParams{})
	require.NoError(t, err)
	require.Contains(t, hash, "m=19456,t=2,p=1")
	ok, err := VerifyPassword("pw", hash)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestVerifyPasswordCustomParams(t *testing.T) {
	p := HashParams{Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 16, SaltLen: 8}
	hash, err := HashPassword("custom", p)
	require.NoError(t, err)
	require.Contains(t, hash, "m=8192,t=1,p=2")

	ok, err := VerifyPassword("custom", hash)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not phc", "plaintext"},
		{"wrong algo", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA"},
		{"bad version", "$argon2id$v=99$m=19456,t=2,p=1$c2FsdA$aGFzaA"},
		{"missing fields", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA"},
		{"bad params", "$argon2id$v=19$m=x,t=2,p=1$c2FsdA$aGFzaA"},
		{"bad salt b64", "$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA"},
		{"bad key b64", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$!!!"},
		{"empty salt", "$argon2id$v=19$m=19456,t=2,p=1$$aGFzaA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword("pw", tc.hash)
			require.False(t, ok)
			require.Error(t, err)
			require.Equal(t, core.CodeValidation, core.CodeOf(err))
		})
	}
}

func TestVerifyPasswordTamperedKeyReturnsFalse(t *testing.T) {
	hash, err := HashPassword("pw", DefaultHashParams())
	require.NoError(t, err)
	parts := strings.Split(hash, "$")
	// Flip the last base64 char of the key (still valid base64), expect false.
	key := parts[5]
	last := key[len(key)-1]
	var repl byte = 'A'
	if last == 'A' {
		repl = 'B'
	}
	parts[5] = key[:len(key)-1] + string(repl)
	tampered := strings.Join(parts, "$")

	ok, err := VerifyPassword("pw", tampered)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestDefaultHashParams(t *testing.T) {
	p := DefaultHashParams()
	require.Equal(t, uint32(2), p.Time)
	require.Equal(t, uint32(19*1024), p.Memory)
	require.Equal(t, uint8(1), p.Threads)
	require.Equal(t, uint32(32), p.KeyLen)
	require.Equal(t, uint32(16), p.SaltLen)
}
