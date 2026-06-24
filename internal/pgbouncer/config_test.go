package pgbouncer

import (
	"strings"
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// parseINI is a tiny helper that pulls `key = value` pairs out of a rendered
// config, ignoring section headers, comments, and blank lines. It is sufficient
// for asserting on the [pgbouncer] settings (the only section with kv lines we
// care about here; the [databases] route is asserted separately).
func parseINI(t *testing.T, cfg string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func TestRenderConfig_DefaultsAndStructure(t *testing.T) {
	pool := RecommendPool(100, pg.ProfileMixed) // default 66/16/13/660/300
	cfg, err := RenderConfig(ConfigParams{Pool: pool})
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}

	// The managed marker MUST be the first line so the installer can recognize a
	// file it owns before overwriting.
	if !strings.HasPrefix(cfg, ConfigMarker+"\n") {
		t.Fatalf("config must start with the managed marker; got:\n%s", cfg)
	}

	// [databases] catch-all over loopback to the local Postgres.
	if !strings.Contains(cfg, "[databases]\n* = host=127.0.0.1 port=5432\n") {
		t.Errorf("missing loopback [databases] route; got:\n%s", cfg)
	}

	kv := parseINI(t, cfg)
	wantKV := map[string]string{
		// Hardened, non-configurable safe defaults.
		"auth_type":          "scram-sha-256",
		"pool_mode":          "transaction",
		"server_reset_query": "DISCARD ALL",
		// Loopback-only listener with conventional defaults.
		"listen_addr": "127.0.0.1",
		"listen_port": "6432",
		"auth_file":   "/etc/pgbouncer/userlist.txt",
		"pidfile":     "/var/run/pgbouncer/pgbouncer.pid",
		"admin_users": "postgres",
		"stats_users": "postgres",
		// Pool sizing carried straight from the recommendation.
		"max_client_conn":     "660",
		"default_pool_size":   "66",
		"min_pool_size":       "16",
		"reserve_pool_size":   "13",
		"server_idle_timeout": "300",
	}
	for k, want := range wantKV {
		if kv[k] != want {
			t.Errorf("setting %q = %q; want %q", k, kv[k], want)
		}
	}
}

// TestRenderConfig_NeverWidensOrWeakens locks the security invariants: the
// listener never binds beyond loopback and auth is never trust/plain, no matter
// the input.
func TestRenderConfig_NeverWidensOrWeakens(t *testing.T) {
	cfg, err := RenderConfig(ConfigParams{Pool: RecommendPool(50, pg.ProfileOLTP)})
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	// Note: "*" is intentionally excluded — it legitimately appears in the
	// [databases] catch-all route; a widened listener would show as listen_addr.
	for _, banned := range []string{"0.0.0.0", "::", "auth_type = trust", "auth_type = any", "auth_type = plain"} {
		if strings.Contains(cfg, banned) {
			t.Errorf("config must never contain %q (widened/weakened); got:\n%s", banned, cfg)
		}
	}
	if !strings.Contains(cfg, "listen_addr = 127.0.0.1\n") {
		t.Errorf("listen_addr must be pinned to loopback; got:\n%s", cfg)
	}
}

func TestRenderConfig_CustomParamsInterpolated(t *testing.T) {
	cfg, err := RenderConfig(ConfigParams{
		Pool:       RecommendPool(200, pg.ProfileOLAP),
		PGPort:     5433,
		ListenPort: 7000,
		AuthFile:   "/srv/pgb/users.txt",
		PidFile:    "/srv/pgb/pgb.pid",
		AdminUser:  "indiepg_admin",
	})
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(cfg, "* = host=127.0.0.1 port=5433\n") {
		t.Errorf("custom pg_port not interpolated into [databases]; got:\n%s", cfg)
	}
	kv := parseINI(t, cfg)
	for k, want := range map[string]string{
		"listen_port": "7000",
		"auth_file":   "/srv/pgb/users.txt",
		"pidfile":     "/srv/pgb/pgb.pid",
		"admin_users": "indiepg_admin",
		"stats_users": "indiepg_admin",
	} {
		if kv[k] != want {
			t.Errorf("setting %q = %q; want %q", k, kv[k], want)
		}
	}
}

// TestRenderConfig_Deterministic proves the same input renders byte-identical,
// so the installer can skip a needless rewrite + reload.
func TestRenderConfig_Deterministic(t *testing.T) {
	p := ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AdminUser: "postgres"}
	a, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	b, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if a != b {
		t.Errorf("render not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

// TestRenderConfig_RejectsInjection proves an attacker-controlled value cannot
// smuggle an extra option by embedding a newline (or other line breaker).
func TestRenderConfig_RejectsInjection(t *testing.T) {
	cases := []struct {
		name   string
		params ConfigParams
	}{
		{"newline in auth_file", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AuthFile: "/x\nadmin_users = attacker"}},
		{"newline in admin_user", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AdminUser: "postgres\nlisten_addr = 0.0.0.0"}},
		{"carriage return in pidfile", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), PidFile: "/x\rpid"}},
		{"line separator in auth_file", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AuthFile: "/x\u2028y"}},
		// ';' and '#' start a comment in pgbouncer.ini; an embedded one would
		// silently truncate a value (e.g. an admin user) to a comment.
		{"semicolon in admin_user", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AdminUser: "postgres ; admin_users = attacker"}},
		{"hash in admin_user", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), AdminUser: "postgres # x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderConfig(tc.params); err == nil {
				t.Fatalf("expected injection to be rejected, got nil error")
			}
		})
	}
}

