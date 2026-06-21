package config

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Store is the minimal store surface config needs. The concrete
// *store.Store satisfies it; tests can supply a fake.
type Store interface {
	AllConfig(ctx context.Context) (map[string]string, error)
	SetConfig(ctx context.Context, key, value string) error
}

// Config key names persisted in the store.
const (
	keyBindAddr         = "bind_addr"
	keyForcePublicBind  = "force_public_bind"
	keyOTLPEndpoint     = "otlp_endpoint"
	keyOTLPInsecure     = "otlp_insecure"
	keyStanza           = "stanza"
	keyRetentionDays    = "retention_days"
	keyStatementTimeout = "statement_timeout_ms"
	keyQueryLimit       = "query_limit"
	keyPGSocketDir      = "pg_socket_dir"

	keyS3Endpoint  = "backup_s3_endpoint"
	keyS3Region    = "backup_s3_region"
	keyS3Bucket    = "backup_s3_bucket"
	keyS3Prefix    = "backup_s3_prefix"
	keyS3AccessKey = "backup_s3_access_key"
	keyS3SecretKey = "backup_s3_secret_key"
	keyS3UseSSL    = "backup_s3_use_ssl"

	keySchedFull        = "sched_full_backup"
	keySchedIncremental = "sched_incremental_backup"
	keySchedRestoreTest = "sched_restore_test"
	keySchedTelemetry   = "sched_telemetry_sample"
	keySchedDigest      = "sched_digest"
)

