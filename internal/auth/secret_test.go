package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratePasswordLengthAndAlphabet(t *testing.T) {
	pw := GeneratePassword()
	require.Len(t, pw, 48)
	for i := 0; i < len(pw); i++ {
		require.Contains(t, passwordAlphabet, string(pw[i]), "char %q out of alphabet", pw[i])
	}
}

func TestGeneratePasswordIsRandom(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		pw := GeneratePassword()
		_, dup := seen[pw]
		require.False(t, dup, "generated a duplicate password")
		seen[pw] = struct{}{}
	}
}

func TestNewSessionSecret(t *testing.T) {
	s1, err := NewSessionSecret()
	require.NoError(t, err)
	require.Len(t, s1, 32)

	s2, err := NewSessionSecret()
	require.NoError(t, err)
	require.NotEqual(t, s1, s2)
}

func TestRandomStringEdgeCases(t *testing.T) {
	require.Equal(t, "", randomString(passwordAlphabet, 0))
	require.Equal(t, "", randomString(passwordAlphabet, -3))
	require.Equal(t, "", randomString("", 10))

	got := randomString("AB", 100)
	require.Len(t, got, 100)
	for i := 0; i < len(got); i++ {
		require.Contains(t, "AB", string(got[i]))
	}
}
