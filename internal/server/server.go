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
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
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

	return newServer(opts.Config, opts.Store, log, authn, dist, defaultSessionTTL)
}

// newServer is the unexported builder used by New and by tests to inject a
// pre-wired authenticator and SPA filesystem.
func newServer(cfg config.Config, st *store.Store, log *core.Logger, authn *auth.Authenticator, dist fs.FS, ttl time.Duration) (*Server, error) {
	s := &Server{
		cfg:        cfg,
		store:      st,
		log:        log,
		auth:       authn,
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
