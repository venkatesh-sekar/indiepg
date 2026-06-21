package server

import (
	"net/http"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
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
