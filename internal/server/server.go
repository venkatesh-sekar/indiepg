// Package server is the HTTP panel: a chi router serving a small JSON API for
// every feature plus the embedded SPA, behind a signed-session auth middleware.
// It also exposes Install and ResetPassword orchestration invoked by the CLI.
//
// Network binding is private by default (enforced by config.Validate); the
// router never exposes a mutating verb outside the authenticated /api surface,
// and every typed core error is rendered as a stable JSON envelope so the SPA
// can branch on the failure kind.
package server

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/backup"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
	"github.com/venkatesh-sekar/indiepg/internal/identity"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
	"github.com/venkatesh-sekar/indiepg/internal/pg/guard"
	"github.com/venkatesh-sekar/indiepg/internal/server/web"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// defaultSessionTTL is how long an issued session token stays valid.
const defaultSessionTTL = 12 * time.Hour

// maxBodyBytes caps JSON request bodies to a sane size for an admin API.
const maxBodyBytes = 1 << 20 // 1 MiB

// Options configure the Server. Feature managers are constructed internally
// from Config + Store; tests inject fakes via the unexported builder newServer.
type Options struct {
	Config config.Config
	Store  *store.Store
	Logger *core.Logger
}

// Server is the HTTP panel. It owns the chi router, the authenticator, and the
// embedded SPA filesystem.
type Server struct {
	cfg   config.Config
	store *store.Store
	log   *core.Logger
	auth  *auth.Authenticator

	// Feature managers, constructed from cfg+store in newServer. pg owns the
	// Postgres connection pools (read-only + privileged) used by the query box,
	// schema/role/database browsing, and guided admin actions; guard is the
	// read-only SQL gate for the query box; backups drives pgBackRest; sampler
	// produces the dashboard telemetry snapshot.
	pg      *pg.Manager
	guard   *guard.Guard
	backups *backup.Manager
	sampler *pg.Sampler

	sessionTTL time.Duration
	spa        http.Handler
	handler    http.Handler
}

// New builds a Server from Options, wiring the authenticator over the store and
// loading the embedded SPA. It returns a *core.Error if a dependency is missing
// or the embedded SPA cannot be opened.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, core.InternalError("server: Store is required")
	}
	log := opts.Logger
	if log == nil {
		log = core.Discard()
	}

	dist, err := web.DistFS()
	if err != nil {
		return nil, core.InternalError("server: open embedded SPA").Wrap(err)
	}

	authn := auth.New(opts.Store, auth.DefaultLockoutPolicy(), defaultSessionTTL)

	srv, err := newServer(opts.Config, opts.Store, log, authn, dist, defaultSessionTTL)
	if err != nil {
		return nil, err
	}

	// Best-effort connect to the managed Postgres so the query box, browsing,
	// admin actions, and dashboard work immediately. A failure here is not fatal:
	// the panel still serves login and config, and database features return a
	// typed "not connected" error until Postgres is reachable.
	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Self-heal pg_hba.conf so the panel's dedicated roles can authenticate over
	// the local socket. Idempotent (a no-op once the rule is present), so an
	// existing install is fixed by a binary upgrade + restart without re-running
	// install. Best-effort: a failure (e.g. Postgres down, not root) just leaves
	// Connect to fail with a clear warning.
	if _, herr := srv.pg.EnsureSocketAuth(connectCtx); herr != nil {
		log.Warn("could not configure pg_hba.conf socket auth; database features may be unavailable", "err", herr)
	}

	if cerr := srv.pg.Connect(connectCtx); cerr != nil {
		log.Warn("postgres not connected at startup; database features unavailable until reachable", "err", cerr)
	}

	// Self-heal the pgBackRest config from the persisted S3 settings so an
	// upgrade/restart re-applies a backup target configured in a prior run. Best
	// effort: a failure (Postgres down, not root, bad credentials) only logs —
	// the panel still serves, and a later config save re-attempts it.
	if _, berr := srv.ensureBackupConfigured(connectCtx, srv.cfg); berr != nil {
		log.Warn("could not configure pgBackRest from stored settings; backups may be unavailable until fixed", "err", berr)
	}

	return srv, nil
}

