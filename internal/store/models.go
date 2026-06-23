package store

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Instance is the panel's stable identity row.
type Instance struct {
	InstanceID   string    `json:"instance_id"`
	Label        string    `json:"label"`
	Hostname     string    `json:"hostname"`
	PGSystemID   string    `json:"pg_system_id"`
	PanelVersion string    `json:"panel_version"`
	CreatedAt    time.Time `json:"created_at"`
}

// AuthRecord holds the admin credential and lockout state.
type AuthRecord struct {
	PasswordHash   string     `json:"-"`
	SessionSecret  []byte     `json:"-"`
	FailedAttempts int        `json:"failed_attempts"`
	LockedUntil    *time.Time `json:"locked_until,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// String renders the record with its two secrets — the Argon2id PasswordHash and
// the raw HMAC SessionSecret used to sign every session token — replaced by the
// redaction marker. These are the most sensitive bytes the panel holds, so an
// AuthRecord must never leak them through a log line, an error string, or any
// fmt verb; only the non-secret lockout/state fields are shown.
func (r AuthRecord) String() string {
	return fmt.Sprintf(
		"AuthRecord{PasswordHash:%s SessionSecret:%s FailedAttempts:%d LockedUntil:%v UpdatedAt:%v}",
		core.Redact(r.PasswordHash), core.RedactBytes(r.SessionSecret),
		r.FailedAttempts, r.LockedUntil, r.UpdatedAt,
	)
}

// LogValue makes AuthRecord an slog.LogValuer so structured logging renders the
// redacted String() form instead of reflecting into the secret fields.
func (r AuthRecord) LogValue() slog.Value { return slog.StringValue(r.String()) }

// GoString makes AuthRecord an fmt.GoStringer so even the %#v Go-syntax verb —
// which bypasses String() — renders the redacted form rather than the raw
// password hash and session signing secret.
func (r AuthRecord) GoString() string { return r.String() }

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID      int64     `json:"id"`
	TS      time.Time `json:"ts"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Summary string    `json:"summary"`
	Result  string    `json:"result"`
	Detail  string    `json:"detail"`
}

// BackupRecord is one pgBackRest run record with stats.
type BackupRecord struct {
	ID            int64      `json:"id"`
	Label         string     `json:"label"`
	BackupType    string     `json:"backup_type"`
	StartedAt     time.Time  `json:"started_at"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
	SizeBytes     int64      `json:"size_bytes"`
	DatabaseBytes int64      `json:"database_bytes"`
	RepoBytes     int64      `json:"repo_bytes"`
	WALStart      string     `json:"wal_start"`
	WALStop       string     `json:"wal_stop"`
	Result        string     `json:"result"`
	RepoPath      string     `json:"repo_path"`
	Error         string     `json:"error"`
}

// RestoreTestRecord is one restore-test result.
type RestoreTestRecord struct {
	ID           int64     `json:"id"`
	TestedAt     time.Time `json:"tested_at"`
	SourceLabel  string    `json:"source_label"`
	VerifiedRows int64     `json:"verified_rows"`
	Result       string    `json:"result"`
	DurationMS   int64     `json:"duration_ms"`
	Detail       string    `json:"detail"`
}

// AlertRecord is a persisted alert rule plus its evaluation state. Definition
// is the JSON-encoded rule body (thresholds, channels, cooldowns); the alert
// package owns its schema.
type AlertRecord struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Definition  string     `json:"definition"`
	Severity    string     `json:"severity"`
	State       string     `json:"state"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	LastEvalAt  *time.Time `json:"last_eval_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// MigrationRecord is the local-store source of truth for one database-migration
// job — direct-pull (single-db/cluster) or ssh-less handshake. Source passwords
// are NEVER stored here; SourceSummary holds only a redacted "user@host:port/db".
// RowCountsSrc/RowCountsTgt are JSON objects mapping "schema.table" -> count.
type MigrationRecord struct {
	ID             int64      `json:"id"`
	Mode           string     `json:"mode"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	Phase          string     `json:"phase"`
	SourceSummary  string     `json:"source_summary"`
	TargetDatabase string     `json:"target_database"`
	Overwrite      bool       `json:"overwrite"`
	Code           string     `json:"code"`
	ProgressDone   int64      `json:"progress_done"`
	ProgressTotal  int64      `json:"progress_total"`
	BytesTotal     int64      `json:"bytes_total"`
	Error          string     `json:"error"`
	RowCountsSrc   string     `json:"row_counts_src"`
	RowCountsTgt   string     `json:"row_counts_tgt"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// TelemetrySample is one buffered metric sample for the in-panel dashboard.
// Labels is a JSON object string.
type TelemetrySample struct {
	ID     int64     `json:"id"`
	TS     time.Time `json:"ts"`
	Metric string    `json:"metric"`
	Value  float64   `json:"value"`
	Labels string    `json:"labels"`
}
