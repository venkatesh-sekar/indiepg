//go:build e2e

package harness

import (
	"fmt"
	"time"
)

// S3Config is the S3 backup target a scenario hands to ConfigureS3. It mirrors the
// panel's editable S3 fields (PUT /api/config -> backup{...}). For the e2e MinIO,
// pass URIStyle:"path" and UseSSL:true (the panel trusts the MinIO CA).
type S3Config struct {
	Endpoint   string
	Region     string
	Bucket     string
	Prefix     string
	AccessKey  string
	SecretKey  string
	URIStyle   string // "host" (default) or "path" (MinIO)
	UseSSL     bool
	CipherPass string
}

// MinIOS3Config returns the standard S3Config that points the panel at the e2e
// MinIO over the compose network: path-style, TLS, the fixed creds/bucket. The
// prefix is left empty so install's instance-id namespacing (or the operator's
// choice) applies; pass a Prefix to override.
func MinIOS3Config() S3Config {
	return S3Config{
		Endpoint:  MinIOEndpointInternal,
		Region:    "us-east-1",
		Bucket:    MinIOBucket,
		AccessKey: MinIOAccessKey,
		SecretKey: MinIOSecretKey,
		URIStyle:  "path",
		UseSSL:    true,
	}
}

// updateConfigRequest mirrors the panel's PUT /api/config body. All fields are
// pointers so an unset field preserves the stored value.
type updateConfigRequest struct {
	Backup *backupTargetUpdate `json:"backup,omitempty"`
	Stanza *string             `json:"stanza,omitempty"`
}

type backupTargetUpdate struct {
	Endpoint   *string `json:"endpoint,omitempty"`
	Region     *string `json:"region,omitempty"`
	Bucket     *string `json:"bucket,omitempty"`
	Prefix     *string `json:"prefix,omitempty"`
	AccessKey  *string `json:"access_key,omitempty"`
	SecretKey  *string `json:"secret_key,omitempty"`
	UseSSL     *bool   `json:"use_ssl,omitempty"`
	URIStyle   *string `json:"uri_style,omitempty"`
	CipherPass *string `json:"cipher_pass,omitempty"`
}

// ConfigResponse is the PUT/GET /api/config response (the parts the suite reads).
// On a config save that (re)provisions pgBackRest, BackupConfigured reports
// whether stanza-create + archiving succeeded; BackupWarning/Detail carry the
// reason when it did not.
type ConfigResponse struct {
	BackupConfigured  bool   `json:"backup_configured"`
	BackupSecretIsSet bool   `json:"backup_secret_is_set"`
	BackupWarning     string `json:"backup_warning"`
	BackupHint        string `json:"backup_hint"`
	BackupDetail      string `json:"backup_detail"`
}

// ConfigureS3 sets the panel's S3 backup target via PUT /api/config. The panel
// then renders the pgBackRest config, enables WAL archiving (restarting Postgres
// if needed), and runs stanza-create — so a returned BackupConfigured=false (with
// BackupWarning/Detail) means the repo could not be initialized. The scenario
// decides whether that is a failure.
func (p *Panel) ConfigureS3(cfg S3Config) (ConfigResponse, error) {
	body := updateConfigRequest{
		Backup: &backupTargetUpdate{
			Endpoint:  strptr(cfg.Endpoint),
			Region:    strptr(cfg.Region),
			Bucket:    strptr(cfg.Bucket),
			Prefix:    strptr(cfg.Prefix),
			AccessKey: strptr(cfg.AccessKey),
			SecretKey: strptr(cfg.SecretKey),
			UseSSL:    boolptr(cfg.UseSSL),
			URIStyle:  strptr(cfg.URIStyle),
		},
	}
	if cfg.CipherPass != "" {
		body.Backup.CipherPass = strptr(cfg.CipherPass)
	}
	var out ConfigResponse
	err := p.PUT("/api/config", body, &out)
	return out, err
}

// GetConfig fetches GET /api/config (the assertion-relevant fields).
func (p *Panel) GetConfig() (ConfigResponse, error) {
	var out ConfigResponse
	err := p.GET("/api/config", &out)
	return out, err
}

// ---- Backups ----

// RunBackupResponse is the POST /api/backups/run 202 acknowledgement.
type RunBackupResponse struct {
	ID     int64  `json:"id"`
	Type   string `json:"type"`
	Result string `json:"result"` // "running"
}

// RunBackup starts a backup of the given type ("full"|"incr"|"diff"). It returns
// immediately with the new history-row id; poll ListBackups (or AwaitBackup) for
// completion. A misconfig/conflict/foreign-owner surfaces as a typed error here.
func (p *Panel) RunBackup(backupType string) (RunBackupResponse, error) {
	var out RunBackupResponse
	err := p.POST("/api/backups/run", map[string]string{"type": backupType}, &out)
	return out, err
}

// BackupRecord mirrors one row of the backup history.
type BackupRecord struct {
	ID            int64      `json:"id"`
	Label         string     `json:"label"`
	BackupType    string     `json:"backup_type"`
	StartedAt     time.Time  `json:"started_at"`
	StoppedAt     *time.Time `json:"stopped_at"`
	SizeBytes     int64      `json:"size_bytes"`
	DatabaseBytes int64      `json:"database_bytes"`
	RepoBytes     int64      `json:"repo_bytes"`
	WALStart      string     `json:"wal_start"`
	WALStop       string     `json:"wal_stop"`
	Result        string     `json:"result"` // running|success|fail
	RepoPath      string     `json:"repo_path"`
	Error         string     `json:"error"`
}

// BackupHistory is the GET /api/backups payload.
type BackupHistory struct {
	Backups      []BackupRecord `json:"backups"`
	RestoreTests []struct {
		ID           int64  `json:"id"`
		VerifiedRows int64  `json:"verified_rows"`
		Result       string `json:"result"`
		Detail       string `json:"detail"`
	} `json:"restore_tests"`
}

// ListBackups returns the persisted backup + restore-test history.
func (p *Panel) ListBackups() (BackupHistory, error) {
	var out BackupHistory
	err := p.GET("/api/backups", &out)
	return out, err
}

// FindBackup returns the history row with the given id (and whether it was found).
func (h BackupHistory) FindBackup(id int64) (BackupRecord, bool) {
	for _, b := range h.Backups {
		if b.ID == id {
			return b, true
		}
	}
	return BackupRecord{}, false
}

// AwaitBackup polls the backup history until the row with id reaches a terminal
// state, then returns it. A "fail" terminal state is returned (not an error) so
// the scenario can assert on it; a poll/transport error aborts.
func (p *Panel) AwaitBackup(id int64, timeout time.Duration) (BackupRecord, error) {
	var final BackupRecord
	err := Poll(timeout, 2*time.Second, func() (bool, error) {
		h, err := p.ListBackups()
		if err != nil {
			return false, err
		}
		rec, ok := h.FindBackup(id)
		if !ok {
			return false, fmt.Errorf("backup row %d not found yet", id)
		}
		if rec.Result == "running" {
			return false, nil
		}
		final = rec
		return true, nil
	})
	return final, err
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }
