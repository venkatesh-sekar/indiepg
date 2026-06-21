// Package config holds the panel's typed configuration: where it binds (private
// by default), the OTLP endpoint, the S3 backup target, backup schedules, and
// retention. Values are persisted as key/value rows in the store and may be
// overridden by environment variables at load time.
package config

import (
	"time"
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
