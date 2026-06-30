package pgbouncer

import (
	"context"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg"
)

// EnabledConfigKey is the config-store key recording whether the operator has
// turned the opt-in pooler on. Absent (or any value other than enabledValue)
// means OFF: the pooler ships disabled and only runs after an explicit Enable.
const EnabledConfigKey = "pooler.enabled"

// enabledValue is the single value EnabledConfigKey holds when the pooler is on.
const enabledValue = "true"

// disabledValue is the value EnabledConfigKey holds after the operator turns the
// pooler off. Any value other than enabledValue already reads as OFF (see
// IsEnabled), but recording an explicit "false" makes a deliberate disable
// distinguishable from the never-enabled default.
const disabledValue = "false"

// VerifierSource supplies the SCRAM-SHA-256 verifiers for the login roles routed
// through the pooler, read from pg_authid. *pg.Manager satisfies it. It is an
// interface so the orchestrator stays decoupled from the privileged psql path
// and is unit-testable with a fake.
type VerifierSource interface {
	RoleVerifiers(ctx context.Context, roleNames []string) ([]pg.RoleVerifier, error)
}

// PoolerState persists the operator's enable decision so the panel (and a future
// runtime check) can tell, after a restart, whether the pooler is meant to be
// on. *store.Store satisfies it (GetConfig/SetConfig).
type PoolerState interface {
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
}

// Compile-time proof that the privileged pg manager can feed the enable flow.
var _ VerifierSource = (*pg.Manager)(nil)

// EnableParams is the operator-facing input to Enable.
type EnableParams struct {
	// RoleNames are the login roles whose traffic the pooler accepts. At least one
	// is required — an empty auth_file would lock every app out of the pooler.
	RoleNames []string
	// PGMaxConnections is Postgres' configured max_connections, the basis for the
	// host-sized pool sizing (RecommendPool). Must be positive.
	PGMaxConnections int
	// Profile selects the workload sizing (an unrecognised value falls back to
	// Mixed inside RecommendPool).
	Profile pg.WorkloadProfile

	// PGPort / ListenPort override the loopback ports; zero uses the safe defaults
	// (5432 upstream, 6432 listener).
	PGPort     int
	ListenPort int
}

// EnableResult reports what the (idempotent) enable flow did, so a caller/UI can
// honestly show "already up to date" vs "applied changes".
type EnableResult struct {
	PooledRoles     []string           `json:"pooled_roles"`
	Pool            PoolRecommendation `json:"pool"`
	ConfigChanged   bool               `json:"config_changed"`
	UserlistChanged bool               `json:"userlist_changed"`
	Reloaded        bool               `json:"reloaded"`
	Running         bool               `json:"running"`
}

