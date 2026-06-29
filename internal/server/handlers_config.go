package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// handleGetConfig returns the current panel configuration. The S3 secret key is
// never serialized (config.S3Target tags it json:"-"); the response indicates
// whether a secret is set without revealing it.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(r.Context(), s.store)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"config":               cfg,
		"backup_secret_is_set": cfg.Backup.SecretKey != "",
		"backup_cipher_is_set": cfg.Backup.CipherPass != "",
	})
}

// handleGetTuning returns the read-only host-sized tuning surface: the detected
// RAM/CPU, the live applied settings (best-effort — absent if Postgres is
// unreachable), and the recommendation for each workload profile so the UI can
// label every override by its effect. It never mutates Postgres.
//
// ActiveProfile reflects the operator's PERSISTED choice (config.TuningProfile,
// default mixed) rather than pg's hardcoded best-default: pg knows "what's
// applied" (read from pg_settings), the handler owns "what's chosen". This is
// what lets the UI flip the "— current" marker once a profile is applied.
func (s *Server) handleGetTuning(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	status, err := s.pg.CurrentTuning(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	profile, err := s.persistedTuningProfile(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	status.ActiveProfile = profile

	writeData(w, http.StatusOK, status)
}

// persistedTuningProfile reports the workload profile the operator has chosen and
// we have applied to this box: the persisted config value (default mixed). A
// stored value that fails to parse falls back to mixed rather than failing the
// surface, so a hand-edited or stale store row can never break the tuning page —
// pg.ParseWorkloadProfile only rejects genuinely unknown strings.
func (s *Server) persistedTuningProfile(ctx context.Context) (pg.WorkloadProfile, error) {
	cfg, err := config.Load(ctx, s.store)
	if err != nil {
		return "", err
	}
	profile, perr := pg.ParseWorkloadProfile(cfg.TuningProfile)
	if perr != nil {
		return pg.ProfileMixed, nil
	}
	return profile, nil
}

// applyTuningRequest selects the workload profile to apply. An invalid or missing
// value is a 400 (pg.ParseWorkloadProfile refuses to silently mis-size the box),
// so a typo can't quietly restart Postgres onto the wrong profile.
type applyTuningRequest struct {
	Profile string `json:"profile"`
}

// handleApplyTuning applies a workload profile to Postgres' host-sized settings.
// This is the deliberate, system-mutating counterpart to GET /tuning: it resizes
// shared_buffers/max_connections and restarts Postgres (a few seconds of
// downtime), so it is a CSRF-gated POST behind requireAuth.
//
// Order matters and is the contract: ApplyProfile runs FIRST, and the chosen
// profile is persisted ONLY once Postgres is confirmed running on it. So the
// recorded "chosen" profile can never get ahead of what's actually applied — if
// the postmaster rejects a value, ApplyTuning rolls back to last-known-good and
// returns CodeSafety, and if Postgres is unreachable ApplyProfile errors; either
// way we surface the error and leave the persisted profile untouched. On success
// we re-read the live status and stamp the now-persisted profile so the response
// reflects exactly what the box is running.
func (s *Server) handleApplyTuning(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req applyTuningRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	// A missing profile is a 400 here, not the Mixed best-default: applying
	// restarts Postgres, so we never silently restart onto a profile the operator
	// didn't pick. ParseWorkloadProfile then rejects any unknown string.
	if strings.TrimSpace(req.Profile) == "" {
		writeError(w, core.ValidationError("profile is required (oltp, mixed, or olap)"))
		return
	}
	profile, err := pg.ParseWorkloadProfile(req.Profile)
	if err != nil {
		writeError(w, err)
		return
	}

	// Serialize the WHOLE apply-and-persist sequence below. Two overlapping applies
	// could otherwise interleave ALTER SYSTEM / restart / rollback / persist, so the
	// last persisted profile might differ from the settings that actually won, and
	// one apply's rollback could undo the other's. We Lock (queue) rather than reject
	// with 409: applying is idempotent and the operator's intent is simply "make the
	// box this profile", so briefly serializing yields a consistent last-writer-wins
	// result — friendlier than a conflict the SPA would only have to retry.
	s.tuningMu.Lock()
	defer s.tuningMu.Unlock()

	// Apply FIRST. On any failure (rejected value rolled back to last-known-good,
	// or Postgres unreachable) surface it and DO NOT persist the profile. The
	// audit target is the profile itself (oltp/mixed/olap), not a generic "tuning":
	// this is the only persistent record of an action that restarts Postgres, so it
	// must say WHICH profile a restart switched to — matching the convention where
	// target is the specific object acted on (extensions use the extension name,
	// backups the backup type).
	if _, err := s.pg.ApplyProfile(ctx, profile); err != nil {
		s.audit(ctx, "apply_tuning", string(profile), "failure", "apply workload profile failed", core.CodeOf(err))
		writeError(w, err)
		return
	}

	// Postgres is now running the profile. Record the success in the audit log
	// BEFORE persisting the choice: the box has already been reconfigured, so the
	// event must be captured even if the persist below fails — otherwise a store
	// error after a real Postgres change would leave that change with no audit trail.
	s.audit(ctx, "apply_tuning", string(profile), "success", "applied workload profile", "")

	// Persist the chosen profile with a targeted single-key write, not a full
	// config Load+Save: only this one string key changes, so re-reading and
	// re-writing (and re-Validating) every field would only widen the window in
	// which a store error could fail the request after the box is already retuned.
	// If this does fail, the next GET /tuning shows the new applied values against
	// the stale active profile (visible drift) and re-applying is a PG-side no-op.
	// Surface that explicitly: a bare store error ("write config 'tuning_profile':
	// ...") would leave the operator unable to tell whether their downtime was
	// wasted. Name what WAS applied and that recovery is safe, so they don't fear a
	// half-done change.
	//
	// Crucially, do NOT interpolate the raw err into the client Message: toAPIError
	// returns a typed error's Message verbatim, so a %v would leak the wrapped
	// SQLite/OS error to the SPA. Log the underlying cause server-side for
	// diagnostics and Wrap it (the cause is never serialized — only Message/Hint/
	// Details reach the wire), returning a clean operator-facing explanation.
	if err := config.SaveTuningProfile(ctx, s.store, string(profile)); err != nil {
		s.log.Error("persisting tuning profile failed after Postgres was retuned",
			"profile", profile, "err", err)
		writeError(w, core.InternalError(
			"Postgres is now running the %s profile, but saving that choice failed "+
				"— reload the page; the active profile will show as drifted and re-applying is safe",
			profile).Wrap(err))
		return
	}

	// Re-read the fresh applied values and stamp the now-persisted profile so the
	// SPA can update from the returned status in one round trip.
	status, err := s.pg.CurrentTuning(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	status.ActiveProfile = profile

	writeData(w, http.StatusOK, status)
}

// updateConfigRequest is the mutable subset of configuration exposed to the UI.
// Network bind and PG socket settings are deliberately excluded from the API:
// changing the bind address over the network could lock the operator out, so it
// is install/CLI-only. Secrets are write-only — an empty SecretKey preserves
// the stored value rather than clearing it.
type updateConfigRequest struct {
	OTLPEndpoint  *string             `json:"otlp_endpoint,omitempty"`
	OTLPInsecure  *bool               `json:"otlp_insecure,omitempty"`
	Stanza        *string             `json:"stanza,omitempty"`
	RetentionDays *int                `json:"retention_days,omitempty"`
	QueryLimit    *int                `json:"query_limit,omitempty"`
	Backup        *backupTargetUpdate `json:"backup,omitempty"`
	Schedules     *schedulesUpdate    `json:"schedules,omitempty"`
}

// backupTargetUpdate mirrors the editable S3 fields; SecretKey is write-only.
type backupTargetUpdate struct {
	Endpoint  *string `json:"endpoint,omitempty"`
	Region    *string `json:"region,omitempty"`
	Bucket    *string `json:"bucket,omitempty"`
	Prefix    *string `json:"prefix,omitempty"`
	AccessKey *string `json:"access_key,omitempty"`
	SecretKey *string `json:"secret_key,omitempty"`
	UseSSL    *bool   `json:"use_ssl,omitempty"`
	// CipherPass enables repo encryption; write-only, like SecretKey.
	CipherPass *string `json:"cipher_pass,omitempty"`
}

// schedulesUpdate mirrors the editable cron expressions.
type schedulesUpdate struct {
	FullBackup        *string `json:"full_backup,omitempty"`
	IncrementalBackup *string `json:"incremental_backup,omitempty"`
	RestoreTest       *string `json:"restore_test,omitempty"`
	TelemetrySample   *string `json:"telemetry_sample,omitempty"`
	Digest            *string `json:"digest,omitempty"`
}

// handleUpdateConfig applies a partial configuration update. It loads the
// current config, overlays only the provided fields, re-validates (which
// re-asserts the private-bind safety rule on the unchanged bind addr), and
// persists. Validation failures return the typed core error unchanged.
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req updateConfigRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	cfg, err := config.Load(ctx, s.store)
	if err != nil {
		writeError(w, err)
		return
	}

	applyConfigUpdate(&cfg, req)

	if err := cfg.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := config.Save(ctx, s.store, cfg); err != nil {
		writeError(w, err)
		return
	}

	// Refresh the live backup Manager so a freshly-saved S3 target (and its
	// single-writer ownership guard) takes effect immediately, without a restart.
	s.backups.Reconfigure(cfg, backupOwnerFor(ctx, s.store, cfg, s.log))

	s.audit(ctx, "update_config", "config", "success", "panel configuration updated", "")

	resp := map[string]any{
		"config":               cfg,
		"backup_secret_is_set": cfg.Backup.SecretKey != "",
		"backup_cipher_is_set": cfg.Backup.CipherPass != "",
	}

	// If a backup target is configured, (re)render the pgBackRest config and
	// initialize the stanza. The config is already saved, so a provisioning
	// failure does NOT fail the request — it is surfaced as a non-fatal warning so
	// the operator can fix the underlying cause (Postgres down, bad credentials)
	// and re-save. Never echo secrets in the warning (typed errors are curated).
	if changed, perr := s.ensureBackupConfigured(ctx, cfg); perr != nil {
		s.audit(ctx, "configure_backup", "pgbackrest", "failure", "configure pgBackRest failed", core.CodeOf(perr))
		ae, _ := toAPIError(perr)
		resp["backup_configured"] = false
		resp["backup_warning"] = ae.Message
		if ae.Hint != "" {
			resp["backup_hint"] = ae.Hint
		}
		// Surface the underlying command's stderr (the precise reason) so the
		// operator sees WHY the target failed right where they entered it.
		if s, ok := ae.Details["stderr"].(string); ok && s != "" {
			resp["backup_detail"] = s
		}
		s.log.Warn("pgBackRest configuration failed after config save", "err", perr)
	} else {
		resp["backup_configured"] = true
		if changed {
			s.audit(ctx, "configure_backup", "pgbackrest", "success", "pgBackRest configured", "")
		}
	}

	// Reload so the response reflects exactly what was persisted (secrets stay
	// redacted via the json:"-" tags).
	writeData(w, http.StatusOK, resp)
}

// applyConfigUpdate overlays the provided pointer fields onto cfg. Nil pointers
// are left unchanged. An empty SecretKey string preserves the stored secret
// (write-only semantics); to clear it the operator must use the CLI.
func applyConfigUpdate(cfg *config.Config, req updateConfigRequest) {
	setIf(&cfg.OTLPEndpoint, req.OTLPEndpoint)
	setIfBool(&cfg.OTLPInsecure, req.OTLPInsecure)
	setIf(&cfg.Stanza, req.Stanza)
	setIfInt(&cfg.RetentionDays, req.RetentionDays)
	setIfInt(&cfg.QueryLimit, req.QueryLimit)

	if b := req.Backup; b != nil {
		setIf(&cfg.Backup.Endpoint, b.Endpoint)
		setIf(&cfg.Backup.Region, b.Region)
		setIf(&cfg.Backup.Bucket, b.Bucket)
		setIf(&cfg.Backup.Prefix, b.Prefix)
		setIf(&cfg.Backup.AccessKey, b.AccessKey)
		setIfBool(&cfg.Backup.UseSSL, b.UseSSL)
		// Write-only secrets: only overwrite when a non-empty value is given, so a
		// round-tripped (redacted) form never clears the stored value.
		if b.SecretKey != nil && *b.SecretKey != "" {
			cfg.Backup.SecretKey = *b.SecretKey
		}
		if b.CipherPass != nil && *b.CipherPass != "" {
			cfg.Backup.CipherPass = *b.CipherPass
		}
	}

	if sc := req.Schedules; sc != nil {
		setIf(&cfg.Schedules.FullBackup, sc.FullBackup)
		setIf(&cfg.Schedules.IncrementalBackup, sc.IncrementalBackup)
		setIf(&cfg.Schedules.RestoreTest, sc.RestoreTest)
		setIf(&cfg.Schedules.TelemetrySample, sc.TelemetrySample)
		setIf(&cfg.Schedules.Digest, sc.Digest)
	}
}

func setIf(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func setIfBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

func setIfInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}
