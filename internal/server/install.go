package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// fallbackStatePath mirrors the CLI's default state path. It is used only when
// an explicit path was not threaded into the systemd unit's ExecStart, so the
// generated service still points at the canonical location.
const fallbackStatePath = "/var/lib/indiepg/indiepg.db"

// stateFileMode is the mode the SQLite state file must carry: owner read/write
// only. The store hardens the file to this on Open; ResetPassword treats a file
// that is at-or-tighter-than this and owned by the caller as proof the caller is
// the panel's operator on the box.
const stateFileMode os.FileMode = 0o600

// InstallOptions drive first-run install (called by cmd/indiepg install).
type InstallOptions struct {
	Store    *store.Store
	Logger   *core.Logger
	Label    string
	BindAddr string
	Password string // generated and printed once if empty

	// StatePath is the SQLite state file the installed systemd service will
	// serve from; it is baked into the unit's ExecStart. Empty falls back to the
	// canonical location.
	StatePath string
	// NoService skips writing/enabling the systemd unit (useful on non-systemd
	// hosts or for hands-on setups). Install still prints how to start manually.
	NoService bool
	// PGMajor is the PostgreSQL major version to install (from `--pg-version`).
	// Zero selects the version catalog's default. A non-zero value must be a
	// supported major or Install refuses with a validation error.
	PGMajor int
}

// Install generates the instance identity, provisions Postgres, writes default
// config, sets the admin password, and installs+starts a systemd service so a
// single `indiepg install` leaves a running, reboot-safe panel — no second
// `systemctl` step. It is idempotent where possible: an existing identity is
// reused, config defaults are merged with any persisted values, and both
// Postgres provisioning and the service install are themselves idempotent. Real
// OS runners are constructed internally for the shell steps; the
// identity/config/password steps perform no external side effects and are
// unit-tested via installCore. It ends by printing a single summary block with
// the panel URL, the one-time admin password, and how to reset it.
func Install(ctx context.Context, opts InstallOptions) error {
	if opts.Store == nil {
		return core.InternalError("install: Store is required")
	}
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	// Validate the requested major up front (a zero means "catalog default").
	if opts.PGMajor != 0 && !pg.IsSupported(opts.PGMajor) {
		return core.ValidationError("PostgreSQL %d is not a supported version", opts.PGMajor).
			WithHint("choose a supported major (see `indiepg install --help`) or omit --pg-version for the default")
	}

	cfg, generatedPassword, pwState, err := installCore(ctx, opts.Store, log, opts.Label, opts.BindAddr, opts.Password)
	if err != nil {
		return err
	}

	// Safety net: the admin password is already persisted (hashed and
	// unrecoverable). If a later step fails before the summary prints, still
	// surface the generated password so a partial install never locks the
	// operator out. On the happy path the summary is the single place it shows.
	showGeneratedPassword := func() {
		if generatedPassword != "" {
			announceGeneratedPassword(generatedPassword)
		}
	}

	// Provision the native Postgres (apt + systemctl) via a real OS runner.
	// This is a non-hermetic step and is delegated to internal/pg. We hand it the
	// operator's PERSISTED workload profile (installCore merged persisted config
	// over defaults, so this is "mixed" on a fresh box) so a re-run after the
	// operator picked OLTP/OLAP re-applies that profile rather than silently
	// restarting Postgres back onto Mixed. A hand-edited/unparseable stored value
	// falls back to Mixed rather than failing the whole install — mirroring the
	// tuning surface, which never breaks on a stale store row.
	profile, perr := pg.ParseWorkloadProfile(cfg.TuningProfile)
	if perr != nil {
		profile = pg.ProfileMixed
	}
	mgr := pg.New(pg.Options{
		Runner:  exec.NewOSRunner(log, false),
		Config:  cfg,
		Logger:  log,
		PGMajor: opts.PGMajor,
	})
	res, err := mgr.Provision(ctx, profile)
	if err != nil {
		showGeneratedPassword()
		return err
	}
	log.Info("postgres provisioned", "message", res.Message, "socket_auth", res.Data["socket_auth"])

	// Record the cluster system identifier so ownership checks can detect a
	// different cluster pointed at the same repo.
	if sysID, sErr := mgr.SystemIdentifier(ctx); sErr == nil && sysID != "" {
		if uErr := opts.Store.SetPGSystemID(ctx, sysID); uErr != nil {
			log.Warn("record pg system id", "err", uErr)
		}
	}

	// Install and start the systemd service so the panel runs immediately and
	// survives reboots. Skipped on --no-service or a non-systemd host; either
	// way the summary tells the operator how to start it.
	serviceRunning := false
	switch {
	case opts.NoService:
		log.Info("skipping systemd service setup (--no-service)")
	case !systemctlAvailable():
		log.Warn("systemctl not found; skipping service setup — start with `indiepg serve`")
	default:
		statePath := opts.StatePath
		if statePath == "" {
			statePath = fallbackStatePath
		}
		runner := exec.NewOSRunner(log, false)
		if err := installSystemdService(ctx, runner, log, resolveExecPath(), statePath); err != nil {
			showGeneratedPassword()
			return err
		}
		serviceRunning = true
	}

	announceInstallSummary(cfg.BindAddr, generatedPassword, pwState, serviceRunning)
	log.Info("install complete")
	return nil
}

