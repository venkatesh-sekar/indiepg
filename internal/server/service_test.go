package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderSystemdUnit(t *testing.T) {
	unit := renderSystemdUnit("/usr/local/bin/indiepg", "/var/lib/indiepg/indiepg.db")

	require.Contains(t, unit,
		`ExecStart="/usr/local/bin/indiepg" serve --state "/var/lib/indiepg/indiepg.db"`,
		"ExecStart must bake in the resolved binary and state paths, quoted for whitespace safety")
	require.Contains(t, unit, "[Install]")
	require.Contains(t, unit, "WantedBy=multi-user.target", "must be reboot-enabled")
	require.Contains(t, unit, "Restart=on-failure", "must self-heal on crash")
	require.NotContains(t, unit, "User=", "runs as root (manages Postgres + socket)")
}

func TestPanelURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8443":  "http://127.0.0.1:8443",  // loopback as-is
		"0.0.0.0:8080":    "http://localhost:8080",  // wildcard -> localhost
		":8443":           "http://localhost:8443",  // empty host -> localhost
		"[::]:8443":       "http://localhost:8443",  // IPv6 wildcard -> localhost
		"100.64.0.1:443":  "http://100.64.0.1:443",  // Tailscale-style private IP
		"[fe80::1]:8443":  "http://[fe80::1]:8443",  // IPv6 literal stays bracketed
		"not-a-host-port": "http://not-a-host-port", // best effort on odd input
	}
	for in, want := range cases {
		require.Equal(t, want, panelURL(in), "panelURL(%q)", in)
	}
}
