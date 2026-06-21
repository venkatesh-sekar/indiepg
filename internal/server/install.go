package server

import (
	"context"
	"os"
	"strings"
	"syscall"

	"github.com/venkatesh-sekar/pgpanel/internal/auth"
	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
	"github.com/venkatesh-sekar/pgpanel/internal/identity"
	"github.com/venkatesh-sekar/pgpanel/internal/pg"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

// stateFileMode is the mode the SQLite state file must carry: owner read/write
// only. The store hardens the file to this on Open; ResetPassword treats a file
// that is at-or-tighter-than this and owned by the caller as proof the caller is
// the panel's operator on the box.
const stateFileMode os.FileMode = 0o600

// InstallOptions drive first-run install (called by cmd/pgpanel install).
type InstallOptions struct {
	Store    *store.Store
	Logger   *core.Logger
	Label    string
	BindAddr string
	Password string // generated and printed once if empty
}

// Install generates the instance identity, provisions Postgres, writes default
// config, and sets the admin password. It is idempotent where possible: an
// existing identity is reused, config defaults are merged with any persisted
// values, and Postgres provisioning is itself idempotent. A real OS runner is
// constructed internally for the shell steps; the identity/config/password
// steps perform no external side effects and are unit-tested via installCore.
func Install(ctx context.Context, opts InstallOptions) error {
	if opts.Store == nil {
		return core.InternalError("install: Store is required")
	}
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	cfg, err := installCore(ctx, opts.Store, log, opts.Label, opts.BindAddr, opts.Password)
	if err != nil {
		return err
	}

	// Provision the native Postgres (apt + systemctl) via a real OS runner.
	// This is the only non-hermetic step and is delegated to internal/pg.
	mgr := pg.New(pg.Options{
		Runner: exec.NewOSRunner(log, false),
		Config: cfg,
		Logger: log,
	})
	res, err := mgr.Provision(ctx)
	if err != nil {
		return err
	}
	log.Info("postgres provisioned", "message", res.Message)

	// Record the cluster system identifier so ownership checks can detect a
	// different cluster pointed at the same repo.
	if sysID, sErr := mgr.SystemIdentifier(ctx); sErr == nil && sysID != "" {
		if uErr := opts.Store.SetPGSystemID(ctx, sysID); uErr != nil {
			log.Warn("record pg system id", "err", uErr)
		}
	}

	log.Info("install complete")
	return nil
}

// installCore performs the hermetic part of install: ensure an instance
// identity exists, persist default configuration (honoring an explicit bind
// address and namespacing the backup prefix by instance id), and set the admin
// password. It returns the effective config so the caller can hand it to
// Postgres provisioning. It performs no shell or network I/O, which makes it
// directly unit-testable with an in-memory store.
//
// When password is empty, a strong one is generated and printed once; an
// explicit password (flag/env) remains supported as an override.
func installCore(ctx context.Context, st *store.Store, log *core.Logger, label, bindAddr, password string) (config.Config, error) {
	// 1) Identity — reuse an existing one (idempotent re-install) or generate.
	id, err := identity.Load(ctx, st)
	if err != nil {
		if core.CodeOf(err) != core.CodeNotFound {
			return config.Config{}, err
		}
		if label == "" {
			label = defaultLabel()
		}
		if id, err = identity.Generate(ctx, st, label, panelVersion()); err != nil {
			return config.Config{}, err
		}
		log.Info("instance identity generated", "label", label)
	} else {
		log.Info("reusing existing instance identity")
	}

	// 2) Config — start from persisted values merged over defaults, then apply
	// an explicit bind address if provided, namespace the backup prefix by
	// instance id when a bucket is configured but no explicit prefix was set,
	// validate, and save.
	cfg, err := config.Load(ctx, st)
	if err != nil {
		// A fresh store has no rows; fall back to defaults rather than failing.
		cfg = config.Default()
	}
	if bindAddr != "" {
		cfg.BindAddr = bindAddr
	}
	// Defense layer 1 (design §6): when an S3 bucket is configured and the
	// operator has not pinned an explicit prefix, namespace the persisted repo
	// prefix by instance id ("panel/<instance_id>") so two panels on the same
	// bucket can never collide by construction. The operator-chosen prefix (if
	// any) is preserved as the base sub-path.
	if cfg.Backup.Bucket != "" && strings.TrimSpace(cfg.Backup.Prefix) == "" {
		cfg.Backup.Prefix = id.DefaultPrefix("")
		log.Info("namespaced backup prefix by instance id", "prefix", cfg.Backup.Prefix)
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	if err := config.Save(ctx, st, cfg); err != nil {
		return config.Config{}, err
	}

	// 3) Admin password — generate-when-empty, then hash and store via the
	// authenticator. A generated password is shown exactly once: there is no
	// recovery path other than reset-password, so the operator must save it now.
	password, generated := resolveAdminPassword(password)
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	if err := authn.SetPassword(ctx, password); err != nil {
		return config.Config{}, err
	}
	if generated {
		announceGeneratedPassword(password)
	}
	log.Info("admin password set")

	return cfg, nil
}

// resolveAdminPassword returns the admin password to set and whether it was
// generated. An empty/blank input yields a freshly generated strong password;
// any explicit value is used as-is so a flag/env override still works.
func resolveAdminPassword(password string) (resolved string, generated bool) {
	if strings.TrimSpace(password) == "" {
		return auth.GeneratePassword(), true
	}
	return password, false
}

// announceGeneratedPassword prints a generated admin password to stdout exactly
// once, loudly framed so the operator copies it before it is gone. It is never
// logged (logs may be shipped off-box) — stdout on the install TTY only.
func announceGeneratedPassword(password string) {
	const banner = "============================================================"
	os.Stdout.WriteString("\n" + banner + "\n")
	os.Stdout.WriteString("GENERATED ADMIN PASSWORD — SAVE THIS NOW:\n\n")
	os.Stdout.WriteString("    " + password + "\n\n")
	os.Stdout.WriteString("This is shown only once. There is no recovery other than\n")
	os.Stdout.WriteString("`pgpanel reset-password` from an SSH/root session on this box.\n")
	os.Stdout.WriteString(banner + "\n\n")
}

// EnsureAdminPassword makes the panel usable on first run without a separate
// install step: if no admin password has been set yet, it generates a strong
// one, stores it, and prints it once. This is what lets `pgpanel serve` (e.g.
// `make run`) be logged into out of the box — otherwise the operator would face
// a login screen with no credentials. It is a no-op once an admin password
// exists. Returns true if a password was generated.
func EnsureAdminPassword(ctx context.Context, st *store.Store, log *core.Logger) (bool, error) {
	if st == nil {
		return false, core.InternalError("ensure admin password: Store is required")
	}
	// An existing auth record means the panel is already initialized.
	if _, err := st.GetAuth(ctx); err == nil {
		return false, nil
	} else if core.CodeOf(err) != core.CodeNotFound {
		return false, err
	}

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	password := auth.GeneratePassword()
	if err := authn.SetPassword(ctx, password); err != nil {
		return false, err
	}
	log.Info("no admin password was set; generated one for first-run login")
	announceGeneratedPassword(password)
	return true, nil
}

// ResetPassword sets a new admin password from an SSH/root context. It is the
// privileged escape hatch the design deliberately keeps off the network, so it
// enforces a local-operator check: the caller must be root (euid 0) or own the
// 0600 state DB file. An empty password is rejected (the caller is expected to
// have prompted). It requires the panel to have been installed (an auth record
// must exist), surfacing a CodeNotFound otherwise.
func ResetPassword(ctx context.Context, st *store.Store, log *core.Logger, password string) error {
	if st == nil {
		return core.InternalError("reset-password: Store is required")
	}
	if log == nil {
		log = core.Discard()
	}
	if strings.TrimSpace(password) == "" {
		return core.ValidationError("reset-password: new password must not be empty")
	}

	// Enforce the SSH/root-only invariant (design §3, §4.2, §10): the network
	// API never exposes reset, so the only barrier left is local privilege.
	if err := authorizeReset(st); err != nil {
		return err
	}

	// Ensure the panel is installed; SetPassword on a missing auth row would
	// otherwise surface an opaque NotFound from the store.
	if _, err := st.GetAuth(ctx); err != nil {
		return err
	}

	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	if err := authn.SetPassword(ctx, password); err != nil {
		return err
	}

	if _, err := st.AppendAudit(ctx, storeAuditEntry(
		"reset_password", "auth", "success", "admin password reset from CLI", "")); err != nil {
		log.Warn("audit append failed", "action", "reset_password", "err", err)
	}
	log.Info("admin password reset")
	return nil
}

// authorizeReset enforces that the caller is allowed to reset the admin
// password: either running as root, or the owner of the 0600 state DB file.
// It refuses with a CodeSafety error otherwise. An in-memory / file-less store
// (tests, ephemeral runs) has no on-disk permission boundary to check, so it is
// exempt — real deployments always back the store with a file.
func authorizeReset(st *store.Store) error {
	path, err := stateFilePath(st)
	if err != nil {
		return err
	}
	if path == "" {
		// :memory: or shared-cache DSN with no backing file: nothing to own.
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return core.InternalError("reset-password: stat state file %q", path).Wrap(err)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		// Non-POSIX filesystem info; fall back to requiring root.
		return resetDecision(os.Geteuid(), -1, info.Mode())
	}
	return resetDecision(os.Geteuid(), uid, info.Mode())
}

// resetDecision is the pure authorization rule, split out for testability: a
// reset is permitted when the caller is root, or when the caller owns a state
// file whose permission bits are no looser than 0600. groupUID is the file's
// owner uid (-1 if unknown).
func resetDecision(euid, ownerUID int, mode os.FileMode) error {
	if euid == 0 {
		return nil
	}
	if ownerUID >= 0 && ownerUID == euid && mode.Perm()&^stateFileMode == 0 {
		return nil
	}
	return core.NewSafetyError(
		"reset-password",
		[]string{"run as root (sudo) or as the owner of the 0600 state file"},
		"reset-password requires root or ownership of the 0600 state file on this box",
	)
}

// stateFilePath returns the on-disk path of the store's main SQLite database,
// or "" for a file-less store (":memory:"). It uses PRAGMA database_list, which
// reports an empty file for in-memory databases.
func stateFilePath(st *store.Store) (string, error) {
	rows, err := st.DB().Query("PRAGMA database_list")
	if err != nil {
		return "", core.InternalError("reset-password: read database list").Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", core.InternalError("reset-password: scan database list").Wrap(err)
		}
		if name == "main" {
			return file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", core.InternalError("reset-password: iterate database list").Wrap(err)
	}
	return "", nil
}

// fileOwnerUID extracts the owning uid from a FileInfo on POSIX systems. The
// boolean is false when the underlying syscall info is unavailable.
func fileOwnerUID(info os.FileInfo) (int, bool) {
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid), true
	}
	return 0, false
}

// defaultLabel derives a human label from the hostname, falling back to a
// generic value when the hostname is unavailable.
func defaultLabel() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "pgpanel"
}

// panelVersion returns the build version, defaulting to "dev".
func panelVersion() string {
	if core.Version != "" {
		return core.Version
	}
	return "dev"
}
