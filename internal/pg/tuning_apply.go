package pg

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// tunedSettings is the ordered set of host-sized settings ApplyTuning persists,
// keyed by postgresql.conf name. wanted is the value normalised to a comparable
// integer — bytes for memory settings, the plain count for max_connections — so
// the round-trip comparison against pg_settings is exact and unit-agnostic.
// Memory settings are written in whole MB, which is always an exact multiple of
// the 8kB block Postgres stores them in, so what we write reads back identically.
type tunedSetting struct {
	name    string
	literal string // the ALTER SYSTEM value, e.g. "1228MB" or "300"
	wanted  int64  // bytes for memory settings; the integer for max_connections
	restart bool   // true if changing it needs a full restart (PGC_POSTMASTER)
	numeric bool   // true for an integer GUC, written unquoted (max_connections)
}

const bytesPerMB = int64(1024 * 1024)

func tunedSettings(rec TuningRecommendation) []tunedSetting {
	return []tunedSetting{
		{"shared_buffers", fmt.Sprintf("%dMB", rec.SharedBuffersMB), rec.SharedBuffersMB * bytesPerMB, true, false},
		{"effective_cache_size", fmt.Sprintf("%dMB", rec.EffectiveCacheMB), rec.EffectiveCacheMB * bytesPerMB, false, false},
		{"work_mem", fmt.Sprintf("%dMB", rec.WorkMemMB), rec.WorkMemMB * bytesPerMB, false, false},
		{"maintenance_work_mem", fmt.Sprintf("%dMB", rec.MaintenanceWorkMemMB), rec.MaintenanceWorkMemMB * bytesPerMB, false, false},
		{"max_connections", fmt.Sprintf("%d", rec.MaxConnections), int64(rec.MaxConnections), true, true},
	}
}

// alterValue renders the post-`=` text for an ALTER SYSTEM statement: a memory
// setting is a quoted string literal (e.g. '1228MB'); an integer GUC like
// max_connections is the bare token (179), which Postgres requires unquoted.
func (s tunedSetting) alterValue() string {
	if s.numeric {
		return s.literal
	}
	return core.QuoteLiteral(s.literal)
}

// ApplyTuning persists the host-sized recommendation with ALTER SYSTEM and
// activates it safely, returning whether anything changed.
//
// Settings already at the wanted value are skipped, so re-running provisioning
// is a no-op (no needless restart). When a restart-requiring setting
// (shared_buffers, max_connections) changed, the change is activated through
// restartWithRollback: postgresql.auto.conf is snapshotted BEFORE anything is
// written, and if the postmaster rejects the new value (e.g. a shared_buffers
// the box cannot map) Postgres is rolled back to last-known-good rather than
// left down — surfaced as a CodeSafety error. When only reloadable settings
// changed, the change takes effect via pg_reload_conf with no restart.
//
// Settings are written with ALTER SYSTEM as the postgres OS superuser (peer-auth
// via psql — the panel's pool roles are NOSUPERUSER and cannot ALTER SYSTEM),
// mirroring EnsureArchiving.
func (m *Manager) ApplyTuning(ctx context.Context, rec TuningRecommendation) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pg: ApplyTuning requires a Runner")
	}

	current, err := m.readTunableSettings(ctx)
	if err != nil {
		return false, err
	}

	var stmts []string
	needRestart := false
	for _, s := range tunedSettings(rec) {
		cur, ok := current[s.name]
		if !ok {
			return false, core.InternalError("pg: setting %q missing from pg_settings", s.name)
		}
		if cur == s.wanted {
			continue // already at the host-sized value — leave it alone
		}
		stmts = append(stmts, "ALTER SYSTEM SET "+s.name+" = "+s.alterValue())
		if s.restart {
			needRestart = true
		}
	}
	if len(stmts) == 0 {
		return false, nil
	}

	// A restart-requiring change could fail to come back up. Snapshot
	// postgresql.auto.conf BEFORE writing anything so restartWithRollback can
	// revert to last-known-good if it does.
	var snap autoConfSnapshot
	if needRestart {
		if snap, err = m.snapshotAutoConf(ctx); err != nil {
			return false, err
		}
	}

	for _, s := range stmts {
		if _, err := m.runPsql(ctx, defaultConnectDatabase, s); err != nil {
			return false, core.ExecError("pg: applying host-sized tuning failed").Wrap(err)
		}
	}

	if needRestart {
		if err := m.restartWithRollback(ctx, snap, "host-sized tuning"); err != nil {
			return false, err
		}
		m.log.InfoCtx(ctx, "applied host-sized tuning (restarted Postgres)", "profile", rec.Profile)
		return true, nil
	}

	if _, err := m.runPsql(ctx, defaultConnectDatabase, "SELECT pg_reload_conf()"); err != nil {
		return false, err
	}
	m.log.InfoCtx(ctx, "applied host-sized tuning (reloaded Postgres)", "profile", rec.Profile)
	return true, nil
}

