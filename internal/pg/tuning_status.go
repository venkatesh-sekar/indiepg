package pg

import "context"

// AppliedTuning is the live value of each host-sized setting currently in force,
// read from pg_settings and rendered in whole megabytes (max_connections is a
// plain count). It is the authoritative "what Postgres is actually running"
// figure, as opposed to the recommendation, which is "what this box should run".
type AppliedTuning struct {
	SharedBuffersMB      int64 `json:"shared_buffers_mb"`
	EffectiveCacheMB     int64 `json:"effective_cache_size_mb"`
	WorkMemMB            int64 `json:"work_mem_mb"`
	MaintenanceWorkMemMB int64 `json:"maintenance_work_mem_mb"`
	MaxConnections       int   `json:"max_connections"`
}

// TuningStatus is the read-only view the panel surfaces so the operator can see
// how Postgres is sized to this box and what each workload profile would do.
// Applied is best-effort: if Postgres is unreachable the live values are absent
// (nil), but the host-sized recommendations are pure compute and always present.
type TuningStatus struct {
	MemoryMB      int64                  `json:"memory_mb"`
	CPUCount      int                    `json:"cpu_count"`
	ActiveProfile WorkloadProfile        `json:"active_profile"`
	Applied       *AppliedTuning         `json:"applied"`
	Profiles      []TuningRecommendation `json:"profiles"`
}

// CurrentTuning reports how Postgres is tuned for this host: the detected RAM/CPU,
// the host-sized recommendation for each workload profile (so the UI can label
// each override by its effect), and — best-effort — the live applied values.
//
// ActiveProfile is the Mixed best-default that Provision applies; switching it is
// not exposed here (a profile change restart-resizes shared_buffers/max_connections
// and must funnel through ApplyTuning's restartWithRollback, which is the
// install/provision path). This surface is read-only and never touches Postgres
// except to read pg_settings, so it can be shown safely at any time.
func (m *Manager) CurrentTuning(ctx context.Context) (TuningStatus, error) {
	profiles := make([]TuningRecommendation, 0, 3)
	for _, p := range []WorkloadProfile{ProfileOLTP, ProfileMixed, ProfileOLAP} {
		rec, _ := m.hostTuning(p)
		profiles = append(profiles, rec)
	}
	// Host facts (detected RAM/CPU) are identical across profiles, so read them
	// off the Mixed recommendation rather than detecting a second time.
	mixed := profiles[1]
	status := TuningStatus{
		MemoryMB:      mixed.MemoryMB,
		CPUCount:      mixed.CPUCount,
		ActiveProfile: ProfileMixed,
		Profiles:      profiles,
	}

	// Live applied values need a reachable Postgres (peer-auth psql). If it is
	// down or unreachable, don't fail the whole surface — the recommendations
	// above are still useful. Leave Applied nil and let the UI say so.
	applied, err := m.readTunableSettings(ctx)
	if err != nil {
		m.log.Warn("could not read applied tuning from pg_settings; showing recommendations only",
			"error", err.Error())
		return status, nil
	}
	status.Applied = &AppliedTuning{
		SharedBuffersMB:      applied["shared_buffers"] / bytesPerMB,
		EffectiveCacheMB:     applied["effective_cache_size"] / bytesPerMB,
		WorkMemMB:            applied["work_mem"] / bytesPerMB,
		MaintenanceWorkMemMB: applied["maintenance_work_mem"] / bytesPerMB,
		MaxConnections:       int(applied["max_connections"]),
	}
	return status, nil
}
