package server

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
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

	cfg, genPw, _, err := installCore(ctx, st, core.Discard(), "web-db-01", "127.0.0.1:9000", testPassword)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9000", cfg.BindAddr)
	require.Empty(t, genPw, "an explicit password must not be echoed back")

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

func TestInstallCoreGeneratesPasswordWhenEmpty(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// An empty password is not rejected: install generates a strong one, sets
	// it, and returns the plaintext for one-time display. The blank input must
	// never become the admin password.
	_, genPw, _, err := installCore(ctx, st, core.Discard(), "label", "", "   ")
	require.NoError(t, err)
	require.Len(t, genPw, 48, "a generated password is returned for one-time display")

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	_, err = authn.Authenticate(ctx, "")
	require.Error(t, err, "blank password must not authenticate")
	_, err = authn.Authenticate(ctx, "   ")
	require.Error(t, err, "the blank input must not have been set as the password")

	// The returned generated password is exactly what got stored.
	_, err = authn.Authenticate(ctx, genPw)
	require.NoError(t, err, "the returned generated password must authenticate")
}

func TestResolveAdminPassword(t *testing.T) {
	// Empty / blank inputs are generated; explicit values pass through.
	got, gen := resolveAdminPassword("")
	require.True(t, gen)
	require.Len(t, got, 48)

	got, gen = resolveAdminPassword("   ")
	require.True(t, gen)
	require.Len(t, got, 48)

	got, gen = resolveAdminPassword("explicit-override-pw")
	require.False(t, gen)
	require.Equal(t, "explicit-override-pw", got)
}

func TestInstallCoreRejectsPublicBind(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "0.0.0.0:8443", testPassword)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestInstallCoreIsIdempotentOnIdentity(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	_, _, _, err := installCore(ctx, st, core.Discard(), "first", "", testPassword)
	require.NoError(t, err)
	first, err := st.GetInstance(ctx)
	require.NoError(t, err)

	// Re-running install must reuse the existing identity, not regenerate it.
	_, _, _, err = installCore(ctx, st, core.Discard(), "second", "", testPassword)
	require.NoError(t, err)
	second, err := st.GetInstance(ctx)
	require.NoError(t, err)

	require.Equal(t, first.InstanceID, second.InstanceID)
	require.Equal(t, first.Label, second.Label, "label should not change on re-install")
}

func TestInstallCoreKeepsExistingPasswordOnReinstall(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// First install on a fresh box generates and returns a password.
	_, genPw, outcome, err := installCore(ctx, st, core.Discard(), "label", "", "")
	require.NoError(t, err)
	require.Equal(t, pwGenerated, outcome)
	require.NotEmpty(t, genPw)

	// Re-running install with no --password must NOT rotate the credential — this
	// is what makes `update` (and any idempotent re-install) safe.
	_, genPw2, outcome2, err := installCore(ctx, st, core.Discard(), "label", "", "")
	require.NoError(t, err)
	require.Equal(t, pwKept, outcome2)
	require.Empty(t, genPw2, "a kept password must not be echoed")

	// The original password still authenticates.
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	_, err = authn.Authenticate(ctx, genPw)
	require.NoError(t, err, "re-install must leave the existing password working")
}

func TestInstallCoreExplicitPasswordOverridesOnReinstall(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Fresh install generates one.
	_, genPw, _, err := installCore(ctx, st, core.Discard(), "label", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, genPw)

	// An explicit --password on a re-install is still honored as an override.
	const override = "operator-chosen-override-pw"
	_, genPw2, outcome, err := installCore(ctx, st, core.Discard(), "label", "", override)
	require.NoError(t, err)
	require.Equal(t, pwProvided, outcome)
	require.Empty(t, genPw2, "an explicit password is never echoed")

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	_, err = authn.Authenticate(ctx, override)
	require.NoError(t, err)
	_, err = authn.Authenticate(ctx, genPw)
	require.Error(t, err, "the override must have replaced the generated password")
}

func TestInstallCoreDefaultsBindWhenEmpty(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	cfg, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
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

func TestResetPasswordGeneratesWhenEmpty(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Install first so an auth record exists, then reset with a blank password:
	// it must generate a fresh one (no error) and rotate away from the old one.
	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)

	require.NoError(t, ResetPassword(ctx, st, core.Discard(), "   "))

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	// The old install password no longer works — a generated one replaced it.
	_, err = authn.Authenticate(ctx, testPassword)
	require.Error(t, err, "blank reset must rotate to a new generated password")
	// And a blank password is never what got set.
	_, err = authn.Authenticate(ctx, "")
	require.Error(t, err)
}

