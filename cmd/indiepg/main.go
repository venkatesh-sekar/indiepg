// Command indiepg is the single self-hosted binary: it installs and owns a
// native PostgreSQL, serves a private web admin panel, and exports telemetry.
//
// Subcommands:
//
//	indiepg serve            run the web server
//	indiepg install          provision Postgres + install the service; print URL & password
//	indiepg update           swap in the latest release binary + restart the service
//	indiepg uninstall        stop+remove the service (Postgres and its data are left intact)
//	indiepg start|stop|restart  control the systemd service
//	indiepg reset-password   SSH/root escape hatch to reset the admin password
//	indiepg version          print the version
//
// The everyday surface is just two verbs: `install` to set up (one command,
// leaves a running systemd service) and `reset-password` to get back in;
// `update`/`uninstall` round out the lifecycle. The wiring here is intentionally
// thin: it constructs the foundation (logger, store, config) and hands off to
// the per-feature packages.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/server"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// defaultStatePath is where the panel keeps its SQLite state.
const defaultStatePath = "/var/lib/indiepg/indiepg.db"

func main() {
	if err := rootCmd().Execute(); err != nil {
		// Cobra already prints the error; exit non-zero with a code derived
		// from the panel error kind where possible.
		os.Exit(exitCode(err))
	}
}

func rootCmd() *cobra.Command {
	var (
		statePath string
		logLevel  string
	)

	root := &cobra.Command{
		Use:           "indiepg",
		Short:         "Private web admin panel that installs and owns a native PostgreSQL",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().StringVar(&statePath, "state", defaultStatePath, "path to the panel's SQLite state file")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")

	root.AddCommand(
		serveCmd(&statePath, &logLevel),
		installCmd(&statePath, &logLevel),
		updateCmd(&logLevel),
		uninstallCmd(&statePath, &logLevel),
		serviceCmd("start", "Start the indiepg systemd service", server.ServiceStart, &logLevel),
		serviceCmd("stop", "Stop the indiepg systemd service", server.ServiceStop, &logLevel),
		serviceCmd("restart", "Restart the indiepg systemd service", server.ServiceRestart, &logLevel),
		resetPasswordCmd(&statePath, &logLevel),
		versionCmd(),
	)
	return root
}

// openFoundation builds the logger, opens the store, and loads config. It is
// shared by serve/install/reset-password.
func openFoundation(statePath, logLevel string) (*core.Logger, *store.Store, error) {
	log := core.NewLogger(core.LogLevel(logLevel))
	st, err := store.Open(statePath)
	if err != nil {
		return nil, nil, err
	}
	return log, st, nil
}

func serveCmd(statePath, logLevel *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the web server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, st, err := openFoundation(*statePath, *logLevel)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx := cmd.Context()
			cfg, err := config.Load(ctx, st)
			if err != nil {
				return err
			}

			// First-run convenience: if no admin password exists yet, generate
			// one and print it once so `indiepg serve` (e.g. `make run`) can be
			// logged into without a separate `install` step.
			if _, err := server.EnsureAdminPassword(ctx, st, log); err != nil {
				return err
			}

			srv, err := server.New(server.Options{
				Config: cfg,
				Store:  st,
				Logger: log,
			})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			log.Info("starting indiepg", "version", core.Version, "bind", cfg.BindAddr)
			return srv.ListenAndServe(ctx)
		},
	}
}

func installCmd(statePath, logLevel *string) *cobra.Command {
	var (
		label     string
		bindAddr  string
		password  string
		noService bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up indiepg: provision Postgres, install the service, print URL + password",
		Long: "Install provisions the native Postgres, sets the admin password, and " +
			"installs+starts a systemd service so the panel is running and reboot-safe " +
			"after this one command. It ends by printing the panel URL and a one-time " +
			"admin password. Safe to re-run.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, st, err := openFoundation(*statePath, *logLevel)
			if err != nil {
				return err
			}
			defer st.Close()

			return server.Install(cmd.Context(), server.InstallOptions{
				Store:     st,
				Logger:    log,
				Label:     label,
				BindAddr:  bindAddr,
				Password:  password,
				StatePath: *statePath,
				NoService: noService,
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "human label for this panel (default: hostname)")
	cmd.Flags().StringVar(&bindAddr, "bind", config.DefaultBindAddr, "private bind address")
	cmd.Flags().StringVar(&password, "password", "", "admin password (generated and shown once if empty)")
	cmd.Flags().BoolVar(&noService, "no-service", false, "do not install/start the systemd service")
	return cmd
}

// serviceCmd builds a thin start/stop/restart wrapper over the systemd unit.
func serviceCmd(use, short string, action server.ServiceAction, logLevel *string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.ControlService(cmd.Context(), core.NewLogger(core.LogLevel(*logLevel)), action)
		},
	}
}

func updateCmd(logLevel *string) *cobra.Command {
	var (
		version string
		force   bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update indiepg to the latest release binary and restart the service",
		Long: "Update checks the latest (or --version) release against the running build. " +
			"If you're already on it, it says so and does nothing. Otherwise it downloads " +
			"and checksum-verifies the new binary over the current one, then restarts the " +
			"systemd service so it takes effect. It is a binary swap only: it does NOT " +
			"change your admin password, panel config, or databases. Use --force to " +
			"reinstall the same version (e.g. to repair a corrupted binary). Requires root " +
			"and curl/wget.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.Update(cmd.Context(), server.UpdateOptions{
				Logger:  core.NewLogger(core.LogLevel(*logLevel)),
				Version: version,
				Force:   force,
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "release tag to install (default: latest)")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on the target version")
	return cmd
}

func uninstallCmd(statePath, logLevel *string) *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the indiepg service (Postgres and your data are left intact)",
		Long: "Uninstall stops and disables the systemd service and removes its unit file. " +
			"With --purge it also deletes the panel state DB (admin password, config, " +
			"instance identity) and the indiepg binary. It NEVER touches PostgreSQL or " +
			"the databases it manages — remove those yourself if you want them gone.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return server.Uninstall(cmd.Context(), server.UninstallOptions{
				Logger:    core.NewLogger(core.LogLevel(*logLevel)),
				StatePath: *statePath,
				Purge:     purge,
			})
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the panel state DB and the indiepg binary")
	return cmd
}

func resetPasswordCmd(statePath, logLevel *string) *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset the admin password (requires SSH/root on the box)",
		Long: "Reset the admin password from an SSH/root session on the box. With no " +
			"--password flag it generates a strong one and prints it once; pass " +
			"--password to set a specific value.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, st, err := openFoundation(*statePath, *logLevel)
			if err != nil {
				return err
			}
			defer st.Close()

			return server.ResetPassword(cmd.Context(), st, log, password)
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "new admin password (generated and shown once if empty)")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the indiepg version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(core.Version)
		},
	}
}

// exitCode maps a panel error code to a stable process exit code.
func exitCode(err error) int {
	switch core.CodeOf(err) {
	case core.CodeValidation:
		return 3
	case core.CodeSafety:
		return 4
	case core.CodeExec:
		return 5
	case core.CodeOwnership:
		return 6
	case core.CodeAuth, core.CodeLocked:
		return 7
	case core.CodeNotFound:
		return 8
	default:
		return 1
	}
}
