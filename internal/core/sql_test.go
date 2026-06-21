package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "users", false},
		{"underscore start", "_internal", false},
		{"with digits", "app_v2", false},
		{"mixed case", "MyTable", false},
		{"max length", "a123456789012345678901234567890123456789012345678901234567890123", true},
		{"sixty three ok", "a12345678901234567890123456789012345678901234567890123456789012", false},
		{"empty", "", true},
		{"leading digit", "2cool", true},
		{"hyphen", "my-table", true},
		{"space", "my table", true},
		{"quote injection", `a"; DROP TABLE x; --`, true},
		{"reserved select", "select", true},
		{"reserved upper", "TABLE", true},
		{"reserved user", "user", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdentifier(tc.input, "table")
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, CodeValidation, CodeOf(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	require.Equal(t, `"users"`, QuoteIdent("users"))
	require.Equal(t, `"My Table"`, QuoteIdent("My Table"))
	require.Equal(t, `"a""b"`, QuoteIdent(`a"b`))
	// Injection attempt becomes an inert quoted string.
	require.Equal(t, `"x""; DROP TABLE y; --"`, QuoteIdent(`x"; DROP TABLE y; --`))
}

func TestQuoteQualified(t *testing.T) {
	require.Equal(t, `"public"."users"`, QuoteQualified("public", "users"))
	require.Equal(t, `"users"`, QuoteQualified("", "users"))
}

func TestQuoteLiteral(t *testing.T) {
	require.Equal(t, `'hello'`, QuoteLiteral("hello"))
	require.Equal(t, `'O''Brien'`, QuoteLiteral("O'Brien"))
	require.Equal(t, `E'a\\b'`, QuoteLiteral(`a\b`))
	require.Equal(t, `E'a\\b''c'`, QuoteLiteral(`a\b'c`))
}
