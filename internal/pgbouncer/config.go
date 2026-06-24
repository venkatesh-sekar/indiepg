package pgbouncer

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// ConfigMarker is the first line of every pgbouncer.ini indiepg owns. It is the
// proof-of-ownership the (future) installer checks before overwriting: a file
// that lacks it was written by the operator or the distro package and is NEVER
// clobbered, so a hand-rolled /etc/pgbouncer/pgbouncer.ini is respected. This
// mirrors how the pgBackRest config is guarded.
const ConfigMarker = "# managed by indiepg — regenerated from panel config; do not edit by hand"

// loopbackHost binds both the pooler's listener and its upstream Postgres
// connection to localhost. This is a deliberate, non-configurable security
// default: the pooler exists to be reached by apps on the SAME box, so it never
// listens on a public interface (widening it would be the least-secure option,
// which the project forbids). The [databases] line and listen_addr both use it.
const loopbackHost = "127.0.0.1"

// DefaultListenPort is the loopback port the managed pooler listens on, and
// LoopbackHost is the only host it ever binds. Both are exported so the panel can
// honestly show same-box apps where to reach the pooler; neither is ever widened
// to a public interface (the least-secure option, which the project forbids).
const (
	DefaultListenPort = defaultListenPort
	LoopbackHost      = loopbackHost
)

// Conventional defaults filled in when a ConfigParams field is left zero/empty.
const (
	defaultPGPort     = 5432                          // local Postgres
	defaultListenPort = 6432                          // the standard PgBouncer port
	defaultAuthFile   = "/etc/pgbouncer/userlist.txt" // SCRAM userlist
	defaultPidFile    = "/var/run/pgbouncer/pgbouncer.pid"
	defaultAdminUser  = "postgres" // admin/stats console user
)

// ConfigParams is the host-specific input to RenderConfig, paired with the pure
// host-sized Pool sizing from RecommendPool. Every field has a safe default, so
// a zero-valued ConfigParams (with only Pool set) renders a valid config. The
// string fields are treated as untrusted and validated against config injection
// before they are interpolated, because pgbouncer.ini takes a value literally to
// the end of its line — a newline in any value would otherwise inject an
// arbitrary option.
type ConfigParams struct {
	// Pool is the host-sized pool sizing (from RecommendPool). Its numeric
	// fields are rendered into the [pgbouncer] section.
	Pool PoolRecommendation

	PGPort     int // upstream Postgres port; default defaultPGPort
	ListenPort int // port the pooler listens on (loopback only); default defaultListenPort

	AuthFile  string // SCRAM userlist path; default defaultAuthFile
	PidFile   string // pid file path; default defaultPidFile
	AdminUser string // pgbouncer admin/stats console role; default defaultAdminUser
}

