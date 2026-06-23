package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// secretChannels covers both credential-bearing kinds with distinctive
// sentinels in the secret fields the API masks (Pushover token, webhook URL).
func secretChannels() []alertChannelConfig {
	return []alertChannelConfig{
		{Kind: "pushover", Enabled: true, PushoverToken: "SENTINEL-pushover-token-DEADBEEF", PushoverUser: "ugroupkey"},
		{Kind: "webhook", Enabled: true, WebhookURL: "https://hooks.example/SENTINEL-webhook-secret-CAFEBABE"},
	}
}

func channelSecrets() []string {
	return []string{"SENTINEL-pushover-token-DEADBEEF", "SENTINEL-webhook-secret-CAFEBABE"}
}

func assertNoChannelSecrets(t *testing.T, where, out string) {
	t.Helper()
	for _, s := range channelSecrets() {
		if strings.Contains(out, s) {
			t.Errorf("%s leaked a secret %q in:\n%s", where, s, out)
		}
	}
}

func TestAlertChannelConfig_RendersRedacted(t *testing.T) {
	for _, c := range secretChannels() {
		out := c.String()
		assertNoChannelSecrets(t, "alertChannelConfig.String()", out)
		if !strings.Contains(out, core.RedactedMarker) {
			t.Errorf("String() should mark redacted secrets, got:\n%s", out)
		}
		if !strings.Contains(out, c.Kind) {
			t.Errorf("String() should keep non-secret kind, got:\n%s", out)
		}
	}
}

func TestAlertChannelConfig_FmtVerbsRedactSecrets(t *testing.T) {
	chans := secretChannels()
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		// Format the slice as a whole — fmt recurses into each element's String().
		assertNoChannelSecrets(t, "[]alertChannelConfig "+verb, fmt.Sprintf(verb, chans))
	}
	// PushoverUser is a non-secret key the mask keeps; it should stay visible.
	if !strings.Contains(chans[0].String(), "ugroupkey") {
		t.Errorf("String() should keep the non-secret PushoverUser, got:\n%s", chans[0].String())
	}
}

func TestAlertChannelConfig_StructuredLoggingRedactsSecrets(t *testing.T) {
	for _, h := range []struct {
		name string
		make func(*bytes.Buffer) slog.Handler
	}{
		{"text", func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }},
		{"json", func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }},
	} {
		for _, c := range secretChannels() {
			var buf bytes.Buffer
			log := core.FromSlog(slog.New(h.make(&buf)))
			log.Info("alert channel saved", "channel", c)
			assertNoChannelSecrets(t, "core.Logger "+h.name, buf.String())
			if !strings.Contains(buf.String(), core.RedactedMarker) {
				t.Errorf("%s logging should mark redacted secrets, got:\n%s", h.name, buf.String())
			}
		}
	}
}
