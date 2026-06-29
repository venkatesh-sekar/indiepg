package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
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

// SaveTuningProfile writes ONLY the workload-profile key, leaving every other
// persisted field untouched. This is the narrow single-key path handleApplyTuning
// uses after Postgres is already retuned, so it must not depend on (or disturb) the
// rest of the config, and a subsequent Load must read the new profile back.
func TestSaveTuningProfile_WritesOnlyTheProfileKey(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()

	// Seed unrelated state so we can prove the single-key write leaves it alone.
	st.kv[keyBindAddr] = "10.0.0.5:8443"
	st.kv[keyS3Bucket] = "my-pg-backups"

	require.NoError(t, SaveTuningProfile(ctx, st, "olap"))

	require.Equal(t, "olap", st.kv[keyTuningProfile])
	require.Equal(t, "10.0.0.5:8443", st.kv[keyBindAddr], "unrelated keys must be untouched")
	require.Equal(t, "my-pg-backups", st.kv[keyS3Bucket], "unrelated keys must be untouched")

	got, err := Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "olap", got.TuningProfile)
}

// A full config Save must NOT write the tuning_profile key — it is independently
// managed by SaveTuningProfile alone. Otherwise a PUT /config (which does Load then
// a whole-config Save) would silently restore a stale profile over one a tuning
// apply just persisted, while Postgres stays on the new one. We seed a profile, run
// a full Save carrying a DIFFERENT in-memory value, and prove the stored key is
// left untouched.
func TestSaveDoesNotWriteTuningProfile(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()

	// A tuning apply already persisted "olap" via the dedicated single-key path.
	st.kv[keyTuningProfile] = "olap"

	// A full config Save (e.g. PUT /config carrying the default-loaded "mixed")
	// must not overwrite that independently-managed key.
	cfg := Default() // TuningProfile == "mixed"
	require.NoError(t, Save(ctx, st, cfg))

	require.Equal(t, "olap", st.kv[keyTuningProfile],
		"full Save must leave tuning_profile alone; only SaveTuningProfile owns it")

	// And it is still read back on Load, so the round-trip is intact.
	got, err := Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "olap", got.TuningProfile)
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

	t.Setenv("INDIEPG_BIND_ADDR", "10.1.2.3:9000")
	t.Setenv("INDIEPG_S3_BUCKET", "env-bucket")

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
