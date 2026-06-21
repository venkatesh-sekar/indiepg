package guard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenizeBasic(t *testing.T) {
	toks := tokenize("SELECT id FROM users")
	require.Len(t, toks, 4)
	require.True(t, toks[0].isWord("select"))
	require.True(t, toks[1].isWord("id"))
	require.True(t, toks[2].isWord("from"))
	require.True(t, toks[3].isWord("users"))
}

func TestTokenizeDepth(t *testing.T) {
	toks := tokenize("SELECT * FROM (SELECT 1) q")
	// the inner SELECT and 1 are at depth 1.
	var innerSelectDepth, qDepth int
	seenInner := false
	for _, tk := range toks {
		if tk.isWord("select") && tk.depth == 1 {
			innerSelectDepth = tk.depth
			seenInner = true
		}
		if tk.isWord("q") {
			qDepth = tk.depth
		}
	}
	require.True(t, seenInner, "inner SELECT should be at depth 1")
	require.Equal(t, 1, innerSelectDepth)
	require.Equal(t, 0, qDepth)
}

func TestTokenizeStrings(t *testing.T) {
	// keywords inside string literals must not become word tokens.
	toks := tokenize("SELECT 'DROP TABLE x' AS s")
	for _, tk := range toks {
		require.False(t, tk.isWord("drop"), "DROP inside a string must not be a word token")
		require.False(t, tk.isWord("table"))
	}
}

func TestTokenizeEscapeString(t *testing.T) {
	toks := tokenize(`SELECT E'a\'b LIMIT 5' AS s`)
	for _, tk := range toks {
		require.False(t, tk.isWord("limit"), "LIMIT inside an E'' string must not be a word token")
	}
}

func TestTokenizeDollarQuoted(t *testing.T) {
	toks := tokenize("SELECT $tag$ anything DROP TABLE $tag$ AS body")
	for _, tk := range toks {
		require.False(t, tk.isWord("drop"))
		require.False(t, tk.isWord("table"))
	}
	// the trailing AS body should still tokenize.
	last := toks[len(toks)-1]
	require.True(t, last.isWord("body"))
}

func TestTokenizeDollarParam(t *testing.T) {
	toks := tokenize("SELECT * FROM t WHERE id = $1")
	var foundParam bool
	for _, tk := range toks {
		if tk.kind == tokParam {
			foundParam = true
			require.Equal(t, "$1", tk.text)
		}
	}
	require.True(t, foundParam)
}

func TestTokenizeComments(t *testing.T) {
	toks := tokenize("SELECT 1 -- DROP TABLE x\n FROM t /* DELETE FROM y */")
	for _, tk := range toks {
		require.False(t, tk.isWord("drop"))
		require.False(t, tk.isWord("delete"))
	}
}

func TestTokenizeNestedBlockComment(t *testing.T) {
	toks := tokenize("SELECT /* outer /* inner */ still comment */ 1")
	// only SELECT and 1 survive.
	require.Len(t, toks, 2)
	require.True(t, toks[0].isWord("select"))
	require.Equal(t, tokNumber, toks[1].kind)
}

func TestTokenizeQuotedIdentifier(t *testing.T) {
	toks := tokenize(`SELECT * FROM "My ""Quoted"" Table"`)
	last := toks[len(toks)-1]
	require.Equal(t, tokQuoted, last.kind)
	require.Equal(t, `My "Quoted" Table`, last.text)
}

func TestScanDollarTag(t *testing.T) {
	tag, end, ok := scanDollarTag("$tag$body$tag$", 0)
	require.True(t, ok)
	require.Equal(t, "$tag$", tag)
	require.Equal(t, 5, end)

	tag, end, ok = scanDollarTag("$$body$$", 0)
	require.True(t, ok)
	require.Equal(t, "$$", tag)
	require.Equal(t, 2, end)

	// a bind param is not a dollar tag.
	_, _, ok = scanDollarTag("$1", 0)
	require.False(t, ok)
}
