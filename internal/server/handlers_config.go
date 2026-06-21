package server

import (
	"net/http"

	"github.com/venkatesh-sekar/pgpanel/internal/config"
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

	// Reload so the response reflects exactly what was persisted (secret stays
	// redacted via the json:"-" tag).
	writeData(w, http.StatusOK, map[string]any{
		"config":               cfg,
		"backup_secret_is_set": cfg.Backup.SecretKey != "",
	})
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
		// Write-only secret: only overwrite when a non-empty value is given.
		if b.SecretKey != nil && *b.SecretKey != "" {
			cfg.Backup.SecretKey = *b.SecretKey
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
