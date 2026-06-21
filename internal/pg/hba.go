package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// pg_hba.conf authentication for the panel's dedicated roles.
//
// The panel connects to its managed Postgres over the LOCAL unix socket as two
// dedicated, non-superuser roles (ReadOnlyRole, AdminRole). Those roles have no
// matching OS user — so the default `local all all peer` rule rejects them — and
// no password, by design. To make the connection work we add a tightly scoped
// trust rule for ONLY these two roles on ONLY the local socket, placed at the
// top of pg_hba.conf so it is matched before the default rules.
//
// Security rationale: the box is single-tenant and the panel binds privately, so
// a local-socket trust for the panel's own roles is the intended trust boundary.
// The read-only role's safety still rests on privilege denial (it holds no write
// grants), not on this auth rule. An operator sharing the host can replace these
// lines with scram-sha-256 + stored passwords.

const (
	hbaMarkerBegin = "# >>> indiepg managed (socket auth for panel roles) — do not edit >>>"
	hbaMarkerEnd   = "# <<< indiepg managed <<<"
)

// hbaManagedBlock is the pg_hba.conf block granting the two panel roles trust
// auth over the local unix socket only.
func hbaManagedBlock() string {
	lines := []string{
		hbaMarkerBegin,
		"# indiepg connects as these dedicated roles over the local unix socket.",
		"# Trust is scoped to the local socket and to these roles only; the",
		"# read-only role's real boundary is privilege denial, not this rule.",
		"local   all   " + ReadOnlyRole + "   trust",
		"local   all   " + AdminRole + "   trust",
		hbaMarkerEnd,
		"",
	}
	return strings.Join(lines, "\n")
}

// injectHBARules prepends the managed block to existing pg_hba.conf content when
// it is not already present. It is pure and idempotent: a content that already
// carries the marker is returned unchanged with changed=false.
func injectHBARules(existing string) (updated string, changed bool) {
	if strings.Contains(existing, hbaMarkerBegin) {
		return existing, false
	}
	block := hbaManagedBlock()
	if existing == "" {
		return block, true
	}
	return block + "\n" + existing, true
}

// EnsureSocketAuth makes the panel's roles connectable over the local socket by
// adding the managed trust block to pg_hba.conf (idempotently) and reloading
// Postgres. It locates the live hba file via SHOW hba_file, preserves the file's
// ownership and permissions, writes atomically, and reloads via
// pg_reload_conf(). It requires root + a peer-authenticated postgres connection
// (the posture Provision and the root-run serve process already have).
//
// It is called both during Provision and best-effort at serve startup, so an
// existing install is self-healed by a binary upgrade + restart without a full
// re-install. It returns whether the file was changed; a no-op (rule already
// present) is not an error.
func (m *Manager) EnsureSocketAuth(ctx context.Context) (bool, error) {
	if m.runner == nil {
		return false, core.InternalError("pg: ensureSocketAuth requires a Runner")
	}

	out, err := m.runPsql(ctx, defaultConnectDatabase, "SHOW hba_file")
	if err != nil {
		return false, err
	}
	path := strings.TrimSpace(out)
	if path == "" {
		return false, core.InternalError("pg: could not determine pg_hba.conf path")
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, core.InternalError("pg: stat pg_hba.conf %q", path).Wrap(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, core.InternalError("pg: read pg_hba.conf %q", path).Wrap(err)
	}

	updated, changed := injectHBARules(string(data))
	if !changed {
		return false, nil
	}

	if err := writePreserving(path, []byte(updated), info); err != nil {
		return false, err
	}

	// Reload so the new rule takes effect without a restart. pg_reload_conf()
	// re-reads pg_hba.conf as well as postgresql.conf.
	if _, err := m.runPsql(ctx, defaultConnectDatabase, "SELECT pg_reload_conf()"); err != nil {
		return false, core.InternalError("pg: reloading config after pg_hba.conf update").Wrap(err)
	}
	return true, nil
}

// writePreserving atomically replaces path with data, preserving the original
// file's mode and ownership (so the postgres process can still read it). It
// writes a temp file in the same directory and renames it into place.
func writePreserving(path string, data []byte, info os.FileInfo) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".indiepg-hba-*")
	if err != nil {
		return core.InternalError("pg: creating temp pg_hba.conf").Wrap(err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return core.InternalError("pg: writing temp pg_hba.conf").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return core.InternalError("pg: closing temp pg_hba.conf").Wrap(err)
	}

	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return core.InternalError("pg: chmod temp pg_hba.conf").Wrap(err)
	}
	// Preserve owner (root rewriting a postgres-owned file must hand it back so
	// the postgres process can read it). Best-effort: a platform without the
	// syscall stat shape simply keeps the current owner.
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(tmpName, int(st.Uid), int(st.Gid))
	}

	if err := os.Rename(tmpName, path); err != nil {
		return core.InternalError("pg: replacing pg_hba.conf").Wrap(err)
	}
	return nil
}
