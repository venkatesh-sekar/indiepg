// Package pgbouncer computes host-sized PgBouncer pool settings, ported from the
// `sm` CLI (see scripts/ralph/DEFAULTS.md). PgBouncer is an OPT-IN pooler: it is
// off by default and only relevant when an indie hacker's app exhausts
// Postgres' own connection slots. This package holds the pure sizing math; it
// installs, configures, and applies nothing.
package pgbouncer

import (
	"fmt"

	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// Fixed safe defaults for the pooler — never computed, never weakened. SCRAM
// only (reject trust/plain); transaction pooling (the most efficient default,
// matching DEFAULTS.md); DISCARD ALL between leases so no session state leaks
// across apps sharing a pooled server connection.
const (
	AuthType         = "scram-sha-256"
	PoolMode         = "transaction"
	ServerResetQuery = "DISCARD ALL"

	// reservedForAdmin keeps a few Postgres connection slots free for the
	// operator/superuser (and the panel itself) so a saturated pool can never
	// lock the box's owner out. Matches `sm` RESERVED_FOR_ADMIN.
	reservedForAdmin = 5

	// defaultPoolFloor / poolSizeFloor keep a tiny box's pool usable rather
	// than collapsing to zero ready connections. Matches `sm`.
	defaultPoolFloor = 20
	poolSizeFloor    = 5
)

// poolUtilization is the fraction of Postgres' available (non-reserved)
// connections a single app pool may consume. OLTP packs more (short txns free
// slots fast); OLAP holds connections longer so it reserves fewer.
var poolUtilization = map[pg.WorkloadProfile]float64{
	pg.ProfileOLTP:  0.80,
	pg.ProfileOLAP:  0.60,
	pg.ProfileMixed: 0.70,
}

// multiplexRatio sets max_client_conn = default_pool_size × ratio: how many
// client connections the pooler accepts per pooled server connection. Short
// OLTP transactions multiplex heavily; long OLAP queries barely at all.
var multiplexRatio = map[pg.WorkloadProfile]int{
	pg.ProfileOLTP:  20,
	pg.ProfileOLAP:  5,
	pg.ProfileMixed: 10,
}

// idleTimeoutSeconds closes server connections idle this long. OLAP keeps them
// longer (expensive to re-establish for heavy work); OLTP/Mixed reclaim sooner.
var idleTimeoutSeconds = map[pg.WorkloadProfile]int{
	pg.ProfileOLTP:  300,
	pg.ProfileOLAP:  600,
	pg.ProfileMixed: 300,
}

// PoolRecommendation is the host-sized set of core PgBouncer pool settings
// derived from Postgres' max_connections and the workload profile. It is a pure
// value: computing it touches no host, no Postgres, and applies nothing.
type PoolRecommendation struct {
	Profile          pg.WorkloadProfile `json:"profile"`
	PgMaxConnections int                `json:"pg_max_connections"`

	DefaultPoolSize   int `json:"default_pool_size"`   // pooled server conns per database
	MinPoolSize       int `json:"min_pool_size"`       // kept warm
	ReservePoolSize   int `json:"reserve_pool_size"`   // burst overflow
	MaxClientConn     int `json:"max_client_conn"`     // client conns the pooler accepts
	ServerIdleTimeout int `json:"server_idle_timeout"` // seconds
}

// RecommendPool computes host-sized PgBouncer pool settings from Postgres'
// max_connections and a workload profile, mirroring the `sm` CLI pool math (see
// scripts/ralph/DEFAULTS.md). It is pure and deterministic — sizing input in,
// recommendation out. An unrecognised profile falls back to Mixed and a
// nonsensical max_connections is clamped, so the result is always valid and
// panic-free.
//
// The sizing assumes it is paired with Postgres sized by RecommendTuning, whose
// max_connections floors at 30, so the defaultPoolFloor (20) never exceeds the
// box's real capacity in practice.
func RecommendPool(pgMaxConnections int, profile pg.WorkloadProfile) PoolRecommendation {
	if _, ok := poolUtilization[profile]; !ok {
		profile = pg.ProfileMixed
	}
	if pgMaxConnections < 1 {
		pgMaxConnections = 1
	}

	available := pgMaxConnections - reservedForAdmin
	if available < 0 {
		available = 0
	}

	// default_pool_size: a profile-dependent slice of the available slots,
	// truncated (matching `sm`'s int()), floored so a small box stays usable.
	defaultPool := int(float64(available) * poolUtilization[profile])
	if defaultPool < defaultPoolFloor {
		defaultPool = defaultPoolFloor
	}

	minPool := defaultPool / 4 // 25% kept warm
	if minPool < poolSizeFloor {
		minPool = poolSizeFloor
	}

	reservePool := defaultPool / 5 // 20% burst overflow
	if reservePool < poolSizeFloor {
		reservePool = poolSizeFloor
	}

	maxClient := defaultPool * multiplexRatio[profile]

	return PoolRecommendation{
		Profile:           profile,
		PgMaxConnections:  pgMaxConnections,
		DefaultPoolSize:   defaultPool,
		MinPoolSize:       minPool,
		ReservePoolSize:   reservePool,
		MaxClientConn:     maxClient,
		ServerIdleTimeout: idleTimeoutSeconds[profile],
	}
}

// SettingsMap renders the recommendation as pgbouncer.ini key=value pairs,
// including the fixed safe defaults, suitable for a read-only preview/audit
// surface. It applies nothing.
func (p PoolRecommendation) SettingsMap() map[string]string {
	return map[string]string{
		"default_pool_size":   fmt.Sprintf("%d", p.DefaultPoolSize),
		"min_pool_size":       fmt.Sprintf("%d", p.MinPoolSize),
		"reserve_pool_size":   fmt.Sprintf("%d", p.ReservePoolSize),
		"max_client_conn":     fmt.Sprintf("%d", p.MaxClientConn),
		"server_idle_timeout": fmt.Sprintf("%d", p.ServerIdleTimeout),
		"pool_mode":           PoolMode,
		"auth_type":           AuthType,
		"server_reset_query":  ServerResetQuery,
	}
}