func TestResetPasswordChangesPassword(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
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

func TestInstallCoreNamespacesBackupPrefixWhenBucketSet(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Seed config with a bucket but no explicit prefix, the out-of-the-box
	// shape once an operator points the panel at a bucket.
	seeded := config.Default()
	seeded.Backup.Bucket = "my-backups"
	require.NoError(t, config.Save(ctx, st, seeded))

	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)

	loaded, err := config.Load(ctx, st)
	require.NoError(t, err)
	inst, err := st.GetInstance(ctx)
	require.NoError(t, err)

	// Defense layer 1: prefix is namespaced by instance id.
	require.Equal(t, "panel/"+inst.InstanceID, loaded.Backup.Prefix)
}

func TestInstallCorePreservesExplicitBackupPrefix(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	seeded := config.Default()
	seeded.Backup.Bucket = "my-backups"
	seeded.Backup.Prefix = "operator/chosen"
	require.NoError(t, config.Save(ctx, st, seeded))

	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)

	loaded, err := config.Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, "operator/chosen", loaded.Backup.Prefix,
		"an explicit operator prefix must not be overwritten")
}

func TestInstallCoreLeavesPrefixEmptyWithoutBucket(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// No bucket configured -> nothing to namespace.
	_, _, _, err := installCore(ctx, st, core.Discard(), "label", "", testPassword)
	require.NoError(t, err)

	loaded, err := config.Load(ctx, st)
	require.NoError(t, err)
	require.Empty(t, loaded.Backup.Prefix)
}

func TestResetDecision(t *testing.T) {
	const me = 1000
	const other = 1001

	// Root may always reset.
	require.NoError(t, resetDecision(0, other, 0o644))

	// Owner of a 0600 (or tighter) file may reset.
	require.NoError(t, resetDecision(me, me, 0o600))
	require.NoError(t, resetDecision(me, me, 0o400))
	require.NoError(t, resetDecision(me, me, 0o000))

	// Owner but file too permissive -> refused (CodeSafety).
	err := resetDecision(me, me, 0o644)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))

	// Not the owner and not root -> refused.
	err = resetDecision(me, other, 0o600)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))

	// Unknown owner (ownerUID < 0) and not root -> refused.
	err = resetDecision(me, -1, 0o600)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestAuthorizeResetRefusesNonOwnerOfFileStore(t *testing.T) {
	// A file-backed store owned by the current user but left world-readable
	// must be refused when not running as root. We only assert the refusal
	// branch when the test is not root and the file is loosened past 0600.
	if os.Geteuid() == 0 {
		t.Skip("running as root: ownership gate is satisfied unconditionally")
	}

	dir := t.TempDir()
	path := dir + "/state.db"
	st, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// store.Open hardens to 0600; loosen it to simulate a permissive umask so
	// the non-root owner check fails.
	require.NoError(t, os.Chmod(path, 0o644))

	err = authorizeReset(st)
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestAuthorizeResetAllowsInMemoryStore(t *testing.T) {
	// :memory: has no backing file, so there is no ownership boundary to
	// enforce; authorizeReset must not block tests/ephemeral runs.
	st := openTestStore(t)
	require.NoError(t, authorizeReset(st))
}

func TestEnsureAdminPasswordGeneratesOnFirstRun(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Fresh store: no auth record yet.
	_, err := st.GetAuth(ctx)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	generated, err := EnsureAdminPassword(ctx, st, core.Discard())
	require.NoError(t, err)
	require.True(t, generated, "first run must generate a password")

	// Auth is now initialized so the panel is loginable.
	_, err = st.GetAuth(ctx)
	require.NoError(t, err)

	// Second call is a no-op — it must not rotate an existing password.
	generated, err = EnsureAdminPassword(ctx, st, core.Discard())
	require.NoError(t, err)
	require.False(t, generated)
}

func TestEnsureAdminPasswordNoOpWhenAlreadySet(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	require.NoError(t, authn.SetPassword(ctx, "an-existing-password"))

	generated, err := EnsureAdminPassword(ctx, st, core.Discard())
	require.NoError(t, err)
	require.False(t, generated)

	// The existing password is untouched.
	_, err = authn.Authenticate(ctx, "an-existing-password")
	require.NoError(t, err)
}
