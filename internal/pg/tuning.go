package pg

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// WorkloadProfile selects the memory/connection balance Postgres is sized for.
// The default is Mixed: a balanced general-purpose profile that suits the
// typical indie-hacker box running a web app plus the odd report.
type WorkloadProfile string

const (
	// ProfileOLTP favours many short, concurrent transactions (web apps).
	ProfileOLTP WorkloadProfile = "oltp"
	// ProfileOLAP favours fewer, heavier analytical queries (reporting).
	ProfileOLAP WorkloadProfile = "olap"
	// ProfileMixed is the balanced default.
	ProfileMixed WorkloadProfile = "mixed"
)

// ParseWorkloadProfile resolves an operator-supplied profile string. An empty
// string is the best-default (Mixed); a recognised value (case-insensitive) is
// returned as-is; anything else is a validation error rather than a silent
// fallback, so a typo can't quietly mis-size the box.
func ParseWorkloadProfile(s string) (WorkloadProfile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return ProfileMixed, nil
	case "oltp":
		return ProfileOLTP, nil
	case "olap":
		return ProfileOLAP, nil
	case "mixed":
		return ProfileMixed, nil
	default:
		return "", core.ValidationError(
			"unknown workload profile %q (want oltp, olap, or mixed)", s)
	}
}

// TuningRecommendation is the host-sized set of core Postgres settings computed
// from detected RAM/CPU. Memory figures are in megabytes. It is a pure value —
// computing it touches no Postgres and applies nothing.
type TuningRecommendation struct {
	Profile  WorkloadProfile `json:"profile"`
	MemoryMB int64           `json:"memory_mb"`
	CPUCount int             `json:"cpu_count"`

	SharedBuffersMB      int64 `json:"shared_buffers_mb"`       // restart required
	EffectiveCacheMB     int64 `json:"effective_cache_size_mb"` // reloadable
	WorkMemMB            int64 `json:"work_mem_mb"`             // reloadable
	MaintenanceWorkMemMB int64 `json:"maintenance_work_mem_mb"` // reloadable
	MaxConnections       int   `json:"max_connections"`         // restart required
}

// sharedBuffersPct is the fraction of RAM given to shared_buffers per profile.
// OLAP caches more aggressively; OLTP leaves more headroom for many backends.
var sharedBuffersPct = map[WorkloadProfile]float64{
	ProfileOLTP:  0.25,
	ProfileOLAP:  0.40,
	ProfileMixed: 0.30,
}

// perConnOverheadMB estimates the working memory a single backend consumes,
// used to bound max_connections so the box can't be oversubscribed.
var perConnOverheadMB = map[WorkloadProfile]int64{
	ProfileOLTP:  5,
	ProfileOLAP:  15,
	ProfileMixed: 8,
}

// maxConnBounds clamps the computed max_connections into a sane per-profile
// range (floor keeps a small box usable; ceiling stops a huge box from sizing
// to thousands of backends it shouldn't actually run without a pooler).
var maxConnBounds = map[WorkloadProfile][2]int{
	ProfileOLTP:  {100, 500},
	ProfileOLAP:  {30, 100},
	ProfileMixed: {50, 300},
}

// RecommendTuning computes host-sized core Postgres settings from RAM (in MB),
// CPU count, and a workload profile, mirroring the `sm` CLI tuning math (see
// scripts/ralph/DEFAULTS.md). It is pure and deterministic: no host detection,
// no Postgres, no side effects — sizing input in, recommendation out. An
// unrecognised profile falls back to Mixed so the result is always valid.
func RecommendTuning(memoryMB int64, cpuCount int, profile WorkloadProfile) TuningRecommendation {
	if _, ok := sharedBuffersPct[profile]; !ok {
		profile = ProfileMixed
	}
	if memoryMB < 0 {
		memoryMB = 0
	}
	if cpuCount < 1 {
		cpuCount = 1
	}

	mem := float64(memoryMB)

	// shared_buffers: profile-dependent slice of RAM (restart required).
	sharedBuffers := int64(mem * sharedBuffersPct[profile])

	// effective_cache_size: 75% of RAM — the planner's estimate of OS cache.
	effectiveCache := int64(mem * 0.75)

	// work_mem: per-operation sort/hash memory. OLAP scales with RAM (heavier
	// sorts) but is bounded; OLTP/Mixed use fixed, conservative amounts so many
	// concurrent backends can't collectively exhaust RAM.
	var workMem int64
	switch profile {
	case ProfileOLTP:
		workMem = 64
	case ProfileOLAP:
		workMem = clampInt64(memoryMB/32, 128, 1024)
	default: // mixed
		workMem = 128
	}

	// maintenance_work_mem: for VACUUM / CREATE INDEX. OLAP gets more headroom.
	var maintMem int64
	if profile == ProfileOLAP {
		maintMem = min64(memoryMB/4, 4096)
	} else {
		maintMem = min64(memoryMB/8, 2048)
	}

	// max_connections: size from the RAM left after shared_buffers, spending
	// half of it on backends at the per-connection overhead, then clamp to the
	// profile's range (restart required).
	availableForConns := mem * (1 - sharedBuffersPct[profile]) * 0.5
	rawConns := int(availableForConns / float64(perConnOverheadMB[profile]))
	bounds := maxConnBounds[profile]
	maxConns := clampInt(rawConns, bounds[0], bounds[1])

	return TuningRecommendation{
		Profile:              profile,
		MemoryMB:             memoryMB,
		CPUCount:             cpuCount,
		SharedBuffersMB:      sharedBuffers,
		EffectiveCacheMB:     effectiveCache,
		WorkMemMB:            workMem,
		MaintenanceWorkMemMB: maintMem,
		MaxConnections:       maxConns,
	}
}

// SettingsMap renders the recommendation as postgresql.conf-style name=value
// pairs (e.g. shared_buffers="2457MB", max_connections="300"), suitable for an
// audit/preview surface. It does not apply anything.
func (t TuningRecommendation) SettingsMap() map[string]string {
	return map[string]string{
		"shared_buffers":       fmt.Sprintf("%dMB", t.SharedBuffersMB),
		"effective_cache_size": fmt.Sprintf("%dMB", t.EffectiveCacheMB),
		"work_mem":             fmt.Sprintf("%dMB", t.WorkMemMB),
		"maintenance_work_mem": fmt.Sprintf("%dMB", t.MaintenanceWorkMemMB),
		"max_connections":      fmt.Sprintf("%d", t.MaxConnections),
	}
}

// detectHostTuning detects this host's RAM and CPU and returns the recommended
// tuning for the given profile. When memory can't be read it falls back to a
// conservative 4GB assumption (matching `sm`) so the recommendation is always
// produced; ok reports whether detection succeeded.
func detectHostTuning(profile WorkloadProfile) (rec TuningRecommendation, ok bool) {
	const fallbackMemMB = 4096
	memMB := int64(fallbackMemMB)
	totalBytes, _, memOK := readMemInfo()
	if memOK && totalBytes > 0 {
		memMB = totalBytes / (1024 * 1024)
	}
	return RecommendTuning(memMB, runtime.NumCPU(), profile), memOK
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