// RenderConfig builds the full pgbouncer.ini text from p. It fills safe defaults
// for any unset field, validates every interpolated value against config
// injection, and returns a *core.Error (CodeValidation) on a bad value. The
// result always begins with ConfigMarker so the installer can recognize a file
// it owns, and is deterministic for a given input (fixed line order) so an
// unchanged config renders byte-identical and a needless rewrite/reload can be
// skipped.
//
// The pooler is pinned to loopback (listen_addr and the upstream host are both
// 127.0.0.1) and to the fixed safe defaults — SCRAM auth, transaction pooling,
// DISCARD ALL on lease return — none of which are configurable here.
func RenderConfig(p ConfigParams) (string, error) {
	pgPort := p.PGPort
	if pgPort == 0 {
		pgPort = defaultPGPort
	}
	listenPort := p.ListenPort
	if listenPort == 0 {
		listenPort = defaultListenPort
	}
	authFile := strings.TrimSpace(p.AuthFile)
	if authFile == "" {
		authFile = defaultAuthFile
	}
	pidFile := strings.TrimSpace(p.PidFile)
	if pidFile == "" {
		pidFile = defaultPidFile
	}
	adminUser := strings.TrimSpace(p.AdminUser)
	if adminUser == "" {
		adminUser = defaultAdminUser
	}

	if err := validatePort("pg_port", pgPort); err != nil {
		return "", err
	}
	if err := validatePort("listen_port", listenPort); err != nil {
		return "", err
	}
	// Both sit on loopback; identical ports would make the pooler collide with
	// Postgres and never bind. Catch the misconfiguration at render time.
	if listenPort == pgPort {
		return "", core.ValidationError(
			"listen_port (%d) must differ from the Postgres port (%d): the pooler and Postgres cannot share a port on loopback",
			listenPort, pgPort,
		)
	}
	// The Pool numbers are normally produced by RecommendPool (always positive),
	// but ConfigParams.Pool is a plain struct any caller can zero-fill directly.
	// A rendered `server_idle_timeout = 0` would DISABLE the idle timeout (server
	// slots leak forever); a zero pool size is equally broken. Reject it rather
	// than emit a quietly-harmful config.
	if err := validatePool(p.Pool); err != nil {
		return "", err
	}
	if err := validateConfToken("auth_file", authFile); err != nil {
		return "", err
	}
	if err := validateConfToken("pidfile", pidFile); err != nil {
		return "", err
	}
	if err := validateConfToken("admin_users", adminUser); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(ConfigMarker)
	b.WriteByte('\n')
	b.WriteString("# Edit pool settings and the workload profile in the indiepg panel, not here.\n\n")

	// [databases]: a single catch-all route to the local Postgres over loopback.
	// Apps connect to the pooler with their own database name; pgbouncer maps any
	// database (*) to the same local server.
	b.WriteString("[databases]\n")
	b.WriteString("* = host=" + loopbackHost + " port=" + strconv.Itoa(pgPort) + "\n\n")

	// [pgbouncer]: listener + auth + pool sizing. Lines are emitted in a fixed
	// order so the output is byte-stable for a given input.
	b.WriteString("[pgbouncer]\n")
	writeKV(&b, "listen_addr", loopbackHost)
	writeKV(&b, "listen_port", strconv.Itoa(listenPort))
	writeKV(&b, "auth_type", AuthType)
	writeKV(&b, "auth_file", authFile)
	writeKV(&b, "pidfile", pidFile)
	writeKV(&b, "admin_users", adminUser)
	writeKV(&b, "stats_users", adminUser)
	writeKV(&b, "pool_mode", PoolMode)
	writeKV(&b, "server_reset_query", ServerResetQuery)
	writeKV(&b, "max_client_conn", strconv.Itoa(p.Pool.MaxClientConn))
	writeKV(&b, "default_pool_size", strconv.Itoa(p.Pool.DefaultPoolSize))
	writeKV(&b, "min_pool_size", strconv.Itoa(p.Pool.MinPoolSize))
	writeKV(&b, "reserve_pool_size", strconv.Itoa(p.Pool.ReservePoolSize))
	writeKV(&b, "server_idle_timeout", strconv.Itoa(p.Pool.ServerIdleTimeout))

	return b.String(), nil
}

// writeKV emits one `key = value` line in pgbouncer.ini's style.
func writeKV(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(" = ")
	b.WriteString(value)
	b.WriteByte('\n')
}

// HasManagedMarker reports whether existing config content was written by
// indiepg. The marker must be the FIRST line (matching what RenderConfig emits),
// not merely present somewhere — so an operator file that quotes the marker in a
// mid-file comment is still treated as foreign and never clobbered.
func HasManagedMarker(existing string) bool {
	return strings.HasPrefix(existing, ConfigMarker+"\n") || existing == ConfigMarker
}

// validatePool rejects a PoolRecommendation whose rendered numbers would be
// zero or negative. Every value here governs a connection limit or timeout; a
// zero pool size starves apps and a zero server_idle_timeout disables idle
// reclamation entirely. RecommendPool never produces such values, but a
// hand-built struct could, so the render refuses it.
func validatePool(p PoolRecommendation) error {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"default_pool_size", p.DefaultPoolSize},
		{"min_pool_size", p.MinPoolSize},
		{"reserve_pool_size", p.ReservePoolSize},
		{"max_client_conn", p.MaxClientConn},
		{"server_idle_timeout", p.ServerIdleTimeout},
	} {
		if f.v < 1 {
			return core.ValidationError(
				"invalid pool setting %s=%d: must be positive (build the pool via RecommendPool)",
				f.name, f.v,
			)
		}
	}
	return nil
}

// validatePort rejects a port outside the usable 1–65535 range.
func validatePort(field string, port int) error {
	if port < 1 || port > 65535 {
		return core.ValidationError("invalid %s %d: must be between 1 and 65535", field, port)
	}
	return nil
}

// validateConfToken rejects characters that could break an interpolated value
// out of its line: control characters (newlines, carriage returns, NUL, tabs),
// Unicode line separators, and the pgbouncer.ini comment starters '#' and ';'
// (an embedded ';' would silently truncate a value to a comment, e.g.
// `postgres ; admin = attacker`). None are legitimate in the paths/usernames we
// interpolate. Mirrors the pgBackRest config validator.
func validateConfToken(field, v string) error {
	for i, r := range v {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '#' || r == ';' {
			return core.ValidationError(
				"invalid character in pgbouncer setting %q at offset %d: control/line-break characters are not allowed",
				field, i,
			).WithHint("control and line-break characters are rejected to prevent config injection")
		}
	}
	return nil
}
