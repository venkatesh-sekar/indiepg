// Package config holds the panel's typed configuration: where it binds (private
// by default), the OTLP endpoint, the S3 backup target, backup schedules, and
// retention. Values are persisted as key/value rows in the store and may be
// overridden by environment variables at load time.
package config

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Defaults captured as constants so install and load agree.
const (
	// DefaultBindAddr binds to localhost only — never 0.0.0.0 unless explicitly
	// forced. This is the private-by-default network tenet.
	DefaultBindAddr = "127.0.0.1:8443"

	// DefaultStatementTimeout bounds the read-only query box.
	DefaultStatementTimeout = 30 * time.Second

	// DefaultQueryLimit is the auto-LIMIT applied to unbounded SELECTs.
	DefaultQueryLimit = 1000

	// DefaultStanza is the pgBackRest stanza name.
	DefaultStanza = "main"

	// DefaultRetentionDays is how long backups are kept (design proposal).
	DefaultRetentionDays = 14

	// DefaultTuningProfile is the workload profile a fresh box is sized for. It
	// mirrors pg.ProfileMixed by value — config stays free of any pg import (pg
	// already imports config, so depending on it here would cycle), so the profile
	// is held as a plain string and the server handler converts it via
	// pg.ParseWorkloadProfile. "mixed" is the balanced general-purpose default
	// Provision applies.
	DefaultTuningProfile = "mixed"
)

// S3Target describes an S3-compatible backup destination. Secret is never
// serialized in API responses; it lives only in the store and config.
type S3Target struct {
	Endpoint  string `json:"endpoint"`   // e.g. "s3.us-west-002.backblazeb2.com"
	Region    string `json:"region"`     // e.g. "us-west-002"
	Bucket    string `json:"bucket"`     // bucket name
	Prefix    string `json:"prefix"`     // base key prefix (namespaced by instance id)
	AccessKey string `json:"access_key"` // access key id
	SecretKey string `json:"-"`          // secret access key (never serialized)
	UseSSL    bool   `json:"use_ssl"`    // TLS to the endpoint
	// CipherPass, when set, enables aes-256-cbc repository encryption. It is a
	// secret (never serialized) and must be preserved to restore the repo — losing
	// it makes every encrypted backup unrecoverable.
	CipherPass string `json:"-"`
}

// String renders the target with its two secrets (SecretKey, CipherPass)
// replaced by the redaction marker, so an S3Target — including one reached as
// the Backup field of a Config formatted with %+v — can never leak its
// credentials through a log line, an error string, or any fmt verb. AccessKey is
// an access-key *id* (already serialized to the API), not a secret, so it stays
// visible to keep the rendering useful for debugging.
func (t S3Target) String() string {
	return fmt.Sprintf(
		"S3Target{Endpoint:%q Region:%q Bucket:%q Prefix:%q AccessKey:%q SecretKey:%s CipherPass:%s UseSSL:%t}",
		t.Endpoint, t.Region, t.Bucket, t.Prefix, t.AccessKey,
		core.Redact(t.SecretKey), core.Redact(t.CipherPass), t.UseSSL,
	)
}

// LogValue makes S3Target an slog.LogValuer so structured logging (the panel's
// core.Logger) renders the redacted String() form rather than reflecting into
// the raw secret fields.
func (t S3Target) LogValue() slog.Value { return slog.StringValue(t.String()) }

// GoString makes S3Target an fmt.GoStringer so even the %#v Go-syntax verb —
// which bypasses String() and would otherwise reflect into the raw fields —
// renders the redacted form. Closes the last fmt path that could leak a secret.
func (t S3Target) GoString() string { return t.String() }

// Schedules holds cron expressions (robfig/cron syntax) for periodic jobs. An
// empty expression disables that job.
type Schedules struct {
	FullBackup        string `json:"full_backup"`        // e.g. weekly full
	IncrementalBackup string `json:"incremental_backup"` // e.g. daily incremental
	RestoreTest       string `json:"restore_test"`       // periodic restore verification
	TelemetrySample   string `json:"telemetry_sample"`   // metric sampling loop cadence
	Digest            string `json:"digest"`             // weekly digest
}

// Config is the full panel configuration.
type Config struct {
	// BindAddr is the host:port the web server binds to (private by default).
	BindAddr string `json:"bind_addr"`
	// ForcePublicBind must be true to allow binding a non-loopback/private addr.
	ForcePublicBind bool `json:"force_public_bind"`

	// OTLPEndpoint is the OTLP/HTTP metrics endpoint; empty disables export.
	OTLPEndpoint string `json:"otlp_endpoint"`
	// OTLPInsecure allows plaintext OTLP (e.g. a local collector).
	OTLPInsecure bool `json:"otlp_insecure"`

	// Stanza is the pgBackRest stanza name.
	Stanza string `json:"stanza"`
	// Backup is the S3 destination for pgBackRest.
	Backup S3Target `json:"backup"`
	// RetentionDays is how many days of backups to keep.
	RetentionDays int `json:"retention_days"`
	// Schedules holds the cron expressions for periodic jobs.
	Schedules Schedules `json:"schedules"`

	// TuningProfile is the workload profile (oltp/mixed/olap) the operator has
	// chosen and we have applied to Postgres' host-sized settings. It records
	// "what's chosen"; pg owns "what's actually applied" (read live from
	// pg_settings). Held as a plain string to keep config free of a pg import —
	// the server handler converts it via pg.ParseWorkloadProfile. Defaults to
	// "mixed".
	TuningProfile string `json:"tuning_profile"`

	// StatementTimeout bounds the read-only query box.
	StatementTimeout time.Duration `json:"statement_timeout"`
	// QueryLimit is the auto-LIMIT cap for unbounded SELECTs.
	QueryLimit int `json:"query_limit"`

	// PGSocketDir is the Postgres unix-socket directory used for local conns.
	PGSocketDir string `json:"pg_socket_dir"`
}

// Default returns a Config populated with safe defaults.
func Default() Config {
	return Config{
		BindAddr:         DefaultBindAddr,
		ForcePublicBind:  false,
		Stanza:           DefaultStanza,
		RetentionDays:    DefaultRetentionDays,
		TuningProfile:    DefaultTuningProfile,
		StatementTimeout: DefaultStatementTimeout,
		QueryLimit:       DefaultQueryLimit,
		PGSocketDir:      "/var/run/postgresql",
		Backup:           S3Target{UseSSL: true},
		Schedules: Schedules{
			FullBackup:        "0 3 * * 0", // 03:00 Sundays
			IncrementalBackup: "0 3 * * 1-6",
			RestoreTest:       "0 5 * * 0",
			TelemetrySample:   "@every 30s",
			Digest:            "0 8 * * 1", // Monday 08:00
		},
	}
}
