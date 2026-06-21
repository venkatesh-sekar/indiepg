package backup

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// configMarker is the first line of every pgBackRest config file indiepg owns.
// It is the proof-of-ownership the provisioner checks before overwriting: a file
// that lacks it was written by the operator (or the package) and is NEVER
// clobbered, so a hand-rolled /etc/pgbackrest/pgbackrest.conf is respected.
const configMarker = "# managed by indiepg — regenerated from panel config; do not edit by hand"

// defaultLocalRepoPath is the on-disk repo path used when no S3 bucket is
// configured (an explicitly local-only repository).
const defaultLocalRepoPath = "/var/lib/pgbackrest"

// ConfigParams is the full input to RenderConfig. Every string field is treated
// as untrusted and validated against control-character/newline injection before
// it is written, because pgBackRest's INI format takes a value literally to the
// end of its line — a newline in any value would otherwise inject an arbitrary
// option (e.g. a forged repo1-s3-key-secret).
type ConfigParams struct {
	Stanza string

	// S3 destination. A non-empty Bucket selects an S3 repo; otherwise the repo
	// is local (posix) at defaultLocalRepoPath.
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string // path within the bucket; normalized to a leading "/"
	AccessKey string
	SecretKey string
	UseSSL    bool
	URIStyle  string // "host" (default, AWS/B2/R2) or "path" (MinIO-style)

	// RetentionDays drives time-based full-backup retention. Zero keeps all.
	RetentionDays int

	// CipherPass, when non-empty, enables aes-256-cbc repository encryption. It
	// is a secret and must be preserved verbatim to ever restore the repo.
	CipherPass string

	// Postgres locators for the stanza section.
	PGDataDir   string
	PGPort      string
	PGSocketDir string
}

// RemoteConfigured reports whether an S3 destination is set (bucket or
// endpoint). It mirrors the Manager's notion of a remote repo.
func (p ConfigParams) RemoteConfigured() bool {
	return strings.TrimSpace(p.Bucket) != "" || strings.TrimSpace(p.Endpoint) != ""
}

// RenderConfig builds the full /etc/pgbackrest/pgbackrest.conf text from p. It
// validates every interpolated value against config injection and returns a
// *core.Error (CodeValidation) on a bad value; the result always begins with
// configMarker so the provisioner can recognize a file it owns.
//
// The output is deterministic for a given input (stable key order), so an
// unchanged config renders byte-identical and the provisioner can skip a
// needless rewrite + stanza-create.
func RenderConfig(p ConfigParams) (string, error) {
	if err := validateStanza(p.Stanza); err != nil {
		return "", err
	}

	// Trim surrounding whitespace from the structural S3 locators so a stray
	// leading/trailing space pasted into the panel can't silently point at the
	// wrong endpoint/bucket. Secrets (SecretKey, CipherPass) are NOT trimmed — a
	// credential may legitimately include edge whitespace — they are only
	// rejected for embedded control characters by validateConfToken.
	endpoint := strings.TrimSpace(p.Endpoint)
	region := strings.TrimSpace(p.Region)
	bucket := strings.TrimSpace(p.Bucket)
	accessKey := strings.TrimSpace(p.AccessKey)

	var b strings.Builder
	b.WriteString(configMarker)
	b.WriteByte('\n')
	b.WriteString("# Edit S3 settings and retention in the indiepg panel, not here.\n\n")

	// [global] — repo definition shared by every stanza.
	global := newSection()
	if bucket != "" {
		global.set("repo1-type", "s3")
		global.set("repo1-path", normalizeRepoPath(p.Prefix))
		global.set("repo1-s3-bucket", bucket)
		global.set("repo1-s3-endpoint", endpoint)
		global.set("repo1-s3-region", region)
		global.set("repo1-s3-key", accessKey)
		global.set("repo1-s3-key-secret", p.SecretKey)
		global.set("repo1-s3-uri-style", normalizeURIStyle(p.URIStyle))
		if !p.UseSSL {
			// Only meaningful for plaintext endpoints (e.g. a local MinIO). The
			// default posture is TLS, so we never silently weaken it.
			global.set("repo1-storage-verify-tls", "n")
		}
	} else {
		global.set("repo1-type", "posix")
		global.set("repo1-path", defaultLocalRepoPath)
	}

	if p.RetentionDays > 0 {
		global.set("repo1-retention-full-type", "time")
		global.set("repo1-retention-full", strconv.Itoa(p.RetentionDays))
	}

	if p.CipherPass != "" {
		global.set("repo1-cipher-type", "aes-256-cbc")
		global.set("repo1-cipher-pass", p.CipherPass)
	} else {
		global.set("repo1-cipher-type", "none")
	}

	// Conservative, widely-compatible operational defaults.
	global.set("start-fast", "y")
	global.set("compress-type", "gz")
	global.set("log-level-console", "info")
	global.set("log-level-file", "detail")

	// [stanza] — the managed Postgres cluster.
	stanza := newSection()
	stanza.set("pg1-path", p.PGDataDir)
	if p.PGPort != "" {
		stanza.set("pg1-port", p.PGPort)
	}
	if p.PGSocketDir != "" {
		stanza.set("pg1-socket-path", p.PGSocketDir)
	}

	if err := global.validate("global"); err != nil {
		return "", err
	}
	if err := stanza.validate(p.Stanza); err != nil {
		return "", err
	}

	b.WriteString("[global]\n")
	b.WriteString(global.render())
	b.WriteByte('\n')
	b.WriteString("[" + p.Stanza + "]\n")
	b.WriteString(stanza.render())

	return b.String(), nil
}