// Load reads configuration from the store, applies environment-variable
// overrides (INDIEPG_*), and validates it. Missing keys fall back to defaults.
func Load(ctx context.Context, st Store) (Config, error) {
	cfg := Default()

	kv, err := st.AllConfig(ctx)
	if err != nil {
		return Config{}, err
	}
	applyMap(&cfg, kv)
	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save persists every field of cfg to the store as key/value rows.
func Save(ctx context.Context, st Store, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	pairs := map[string]string{
		keyBindAddr:         cfg.BindAddr,
		keyForcePublicBind:  strconv.FormatBool(cfg.ForcePublicBind),
		keyOTLPEndpoint:     cfg.OTLPEndpoint,
		keyOTLPInsecure:     strconv.FormatBool(cfg.OTLPInsecure),
		keyStanza:           cfg.Stanza,
		keyRetentionDays:    strconv.Itoa(cfg.RetentionDays),
		keyStatementTimeout: strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10),
		keyQueryLimit:       strconv.Itoa(cfg.QueryLimit),
		keyPGSocketDir:      cfg.PGSocketDir,
		keyS3Endpoint:       cfg.Backup.Endpoint,
		keyS3Region:         cfg.Backup.Region,
		keyS3Bucket:         cfg.Backup.Bucket,
		keyS3Prefix:         cfg.Backup.Prefix,
		keyS3AccessKey:      cfg.Backup.AccessKey,
		keyS3SecretKey:      cfg.Backup.SecretKey,
		keyS3UseSSL:         strconv.FormatBool(cfg.Backup.UseSSL),
		keySchedFull:        cfg.Schedules.FullBackup,
		keySchedIncremental: cfg.Schedules.IncrementalBackup,
		keySchedRestoreTest: cfg.Schedules.RestoreTest,
		keySchedTelemetry:   cfg.Schedules.TelemetrySample,
		keySchedDigest:      cfg.Schedules.Digest,
	}
	for k, v := range pairs {
		if err := st.SetConfig(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

func applyMap(cfg *Config, kv map[string]string) {
	setStr(kv, keyBindAddr, &cfg.BindAddr)
	setBool(kv, keyForcePublicBind, &cfg.ForcePublicBind)
	setStr(kv, keyOTLPEndpoint, &cfg.OTLPEndpoint)
	setBool(kv, keyOTLPInsecure, &cfg.OTLPInsecure)
	setStr(kv, keyStanza, &cfg.Stanza)
	setInt(kv, keyRetentionDays, &cfg.RetentionDays)
	setMillis(kv, keyStatementTimeout, &cfg.StatementTimeout)
	setInt(kv, keyQueryLimit, &cfg.QueryLimit)
	setStr(kv, keyPGSocketDir, &cfg.PGSocketDir)

	setStr(kv, keyS3Endpoint, &cfg.Backup.Endpoint)
	setStr(kv, keyS3Region, &cfg.Backup.Region)
	setStr(kv, keyS3Bucket, &cfg.Backup.Bucket)
	setStr(kv, keyS3Prefix, &cfg.Backup.Prefix)
	setStr(kv, keyS3AccessKey, &cfg.Backup.AccessKey)
	setStr(kv, keyS3SecretKey, &cfg.Backup.SecretKey)
	setBool(kv, keyS3UseSSL, &cfg.Backup.UseSSL)

	setStr(kv, keySchedFull, &cfg.Schedules.FullBackup)
	setStr(kv, keySchedIncremental, &cfg.Schedules.IncrementalBackup)
	setStr(kv, keySchedRestoreTest, &cfg.Schedules.RestoreTest)
	setStr(kv, keySchedTelemetry, &cfg.Schedules.TelemetrySample)
	setStr(kv, keySchedDigest, &cfg.Schedules.Digest)
}

// applyEnv overlays INDIEPG_* environment overrides (highest precedence).
func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv("INDIEPG_BIND_ADDR"); ok {
		cfg.BindAddr = v
	}
	if v, ok := os.LookupEnv("INDIEPG_FORCE_PUBLIC_BIND"); ok {
		cfg.ForcePublicBind = truthy(v)
	}
	if v, ok := os.LookupEnv("INDIEPG_OTLP_ENDPOINT"); ok {
		cfg.OTLPEndpoint = v
	}
	if v, ok := os.LookupEnv("INDIEPG_OTLP_INSECURE"); ok {
		cfg.OTLPInsecure = truthy(v)
	}
	if v, ok := os.LookupEnv("INDIEPG_S3_BUCKET"); ok {
		cfg.Backup.Bucket = v
	}
	if v, ok := os.LookupEnv("INDIEPG_S3_ENDPOINT"); ok {
		cfg.Backup.Endpoint = v
	}
	if v, ok := os.LookupEnv("INDIEPG_S3_ACCESS_KEY"); ok {
		cfg.Backup.AccessKey = v
	}
	if v, ok := os.LookupEnv("INDIEPG_S3_SECRET_KEY"); ok {
		cfg.Backup.SecretKey = v
	}
}

// Validate checks the configuration for safety and sanity, most importantly the
// private-by-default bind rule.
func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.BindAddr)
	if err != nil {
		return core.ValidationError("invalid bind address %q", c.BindAddr).Wrap(err)
	}
	if !c.ForcePublicBind && !isPrivateBind(host) {
		return core.NewSafetyError(
			"bind to public address",
			[]string{"force_public_bind=true"},
			"refusing to bind to non-private address %q; set force_public_bind to override", host,
		)
	}
	if c.RetentionDays < 0 {
		return core.ValidationError("retention_days must be >= 0")
	}
	if c.QueryLimit < 1 {
		return core.ValidationError("query_limit must be >= 1")
	}
	if c.StatementTimeout < 0 {
		return core.ValidationError("statement_timeout must be >= 0")
	}
	return nil
}

// isPrivateBind reports whether host is a loopback, unspecified-but-local, or
// RFC1918/CGNAT/private address suitable for private-by-default binding.
func isPrivateBind(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A non-IP hostname (e.g. a Tailscale MagicDNS name) is treated as
		// private; operators choosing a DNS name are opting into a known host.
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	// Carrier-grade NAT range 100.64.0.0/10 (Tailscale).
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func setStr(kv map[string]string, key string, dst *string) {
	if v, ok := kv[key]; ok {
		*dst = v
	}
}

func setBool(kv map[string]string, key string, dst *bool) {
	if v, ok := kv[key]; ok {
		*dst = truthy(v)
	}
}

func setInt(kv map[string]string, key string, dst *int) {
	if v, ok := kv[key]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func setMillis(kv map[string]string, key string, dst *time.Duration) {
	if v, ok := kv[key]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*dst = time.Duration(n) * time.Millisecond
		}
	}
}
