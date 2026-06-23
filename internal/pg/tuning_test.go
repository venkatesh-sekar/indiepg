package pg

import (
	"testing"
)

func TestRecommendTuning_SampleRAMValues(t *testing.T) {
	// Expected values computed by hand from the sm tuning math (DEFAULTS.md):
	//   shared_buffers       = pct(profile) * RAM     (oltp .25 / mixed .30 / olap .40)
	//   effective_cache_size = 0.75 * RAM
	//   work_mem             = oltp 64 / mixed 128 / olap clamp(RAM/32, 128, 1024)
	//   maintenance_work_mem = olap min(RAM/4, 4096) / else min(RAM/8, 2048)
	//   max_connections      = clamp(int(RAM*(1-pct)*0.5 / perConn), bounds)
	tests := []struct {
		name     string
		memMB    int64
		cpu      int
		profile  WorkloadProfile
		shared   int64
		effCache int64
		workMem  int64
		maintMem int64
		maxConns int
	}{
		// --- Mixed (default) across a spread of box sizes ---
		{"mixed-1GB", 1024, 1, ProfileMixed, 307, 768, 128, 128, 50},     // conns clamp up to floor 50
		{"mixed-2GB", 2048, 2, ProfileMixed, 614, 1536, 128, 256, 89},    // conns: 2048*(1-.30)*.5/8=89.6 -> 89, in-range
		{"mixed-8GB", 8192, 4, ProfileMixed, 2457, 6144, 128, 1024, 300}, // conns clamp down to ceil 300
		{"mixed-32GB", 32768, 8, ProfileMixed, 9830, 24576, 128, 2048, 300},
		// --- OLTP: smaller buffers, more backends ---
		{"oltp-8GB", 8192, 4, ProfileOLTP, 2048, 6144, 64, 1024, 500}, // conns: 8192*(1-.25)*.5/5=614.4 -> ceil 500
		{"oltp-2GB", 2048, 2, ProfileOLTP, 512, 1536, 64, 256, 153},   // 2048*.375/5=153.6 -> 153
		// --- OLAP: bigger buffers/work_mem, fewer backends ---
		{"olap-2GB", 2048, 2, ProfileOLAP, 819, 1536, 128, 512, 40},            // work_mem floor 128; conns 614/15=40
		{"olap-8GB", 8192, 4, ProfileOLAP, 3276, 6144, 256, 2048, 100},         // work_mem 256; conns clamp ceil 100
		{"olap-128GB", 131072, 16, ProfileOLAP, 52428, 98304, 1024, 4096, 100}, // work_mem ceil 1024; maint ceil 4096
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecommendTuning(tt.memMB, tt.cpu, tt.profile)
			if got.SharedBuffersMB != tt.shared {
				t.Errorf("shared_buffers = %d MB, want %d", got.SharedBuffersMB, tt.shared)
			}
			if got.EffectiveCacheMB != tt.effCache {
				t.Errorf("effective_cache_size = %d MB, want %d", got.EffectiveCacheMB, tt.effCache)
			}
			if got.WorkMemMB != tt.workMem {
				t.Errorf("work_mem = %d MB, want %d", got.WorkMemMB, tt.workMem)
			}
			if got.MaintenanceWorkMemMB != tt.maintMem {
				t.Errorf("maintenance_work_mem = %d MB, want %d", got.MaintenanceWorkMemMB, tt.maintMem)
			}
			if got.MaxConnections != tt.maxConns {
				t.Errorf("max_connections = %d, want %d", got.MaxConnections, tt.maxConns)
			}
			// Echo-back fields.
			if got.Profile != tt.profile || got.MemoryMB != tt.memMB || got.CPUCount != tt.cpu {
				t.Errorf("echo fields = {%s %d %d}, want {%s %d %d}",
					got.Profile, got.MemoryMB, got.CPUCount, tt.profile, tt.memMB, tt.cpu)
			}
		})
	}
}