// pwOutcome reports how install handled the admin password, so the summary can
// say the right thing: a freshly generated password is shown once, an operator-
// supplied one is acknowledged but never echoed, and on an idempotent re-install
// an already-set password is left untouched.
type pwOutcome int

const (
	pwGenerated pwOutcome = iota // freshly generated; shown once
	pwProvided                   // set from an explicit --password; not echoed
	pwKept                       // pre-existing password left unchanged
)

// installCore performs the hermetic part of install: ensure an instance
// identity exists, persist default configuration (honoring an explicit bind
// address and namespacing the backup prefix by instance id), and set the admin
// password. It returns the effective config (handed to Postgres provisioning)
// and the generated admin password, which is non-empty only when install
// generated one — so the caller can show it in the install summary. It performs
// no shell or network I/O, which makes it directly unit-testable with an
// in-memory store.
//
// When password is empty, a strong one is generated and returned for one-time
// display; an explicit password (flag/env) remains supported as an override and
// is never echoed back.
func installCore(ctx context.Context, st *store.Store, log *core.Logger, label, bindAddr, password string) (config.Config, string, pwOutcome, error) {
	// 1) Identity — reuse an existing one (idempotent re-install) or generate.
	id, err := identity.Load(ctx, st)
	if err != nil {
		if core.CodeOf(err) != core.CodeNotFound {
			return config.Config{}, "", pwKept, err
		}
		if label == "" {
			label = defaultLabel()
		}
		if id, err = identity.Generate(ctx, st, label, panelVersion()); err != nil {
			return config.Config{}, "", pwKept, err
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
		return config.Config{}, "", pwKept, err
	}
	if err := config.Save(ctx, st, cfg); err != nil {
		return config.Config{}, "", pwKept, err
	}

	// 3) Admin password. Install is meant to be safe to re-run (to update, change
	// the bind address, etc.), so an already-set password is left UNTOUCHED unless
	// the operator explicitly passes one — re-running install must never silently
	// rotate the admin credential. On a fresh box with no password, a strong one
	// is generated and returned for one-time display; there is no recovery path
	// other than reset-password, so the operator must save it now.
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)

	// 3a) Explicit --password always wins (set/override), and is never echoed.
	if strings.TrimSpace(password) != "" {
		if err := authn.SetPassword(ctx, password); err != nil {
			return config.Config{}, "", pwProvided, err
		}
		auditPasswordSet(ctx, st, log, "admin password set from --password during install")
		log.Info("admin password set from --password")
		return cfg, "", pwProvided, nil
	}

	// 3b) No explicit password: keep an existing one rather than rotating it.
	if _, err := st.GetAuth(ctx); err == nil {
		log.Info("admin password already set; leaving it unchanged")
		return cfg, "", pwKept, nil
	} else if core.CodeOf(err) != core.CodeNotFound {
		return config.Config{}, "", pwKept, err
	}

	// 3c) Fresh box: generate, store, and return for one-time display.
	generated := auth.GeneratePassword()
	if err := authn.SetPassword(ctx, generated); err != nil {
		return config.Config{}, "", pwGenerated, err
	}
	auditPasswordSet(ctx, st, log, "admin password generated during install")
	log.Info("admin password generated")
	return cfg, generated, pwGenerated, nil
}