// ApplyProfile resolves the host-sized recommendation for the given workload
// profile and activates it through ApplyTuning. It is the "switch the box to this
// workload" entry point the panel calls when the operator picks a different
// profile: the recommendation is pure compute from detected RAM/CPU (hostTuning),
// and ApplyTuning does the safe part — ALTER SYSTEM, snapshot-then-restart-with-
// rollback for the restart-requiring settings (shared_buffers, max_connections),
// a plain reload when only reloadable settings changed, or a no-op when the box
// already holds the profile's values. It returns whether anything changed.
//
// pg stays deliberately ignorant of WHICH profile is "chosen"/persisted — that is
// the handler's concern (it owns config). Here we only compute and apply: callers
// resolve the persisted choice, call ApplyProfile, and record it only on success.
func (m *Manager) ApplyProfile(ctx context.Context, profile WorkloadProfile) (bool, error) {
	rec, _ := m.hostTuning(profile)
	return m.ApplyTuning(ctx, rec)
}

// tunableSettingNames is the pg_settings names ApplyTuning manages, in the
// SELECT below.
const tunableSettingNames = "'shared_buffers','effective_cache_size','work_mem'," +
	"'maintenance_work_mem','max_connections'"

// readTunableSettings reads the current value of every setting ApplyTuning
// manages, normalised to the same comparable integer as tunedSetting.wanted:
// bytes for memory settings (using pg_settings.unit), or the bare integer for a
// unit-less setting like max_connections. Reading via pg_settings (not SHOW)
// returns the raw numeric value plus its unit, so the comparison is exact
// regardless of how Postgres chooses to render the value.
func (m *Manager) readTunableSettings(ctx context.Context) (map[string]int64, error) {
	out, err := m.runPsql(ctx, defaultConnectDatabase,
		"SELECT name, setting, unit FROM pg_settings WHERE name IN ("+tunableSettingNames+")")
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, 5)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 3 {
			return nil, core.InternalError("pg: unexpected pg_settings row %q", line)
		}
		name := strings.TrimSpace(fields[0])
		setting, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil {
			return nil, core.InternalError("pg: non-integer %s value %q in pg_settings", name, fields[1])
		}
		factor, ok := settingUnitBytes(strings.TrimSpace(fields[2]))
		if !ok {
			return nil, core.InternalError("pg: unrecognised unit %q for setting %s", fields[2], name)
		}
		result[name] = setting * factor
	}
	return result, nil
}

// settingUnitBytes converts a pg_settings.unit (e.g. "8kB", "kB", "MB", or "" for
// a unit-less integer setting) into the byte multiplier for one unit. A
// unit-less setting (max_connections) has multiplier 1, so its raw integer is
// compared directly.
func settingUnitBytes(unit string) (int64, bool) {
	if unit == "" {
		return 1, true
	}
	i := 0
	for i < len(unit) && unit[i] >= '0' && unit[i] <= '9' {
		i++
	}
	mult := int64(1)
	if i > 0 {
		v, err := strconv.ParseInt(unit[:i], 10, 64)
		if err != nil || v <= 0 {
			return 0, false
		}
		mult = v
	}
	switch unit[i:] {
	case "B":
		return mult, true
	case "kB":
		return mult * 1024, true
	case "MB":
		return mult * 1024 * 1024, true
	case "GB":
		return mult * 1024 * 1024 * 1024, true
	default:
		return 0, false
	}
}
