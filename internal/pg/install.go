package pg

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// This file owns the PGDG (PostgreSQL Global Development Group) apt repository
// setup and the versioned package install that replaces the generic
// `postgresql` metapackage. PGDG is the precondition for the whole feature: only
// it ships every supported major side-by-side
// (/usr/lib/postgresql/16/bin AND /usr/lib/postgresql/17/bin), which is what
// makes install-time version selection and pg_upgradecluster possible.
//
// Every filesystem write here goes through the exec.Runner (mkdir/curl/tee), not
// os.WriteFile, so the steps are unit-testable with a FakeRunner exactly like
// the rest of Provision — and so a non-root test process never touches /etc.

const (
	// pgdgKeyURL is the PGDG repository signing key.
	pgdgKeyURL = "https://www.postgresql.org/media/keys/ACCC4CF8.asc"
	// pgdgKeyDir / pgdgKeyPath are where the dearmored key is stored and
	// referenced by the sources file's signed-by option.
	pgdgKeyDir  = "/usr/share/postgresql-common/pgdg"
	pgdgKeyPath = pgdgKeyDir + "/apt.postgresql.org.asc"
	// pgdgSourcesPath is the apt sources file pointing at apt.postgresql.org.
	pgdgSourcesPath = "/etc/apt/sources.list.d/pgdg.list"
	// pgdgPinPath holds the apt pin that keeps the box on its installed major so
	// an unattended `apt upgrade` cannot silently jump majors.
	pgdgPinPath = "/etc/apt/preferences.d/99-indiepg-pgdg.pref"
	// pgdgBaseURL is the apt.postgresql.org pool base.
	pgdgBaseURL = "https://apt.postgresql.org/pub/repos/apt"
)

// versionedPackages is the set of packages a versioned install lays down for a
// given major: the versioned server (which BUNDLES the contrib modules on
// Debian/Ubuntu — pg_stat_statements, citext, hstore, pgcrypto, … all ship
// inside postgresql-<major> since PG 10; it merely Provides the virtual
// "postgresql-contrib-<major>", there is NO installable "postgresql-<major>-contrib"
// package on PGDG/Debian), plus pgbackrest (kept here, not installed lazily,
// because the backup feature shells out to the `pgbackrest` binary).
//
// Requesting a literal "postgresql-<major>-contrib" makes `apt-get install` abort
// with "Unable to locate package", which would fail every fresh install — so the
// contrib bundle is obtained through the server package alone.
func versionedPackages(major int) []string {
	return []string{
		fmt.Sprintf("postgresql-%d", major),
		"pgbackrest",
	}
}

// detectOSCodename returns the distro release codename (e.g. "bookworm",
// "jammy") from /etc/os-release's VERSION_CODENAME, which is what the PGDG
// sources line keys on. An empty result (field absent / file unreadable) is
// returned without error so Provision still proceeds on a host where the
// definitive codename comes from elsewhere; the install-time preflight is where
// an unsupported release becomes a hard block.
func detectOSCodename() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return parseOSReleaseCodename(string(data))
}

// parseOSReleaseCodename extracts VERSION_CODENAME (falling back to the codename
// embedded in UBUNTU_CODENAME) from /etc/os-release content. It is split out as
// a pure function for testability.
func parseOSReleaseCodename(content string) string {
	fields := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		fields[strings.TrimSpace(k)] = v
	}
	if c := fields["VERSION_CODENAME"]; c != "" {
		return c
	}
	return fields["UBUNTU_CODENAME"]
}

// pgdgSupportedCodenames is the set of Debian/Ubuntu release codenames PGDG
// publishes for. It backs the install-time "OS supported by PGDG" preflight
// check. It is intentionally a small, current list; an unrecognised codename is
// surfaced as a blocker rather than silently producing a broken sources line.
var pgdgSupportedCodenames = map[string]bool{
	// Debian
	"bookworm": true, // 12
	"bullseye": true, // 11
	"trixie":   true, // 13
	// Ubuntu
	"jammy":    true, // 22.04
	"noble":    true, // 24.04
	"focal":    true, // 20.04
	"oracular": true, // 24.10
}

