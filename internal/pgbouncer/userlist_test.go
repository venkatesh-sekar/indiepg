package pgbouncer

import (
	"strings"
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// A realistic SCRAM-SHA-256 verifier shape (iterations:salt$storedkey:serverkey),
// safe for tests — these are not real keys.
const (
	scramApp   = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0c2E=$c3RvcmVka2V5c3RvcmVka2V5c3RvcmVrPQ==:c2VydmVya2V5c2VydmVya2V5c2VydmU="
	scramOther = "SCRAM-SHA-256$4096:b3RoZXJzYWx0b3RoZXJz$b3RoZXJzdG9yZWRrZXlvdGhlcnN0bz0=:b3RoZXJzZXJ2ZXJrZXlvdGhlcg=="
)

func TestRenderUserlist_BasicLineFormat(t *testing.T) {
	out, err := RenderUserlist([]UserlistEntry{{Username: "appuser", Verifier: scramApp}})
	if err != nil {
		t.Fatalf("RenderUserlist: %v", err)
	}
	want := `"appuser" "` + scramApp + "\"\n"
	if out != want {
		t.Fatalf("line format mismatch:\n got: %q\nwant: %q", out, want)
	}
}

func TestRenderUserlist_DeterministicSortedAndStable(t *testing.T) {
	// Same set in two orders must render byte-identical (sorted by username) so
	// the enable flow can compare against the on-disk file and skip a reload.
	a, err := RenderUserlist([]UserlistEntry{
		{Username: "zeta", Verifier: scramOther},
		{Username: "alpha", Verifier: scramApp},
	})
	if err != nil {
		t.Fatalf("RenderUserlist a: %v", err)
	}
	b, err := RenderUserlist([]UserlistEntry{
		{Username: "alpha", Verifier: scramApp},
		{Username: "zeta", Verifier: scramOther},
	})
	if err != nil {
		t.Fatalf("RenderUserlist b: %v", err)
	}
	if a != b {
		t.Fatalf("render is not order-independent:\n a=%q\n b=%q", a, b)
	}
	// alpha must come before zeta.
	if !strings.HasPrefix(a, `"alpha"`) {
		t.Errorf("entries not sorted ascending; got:\n%s", a)
	}
	lines := strings.Count(a, "\n")
	if lines != 2 {
		t.Errorf("want 2 entry lines, got %d:\n%s", lines, a)
	}
}

func TestRenderUserlist_EmptyIsRefused(t *testing.T) {
	// An empty auth_file would lock every app out of the pooler — refuse it.
	if _, err := RenderUserlist(nil); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("empty userlist: want CodeValidation, got %v (err=%v)", core.CodeOf(err), err)
	}
	if _, err := RenderUserlist([]UserlistEntry{}); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("empty slice: want CodeValidation, got %v (err=%v)", core.CodeOf(err), err)
	}
}

func TestRenderUserlist_NonSCRAMVerifierRefused(t *testing.T) {
	// md5 and plaintext verifiers must never be written — no auth downgrade.
	for _, bad := range []string{
		"md5d41d8cd98f00b204e9800998ecf8427e", // md5 verifier
		"plaintextpassword",                   // plaintext
		"scram-sha-256$4096:abc$def:ghi",      // wrong-case prefix
		"SCRAM-SHA-1$4096:abc$def:ghi",        // wrong algorithm
		"",                                    // empty verifier
	} {
		_, err := RenderUserlist([]UserlistEntry{{Username: "appuser", Verifier: bad}})
		if core.CodeOf(err) != core.CodeValidation {
			t.Errorf("verifier %q: want CodeValidation, got %v", bad, core.CodeOf(err))
		}
	}
}

func TestRenderUserlist_VerifierInjectionRefused(t *testing.T) {
	// A correct prefix but a quote/newline/space in the body would break out of
	// the quoted token. The charset guard must reject it.
	for _, bad := range []string{
		scramPrefix + `4096:abc$def:ghi" "admin`,  // embedded quote -> second entry
		scramPrefix + "4096:abc$def\nadmin \"x\"", // embedded newline
		scramPrefix + "4096:abc def:ghi",          // embedded space
		scramPrefix + "4096:abc#def:ghi",          // stray '#'
	} {
		_, err := RenderUserlist([]UserlistEntry{{Username: "appuser", Verifier: bad}})
		if core.CodeOf(err) != core.CodeValidation {
			t.Errorf("verifier %q: want CodeValidation, got %v", bad, core.CodeOf(err))
		}
	}
}

func TestRenderUserlist_UsernameInjectionRefused(t *testing.T) {
	// A quote, whitespace, control char, or line separator in the username could
	// inject a second auth entry — reject rather than escape.
	for _, name := range []string{
		`app" "` + scramApp + `" ; "admin`, // embedded quote
		"app user",                         // space
		"app\tuser",                        // tab
		"app\nadmin",                       // newline
		"app\u2028admin",                   // Unicode LINE SEPARATOR (U+2028)
		"app\u2029admin",                   // Unicode PARAGRAPH SEPARATOR (U+2029)
		"app\x00user",                      // NUL
		"",                                 // empty
	} {
		_, err := RenderUserlist([]UserlistEntry{{Username: name, Verifier: scramApp}})
		if core.CodeOf(err) != core.CodeValidation {
			t.Errorf("username %q: want CodeValidation, got %v", name, core.CodeOf(err))
		}
	}
}

func TestRenderUserlist_DuplicateUsernameRefused(t *testing.T) {
	// pgbouncer honours only the first matching entry; a second is an ambiguous
	// mistake, not a silent override.
	_, err := RenderUserlist([]UserlistEntry{
		{Username: "appuser", Verifier: scramApp},
		{Username: "appuser", Verifier: scramOther},
	})
	if core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("duplicate username: want CodeValidation, got %v (err=%v)", core.CodeOf(err), err)
	}
}

func TestRenderUserlist_NoWriteOnError(t *testing.T) {
	// On any validation failure the function returns the empty string and writes
	// nothing — a partially-rendered auth_file must never reach the installer.
	out, err := RenderUserlist([]UserlistEntry{
		{Username: "good", Verifier: scramApp},
		{Username: "bad", Verifier: "plaintext"},
	})
	if err == nil {
		t.Fatal("expected an error on the bad entry")
	}
	if out != "" {
		t.Errorf("want empty output on error, got %q", out)
	}
}
