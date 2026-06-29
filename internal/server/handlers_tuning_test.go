package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// decodeActiveProfile pulls active_profile out of the standard {"data": ...}
// envelope a TuningStatus response is wrapped in.
func decodeActiveProfile(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Data struct {
			ActiveProfile string `json:"active_profile"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Data.ActiveProfile
}

// persistedProfile reads the workload profile recorded in the store, so a test
// can prove apply persisted (or, on failure, did NOT persist) the choice.
func persistedProfile(t *testing.T, st config.Store) string {
	t.Helper()
	cfg, err := config.Load(context.Background(), st)
	require.NoError(t, err)
	return cfg.TuningProfile
}

// GET /tuning reports the PERSISTED workload profile as active, not pg's
// hardcoded best-default: with nothing persisted it is the "mixed" default, and
// once a profile is persisted the surface reflects it. This proves the handler
// reads the same config key apply writes — the source of the UI's "— current"
// marker — rather than always echoing Mixed.
func TestGetTuning_ActiveProfileReflectsPersistedConfig(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	// Default-off state: nothing persisted → the "mixed" default.
	rec := authedRequest(t, srv, http.MethodGet, "/api/tuning", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "mixed", decodeActiveProfile(t, rec.Body.Bytes()))

	// Persist a different profile through the same key apply writes.
	require.NoError(t, st.SetConfig(context.Background(), "tuning_profile", "oltp"))

	rec = authedRequest(t, srv, http.MethodGet, "/api/tuning", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "oltp", decodeActiveProfile(t, rec.Body.Bytes()),
		"GET /tuning must reflect the persisted profile, not a hardcoded mixed")
}

// Applying a profile resizes shared_buffers/max_connections and restarts
// Postgres, so the endpoint is behind requireAuth: an unauthenticated POST is
// rejected before any side effect, and the persisted profile is left untouched.
func TestApplyTuning_RequiresAuth(t *testing.T) {
	srv, st := newTestServer(t)

	rec := authedRequest(t, srv, http.MethodPost, "/api/tuning/apply", "not-a-valid-token",
		map[string]any{"profile": "oltp"})
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())

	require.Equal(t, "mixed", persistedProfile(t, st),
		"a rejected request must not change the persisted profile")
}

// A missing profile is a 400, NOT the Mixed best-default: applying restarts
// Postgres, so we never silently restart onto a profile the operator didn't pick.
// Rejected fast, before any apply/persist.
func TestApplyTuning_MissingProfileRejected(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/tuning/apply", token,
		map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	require.Equal(t, "mixed", persistedProfile(t, st),
		"a rejected request must not change the persisted profile")
}

// A typo'd profile is a 400 (ParseWorkloadProfile refuses to silently mis-size
// the box), again before any apply/persist.
func TestApplyTuning_UnknownProfileRejected(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/tuning/apply", token,
		map[string]any{"profile": "nonsense"})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	require.Equal(t, "mixed", persistedProfile(t, st),
		"a rejected request must not change the persisted profile")
}

// With a valid profile but Postgres unreachable (the test server never connects),
// ApplyProfile fails. The handler must surface the error and DO NOT persist the
// profile — so the recorded choice never gets ahead of what is actually applied.
// This exercises the apply-fails (rollback / PG-unreachable) contract over HTTP;
// the side-effecting happy path is proven against a fake runner in the pg package
// (TestApplyProfile_*), by the same convention the pooler handler tests use.
func TestApplyTuning_DoesNotPersistWhenApplyFails(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/tuning/apply", token,
		map[string]any{"profile": "oltp"})
	require.NotEqual(t, http.StatusOK, rec.Code,
		"apply against an unreachable Postgres must not succeed; body: %s", rec.Body.String())

	require.Equal(t, "mixed", persistedProfile(t, st),
		"a failed apply must not persist the chosen profile")
}

// pgSettingsRows renders a recommendation as the pg_settings text the FakeRunner
// returns. It uses the plain "MB" unit (which readTunableSettings understands) so
// the byte round-trip lands exactly on the recommendation's values without having
// to re-derive Postgres' native 8kB/kB block units here — the only thing the
// success-path test needs is for ApplyTuning to read back values identical to the
// recommendation it computes, so its no-op branch fires.
func pgSettingsRows(rec pg.TuningRecommendation) string {
	return strings.Join([]string{
		fmt.Sprintf("shared_buffers|%d|MB", rec.SharedBuffersMB),
		fmt.Sprintf("effective_cache_size|%d|MB", rec.EffectiveCacheMB),
		fmt.Sprintf("work_mem|%d|MB", rec.WorkMemMB),
		fmt.Sprintf("maintenance_work_mem|%d|MB", rec.MaintenanceWorkMemMB),
		fmt.Sprintf("max_connections|%d|", rec.MaxConnections),
	}, "\n")
}

// The post-apply success path is the property the whole contract rests on, and
// the unreachable-Postgres tests above cannot reach it: on a SUCCESSFUL apply the
// handler persists the chosen profile AND returns a TuningStatus whose
// active_profile is that profile. This drives that path end to end over HTTP.
//
// To succeed without a real Postgres restart we swap in a pg Manager backed by a
// FakeRunner and program pg_settings to already report the target profile's
// host-sized values, so ApplyTuning takes its no-op branch (nothing differs → no
// ALTER SYSTEM, no restart) and returns cleanly. The recommendation is read back
// from CurrentTuning (computed from this host's RAM/CPU) so the fake's pg_settings
// match exactly regardless of the machine the test runs on.
func TestApplyTuning_PersistsAndStampsActiveProfileOnSuccess(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	// Replace the real-OSRunner Manager with one driven by a fake runner, so the
	// apply path is deterministic and never shells out to systemctl/psql.
	fake := exec.NewFakeRunner()
	srv.pg = pg.New(pg.Options{Runner: fake, Config: config.Default(), Logger: core.Discard()})

	// Resolve this host's OLTP recommendation the same way the handler will, then
	// make pg_settings report exactly those values so ApplyProfile(oltp) is a
	// no-op (already applied) — a clean success with no restart.
	cur, err := srv.pg.CurrentTuning(context.Background())
	require.NoError(t, err)
	var target pg.TuningRecommendation
	for _, p := range cur.Profiles {
		if p.Profile == pg.ProfileOLTP {
			target = p
		}
	}
	require.Equal(t, pg.ProfileOLTP, target.Profile, "OLTP recommendation must be present")
	fake.On("pg_settings", exec.FakeResponse{Stdout: pgSettingsRows(target)})

	rec := authedRequest(t, srv, http.MethodPost, "/api/tuning/apply", token,
		map[string]any{"profile": "oltp"})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// The response stamps the now-active profile (re-read + ActiveProfile = the
	// applied profile), and the choice is persisted — both moved off the "mixed"
	// default and agree, proving persist-only-on-success + the response stamp.
	require.Equal(t, "oltp", decodeActiveProfile(t, rec.Body.Bytes()),
		"a successful apply must return active_profile == the applied profile")
	require.Equal(t, "oltp", persistedProfile(t, st),
		"a successful apply must persist the chosen profile")

	// The audit trail must name WHICH profile the apply switched to. target is the
	// profile (oltp), not a generic "tuning" — this is the only persistent record
	// of an action that restarts Postgres, and it follows the convention where
	// target is the specific object acted on (extensions use the extension name).
	entry := latestAudit(t, st, "apply_tuning")
	require.Equal(t, "oltp", entry.Target,
		"the apply audit must record which profile was applied, not a generic target")
	require.Equal(t, "success", entry.Result)
}

// latestAudit returns the most recent audit entry for the given action, failing
// the test if none was recorded.
func latestAudit(t *testing.T, st *store.Store, action string) store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), 100, 0)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Action == action {
			return e
		}
	}
	require.Failf(t, "no audit entry", "expected an audit entry for action %q", action)
	return store.AuditEntry{}
}
