package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// pinHostTuning makes CurrentTuning size against a fixed host (RAM/CPU) so the
// recommendations are deterministic regardless of the test machine, while still
// computing a genuinely distinct recommendation per profile.
func pinHostTuning(m *Manager, memoryMB int64, cpu int) {
	m.detectTuning = func(p WorkloadProfile) (TuningRecommendation, bool) {
		return RecommendTuning(memoryMB, cpu, p), true
	}
}

func TestCurrentTuning_SurfacesAppliedAndAllProfiles(t *testing.T) {
	r := exec.NewFakeRunner()
	// Live pg_settings in native units: 8kB blocks for the buffer settings, kB for
	// work_mem/maintenance_work_mem, unit-less for max_connections.
	r.On("pg_settings", exec.FakeResponse{Stdout: strings.Join([]string{
		fmt.Sprintf("shared_buffers|%d|8kB", 2048*128),       // 2048MB
		fmt.Sprintf("effective_cache_size|%d|8kB", 6144*128), // 6144MB
		fmt.Sprintf("work_mem|%d|kB", 16*1024),               // 16MB
		fmt.Sprintf("maintenance_work_mem|%d|kB", 512*1024),  // 512MB
		"max_connections|200|",
	}, "\n")})
	m := newManager(r)
	pinHostTuning(m, 8192, 4)

	status, err := m.CurrentTuning(context.Background())
	require.NoError(t, err)

	// Host facts come from the detected recommendation.
	require.Equal(t, int64(8192), status.MemoryMB)
	require.Equal(t, 4, status.CPUCount)
	require.Equal(t, ProfileMixed, status.ActiveProfile)

	// All three profiles are present, in a stable order, each genuinely sized to
	// the host — so the UI can label every override by its effect.
	require.Len(t, status.Profiles, 3)
	require.Equal(t, ProfileOLTP, status.Profiles[0].Profile)
	require.Equal(t, ProfileMixed, status.Profiles[1].Profile)
	require.Equal(t, ProfileOLAP, status.Profiles[2].Profile)
	// The profiles differ by effect — OLAP caches more aggressively than OLTP, so
	// its shared_buffers is larger and its max_connections smaller. Non-vacuous.
	require.Greater(t, status.Profiles[2].SharedBuffersMB, status.Profiles[0].SharedBuffersMB)
	require.Less(t, status.Profiles[2].MaxConnections, status.Profiles[0].MaxConnections)

	// Live applied values, normalised to whole MB.
	require.NotNil(t, status.Applied)
	require.Equal(t, int64(2048), status.Applied.SharedBuffersMB)
	require.Equal(t, int64(6144), status.Applied.EffectiveCacheMB)
	require.Equal(t, int64(16), status.Applied.WorkMemMB)
	require.Equal(t, int64(512), status.Applied.MaintenanceWorkMemMB)
	require.Equal(t, 200, status.Applied.MaxConnections)
}

// When Postgres is unreachable the read-only surface must still return the
// host-sized recommendations (pure compute) rather than failing — Applied is
// simply nil so the UI can say the live values are unavailable.
func TestCurrentTuning_DegradesWhenPostgresUnreachable(t *testing.T) {
	r := exec.NewFakeRunner()
	r.On("pg_settings", exec.FakeResponse{Err: errors.New("connection refused")})
	m := newManager(r)
	pinHostTuning(m, 4096, 2)

	status, err := m.CurrentTuning(context.Background())
	require.NoError(t, err, "an unreachable Postgres must not fail the read-only surface")
	require.Nil(t, status.Applied, "applied values are absent when pg_settings can't be read")
	require.Len(t, status.Profiles, 3, "recommendations are still computed without Postgres")
	require.Equal(t, int64(4096), status.MemoryMB)
}
