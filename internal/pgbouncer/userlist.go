package pgbouncer

import (
	"sort"
	"strings"
	"unicode"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// scramPrefix is the only password verifier kind indiepg will write into the
// pooler auth_file. PgBouncer can also accept plaintext or md5 entries, but
// those weaken the auth boundary (md5 is broken; plaintext stores the secret in
// the clear) — the project's security tie-break forbids them, so a verifier that
// is not a SCRAM-SHA-256 challenge is rejected, never downgraded.
const scramPrefix = "SCRAM-SHA-256$"

// UserlistEntry pairs a Postgres role with its SCRAM-SHA-256 verifier exactly as
// stored in pg_authid.rolpassword (a.k.a. pg_shadow.passwd). The verifier is the
// challenge material, not the password — it cannot be replayed to log in, but it
// is still secret-adjacent, which is why the auth_file is written 0640 owned by
// the pgbouncer user (the installer slice's concern, not this pure render's).
type UserlistEntry struct {
	Username string // Postgres role name
	Verifier string // pg_authid.rolpassword, must begin with scramPrefix
}

// RenderUserlist builds the pgbouncer auth_file (userlist.txt) text from entries.
// Each line is `"username" "verifier"` — pgbouncer's auth_file format. The output
// is deterministic (entries sorted by username) so an unchanged user set renders
// byte-identical and the enable flow can skip a needless reload.
//
// It is strict by design, because the auth_file is a security boundary:
//   - at least one entry is required — an empty userlist locks every app out of
//     the pooler, a silent footgun, so it is refused rather than written;
//   - every verifier must be a SCRAM-SHA-256 challenge (scramPrefix) made only of
//     the verifier's own alphabet — a plaintext/md5 verifier is rejected, never
//     written (no auth downgrade), and the charset check doubles as an injection
//     guard;
//   - usernames are rejected (not escaped) if they contain a double-quote,
//     control/line-break character, or whitespace — any of which could break out
//     of the quoted token and inject an extra auth entry. The app roles indiepg
//     manages never need such characters;
//   - duplicate usernames are refused — pgbouncer would silently honour only the
//     first, so a second entry for the same role is an ambiguous mistake.
//
// On any violation it returns a *core.Error (CodeValidation) and writes nothing.
func RenderUserlist(entries []UserlistEntry) (string, error) {
	if len(entries) == 0 {
		return "", core.ValidationError(
			"pgbouncer userlist needs at least one user: an empty auth_file would lock every app out of the pooler",
		)
	}

	seen := make(map[string]struct{}, len(entries))
	sorted := make([]UserlistEntry, 0, len(entries))
	for _, e := range entries {
		if err := validateUserlistName(e.Username); err != nil {
			return "", err
		}
		if err := validateScramVerifier(e.Username, e.Verifier); err != nil {
			return "", err
		}
		if _, dup := seen[e.Username]; dup {
			return "", core.ValidationError(
				"duplicate pgbouncer userlist entry for role %q: each role may appear once",
				e.Username,
			)
		}
		seen[e.Username] = struct{}{}
		sorted = append(sorted, e)
	}

	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Username < sorted[j].Username })

	var b strings.Builder
	for _, e := range sorted {
		b.WriteByte('"')
		b.WriteString(e.Username)
		b.WriteString("\" \"")
		b.WriteString(e.Verifier)
		b.WriteString("\"\n")
	}
	return b.String(), nil
}

// validateUserlistName rejects a role name that cannot be safely placed inside a
// double-quoted userlist token. A double-quote, whitespace, control character, or
// Unicode line separator could split the token and inject a second auth entry, so
// such names are refused outright rather than escaped — the app roles indiepg
// provisions never contain them. An empty name is also refused.
func validateUserlistName(name string) error {
	if name == "" {
		return core.ValidationError("empty username in pgbouncer userlist entry")
	}
	for i, r := range name {
		if r == '"' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return core.ValidationError(
				"invalid character in pgbouncer userlist username %q at offset %d: quotes, whitespace, and control characters are not allowed",
				name, i,
			).WithHint("auth_file usernames must be plain role names; such characters are rejected to prevent auth-entry injection")
		}
	}
	return nil
}

// validateScramVerifier requires a SCRAM-SHA-256 verifier and rejects anything
// else (no auth downgrade to md5/plaintext). Beyond the prefix it checks that the
// whole value is drawn from the verifier's own alphabet — uppercase/lowercase
// letters, digits, and the structural bytes '+', '/', '=', ':', '$', '-' that the
// `SCRAM-SHA-256$<iters>:<b64salt>$<b64storedkey>:<b64serverkey>` shape uses. That
// charset cannot contain a quote, whitespace, or newline, so it is also the
// injection guard for the second quoted token.
func validateScramVerifier(username, verifier string) error {
	if !strings.HasPrefix(verifier, scramPrefix) {
		return core.ValidationError(
			"role %q has a non-SCRAM password verifier: pgbouncer auth_file requires SCRAM-SHA-256 (md5/plaintext are refused, never downgraded)",
			username,
		).WithHint("store the role's password as SCRAM-SHA-256 in Postgres (the default since PG 14)")
	}
	for i, r := range verifier {
		if !isSCRAMVerifierRune(r) {
			return core.ValidationError(
				"invalid character in SCRAM verifier for role %q at offset %d: not part of a SCRAM-SHA-256 verifier",
				username, i,
			).WithHint("the verifier must be copied verbatim from pg_authid.rolpassword")
		}
	}
	return nil
}

func isSCRAMVerifierRune(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r == '+' || r == '/' || r == '=' || r == ':' || r == '$' || r == '-':
		return true
	default:
		return false
	}
}
