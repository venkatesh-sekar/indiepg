package backup

import (
	"context"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

const (
	// defaultConfDir is where pgBackRest looks for its config by default. indiepg
	// writes a single managed file here.
	defaultConfDir = "/etc/pgbackrest"
	// confFileName is the managed config file name within confDir.
	confFileName = "pgbackrest.conf"
	// confFileMode is owner read/write only: the file carries the S3 secret (and,
	// when enabled, the repo cipher passphrase), so no group/other access.
	confFileMode os.FileMode = 0o600
	// confDirMode is the managed directory mode (no secrets in the dir itself).
	confDirMode os.FileMode = 0o755
	// stanzaCreateTimeout bounds the stanza-create call, which reaches the repo.
	stanzaCreateTimeout = 2 * time.Minute
)

// ConfigPath is the absolute path to the managed pgBackRest config file.
func (m *Manager) ConfigPath() string {
	return filepath.Join(m.confDir, confFileName)
}

// EnsureConfigured renders the pgBackRest config from p, writes it to the
// managed config file (0600, owned by the pgBackRest OS user), and — when the
// file actually changed — runs `pgbackrest stanza-create` so the repository is
// initialized. It returns whether the config changed.
//
// Safety properties:
//   - It NEVER overwrites a config file that lacks indiepg's managed marker, so
//     an operator's hand-written /etc/pgbackrest/pgbackrest.conf is preserved and
//     surfaced as a conflict rather than clobbered.
//   - The file is written atomically (temp + rename) at 0600 and chown'd to the
//     pgBackRest user; under root a failed chown is fatal (a root-owned 0600 file
//     the postgres process cannot read would silently break every backup).
//   - The rendered text is deterministic, so an unchanged config is a no-op and
//     does not re-run stanza-create.
func (m *Manager) EnsureConfigured(ctx context.Context, p ConfigParams) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("backup: EnsureConfigured requires a Runner")
	}

	desired, err := RenderConfig(p)
	if err != nil {
		return false, err
	}

	path := m.ConfigPath()
	existing, err := readNoFollow(path)
	switch {
	case err == nil:
		// A file we did not write is sacrosanct: do not clobber operator config.
		if !HasManagedMarker(string(existing)) {
			return false, core.ConflictError(
				"refusing to overwrite existing pgBackRest config %q that indiepg did not create", path,
			).WithHint("move or remove the hand-written config (or its repo settings into /etc/pgbackrest/conf.d/) so indiepg can manage it")
		}
		if string(existing) == desired {
			return false, nil // already current; no rewrite, no stanza-create.
		}
	case os.IsNotExist(err):
		// First write; fall through.
	default:
		return false, core.InternalError("backup: read pgBackRest config %q", path).Wrap(err)
	}

	if err := m.writeConfigFile(path, []byte(desired)); err != nil {
		return false, err
	}

	if err := m.stanzaCreate(ctx, p.Stanza, path); err != nil {
		return false, err
	}
	m.log.InfoCtx(ctx, "configured pgBackRest", "stanza", p.Stanza, "path", path, "remote", p.RemoteConfigured())
	return true, nil
}

// readNoFollow reads the config file refusing to traverse a symlink at the
// final path component (O_NOFOLLOW). A symlink planted at the config path —
// pointing at, say, /etc/shadow — therefore errors loudly instead of being read
// (and potentially mis-classified by the managed-marker guard). A missing file
// returns an os.IsNotExist error, which the caller treats as a first write.
func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// writeConfigFile creates confDir if needed and atomically replaces the config
// file with data at confFileMode, owned by the pgBackRest user.
func (m *Manager) writeConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, confDirMode); err != nil {
		return core.InternalError("backup: create config dir %q", dir).Wrap(err)
	}

	tmp, err := os.CreateTemp(dir, ".indiepg-pgbackrest-*.tmp")
	if err != nil {
		return core.InternalError("backup: create temp config in %q", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename (no-op once renamed).
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return core.InternalError("backup: write temp config").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return core.InternalError("backup: close temp config").Wrap(err)
	}

	// Tighten perms BEFORE the secret-bearing file is linked into place. CreateTemp
	// already makes it 0600, but we set it explicitly so the invariant is local.
	if err := os.Chmod(tmpName, confFileMode); err != nil {
		return core.InternalError("backup: chmod temp config").Wrap(err)
	}
	if err := chownToPGUser(tmpName); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return core.InternalError("backup: install config %q", path).Wrap(err)
	}
	return nil
}

// chownToPGUser hands the file to the pgBackRest OS user so the (non-root)
// postgres process can read it. Under root a failure is fatal: a root-owned 0600
// config the postgres user cannot read would break every backup silently. When
// not running as root (tests, hands-on dev) the chown is best-effort — the
// process likely already owns the file — and a failure is logged, not fatal.
func chownToPGUser(path string) error {
	uid, gid, lookupErr := pgUserIDs()
	if lookupErr == nil {
		if err := os.Chown(path, uid, gid); err != nil {
			if os.Geteuid() == 0 {
				return core.InternalError("backup: chown config to %s", pgUser).Wrap(err)
			}
			// Non-root: cannot chown to another user; rely on existing ownership.
			return nil
		}
		return nil
	}
	if os.Geteuid() == 0 {
		return core.InternalError("backup: resolve %s OS user for config ownership", pgUser).Wrap(lookupErr)
	}
	return nil
}

// pgUserIDs resolves the numeric uid/gid of the pgBackRest OS user.
func pgUserIDs() (int, int, error) {
	u, err := user.Lookup(pgUser)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// stanzaCreate initializes the repository for the stanza. It is idempotent in
// pgBackRest (a no-op when the stanza already exists with matching settings) and
// is pointed explicitly at the managed config file so it never picks up a
// different one. The argv carries no secret (those live in the 0600 file), so it
// is not marked Sensitive.
func (m *Manager) stanzaCreate(ctx context.Context, stanza, configPath string) error {
	if err := validateStanza(stanza); err != nil {
		return err
	}
	_, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    pgbackrestBin,
		Args:    []string{"--config=" + configPath, "--stanza=" + stanza, "stanza-create"},
		AsUser:  pgUser,
		Timeout: stanzaCreateTimeout,
	})
	if err != nil {
		return core.ExecError("backup: stanza-create for %q failed", stanza).
			WithHint("verify Postgres is running and the S3 credentials/bucket are correct; " +
				"if it reports an existing or partial repo, run `pgbackrest --stanza=" + stanza + " stanza-delete` and retry").Wrap(err)
	}
	return nil
}