// ensureBackupConfigured renders and installs the pgBackRest config (and runs
// stanza-create on change) from cfg, for BOTH local and S3 repos. A local-only
// repo still needs the managed config (the [stanza] section with pg1-path) and
// an initialized repository, or `pgbackrest backup` fails with "backup command
// requires option: pg1-path". The Postgres data directory and port are discovered
// live, so Postgres must be reachable; an error there is returned to the caller,
// which decides whether it is fatal. The render is deterministic, so an unchanged
// config is a cheap no-op that does not re-run stanza-create.
func (s *Server) ensureBackupConfigured(ctx context.Context, cfg config.Config) (bool, error) {
	dataDir, err := s.pg.DataDirectory(ctx)
	if err != nil {
		return false, core.InternalError("server: discover Postgres data directory for backup config").Wrap(err)
	}
	port, err := s.pg.Port(ctx)
	if err != nil {
		return false, core.InternalError("server: discover Postgres port for backup config").Wrap(err)
	}

	params := backup.ConfigParams{
		Stanza:        cfg.Stanza,
		Endpoint:      cfg.Backup.Endpoint,
		Region:        cfg.Backup.Region,
		Bucket:        cfg.Backup.Bucket,
		Prefix:        cfg.Backup.Prefix,
		AccessKey:     cfg.Backup.AccessKey,
		SecretKey:     cfg.Backup.SecretKey,
		UseSSL:        cfg.Backup.UseSSL,
		RetentionDays: cfg.RetentionDays,
		CipherPass:    cfg.Backup.CipherPass,
		PGDataDir:     dataDir,
		PGPort:        port,
		PGSocketDir:   cfg.PGSocketDir,
	}
	return s.backups.EnsureConfigured(ctx, params)
}

// backupOwnerFor builds the single-writer ownership guard for the configured S3
// repo, or returns nil when there is no remote target (local-only: nothing to
// guard). The marker lives in the SAME bucket as the backups, so a second panel
// pointed at the repo will see it and refuse to share. A nil return when S3 IS
// configured is fail-closed by design: the Manager's acquireForWrite then aborts
// the backup rather than silently dropping the guard.
//
// It is a free function (not a method) so newServer can call it while still
// assembling the Server.
func backupOwnerFor(ctx context.Context, st *store.Store, cfg config.Config, log *core.Logger) *identity.Owner {
	if cfg.Backup.Bucket == "" && cfg.Backup.Endpoint == "" {
		return nil // local-only; no shared resource to corrupt.
	}
	id, err := identity.Load(ctx, st)
	if err != nil {
		log.Warn("backup ownership guard unavailable: panel identity not loaded", "err", err)
		return nil
	}
	objstore, err := backup.NewS3ObjectStore(backup.S3StoreParams{
		Endpoint:  cfg.Backup.Endpoint,
		Region:    cfg.Backup.Region,
		Bucket:    cfg.Backup.Bucket,
		AccessKey: cfg.Backup.AccessKey,
		SecretKey: cfg.Backup.SecretKey,
		UseSSL:    cfg.Backup.UseSSL,
	})
	if err != nil {
		log.Warn("backup ownership guard unavailable: could not build S3 client", "err", err)
		return nil
	}
	return identity.NewOwner(id, objstore, log)
}

// newServer is the unexported builder used by New and by tests to inject a
// pre-wired authenticator and SPA filesystem.
func newServer(cfg config.Config, st *store.Store, log *core.Logger, authn *auth.Authenticator, dist fs.FS, ttl time.Duration) (*Server, error) {
	// Feature managers share one OS command runner. These are pure constructors
	// with no IO until first use, so they are safe to build here (tests that call
	// newServer get a Manager that is simply never Connect-ed).
	runner := exec.NewOSRunner(log, false)
	pgmgr := pg.New(pg.Options{Runner: runner, Config: cfg, Logger: log})

	s := &Server{
		cfg:   cfg,
		store: st,
		log:   log,
		auth:  authn,
		pg:    pgmgr,
		guard: guard.New(guard.Options{ReadOnly: true, AutoLimit: cfg.QueryLimit}),
		backups: backup.New(backup.Options{
			Runner: runner, Store: st, Config: cfg, Logger: log,
			// Wire the single-writer ownership guard when an S3 target is already
			// configured, so backups are protected immediately on startup.
			Owner: backupOwnerFor(context.Background(), st, cfg, log),
		}),
		sampler: pg.NewSampler(pgmgr),

		sessionTTL: ttl,
		spa:        newSPAHandler(dist),
	}
	s.handler = s.buildRouter()
	return s, nil
}

// Handler returns the composed http.Handler (chi router) for tests via
// httptest and for embedding behind another mux.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// ListenAndServe binds cfg.BindAddr and serves until ctx is cancelled, then
// shuts down gracefully within a bounded timeout. The private-bind rule was
// already enforced by config.Validate at load time.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.BindAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	ln, err := net.Listen("tcp", s.cfg.BindAddr)
	if err != nil {
		return core.InternalError("server: bind %s", s.cfg.BindAddr).Wrap(err)
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.cfg.BindAddr)
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.log.Info("http server shutting down")
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return core.InternalError("server: graceful shutdown").Wrap(err)
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return core.InternalError("server: serve").Wrap(err)
		}
		return nil
	}
}