// normalizeRepoPath ensures the in-bucket repo path has exactly one leading
// slash and no trailing slash (pgBackRest treats it as an absolute key prefix).
func normalizeRepoPath(prefix string) string {
	p := strings.Trim(strings.TrimSpace(prefix), "/")
	if p == "" {
		return "/"
	}
	return "/" + p
}

// normalizeURIStyle defaults to host-style addressing (AWS S3, Backblaze B2,
// Cloudflare R2). Only an explicit "path" selects path-style (MinIO).
func normalizeURIStyle(style string) string {
	if strings.EqualFold(strings.TrimSpace(style), "path") {
		return "path"
	}
	return "host"
}

// section accumulates key=value options preserving insertion order, and
// validates every value against config injection at render time.
type section struct {
	keys   []string
	values map[string]string
}

func newSection() *section {
	return &section{values: make(map[string]string)}
}

func (s *section) set(key, value string) {
	if _, ok := s.values[key]; !ok {
		s.keys = append(s.keys, key)
	}
	s.values[key] = value
}

// validate rejects any key or value containing a character that could break out
// of its config line. sectionName is used only for the error message.
func (s *section) validate(sectionName string) error {
	// Stable order so an error is deterministic regardless of map iteration.
	keys := append([]string(nil), s.keys...)
	sort.Strings(keys)
	for _, k := range keys {
		if err := validateConfToken(sectionName, k); err != nil {
			return err
		}
		if err := validateConfToken(sectionName+"."+k, s.values[k]); err != nil {
			return err
		}
	}
	return nil
}

func (s *section) render() string {
	var b strings.Builder
	for _, k := range s.keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.values[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// validateConfToken rejects control characters (including newlines, carriage
// returns, NUL, and tabs) in a config key or value. These are the only
// characters that could let an attacker-controlled setting (e.g. an S3 secret
// pasted from a compromised source) inject an extra pgBackRest option or split a
// line. Empty is allowed for optional values; the renderer omits empty optionals
// it cares about before validation.
func validateConfToken(field, v string) error {
	for i, r := range v {
		// unicode.IsControl covers C0 (<0x20), DEL (0x7f), and C1 (0x80–0x9f,
		// including NEL 0x85). We additionally reject the Unicode line/paragraph
		// separators (U+2028/U+2029), which are not "control" but are line breaks
		// to some parsers — so no rune can split a config line or smuggle an option.
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return core.ValidationError(
				"invalid character in pgBackRest setting %q at offset %d: control/line-break characters are not allowed",
				field, i,
			).WithHint(fmt.Sprintf("U+%04X is rejected to prevent config injection", r))
		}
	}
	return nil
}

// HasManagedMarker reports whether existing config content was written by
// indiepg. The marker must be the FIRST line (matching what RenderConfig emits),
// not merely present somewhere — so an operator file that happens to quote the
// marker in a mid-file comment is still treated as foreign and never clobbered.
func HasManagedMarker(existing string) bool {
	return strings.HasPrefix(existing, configMarker+"\n") || existing == configMarker
}
