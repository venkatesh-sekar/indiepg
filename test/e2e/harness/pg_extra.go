//go:build e2e

package harness

import (
	"fmt"
	"strings"
)

// pg_extra.go adds a connect-AS-a-login-role helper on *PG for the read-only
// enforcement scenario. The frozen PG ground-truth seam (Scalar/Exec) connects
// over the unix socket as the postgres SUPERUSER via peer auth, so it can never
// observe a non-superuser role's privilege denials. PsqlAsRole instead opens a
// REAL authenticated TCP connection as an ordinary login role, so the database's
// own privilege system — not the panel — decides whether a statement runs. This
// is what proves read-only is enforced at the database level, not just the UI.
//
// It is an additive method on the frozen *PG type (same package, new file); the
// frozen pg.go is untouched.

// PsqlAsRole connects to the panel's managed cluster over TCP at 127.0.0.1 AS
// the given login role using password, and runs a single statement with
// ON_ERROR_STOP=1. It returns trimmed stdout on success, or an error carrying
// psql's stderr — e.g. "permission denied for table ..." when the role lacks the
// privilege. -w disables any interactive password prompt so a bad/blocked login
// fails fast and deterministically instead of hanging.
func (pg *PG) PsqlAsRole(role, password, db, sql string) (string, error) {
	port, err := pg.Scalar("SHOW port")
	if err != nil {
		return "", fmt.Errorf("resolve cluster TCP port: %w", err)
	}
	ctx, cancel := shortCtx()
	defer cancel()
	out, stderr, err := dockerExec(ctx, pg.env.panelContainer, "",
		"env", "PGPASSWORD="+password,
		"psql", "-h", "127.0.0.1", "-p", strings.TrimSpace(port),
		"-U", role, "-d", db, "-w",
		"-v", "ON_ERROR_STOP=1", "-tAqX", "-c", sql)
	if err != nil {
		return "", fmt.Errorf("psql as role %q on db %q failed: %w\nstderr: %s",
			role, db, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(out), nil
}