func TestRenderConfig_RejectsBadPorts(t *testing.T) {
	cases := []struct {
		name   string
		params ConfigParams
	}{
		{"pg_port too high", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), PGPort: 70000}},
		{"listen_port negative", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), ListenPort: -1}},
		{"listen equals pg port (collision)", ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed), PGPort: 6000, ListenPort: 6000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderConfig(tc.params); err == nil {
				t.Fatalf("expected a validation error, got nil")
			}
		})
	}
}

// TestRenderConfig_RejectsBadPool proves a hand-built PoolRecommendation with a
// zero/negative field (bypassing RecommendPool) is refused rather than rendered
// into a config that disables limits — a 0 server_idle_timeout would leak server
// slots forever; a 0 pool size starves apps.
func TestRenderConfig_RejectsBadPool(t *testing.T) {
	good := RecommendPool(100, pg.ProfileMixed)
	cases := []struct {
		name string
		pool PoolRecommendation
	}{
		{"zero default_pool_size", PoolRecommendation{}},
		{"zero idle timeout", func() PoolRecommendation { p := good; p.ServerIdleTimeout = 0; return p }()},
		{"zero max_client_conn", func() PoolRecommendation { p := good; p.MaxClientConn = 0; return p }()},
		{"negative min_pool_size", func() PoolRecommendation { p := good; p.MinPoolSize = -1; return p }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderConfig(ConfigParams{Pool: tc.pool}); err == nil {
				t.Fatalf("expected a validation error for a bad pool, got nil")
			}
		})
	}
}

func TestHasManagedMarker(t *testing.T) {
	cfg, err := RenderConfig(ConfigParams{Pool: RecommendPool(100, pg.ProfileMixed)})
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !HasManagedMarker(cfg) {
		t.Error("rendered config should be recognized as managed")
	}
	if !HasManagedMarker(ConfigMarker) {
		t.Error("a bare marker should be recognized as managed")
	}
	// A foreign file that merely quotes the marker mid-file is NOT ours.
	foreign := "[pgbouncer]\nlisten_port = 6432\n" + ConfigMarker + "\n"
	if HasManagedMarker(foreign) {
		t.Error("foreign file with mid-file marker must not be treated as managed")
	}
	if HasManagedMarker("") {
		t.Error("empty content must not be treated as managed")
	}
}
