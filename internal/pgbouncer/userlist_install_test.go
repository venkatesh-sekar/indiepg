package pgbouncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// validVerifier is a well-formed SCRAM-SHA-256 verifier (alphabet-only) usable in
// userlist install tests; it is not a real secret.
const validVerifier = "SCRAM-SHA-256$4096:c2FsdHNhbHQ=$c3RvcmVka2V5:c2VydmVya2V5"

func validUserlist() []UserlistEntry {
	return []UserlistEntry{{Username: "app", Verifier: validVerifier}}
}

func TestEnsureUserlist_WritesFile0640(t *testing.T) {
	m := newInstallManager(t)

	changed, err := m.EnsureUserlist(context.Background(), validUserlist())
	require.NoError(t, err)
	require.True(t, changed)

	data, err := os.ReadFile(m.UserlistPath())
	require.NoError(t, err)
	require.Equal(t, "\"app\" \""+validVerifier+"\"\n", string(data),
		"auth_file must be the exact RenderUserlist output")

	info, err := os.Stat(m.UserlistPath())
	require.NoError(t, err)
	require.Equal(t, userlistFileMode, info.Mode().Perm(), "auth_file must be 0640 (holds SCRAM verifiers)")
}

func TestEnsureUserlist_IdempotentNoRewriteWhenUnchanged(t *testing.T) {
	m := newInstallManager(t)

	// Two users, written once.
	initial := []UserlistEntry{
		{Username: "app", Verifier: validVerifier},
		{Username: "zapp", Verifier: validVerifier},
	}
	changed, err := m.EnsureUserlist(context.Background(), initial)
	require.NoError(t, err)
	require.True(t, changed)

	before, err := os.Stat(m.UserlistPath())
	require.NoError(t, err)

	// Re-submit the SAME set in reverse input order: RenderUserlist sorts entries,
	// so the on-disk content is byte-identical → a true no-op (no reload needed).
	reordered := []UserlistEntry{
		{Username: "zapp", Verifier: validVerifier},
		{Username: "app", Verifier: validVerifier},
	}
	changed, err = m.EnsureUserlist(context.Background(), reordered)
	require.NoError(t, err)
	require.False(t, changed, "an unchanged (deterministic) auth_file must be a no-op")

	after, err := os.Stat(m.UserlistPath())
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "no-op must not rewrite the file")
}

func TestEnsureUserlist_RewritesWhenChanged(t *testing.T) {
	m := newInstallManager(t)

	_, err := m.EnsureUserlist(context.Background(), validUserlist())
	require.NoError(t, err)

	// Adding a user changes the content → a rewrite.
	entries := []UserlistEntry{
		{Username: "app", Verifier: validVerifier},
		{Username: "report", Verifier: validVerifier},
	}
	changed, err := m.EnsureUserlist(context.Background(), entries)
	require.NoError(t, err)
	require.True(t, changed)

	data, _ := os.ReadFile(m.UserlistPath())
	require.Contains(t, string(data), "\"report\"")
}

// TestEnsureUserlist_RefusesSymlinkAtPath proves O_NOFOLLOW refuses to follow a
// symlink planted at the auth_file path (a path-hijack guard) and reports a clear
// conflict — writing nothing through it (the target keeps its bytes).
func TestEnsureUserlist_RefusesSymlinkAtPath(t *testing.T) {
	m := newInstallManager(t)

	target := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(target, []byte("secret\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(m.UserlistPath()), 0o755))
	require.NoError(t, os.Symlink(target, m.UserlistPath()))

	changed, err := m.EnsureUserlist(context.Background(), validUserlist())
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeConflict, core.CodeOf(err))

	data, _ := os.ReadFile(target)
	require.Equal(t, "secret\n", string(data), "symlink target must be untouched")
}

// TestEnsureUserlist_RejectsBadEntryBeforeAnyWrite proves a non-SCRAM verifier is
// caught at render time (no auth downgrade) and no file is written.
func TestEnsureUserlist_RejectsBadEntryBeforeAnyWrite(t *testing.T) {
	m := newInstallManager(t)

	entries := []UserlistEntry{{Username: "app", Verifier: "md5deadbeef"}}
	changed, err := m.EnsureUserlist(context.Background(), entries)
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	_, statErr := os.Stat(m.UserlistPath())
	require.True(t, os.IsNotExist(statErr), "no file may be written when rendering is rejected")
}

// TestEnsureUserlist_RejectsEmptyBeforeAnyWrite proves an empty user set (which
// would lock every app out of the pooler) is refused and nothing is written.
func TestEnsureUserlist_RejectsEmptyBeforeAnyWrite(t *testing.T) {
	m := newInstallManager(t)

	changed, err := m.EnsureUserlist(context.Background(), nil)
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))

	_, statErr := os.Stat(m.UserlistPath())
	require.True(t, os.IsNotExist(statErr))
}

func TestEnsureUserlist_RequiresRunner(t *testing.T) {
	m := New(Options{ConfDir: t.TempDir()}) // no Runner
	_, err := m.EnsureUserlist(context.Background(), validUserlist())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestUserlistPath_DefaultsToEtcPgbouncer(t *testing.T) {
	m := New(Options{Runner: exec.NewFakeRunner()})
	require.Equal(t, "/etc/pgbouncer/userlist.txt", m.UserlistPath())
}
