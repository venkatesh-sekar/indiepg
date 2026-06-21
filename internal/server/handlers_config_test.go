package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/config"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
func intp(n int) *int       { return &n }

func TestApplyConfigUpdateOverlaysOnlyProvidedFields(t *testing.T) {
	cfg := config.Default()
	cfg.OTLPEndpoint = "old-endpoint"
	cfg.Backup.Bucket = "old-bucket"
	cfg.Backup.SecretKey = "stored-secret"
	cfg.RetentionDays = 14

	req := updateConfigRequest{
		OTLPEndpoint:  strp("http://collector:4318"),
		RetentionDays: intp(30),
		Backup: &backupTargetUpdate{
			Bucket: strp("new-bucket"),
			UseSSL: boolp(false),
		},
	}
	applyConfigUpdate(&cfg, req)

	require.Equal(t, "http://collector:4318", cfg.OTLPEndpoint)
	require.Equal(t, 30, cfg.RetentionDays)
	require.Equal(t, "new-bucket", cfg.Backup.Bucket)
	require.False(t, cfg.Backup.UseSSL)
	// Unprovided fields are untouched.
	require.Equal(t, config.DefaultQueryLimit, cfg.QueryLimit)
}

func TestApplyConfigUpdateSecretIsWriteOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Backup.SecretKey = "stored-secret"

	t.Run("empty secret preserves stored value", func(t *testing.T) {
		c := cfg
		applyConfigUpdate(&c, updateConfigRequest{
			Backup: &backupTargetUpdate{SecretKey: strp("")},
		})
		require.Equal(t, "stored-secret", c.Backup.SecretKey)
	})

	t.Run("nil secret preserves stored value", func(t *testing.T) {
		c := cfg
		applyConfigUpdate(&c, updateConfigRequest{
			Backup: &backupTargetUpdate{Bucket: strp("b")},
		})
		require.Equal(t, "stored-secret", c.Backup.SecretKey)
	})

	t.Run("non-empty secret overwrites", func(t *testing.T) {
		c := cfg
		applyConfigUpdate(&c, updateConfigRequest{
			Backup: &backupTargetUpdate{SecretKey: strp("new-secret")},
		})
		require.Equal(t, "new-secret", c.Backup.SecretKey)
	})
}

func TestApplyConfigUpdateSchedules(t *testing.T) {
	cfg := config.Default()
	req := updateConfigRequest{
		Schedules: &schedulesUpdate{
			FullBackup: strp("0 4 * * 0"),
			Digest:     strp(""),
		},
	}
	applyConfigUpdate(&cfg, req)
	require.Equal(t, "0 4 * * 0", cfg.Schedules.FullBackup)
	require.Equal(t, "", cfg.Schedules.Digest)
	// Untouched schedule retains its default.
	require.Equal(t, config.Default().Schedules.IncrementalBackup, cfg.Schedules.IncrementalBackup)
}

func TestApplyConfigUpdateEmptyRequestIsNoop(t *testing.T) {
	cfg := config.Default()
	cfg.Stanza = "custom"
	before := cfg
	applyConfigUpdate(&cfg, updateConfigRequest{})
	require.Equal(t, before, cfg)
}
