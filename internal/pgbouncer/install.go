package pgbouncer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
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
	// defaultSystemdDir is where systemd looks for locally-administered units and
	// their drop-ins. indiepg installs a pgbouncer.service drop-in here (under
	// pgbouncer.service.d/) to provision the pooler's runtime/pidfile directory.
	// It mirrors the panel's own unit location (/etc/systemd/system/indiepg.service).
	defaultSystemdDir = "/etc/systemd/system"
	// bouncerUser is the DEDICATED OS user/group the PgBouncer service runs as when
	// the install provides one; the managed config is handed to it so the
	// (non-root) pooler process can read it. Not every platform creates it — the
	// Debian/Ubuntu package does not — so see bouncerFallbackUser.
	bouncerUser = "pgbouncer"
	// bouncerFallbackUser is the OS user the Debian/Ubuntu pgbouncer package's
	// systemd unit actually launches PgBouncer as. That package ships NO dedicated
	// `pgbouncer` user — its unit runs `User=postgres` — so when bouncerUser is
	// absent the managed config/auth_file are handed to this user instead, keeping
	// the file owner matched to the process that must read them. It mirrors the pg
	// manager's privileged AsUser ("postgres").
	bouncerFallbackUser = "postgres"
)

// bouncerOwnerCandidates lists the OS users the managed PgBouncer files may be
// owned by, in priority order: the dedicated `pgbouncer` user when the install
// created one, otherwise `postgres` — the user the Debian/Ubuntu packaged unit
// launches PgBouncer as. Owner resolution (resolveBouncerOwner) picks the FIRST
// that exists on this host, so the config and auth_file owner always matches the
// (non-root) process that reads them at 0640. Without this fallback a clean
// Debian box (no `pgbouncer` user) makes the chown lookup fail and, running as
// root, turns into a fatal 500 on enable.
var bouncerOwnerCandidates = []string{bouncerUser, bouncerFallbackUser}

// Manager installs and configures the opt-in PgBouncer pooler. Like the pg and
// backup managers, every external side effect goes through an exec.Runner so the
// install/enable flow is unit-testable. The pooler is OFF by default; nothing
// here runs until the operator explicitly enables it.
type Manager struct {
	runner     exec.Runner
	log        *core.Logger
	confDir    string
	systemdDir string
}

// Options configure a Manager. Runner is required for any IO; a nil Logger is
// replaced with a discard logger; an empty ConfDir defaults to /etc/pgbouncer;
// an empty SystemdDir defaults to /etc/systemd/system (overridable so the
// runtime-dir drop-in install is unit-testable without touching the real /etc).
type Options struct {
	Runner     exec.Runner
	Logger     *core.Logger
	ConfDir    string
	SystemdDir string
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
	sysDir := opts.SystemdDir
	if sysDir == "" {
		sysDir = defaultSystemdDir
	}
	return &Manager{runner: opts.Runner, log: log, confDir: dir, systemdDir: sysDir}
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

// ResetPackageConffiles removes the package's PRISTINE default conffiles among
// the files indiepg manages (pgbouncer.ini and, if the package ships it as a
// conffile, userlist.txt). The apt install drops a pristine /etc/pgbouncer/
// pgbouncer.ini that carries no managed marker; without this step EnsureConfig's
// marker guard would 409 on that distro default and the pooler could never be
// enabled on a clean box. Clearing it first lets EnsureConfig write the managed
// config.
//
// "Pristine" is detected precisely so an operator-edited config is never
// clobbered: dpkg records each conffile's md5 at install time
// (`dpkg-query --showformat='${Conffiles}' -W pgbouncer`); a file is only removed
// when its on-disk md5 STILL matches that recorded md5. Any difference means an
// operator (or the distro) edited it, so the file is left in place and
// EnsureConfig's marker guard still refuses to overwrite it (409).
//
// It is best-effort and conservative: if dpkg cannot be queried (absent, package
// not registered) nothing is cleared and the flow proceeds exactly as before this
// step, with the marker guard as the backstop. Only files indiepg actually
// manages are ever considered — a foreign conffile the package also ships (e.g.
// /etc/default/pgbouncer) is never touched. The dpkg query runs through the same
// exec Runner as the apt install so the step stays unit-testable.
func (m *Manager) ResetPackageConffiles(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: ResetPackageConffiles requires a Runner")
	}

	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "dpkg-query",
		Args:    []string{"--showformat=${Conffiles}", "-W", pkgName},
		Timeout: commandTimeout,
	})
	if err != nil {
		// dpkg unavailable or the package is not registered: we cannot prove a file
		// is the untouched package default, so we remove nothing and let the marker
		// guard remain the backstop (a foreign/edited config still 409s).
		m.log.Warn("pgbouncer: could not read package conffiles; leaving config in place",
			"package", pkgName, "err", err.Error())
		return nil
	}

	// Only ever consider files indiepg owns — never another conffile the package
	// also ships (e.g. /etc/default/pgbouncer, /etc/logrotate.d/pgbouncer).
	managed := map[string]struct{}{
		m.ConfigPath():   {},
		m.UserlistPath(): {},
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		path, recordedMD5, ok := parseConffileEntry(line)
		if !ok {
			continue
		}
		if _, isManaged := managed[path]; !isManaged {
			continue
		}
		if !conffileIsPristine(path, recordedMD5) {
			// Operator-edited (or unreadable/symlinked): leave it for the marker guard.
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return core.InternalError("pgbouncer: remove pristine package conffile %q", path).Wrap(err)
		}
		m.log.InfoCtx(ctx, "cleared pristine PgBouncer package conffile", "path", path)
	}
	return nil
}