func TestRecommendTuning_SharedBuffersOrderedByProfile(t *testing.T) {
	// For the same box, OLTP < Mixed < OLAP shared_buffers (the pct ordering),
	// and effective_cache_size is profile-independent (always 75%).
	const memMB = 16384
	oltp := RecommendTuning(memMB, 4, ProfileOLTP)
	mixed := RecommendTuning(memMB, 4, ProfileMixed)
	olap := RecommendTuning(memMB, 4, ProfileOLAP)

	if !(oltp.SharedBuffersMB < mixed.SharedBuffersMB && mixed.SharedBuffersMB < olap.SharedBuffersMB) {
		t.Errorf("shared_buffers not ordered oltp<mixed<olap: %d %d %d",
			oltp.SharedBuffersMB, mixed.SharedBuffersMB, olap.SharedBuffersMB)
	}
	if oltp.EffectiveCacheMB != mixed.EffectiveCacheMB || mixed.EffectiveCacheMB != olap.EffectiveCacheMB {
		t.Errorf("effective_cache_size should be profile-independent: %d %d %d",
			oltp.EffectiveCacheMB, mixed.EffectiveCacheMB, olap.EffectiveCacheMB)
	}
}

func TestRecommendTuning_MaxConnectionsClampedToBounds(t *testing.T) {
	// A tiny box clamps up to the floor; a huge box clamps down to the ceiling.
	for _, p := range []WorkloadProfile{ProfileOLTP, ProfileOLAP, ProfileMixed} {
		bounds := maxConnBounds[p]
		tiny := RecommendTuning(256, 1, p)
		if tiny.MaxConnections != bounds[0] {
			t.Errorf("%s tiny box max_connections = %d, want floor %d", p, tiny.MaxConnections, bounds[0])
		}
		huge := RecommendTuning(1<<20, 64, p) // 1 TB
		if huge.MaxConnections != bounds[1] {
			t.Errorf("%s huge box max_connections = %d, want ceil %d", p, huge.MaxConnections, bounds[1])
		}
	}
}

func TestRecommendTuning_DegenerateInputs(t *testing.T) {
	// Zero/negative RAM and CPU must not panic or produce nonsense: memory
	// figures collapse to 0/clamped, connections to the profile floor, CPU >= 1.
	got := RecommendTuning(-100, 0, ProfileMixed)
	if got.CPUCount != 1 {
		t.Errorf("cpu floored to %d, want 1", got.CPUCount)
	}
	if got.MaxConnections != maxConnBounds[ProfileMixed][0] {
		t.Errorf("max_connections = %d, want floor %d", got.MaxConnections, maxConnBounds[ProfileMixed][0])
	}
	if got.SharedBuffersMB != 0 || got.EffectiveCacheMB != 0 {
		t.Errorf("memory sizing should be 0 on non-positive RAM, got shared=%d cache=%d",
			got.SharedBuffersMB, got.EffectiveCacheMB)
	}

	// An unrecognised profile falls back to Mixed rather than zero-valued maps.
	fallback := RecommendTuning(8192, 4, WorkloadProfile("bogus"))
	if fallback.Profile != ProfileMixed {
		t.Errorf("unknown profile = %q, want fallback mixed", fallback.Profile)
	}
}

func TestParseWorkloadProfile(t *testing.T) {
	tests := []struct {
		in      string
		want    WorkloadProfile
		wantErr bool
	}{
		{"", ProfileMixed, false}, // default
		{"mixed", ProfileMixed, false},
		{"OLTP", ProfileOLTP, false}, // case-insensitive
		{" olap ", ProfileOLAP, false},
		{"nosql", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseWorkloadProfile(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseWorkloadProfile(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWorkloadProfile(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseWorkloadProfile(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTuningRecommendation_SettingsMap(t *testing.T) {
	rec := RecommendTuning(8192, 4, ProfileMixed)
	m := rec.SettingsMap()
	want := map[string]string{
		"shared_buffers":       "2457MB",
		"effective_cache_size": "6144MB",
		"work_mem":             "128MB",
		"maintenance_work_mem": "1024MB",
		"max_connections":      "300",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("SettingsMap[%q] = %q, want %q", k, m[k], v)
		}
	}
}
