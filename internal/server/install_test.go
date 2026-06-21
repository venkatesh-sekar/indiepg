package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/auth"
	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestInstallCoreSetsIdentityConfigAndPassword(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	cfg, err := installCore(ctx, st, core.Discard(), "web-db-01", "127.0.0.1:9000", testPassword)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9000", cfg.BindAddr)

	// Identity persisted.
	inst, err := st.GetInstance(ctx)
	require.NoError(t, err)
	require.Equal(t, "web-db-01", inst.Label)
	require.NotEmpty(t, inst.InstanceID)

	// Config persisted with the chosen bind addr.
	loaded, err := config.Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9000", loaded.BindAddr)

	// Password set: login should succeed.
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	_, err = authn.Authenticate(ctx, testPassword)
	require.NoError(t, err)
}

func TestInstallCoreRejectsEmptyPassword(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	_, err := installCore(ctx, st, core.Discard(), "", "", "")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestInstallCoreRejectsPublicBind(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	_, err := installCore(ctx, st, core.Discard(), "label", "0.0.0.0:8443", testPassword)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestInstallCoreIsIdempotentOnIdentity(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	_, err := installCore(ctx, st, core.Discard(), "first", "", testPassword)
	require.NoError(t, err)
	first, err := st.GetInstance(ctx)
	require.NoError(t, err)

	// Re-running install must reuse the existing identity, not regenerate it.
	_, err = installCore(ctx, st, core.Discard(), "second", "", testPassword)
	require.NoError(t, err)
	second, err := st.GetInstance(ctx)
	require.NoError(t, err)

	require.Equal(t, first.InstanceID, second.InstanceID)
	require.Equal(t, first.Label, second.Label, "label should not change on re-install")
}

func TestInstallCoreDefaultsBindWhenEmpty(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)
	require.Equal(t, config.DefaultBindAddr, cfg.BindAddr)
}

func TestResetPasswordRequiresInstalled(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	// No auth row yet.
	err := ResetPassword(ctx, st, core.Discard(), "new-password-123")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestResetPasswordRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	err := ResetPassword(ctx, st, core.Discard(), "   ")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestResetPasswordChangesPassword(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	_, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)

	const newPass = "a-brand-new-password-99"
	require.NoError(t, ResetPassword(ctx, st, core.Discard(), newPass))

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	_, err = authn.Authenticate(ctx, newPass)
	require.NoError(t, err)

	// Old password must no longer work.
	_, err = authn.Authenticate(ctx, testPassword)
	require.Error(t, err)

	// An audit row was recorded.
	entries, err := st.ListAudit(ctx, 10, 0)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.Equal(t, "reset_password", entries[0].Action)
}
