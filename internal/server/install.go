package server

import (
	"context"
	"os"
	"strings"

	"github.com/venkatesh-sekar/pgpanel/internal/auth"
	"github.com/venkatesh-sekar/pgpanel/internal/config"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
	"github.com/venkatesh-sekar/pgpanel/internal/identity"
	"github.com/venkatesh-sekar/pgpanel/internal/pg"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

// InstallOptions drive first-run install (called by cmd/pgpanel install).
type InstallOptions struct {
	Store    *store.Store
	Logger   *core.Logger
	Label    string
	BindAddr string
	Password string // prompted by caller if empty
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
// address), and set the admin password. It returns the effective config so the
// caller can hand it to Postgres provisioning. It performs no shell or network
// I/O, which makes it directly unit-testable with an in-memory store.
func installCore(ctx context.Context, st *store.Store, log *core.Logger, label, bindAddr, password string) (config.Config, error) {
	if strings.TrimSpace(password) == "" {
		return config.Config{}, core.ValidationError("install: admin password is required").
			WithHint("supply --password or set one when prompted")
	}

	// 1) Identity — reuse an existing one (idempotent re-install) or generate.
	if _, err := identity.Load(ctx, st); err != nil {
		if core.CodeOf(err) != core.CodeNotFound {
			return config.Config{}, err
		}
		if label == "" {
			label = defaultLabel()
		}
		if _, gErr := identity.Generate(ctx, st, label, panelVersion()); gErr != nil {
			return config.Config{}, gErr
		}
		log.Info("instance identity generated", "label", label)
	} else {
		log.Info("reusing existing instance identity")
	}

	// 2) Config — start from persisted values merged over defaults, then apply
	// an explicit bind address if provided, validate, and save.
	cfg, err := config.Load(ctx, st)
	if err != nil {
		// A fresh store has no rows; fall back to defaults rather than failing.
		cfg = config.Default()
	}
	if bindAddr != "" {
		cfg.BindAddr = bindAddr
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	if err := config.Save(ctx, st, cfg); err != nil {
		return config.Config{}, err
	}

	// 3) Admin password — hash and store via the authenticator.
	authn := auth.New(st, auth.DefaultLockoutPolicy(), defaultSessionTTL)
	if err := authn.SetPassword(ctx, password); err != nil {
		return config.Config{}, err
	}
	log.Info("admin password set")

	return cfg, nil
}

// ResetPassword sets a new admin password from an SSH/root context. An empty
// password is rejected (the caller is expected to have prompted). It requires
// the panel to have been installed (an auth record must exist), surfacing a
// CodeNotFound otherwise.
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
