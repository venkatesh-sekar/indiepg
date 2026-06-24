package pg

import (
	"context"
	"sort"
	"strings"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// RoleVerifier pairs a login role with its stored password verifier from
// pg_authid.rolpassword — the SCRAM challenge material the PgBouncer auth_file
// (userlist.txt) is built from. The verifier is secret-adjacent (it cannot be
// replayed to log in, but it is the server-side challenge), so it must never be
// logged; callers feed it straight into pgbouncer.RenderUserlist.
type RoleVerifier struct {
	Name     string
	Verifier string
}

// verifierFieldSep separates the role name from its verifier in the psql -tA
// output. A SCRAM-SHA-256 verifier (and an md5 one) never contains '|', and the
// role names are validated to an identifier charset that excludes it, so
// splitting each output line on the first '|' is unambiguous.
const verifierFieldSep = "|"

// RoleVerifiers reads the stored password verifiers for the named login roles
// from pg_authid, the one input the PgBouncer auth_file installer
// (Manager.EnsureUserlist) still needs fed. pg_authid.rolpassword is
// superuser-only, so this always runs via the privileged psql path (the postgres
// OS user over the local socket, peer-authenticated) — the panel's
// non-superuser pool role cannot read it.
//
// It is strict because the auth_file is a security boundary:
//   - every requested role name is validated as a PostgreSQL identifier and is
//     placed into the query via QuoteLiteral — defense in depth against SQL
//     injection. The roles indiepg pools are its own provisioned app roles, but
//     the name still travels through a SQL statement, so it is never trusted raw.
//   - a requested role that does not exist is an error naming it: you cannot pool
//     a role that is not in the cluster.
//   - a role whose rolpassword is NULL/empty has no stored password and could not
//     authenticate through the pooler; it is surfaced as an error rather than
//     silently dropped, because a missing auth_file entry would lock that app out
//     of the pooler.
//   - duplicate requested names are de-duplicated (asking for the same role twice
//     is not an error).
//   - whether a verifier is SCRAM-SHA-256 (vs the weaker md5/plaintext kinds) is
//     deliberately NOT checked here: pgbouncer.RenderUserlist is the single gate
//     that refuses a non-SCRAM verifier (and never downgrades). This returns the
//     stored verifier verbatim.
//
// The result is ordered by role name (deterministic) so an unchanged role set
// renders a byte-identical auth_file and the enable flow can skip a reload.
func (m *Manager) RoleVerifiers(ctx context.Context, roleNames []string) ([]RoleVerifier, error) {
	if m.runner == nil {
		return nil, core.InternalError("pg: RoleVerifiers requires a Runner")
	}
	if len(roleNames) == 0 {
		return nil, core.ValidationError("pg: no roles requested for the pooler auth_file").
			WithHint("name at least one login role to route through the pooler")
	}

	// Validate + de-duplicate while preserving the requested set. Each name is
	// QuoteLiteral'd for the IN-list; ValidateIdentifier is the defense-in-depth
	// gate that also rejects an empty/oversized/reserved name before any SQL runs.
	wanted := make(map[string]struct{}, len(roleNames))
	literals := make([]string, 0, len(roleNames))
	for _, name := range roleNames {
		if err := core.ValidateIdentifier(name, "role"); err != nil {
			return nil, err
		}
		if _, dup := wanted[name]; dup {
			continue
		}
		wanted[name] = struct{}{}
		literals = append(literals, core.QuoteLiteral(name))
	}

	// No ORDER BY: rows are parsed into a map and the deterministic output order
	// comes from sort.Strings over the requested names below (which also detects a
	// requested role the query did not return).
	query := "SELECT rolname, COALESCE(rolpassword, '') FROM pg_authid WHERE rolname IN (" +
		strings.Join(literals, ", ") + ")"

	out, err := m.runPsql(ctx, defaultConnectDatabase, query)
	if err != nil {
		return nil, err
	}

	found := make(map[string]string, len(wanted))
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, verifierFieldSep, 2)
		if len(parts) != 2 {
			return nil, core.InternalError("pg: unexpected pg_authid output line %q", line)
		}
		found[parts[0]] = parts[1]
	}

	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]RoleVerifier, 0, len(names))
	for _, name := range names {
		verifier, ok := found[name]
		if !ok {
			return nil, core.NotFoundError("pg: role %q does not exist; cannot pool it", name).
				WithHint("create the role in Postgres before routing it through the pooler")
		}
		if verifier == "" {
			return nil, core.ValidationError(
				"pg: role %q has no stored password; it cannot authenticate through the pooler", name,
			).WithHint("set a password for the role (ALTER ROLE ... PASSWORD '...') so PgBouncer can authenticate it")
		}
		result = append(result, RoleVerifier{Name: name, Verifier: verifier})
	}
	return result, nil
}
