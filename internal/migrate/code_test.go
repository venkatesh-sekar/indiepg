package migrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateCode(t *testing.T) {
	t.Run("length and alphabet", func(t *testing.T) {
		for i := 0; i < 200; i++ {
			code := GenerateCode()
			require.Len(t, code, CodeLength, "code must be %d chars", CodeLength)
			for _, r := range code {
				require.True(t, strings.ContainsRune(CodeAlphabet, r),
					"code %q contains char %q outside alphabet", code, string(r))
			}
		}
	})

	t.Run("excludes ambiguous characters", func(t *testing.T) {
		for _, bad := range []string{"I", "O", "1", "0"} {
			require.NotContains(t, CodeAlphabet, bad,
				"alphabet must exclude ambiguous %q", bad)
		}
	})

	t.Run("reasonably unique", func(t *testing.T) {
		seen := make(map[string]struct{}, 1000)
		dupes := 0
		for i := 0; i < 1000; i++ {
			c := GenerateCode()
			if _, ok := seen[c]; ok {
				dupes++
			}
			seen[c] = struct{}{}
		}
		// 32^6 ~= 1e9 space; 1000 draws should essentially never collide.
		require.LessOrEqual(t, dupes, 1, "too many duplicate codes: %d", dupes)
	})

	t.Run("constants", func(t *testing.T) {
		require.Equal(t, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789", CodeAlphabet)
		require.Equal(t, 32, len(CodeAlphabet))
		require.Equal(t, 6, CodeLength)
		require.Equal(t, "pg-migrations/sessions", SessionPrefix)
	})
}
