package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// secretAuth holds distinctive sentinels in both secret fields so any leak is
// greppable. PasswordHash is the Argon2id hash; SessionSecret is the raw HMAC
// signing key — the most sensitive bytes the panel stores.
func secretAuth() AuthRecord {
	return AuthRecord{
		PasswordHash:   "$argon2id$SENTINEL-pw-hash-DEADBEEF",
		SessionSecret:  []byte("SENTINEL-session-signing-secret-CAFEBABE"),
		FailedAttempts: 2,
	}
}

func authSecrets() []string {
	r := secretAuth()
	return []string{r.PasswordHash, string(r.SessionSecret)}
}

func assertNoAuthSecrets(t *testing.T, where, out string) {
	t.Helper()
	for _, s := range authSecrets() {
		if strings.Contains(out, s) {
			t.Errorf("%s leaked a secret %q in:\n%s", where, s, out)
		}
	}
}

func TestAuthRecord_StringRedactsSecrets(t *testing.T) {
	out := secretAuth().String()
	assertNoAuthSecrets(t, "AuthRecord.String()", out)
	if !strings.Contains(out, core.RedactedMarker) {
		t.Errorf("String() should mark redacted secrets, got:\n%s", out)
	}
	if !strings.Contains(out, "FailedAttempts:2") {
		t.Errorf("String() should keep non-secret state, got:\n%s", out)
	}
}

func TestAuthRecord_FmtVerbsRedactSecrets(t *testing.T) {
	r := secretAuth()
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		assertNoAuthSecrets(t, "AuthRecord "+verb, fmt.Sprintf(verb, r))
	}
}

func TestAuthRecord_StructuredLoggingRedactsSecrets(t *testing.T) {
	for _, h := range []struct {
		name string
		make func(*bytes.Buffer) slog.Handler
	}{
		{"text", func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }},
		{"json", func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }},
	} {
		var buf bytes.Buffer
		log := core.FromSlog(slog.New(h.make(&buf)))
		log.Info("loaded credential", "auth", secretAuth())
		assertNoAuthSecrets(t, "core.Logger "+h.name, buf.String())
		if !strings.Contains(buf.String(), core.RedactedMarker) {
			t.Errorf("%s logging should mark redacted secrets, got:\n%s", h.name, buf.String())
		}
	}
}