// ensurePGDGRepo installs the PGDG signing key and apt sources file (idempotent;
// overwriting them is harmless) and refreshes the package index, so the
// versioned postgresql-<major> packages become installable. It returns the
// commands run for the provision Result. The sources line carries the detected
// release codename; on a host where it cannot be detected the apt-get update
// surfaces the failure.
func (m *Manager) ensurePGDGRepo(ctx context.Context) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: configuring the PGDG repo requires a Runner")
	}
	steps := make([]string, 0, 4)

	// 1. Key directory.
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "mkdir",
		Args:    []string{"-p", pgdgKeyDir},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: creating PGDG key directory failed").Wrap(err)
	}
	steps = append(steps, aptStep(res, "mkdir -p "+pgdgKeyDir))

	// 2. Download the signing key.
	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "curl",
		Args:    []string{"--fail", "--silent", "--show-error", "--location", "--output", pgdgKeyPath, pgdgKeyURL},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: downloading the PGDG signing key failed").
			WithHint("the install host needs outbound HTTPS and curl to reach apt.postgresql.org").Wrap(err)
	}
	steps = append(steps, aptStep(res, "curl -o "+pgdgKeyPath+" "+pgdgKeyURL))

	// 3. Write the sources file (via tee so the write is runner-mediated and the
	// root-owned file lands with the right ownership/mode).
	codename := detectOSCodename()
	sourcesLine := fmt.Sprintf("deb [signed-by=%s] %s %s-pgdg main\n", pgdgKeyPath, pgdgBaseURL, codename)
	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "tee",
		Args:    []string{pgdgSourcesPath},
		Stdin:   sourcesLine,
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: writing the PGDG sources file failed").Wrap(err)
	}
	steps = append(steps, "wrote "+pgdgSourcesPath+" ("+strings.TrimSpace(sourcesLine)+")")

	// 4. Refresh the package index so the versioned packages resolve.
	res, err = m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    []string{"update"},
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: apt-get update failed").Wrap(err)
	}
	steps = append(steps, aptStep(res, "apt-get update"))

	return steps, nil
}

// installVersionedPackages installs the server + contrib for the given major
// plus pgbackrest, replacing the generic `postgresql` metapackage. The package
// names are built from a validated integer major, never operator text, and
// passed as an exec arg vector (never a shell string).
func (m *Manager) installVersionedPackages(ctx context.Context, major int) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: installing Postgres requires a Runner")
	}
	pkgs := versionedPackages(major)
	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "apt-get",
		Args:    append([]string{"install", "-y"}, pkgs...),
		Env:     []string{"DEBIAN_FRONTEND=noninteractive"},
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: installing postgresql-%d failed", major).
			WithHint("ensure the PGDG apt repository is reachable and the major is published for this OS release").Wrap(err)
	}
	return []string{aptStep(res, "apt-get install -y "+strings.Join(pkgs, " "))}, nil
}

// writeAptPin pins the installed major so an unattended `apt upgrade` cannot
// silently move the cluster to a newer major. The versioned packages
// (postgresql-<major>) still receive minor updates; only the unversioned
// metapackages are held to the installed series. Written via tee for the same
// runner-mediated reason as the sources file.
func (m *Manager) writeAptPin(ctx context.Context, major int) ([]string, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: writing the apt pin requires a Runner")
	}
	pin := fmt.Sprintf(`# indiepg-managed: keep this box on PostgreSQL %d.
# Versioned packages (postgresql-%d) still receive minor updates; the
# unversioned metapackages are held to the %d series so an unattended
# `+"`apt upgrade`"+` cannot silently jump to a newer major. Major moves happen
# only through the panel's deliberate upgrade flow.
Package: postgresql postgresql-client postgresql-contrib
Pin: version %d*
Pin-Priority: 1001
`, major, major, major, major)

	res, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "tee",
		Args:    []string{pgdgPinPath},
		Stdin:   pin,
		Timeout: commandTimeout,
	})
	if err != nil {
		return nil, core.ExecError("pg: writing the apt pin failed").Wrap(err)
	}
	_ = res
	return []string{"wrote " + pgdgPinPath + fmt.Sprintf(" (pin postgresql to %d*)", major)}, nil
}

// resolveInstallMajor returns the major to install: the explicitly requested one
// (from Options.PGMajor, set via `indiepg install --pg-version`) or the catalog
// default when none was requested.
func (m *Manager) resolveInstallMajor() (int, error) {
	major := m.installMajor
	if major == 0 {
		major = DefaultMajor()
	}
	if !IsSupported(major) {
		return 0, core.ValidationError("PostgreSQL %d is not a supported version", major).
			WithHint("choose a supported major from the version catalog")
	}
	return major, nil
}