// auditPasswordSet records a best-effort genesis/credential audit row, mirroring
// reset-password. A failure to audit must not fail the install.
func auditPasswordSet(ctx context.Context, st *store.Store, log *core.Logger, detail string) {
	if _, err := st.AppendAudit(ctx, storeAuditEntry(
		"set_password", "auth", "success", detail, "")); err != nil {
		log.Warn("audit append failed", "action", "set_password", "err", err)
	}
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
	os.Stdout.WriteString("`indiepg reset-password` from an SSH/root session on this box.\n")
	os.Stdout.WriteString(banner + "\n\n")
}

// announceInstallSummary prints the single end-of-install block: where the panel
// is reachable, the one-time admin password (only when install generated one —
// an operator-supplied password is never echoed), and how to reset it later. It
// writes to stdout (never the log) so a generated secret is not shipped off-box.
func announceInstallSummary(bindAddr, generatedPassword string, pw pwOutcome, serviceRunning bool) {
	const banner = "============================================================"
	out := os.Stdout

	out.WriteString("\n" + banner + "\n")
	out.WriteString("  indiepg is installed.\n")
	if serviceRunning {
		out.WriteString("  Running now as a systemd service (auto-starts on boot).\n")
	} else {
		out.WriteString("  Start it with:   indiepg serve\n")
	}
	out.WriteString("\n")
	fmt.Fprintf(out, "  Panel URL       %s\n", panelURL(bindAddr))
	switch pw {
	case pwGenerated:
		fmt.Fprintf(out, "  Admin password  %s   (shown once — save it now)\n", generatedPassword)
	case pwProvided:
		out.WriteString("  Admin password  (the one you provided)\n")
	case pwKept:
		out.WriteString("  Admin password  (unchanged from the previous install)\n")
	}
	out.WriteString("  Reset it later  sudo indiepg reset-password\n")
	out.WriteString("\n")
	out.WriteString("  The panel binds a PRIVATE address — reach it over localhost,\n")
	out.WriteString("  Tailscale, or your private network.\n")
	out.WriteString(banner + "\n\n")
}

// panelURL renders a clickable http URL from a bind address, normalizing a
// wildcard/empty host to localhost and bracketing IPv6 literals. The server
// speaks plain HTTP behind a private bind, so the scheme is always http.
func panelURL(bindAddr string) string {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		// Not host:port — surface it as-is rather than guessing.
		return "http://" + bindAddr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}
	if strings.Contains(host, ":") { // IPv6 literal needs brackets in a URL.
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port
}

// EnsureAdminPassword makes the panel usable on first run without a separate
// install step: if no admin password has been set yet, it generates a strong
// one, stores it, and prints it once. This is what lets `indiepg serve` (e.g.
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
	if _, err := st.AppendAudit(ctx, storeAuditEntry(
		"set_password", "auth", "success", "admin password generated on first run", "")); err != nil {
		log.Warn("audit append failed", "action", "set_password", "err", err)
	}
	log.Info("no admin password was set; generated one for first-run login")
	announceGeneratedPassword(password)
	return true, nil
}

// ResetPassword sets a new admin password from an SSH/root context. It is the
// privileged escape hatch the design deliberately keeps off the network, so it
// enforces a local-operator check: the caller must be root (euid 0) or own the
// 0600 state DB file. When password is empty/blank a strong one is generated and
// printed once (the no-flag recovery path — "get me back in" with nothing to
// remember); a supplied value is used as-is. It requires the panel to have been
// installed (an auth record must exist), surfacing a CodeNotFound otherwise.
func ResetPassword(ctx context.Context, st *store.Store, log *core.Logger, password string) error {
	if st == nil {
		return core.InternalError("reset-password: Store is required")
	}
	if log == nil {
		log = core.Discard()
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

	// Empty input means "just give me a fresh one" — generate and show it once,
	// mirroring install. An explicit value is set silently (the operator knows
	// it) and never echoed.
	password, generated := resolveAdminPassword(password)
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	if err := authn.SetPassword(ctx, password); err != nil {
		return err
	}

	if _, err := st.AppendAudit(ctx, storeAuditEntry(
		"reset_password", "auth", "success", "admin password reset from CLI", "")); err != nil {
		log.Warn("audit append failed", "action", "reset_password", "err", err)
	}
	log.Info("admin password reset", "generated", generated)
	if generated {
		announceGeneratedPassword(password)
	}
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
	return "indiepg"
}

// panelVersion returns the build version, defaulting to "dev".
func panelVersion() string {
	if core.Version != "" {
		return core.Version
	}
	return "dev"
}
