package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// secretS3 builds a target whose two secret fields hold distinctive sentinels we
// can grep for in any rendered output.
func secretS3() S3Target {
	return S3Target{
		Endpoint:   "s3.us-west-002.backblazeb2.com",
		Region:     "us-west-002",
		Bucket:     "indiepg-backups",
		Prefix:     "inst-123",
		AccessKey:  "AKIAEXAMPLEID", // an id, not a secret — may appear
		SecretKey:  "SENTINEL-secret-key-DEADBEEF",
		CipherPass: "SENTINEL-cipher-pass-CAFEBABE",
		UseSSL:     true,
	}
}

// theSecrets are the substrings that must never appear in any log/fmt rendering.
func s3Secrets() []string {
	t := secretS3()
	return []string{t.SecretKey, t.CipherPass}
}

func assertNoSecrets(t *testing.T, where, out string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%s leaked a secret %q in:\n%s", where, s, out)
		}
	}
}

func TestS3Target_StringRedactsSecrets(t *testing.T) {
	tg := secretS3()
	out := tg.String()
	assertNoSecrets(t, "S3Target.String()", out, s3Secrets())
	if !strings.Contains(out, core.RedactedMarker) {
		t.Errorf("String() should mark redacted secrets, got:\n%s", out)
	}
	if !strings.Contains(out, tg.Bucket) {
		t.Errorf("String() should keep non-secret fields, got:\n%s", out)
	}
}

func TestS3Target_FmtVerbsRedactSecrets(t *testing.T) {
	tg := secretS3()
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		out := fmt.Sprintf(verb, tg)
		assertNoSecrets(t, "S3Target "+verb, out, s3Secrets())
	}
	// A Config containing the target by value must also redact when formatted
	// with %+v (fmt recurses into the Backup field and calls String()).
	cfg := Default()
	cfg.Backup = tg
	assertNoSecrets(t, "Config %+v", fmt.Sprintf("%+v", cfg), s3Secrets())
}

func TestS3Target_StructuredLoggingRedactsSecrets(t *testing.T) {
	for _, h := range []struct {
		name string
		make func(*bytes.Buffer) slog.Handler
	}{
		{"text", func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }},
		{"json", func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }},
	} {
		var buf bytes.Buffer
		log := core.FromSlog(slog.New(h.make(&buf)))
		tg := secretS3()
		log.Info("backup configured", "target", tg)
		assertNoSecrets(t, "core.Logger "+h.name, buf.String(), s3Secrets())
		if !strings.Contains(buf.String(), core.RedactedMarker) {
			t.Errorf("%s logging should mark redacted secrets, got:\n%s", h.name, buf.String())
		}
	}
}
