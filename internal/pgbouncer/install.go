package pgbouncer

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

const (
	// defaultConfDir is where PgBouncer looks for its config by default. indiepg
	// writes a single managed file (pgbouncer.ini) here.
	defaultConfDir = "/etc/pgbouncer"
	// confFileName is the managed config file name within confDir.
	confFileName = "pgbouncer.ini"
	// userlistFileName is the managed auth_file within confDir; it holds the SCRAM
	// verifiers and must match the auth_file path in the rendered pgbouncer.ini
	// (defaultAuthFile in config.go).
	userlistFileName = "userlist.txt"
	// confFileMode matches the `sm` default (DEFAULTS.md): owner read/write,
	// group read, no other access. The file carries no secret (SCRAM verifiers
	// live in the separate auth_file), but the PgBouncer process runs as the
	// pgbouncer group, so group-read lets it read the config without world access.
	confFileMode os.FileMode = 0o640
	// userlistFileMode matches confFileMode (0640, owner pgbouncer): the auth_file
	// holds SCRAM verifiers (secret-adjacent), so it is never world-readable, but
	// the pgbouncer process reads it via its group.
	userlistFileMode os.FileMode = 0o640
	// confDirMode is the managed directory mode (no secrets in the dir itself).
	confDirMode os.FileMode = 0o755
	// bouncerUser is the OS user/group the PgBouncer service runs as; the managed
	// config is handed to it so the (non-root) pooler process can read it.
	bouncerUser = "pgbouncer"
)

// Manager installs and configures the opt-in PgBouncer pooler. Like the pg and
// backup managers, every external side effect goes through an exec.Runner so the
// install/enable flow is unit-testable. The pooler is OFF by default; nothing
// here runs until the operator explicitly enables it.
type Manager struct {
	runner  exec.Runner
	log     *core.Logger
	confDir string
}

// Options configure a Manager. Runner is required for any IO; a nil Logger is
// replaced with a discard logger; an empty ConfDir defaults to /etc/pgbouncer.
type Options struct {
	Runner  exec.Runner
	Logger  *core.Logger
	ConfDir string
}

// New builds a Manager from Options.
func New(opts Options) *Manager {
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}
	dir := opts.ConfDir
	if dir == "" {
		dir = defaultConfDir
	}
	return &Manager{runner: opts.Runner, log: log, confDir: dir}
}

// ConfigPath is the absolute path to the managed pgbouncer.ini.
func (m *Manager) ConfigPath() string {
	return filepath.Join(m.confDir, confFileName)
}

// EnsureConfig renders the pgbouncer.ini from p and installs it atomically,
// returning whether the file changed. It does NOT install the package, start, or
// reload the service — that is the enable flow built on top of this. Splitting
// the write out keeps the riskiest step (touching an on-disk config) isolated and
// fully testable.
//
// Safety properties (mirroring the pgBackRest config installer):
//   - It NEVER overwrites a config file that lacks indiepg's managed marker, so a
//     hand-written /etc/pgbouncer/pgbouncer.ini (operator or distro package) is
//     surfaced as a conflict rather than clobbered.
//   - The file is written atomically (temp + rename) at 0640 and chown'd to the
//     pgbouncer user; under root a failed chown is fatal (a root-owned config the
//     pgbouncer process cannot read would silently break the pooler).
//   - The rendered text is deterministic (RenderConfig fixes the line order), so
//     an unchanged config is a no-op — the enable flow can then skip a needless
//     reload/restart.
func (m *Manager) EnsureConfig(ctx context.Context, p ConfigParams) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pgbouncer: EnsureConfig requires a Runner")
	}

	desired, err := RenderConfig(p)
	if err != nil {
		return false, err
	}

	path := m.ConfigPath()
	existing, err := readNoFollow(path)
	switch {
	case err == nil:
		// A file we did not write is sacrosanct: never clobber operator config.
		if !HasManagedMarker(string(existing)) {
			return false, core.ConflictError(
				"refusing to overwrite existing PgBouncer config %q that indiepg did not create", path,
			).WithHint("move or remove the hand-written pgbouncer.ini so indiepg can manage the pooler")
		}
		if string(existing) == desired {
			return false, nil // already current; no rewrite, no reload.
		}
	case os.IsNotExist(err):
		// First write; fall through.
	case errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR):
		// O_NOFOLLOW refused a symlink (ELOOP) or a non-directory path component
		// (ENOTDIR) at the config path — a possible path-hijack. Surface it loudly
		// as a conflict (not an opaque internal error) and write nothing.
		return false, core.ConflictError(
			"refusing to follow a symlink at the PgBouncer config path %q", path,
		).WithHint("remove the symlink so indiepg can write a real pgbouncer.ini")
	default:
		return false, core.InternalError("pgbouncer: read config %q", path).Wrap(err)
	}

	if err := atomicInstall(path, []byte(desired), confFileMode); err != nil {
		return false, err
	}

	m.log.InfoCtx(ctx, "wrote PgBouncer config", "path", path,
		"pool_size", p.Pool.DefaultPoolSize, "max_client_conn", p.Pool.MaxClientConn)
	return true, nil
}