// Enable turns the opt-in pooler on and is the single entry point that wires the
// primitives together. It is OFF by default (nothing here runs until called) and
// idempotent/re-runnable: a second call with the same inputs installs nothing
// new, rewrites nothing, and does not bounce a healthy pooler.
//
// The steps run in a deliberate order:
//  1. InstallPackage — first, because it creates /etc/pgbouncer and the packaged
//     pgbouncer unit, both of which the config/auth_file install below depend on.
//     (The Debian/Ubuntu package ships NO dedicated `pgbouncer` OS user; its unit
//     runs PgBouncer as the existing `postgres` user, which is therefore the owner
//     the managed config falls back to — see bouncerOwnerCandidates.)
//     ResetPackageConffiles then clears the apt-shipped pristine default
//     pgbouncer.ini (an UNTOUCHED package conffile, detected by its dpkg-recorded
//     md5) so EnsureConfig's marker guard does not 409 on a clean box; an
//     operator-edited config is left in place and still hard-stops the flow.
//  2. RoleVerifiers → EnsureConfig — read the roles' SCRAM verifiers from
//     pg_authid (privileged path), then size the pool and install pgbouncer.ini.
//     EnsureConfig's marker guard is the flow's only ownership check, so it runs
//     BEFORE any secret-adjacent file: a foreign distro/operator config is a hard
//     stop with no auth_file left behind.
//  3. EnsureUserlist — install the auth_file (pointed at by the config from (2)),
//     now that indiepg ownership of /etc/pgbouncer is confirmed. RoleVerifiers
//     and RenderUserlist are strict (missing role / no password / non-SCRAM all
//     error), so a half-built auth_file that locks an app out is never written.
//  4. EnsureRuntimeDir — provision PgBouncer's pidfile directory (/run/pgbouncer)
//     BEFORE the unit starts: a RuntimeDirectory= drop-in (+ daemon-reload) makes
//     systemd recreate it on every start (reboot-safe), plus an explicit
//     `install -d` for the already-booted box the package was just apt-installed
//     on. Without it `systemctl enable --now` fails: the post-boot install missed
//     the package's boot-time runtime provisioning, so the daemon could not open
//     its pidfile ("No such file or directory").
//  5. EnableNow — enable + start the unit (idempotent).
//  6. Reload — apply a changed config/auth_file with a SIGHUP (no dropped client
//     connections), and ONLY when something changed, so an unchanged pooler is
//     never bounced.
//  7. IsRunning — verify the unit is actually up before recording success;
//     "couldn't ask" surfaces as an error, never a silent "down".
//  8. Persist enabled=true — last, only once the pooler is confirmed up, so the
//     stored state can never contradict a failed bring-up.
//
// On any error it stops and returns the partial result; nothing past the failed
// step runs, and the enabled flag is not set.
func (m *Manager) Enable(ctx context.Context, src VerifierSource, state PoolerState, p EnableParams) (EnableResult, error) {
	var res EnableResult
	if m.runner == nil {
		return res, core.InternalError("pgbouncer: Enable requires a Runner")
	}
	if src == nil {
		return res, core.InternalError("pgbouncer: Enable requires a verifier source")
	}
	if state == nil {
		return res, core.InternalError("pgbouncer: Enable requires a state store")
	}
	if len(p.RoleNames) == 0 {
		return res, core.ValidationError("pgbouncer: name at least one login role to route through the pooler").
			WithHint("an empty auth_file would lock every app out of the pooler")
	}
	if p.PGMaxConnections < 1 {
		return res, core.ValidationError("pgbouncer: PGMaxConnections must be positive to size the pool")
	}

	if err := m.InstallPackage(ctx); err != nil {
		return res, err
	}

	// The apt install ships a pristine default pgbouncer.ini that carries no
	// managed marker; on a clean box EnsureConfig's marker guard would 409 on it
	// and block the enable. Clear that (and any other UNTOUCHED package conffile we
	// manage) BEFORE EnsureConfig so it can write the managed config. A genuinely
	// operator-edited conffile is detected by its changed md5 and left in place, so
	// the marker guard still refuses to clobber a hand-edited config.
	if err := m.ResetPackageConffiles(ctx); err != nil {
		return res, err
	}

	verifiers, err := src.RoleVerifiers(ctx, p.RoleNames)
	if err != nil {
		return res, err
	}
	entries := make([]UserlistEntry, 0, len(verifiers))
	roles := make([]string, 0, len(verifiers))
	for _, v := range verifiers {
		entries = append(entries, UserlistEntry{Username: v.Name, Verifier: v.Verifier})
		roles = append(roles, v.Name)
	}
	res.PooledRoles = roles

	// Install pgbouncer.ini BEFORE the auth_file. EnsureConfig's marker guard is
	// the only ownership check in the flow — it refuses to clobber a hand-written
	// or distro-shipped pgbouncer.ini. EnsureUserlist has no such guard (the
	// userlist format cannot carry a marker), so it must never write its
	// secret-adjacent SCRAM verifiers into /etc/pgbouncer until the marker guard
	// has confirmed indiepg owns that directory. Config-first makes a foreign
	// config a hard stop with NO auth_file left behind.
	pool := RecommendPool(p.PGMaxConnections, p.Profile)
	res.Pool = pool
	configChanged, err := m.EnsureConfig(ctx, ConfigParams{
		Pool:       pool,
		PGPort:     p.PGPort,
		ListenPort: p.ListenPort,
		// Point the config's auth_file at the file we actually install, so the two
		// stay consistent even when confDir is overridden (tests; non-default dir).
		AuthFile: m.UserlistPath(),
	})
	if err != nil {
		return res, err
	}
	res.ConfigChanged = configChanged

	userlistChanged, err := m.EnsureUserlist(ctx, entries)
	if err != nil {
		return res, err
	}
	res.UserlistChanged = userlistChanged

	// Provision PgBouncer's runtime (pidfile) directory BEFORE starting the unit.
	// The package was apt-installed above — after systemd booted — so the package's
	// boot-time provisioning of /run/pgbouncer never ran, and `systemctl enable
	// --now` would otherwise fail with "could not open pidfile ... No such file or
	// directory". A RuntimeDirectory= drop-in makes systemd recreate it on every
	// start (reboot-safe); an explicit `install -d` covers the already-booted box.
	if err := m.EnsureRuntimeDir(ctx); err != nil {
		return res, err
	}

	if err := m.EnableNow(ctx); err != nil {
		return res, err
	}

	if configChanged || userlistChanged {
		if err := m.Reload(ctx); err != nil {
			return res, err
		}
		res.Reloaded = true
	}

	running, err := m.IsRunning(ctx)
	if err != nil {
		return res, err
	}
	res.Running = running
	if !running {
		return res, core.InternalError("pgbouncer: service did not come up after enable").
			WithHint("check `systemctl status pgbouncer` and the pgbouncer log")
	}

	if err := state.SetConfig(ctx, EnabledConfigKey, enabledValue); err != nil {
		return res, err
	}

	m.log.InfoCtx(ctx, "PgBouncer pooler enabled",
		"roles", len(roles), "config_changed", configChanged,
		"userlist_changed", userlistChanged, "reloaded", res.Reloaded)
	return res, nil
}

