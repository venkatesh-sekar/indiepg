package pgbouncer

import (
	"testing"

	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

func TestRecommendPool_Table(t *testing.T) {
	cases := []struct {
		name    string
		maxConn int
		profile pg.WorkloadProfile
		want    PoolRecommendation
	}{
		{
			// mixed, 100 conns: available 95, 95*0.70 -> 66.
			name:    "mixed-100",
			maxConn: 100,
			profile: pg.ProfileMixed,
			want: PoolRecommendation{
				Profile: pg.ProfileMixed, PgMaxConnections: 100,
				DefaultPoolSize: 66, MinPoolSize: 16, ReservePoolSize: 13,
				MaxClientConn: 660, ServerIdleTimeout: 300,
			},
		},
		{
			// oltp, 100 conns: available 95, 95*0.80 -> 76, multiplex 20.
			name:    "oltp-100",
			maxConn: 100,
			profile: pg.ProfileOLTP,
			want: PoolRecommendation{
				Profile: pg.ProfileOLTP, PgMaxConnections: 100,
				DefaultPoolSize: 76, MinPoolSize: 19, ReservePoolSize: 15,
				MaxClientConn: 1520, ServerIdleTimeout: 300,
			},
		},
		{
			// olap, 83 conns: available 78, 78*0.60 -> 46, multiplex 5, idle 600.
			name:    "olap-83",
			maxConn: 83,
			profile: pg.ProfileOLAP,
			want: PoolRecommendation{
				Profile: pg.ProfileOLAP, PgMaxConnections: 83,
				DefaultPoolSize: 46, MinPoolSize: 11, ReservePoolSize: 9,
				MaxClientConn: 230, ServerIdleTimeout: 600,
			},
		},
		{
			// floor: tiny box (10 conns) clamps default_pool_size up to 20 and
			// both sub-pools to 5, so the pooler stays usable.
			name:    "floor-mixed-10",
			maxConn: 10,
			profile: pg.ProfileMixed,
			want: PoolRecommendation{
				Profile: pg.ProfileMixed, PgMaxConnections: 10,
				DefaultPoolSize: 20, MinPoolSize: 5, ReservePoolSize: 5,
				MaxClientConn: 200, ServerIdleTimeout: 300,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RecommendPool(tc.maxConn, tc.profile)
			if got != tc.want {
				t.Fatalf("RecommendPool(%d, %q) = %+v; want %+v",
					tc.maxConn, tc.profile, got, tc.want)
			}
		})
	}
}

// TestRecommendPool_Invariants asserts the safety properties that must hold for
// every realistic input, independent of the exact magic numbers above.
func TestRecommendPool_Invariants(t *testing.T) {
	// Include small boxes (1..20) so the floor-activation paths are exercised
	// directly — including maxConn <= reservedForAdmin where available clamps to
	// 0 — rather than relying on the architectural coupling to RecommendTuning's
	// max_connections >= 30 floor.
	for _, maxConn := range []int{1, 5, 10, 20, 30, 50, 100, 200, 300, 500} {
		for _, profile := range []pg.WorkloadProfile{pg.ProfileOLTP, pg.ProfileOLAP, pg.ProfileMixed} {
			p := RecommendPool(maxConn, profile)

			if p.DefaultPoolSize < defaultPoolFloor {
				t.Errorf("%q@%d: default_pool_size %d below floor %d", profile, maxConn, p.DefaultPoolSize, defaultPoolFloor)
			}
			if p.MinPoolSize < poolSizeFloor || p.ReservePoolSize < poolSizeFloor {
				t.Errorf("%q@%d: sub-pool below floor: min=%d reserve=%d", profile, maxConn, p.MinPoolSize, p.ReservePoolSize)
			}
			if p.MaxClientConn != p.DefaultPoolSize*multiplexRatio[profile] {
				t.Errorf("%q@%d: max_client_conn %d != default %d * multiplex %d",
					profile, maxConn, p.MaxClientConn, p.DefaultPoolSize, multiplexRatio[profile])
			}
			// The pool must never demand more server connections than Postgres
			// can give (available = max_connections - reserved), or the pooler
			// would itself saturate PG. Holds once the box is above the floor.
			if available := maxConn - reservedForAdmin; available >= defaultPoolFloor && p.DefaultPoolSize > available {
				t.Errorf("%q@%d: default_pool_size %d exceeds available %d", profile, maxConn, p.DefaultPoolSize, available)
			}
			if p.ServerIdleTimeout != idleTimeoutSeconds[profile] {
				t.Errorf("%q@%d: idle timeout %d != %d", profile, maxConn, p.ServerIdleTimeout, idleTimeoutSeconds[profile])
			}
		}
	}
}

// TestRecommendPool_DegenerateAndUnknown proves no panic and a valid result on
// nonsensical input: max_connections <= 0 is clamped, an unknown profile falls
// back to Mixed (never a silent zero-sized or mis-typed pool).
func TestRecommendPool_DegenerateAndUnknown(t *testing.T) {
	for _, maxConn := range []int{0, -1, -1000} {
		p := RecommendPool(maxConn, pg.ProfileMixed)
		if p.DefaultPoolSize != defaultPoolFloor || p.MinPoolSize != poolSizeFloor || p.ReservePoolSize != poolSizeFloor {
			t.Errorf("degenerate maxConn=%d did not clamp to floors: %+v", maxConn, p)
		}
		if p.PgMaxConnections != 1 {
			t.Errorf("degenerate maxConn=%d should clamp pg_max_connections to 1, got %d", maxConn, p.PgMaxConnections)
		}
	}

	unknown := RecommendPool(100, pg.WorkloadProfile("nonsense"))
	mixed := RecommendPool(100, pg.ProfileMixed)
	if unknown != mixed {
		t.Errorf("unknown profile should fall back to Mixed: got %+v, want %+v", unknown, mixed)
	}
}

func TestPoolRecommendation_SettingsMap(t *testing.T) {
	p := RecommendPool(100, pg.ProfileMixed)
	m := p.SettingsMap()

	// Fixed safe defaults must be present and exactly the hardened values.
	if m["auth_type"] != "scram-sha-256" {
		t.Errorf("auth_type = %q; want scram-sha-256 (never trust/plain)", m["auth_type"])
	}
	if m["pool_mode"] != "transaction" {
		t.Errorf("pool_mode = %q; want transaction", m["pool_mode"])
	}
	if m["server_reset_query"] != "DISCARD ALL" {
		t.Errorf("server_reset_query = %q; want DISCARD ALL", m["server_reset_query"])
	}

	// Computed values must be rendered as their integer strings.
	want := map[string]string{
		"default_pool_size":   "66",
		"min_pool_size":       "16",
		"reserve_pool_size":   "13",
		"max_client_conn":     "660",
		"server_idle_timeout": "300",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("SettingsMap[%q] = %q; want %q", k, m[k], v)
		}
	}
}