// UserlistPath is the absolute path to the managed auth_file (userlist.txt).
func (m *Manager) UserlistPath() string {
	return filepath.Join(m.confDir, userlistFileName)
}

// EnsureUserlist renders the auth_file (userlist.txt) from entries and installs
// it atomically, returning whether the file changed. It does NOT touch the
// service — reload/restart is the enable flow's concern, which can skip a reload
// when nothing changed.
//
// It mirrors EnsureConfig's install contract: atomic temp+rename at 0640 owned by
// the pgbouncer user, an O_NOFOLLOW guard that refuses a symlink planted at the
// path, and a deterministic no-op when the rendered content is byte-identical
// (RenderUserlist sorts entries) so an unchanged user set skips a needless reload.
//
// Unlike pgbouncer.ini, the userlist.txt format is pure `"user" "verifier"` lines
// (the `sm` format) and CANNOT carry an in-file ownership marker, so there is no
// foreign-file marker guard here. Ownership of the pooler is established upstream:
// the enable flow only reaches the auth_file after EnsureConfig's marker guard has
// confirmed indiepg owns the pgbouncer.ini, and the auth_file is a fully
// indiepg-derived satellite of that managed config. The verifier/username
// validation in RenderUserlist still hard-stops any injection before a write.
func (m *Manager) EnsureUserlist(ctx context.Context, entries []UserlistEntry) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pgbouncer: EnsureUserlist requires a Runner")
	}

	desired, err := RenderUserlist(entries)
	if err != nil {
		return false, err
	}

	path := m.UserlistPath()
	existing, err := readNoFollow(path)
	switch {
	case err == nil:
		if string(existing) == desired {
			return false, nil // already current; no rewrite, no reload.
		}
	case os.IsNotExist(err):
		// First write; fall through.
	case errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR):
		return false, core.ConflictError(
			"refusing to follow a symlink at the PgBouncer auth_file path %q", path,
		).WithHint("remove the symlink so indiepg can write a real userlist.txt")
	default:
		return false, core.InternalError("pgbouncer: read auth_file %q", path).Wrap(err)
	}

	if err := atomicInstall(path, []byte(desired), userlistFileMode); err != nil {
		return false, err
	}

	// Never log the verifiers themselves — only the count of users written.
	m.log.InfoCtx(ctx, "wrote PgBouncer auth_file", "path", path, "users", len(entries))
	return true, nil
}

// readNoFollow reads the config refusing to traverse a symlink at the final path
// component (O_NOFOLLOW): a symlink planted at the config path (pointing at, say,
// /etc/shadow) errors loudly instead of being read and mis-classified by the
// managed-marker guard. A missing file returns an os.IsNotExist error.
func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// atomicInstall creates the parent dir if needed and atomically replaces the file
// at path with data at mode, owned by the pgbouncer user. It backs both managed
// files (pgbouncer.ini and userlist.txt), which share the same install contract:
// temp + rename so a reader never sees a partial file, 0640 owned by the pgbouncer
// user so the (non-root) pooler process can read it without world access.
func atomicInstall(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, confDirMode); err != nil {
		return core.InternalError("pgbouncer: create config dir %q", dir).Wrap(err)
	}

	tmp, err := os.CreateTemp(dir, ".indiepg-pgbouncer-*.tmp")
	if err != nil {
		return core.InternalError("pgbouncer: create temp file in %q", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename (no-op once renamed).
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return core.InternalError("pgbouncer: write temp file").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return core.InternalError("pgbouncer: close temp file").Wrap(err)
	}

	// Set the mode explicitly: CreateTemp makes the file 0600, but the pgbouncer
	// process reads via its group, so the managed files are 0640.
	if err := os.Chmod(tmpName, mode); err != nil {
		return core.InternalError("pgbouncer: chmod temp file").Wrap(err)
	}
	if err := chownToBouncerUser(tmpName); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return core.InternalError("pgbouncer: install file %q", path).Wrap(err)
	}
	return nil
}

// chownToBouncerUser hands the file to the pgbouncer OS user so the (non-root)
// pooler process can read it. Under root a failure is fatal: a root-owned config
// the pgbouncer process cannot read would break the pooler silently. When not
// running as root (tests, hands-on dev) the chown is best-effort — the process
// likely already owns the file — and a lookup/chown failure is non-fatal.
func chownToBouncerUser(path string) error {
	uid, gid, lookupErr := bouncerUserIDs()
	if lookupErr == nil {
		if err := os.Chown(path, uid, gid); err != nil {
			if os.Geteuid() == 0 {
				return core.InternalError("pgbouncer: chown config to %s", bouncerUser).Wrap(err)
			}
			// Non-root: cannot chown to another user; rely on existing ownership.
			return nil
		}
		return nil
	}
	if os.Geteuid() == 0 {
		return core.InternalError("pgbouncer: resolve %s OS user for config ownership", bouncerUser).Wrap(lookupErr)
	}
	return nil
}

// bouncerUserIDs resolves the numeric uid/gid of the pgbouncer OS user.
func bouncerUserIDs() (int, int, error) {
	u, err := user.Lookup(bouncerUser)
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