// Disable turns the opt-in pooler back off — the inverse of Enable — so an
// operator who enabled it is never stuck shelling in to undo it. It is
// idempotent/re-runnable: a second call stops an already-stopped unit cleanly and
// re-records the off state.
//
// The order mirrors Enable's "persist last" safety rule: stop the service FIRST,
// then persist enabled=false. If the stop fails we return the error WITHOUT
// touching the flag, so the panel never reports the pooler as off while it is in
// fact still running and accepting connections — the flag only flips to off once
// the service is confirmed down. (Recording off first, then failing to stop,
// would tell the operator a live pooler is gone, the more dangerous lie.)
func (m *Manager) Disable(ctx context.Context, state PoolerState) error {
	if m.runner == nil {
		return core.InternalError("pgbouncer: Disable requires a Runner")
	}
	if state == nil {
		return core.InternalError("pgbouncer: Disable requires a state store")
	}

	if err := m.DisableNow(ctx); err != nil {
		return err
	}

	if err := state.SetConfig(ctx, EnabledConfigKey, disabledValue); err != nil {
		return err
	}

	m.log.InfoCtx(ctx, "PgBouncer pooler disabled")
	return nil
}

// IsEnabled reports whether the operator has enabled the pooler. An unset key
// (NotFound) is the default-off state, not an error — the pooler ships disabled.
func IsEnabled(ctx context.Context, state PoolerState) (bool, error) {
	if state == nil {
		return false, core.InternalError("pgbouncer: IsEnabled requires a state store")
	}
	v, err := state.GetConfig(ctx, EnabledConfigKey)
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return false, nil
		}
		return false, err
	}
	return v == enabledValue, nil
}