// parseConffileEntry parses one line of dpkg-query's ${Conffiles} output, whose
// entries look like " /etc/pgbouncer/pgbouncer.ini <md5> [obsolete|remove-on-upgrade]".
// It returns the conffile path and its recorded md5, or ok=false for a blank line,
// a malformed entry, an obsolete/remove-on-upgrade conffile (which dpkg no longer
// keeps on disk), or a "newconffile" placeholder md5 (not yet a real hash). The
// managed paths under /etc/pgbouncer never contain spaces, so the leading
// whitespace-separated field is the path and the next is the md5.
func parseConffileEntry(line string) (path, md5sum string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", false
	}
	// A trailing flag marks an entry that is not a plain, on-disk hashed conffile.
	for _, flag := range fields[2:] {
		if flag == "obsolete" || flag == "remove-on-upgrade" {
			return "", "", false
		}
	}
	path, md5sum = fields[0], fields[1]
	if md5sum == "newconffile" {
		return "", "", false // placeholder, not a real checksum yet.
	}
	return path, md5sum, true
}

// conffileIsPristine reports whether the file at path is byte-identical to the
// package default dpkg recorded — i.e. its current md5 still equals wantMD5. The
// md5 here is dpkg's conffile digest used purely to detect an unmodified install,
// not a security primitive. A match means apt installed the file and nobody has
// touched it (safe to clear). Any difference — or a missing, symlinked
// (O_NOFOLLOW), or otherwise unreadable file — is treated as NOT pristine, so an
// operator-edited config is never removed and EnsureConfig's marker guard keeps
// protecting it.
func conffileIsPristine(path, wantMD5 string) bool {
	data, err := readNoFollow(path)
	if err != nil {
		return false
	}
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:]) == strings.ToLower(strings.TrimSpace(wantMD5))
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

// chownToBouncerUser hands the file to the OS user the PgBouncer service runs as
// so the (non-root) pooler process can read it. The owner is resolved from
// bouncerOwnerCandidates — the dedicated `pgbouncer` user when present, otherwise
// `postgres`, the user the Debian/Ubuntu packaged unit launches PgBouncer as.
//
// Under root a resolve-or-chown failure is fatal: a root-owned config the
// pgbouncer process cannot read would break the pooler silently. When not running
// as root (tests, hands-on dev) the chown is best-effort — the process likely
// already owns the file — and a lookup/chown failure is non-fatal.
func chownToBouncerUser(path string) error {
	uid, gid, owner, lookupErr := resolveBouncerOwner()
	if lookupErr == nil {
		if err := os.Chown(path, uid, gid); err != nil {
			if os.Geteuid() == 0 {
				return core.InternalError("pgbouncer: chown config to %s", owner).Wrap(err)
			}
			// Non-root: cannot chown to another user; rely on existing ownership.
			return nil
		}
		return nil
	}
	if os.Geteuid() == 0 {
		return core.InternalError(
			"pgbouncer: resolve an OS user (tried %s) for config ownership",
			strings.Join(bouncerOwnerCandidates, ", "),
		).Wrap(lookupErr)
	}
	return nil
}

// resolveBouncerOwner resolves the numeric uid/gid of the FIRST OS user in
// bouncerOwnerCandidates that exists on this host, also returning the resolved
// name (for the chown error message). It errors only when NONE of the candidates
// resolve, returning the last lookup error so the caller can report what failed.
func resolveBouncerOwner() (int, int, string, error) {
	var lastErr error
	for _, candidate := range bouncerOwnerCandidates {
		u, err := user.Lookup(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			lastErr = err
			continue
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			lastErr = err
			continue
		}
		return uid, gid, candidate, nil
	}
	if lastErr == nil {
		// Defensive: only reachable if bouncerOwnerCandidates were emptied; the
		// package-level slice is never empty, but never return a nil error here.
		lastErr = errors.New("no PgBouncer config owner candidates configured")
	}
	return 0, 0, "", lastErr
}
