package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// fakeStore is an in-memory Store for config tests.
type fakeStore struct {
	kv map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{kv: map[string]string{}} }

func (f *fakeStore) AllConfig(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(f.kv))
	for k, v := range f.kv {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) SetConfig(ctx context.Context, key, value string) error {
	f.kv[key] = value
	return nil
}

func TestDefaultIsPrivateAndValid(t *testing.T) {
	cfg := Default()
	require.NoError(t, cfg.Validate())
	require.Equal(t, DefaultBindAddr, cfg.BindAddr)
	require.Equal(t, DefaultQueryLimit, cfg.QueryLimit)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()

	cfg := Default()
	cfg.OTLPEndpoint = "http://collector:4318"
	cfg.Backup.Bucket = "my-pg-backups"
	cfg.Backup.SecretKey = "shhh"
	cfg.RetentionDays = 30
	cfg.StatementTimeout = 15 * time.Second

	require.NoError(t, Save(ctx, st, cfg))

	got, err := Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "http://collector:4318", got.OTLPEndpoint)
	require.Equal(t, "my-pg-backups", got.Backup.Bucket)
	require.Equal(t, "shhh", got.Backup.SecretKey)
	require.Equal(t, 30, got.RetentionDays)
	require.Equal(t, 15*time.Second, got.StatementTimeout)
}

func TestPrivateBindRule(t *testing.T) {
	tests := []struct {
		addr    string
		private bool
	}{
		{"127.0.0.1:8443", true},
		{"localhost:8443", true},
		{"10.0.0.5:8443", true},
		{"192.168.1.10:8443", true},
		{"100.64.0.1:8443", true}, // Tailscale CGNAT
		{"db.internal:8443", true},
		{"0.0.0.0:8443", false},
		{"8.8.8.8:8443", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			cfg := Default()
			cfg.BindAddr = tc.addr
			err := cfg.Validate()
			if tc.private {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, core.CodeSafety, core.CodeOf(err))
			}
		})
	}
}

func TestForcePublicBindOverride(t *testing.T) {
	cfg := Default()
	cfg.BindAddr = "0.0.0.0:8443"
	require.Error(t, cfg.Validate())
	cfg.ForcePublicBind = true
	require.NoError(t, cfg.Validate())
}

func TestEnvOverride(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	require.NoError(t, Save(ctx, st, Default()))

	t.Setenv("PGPANEL_BIND_ADDR", "10.1.2.3:9000")
	t.Setenv("PGPANEL_S3_BUCKET", "env-bucket")

	got, err := Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "10.1.2.3:9000", got.BindAddr)
	require.Equal(t, "env-bucket", got.Backup.Bucket)
}

func TestValidateRejectsBadValues(t *testing.T) {
	cfg := Default()
	cfg.QueryLimit = 0
	require.Error(t, cfg.Validate())

	cfg = Default()
	cfg.BindAddr = "not-a-host-port"
	require.Error(t, cfg.Validate())
}
