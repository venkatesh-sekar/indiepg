package pg

import (
	"context"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// RoleInfo describes one cluster role for the roles list.
type RoleInfo struct {
	Name        string
	CanLogin    bool
	IsSuperuser bool
	MemberOf    []string
}

// listRolesSQL lists non-internal roles with their login/superuser flags and the
// group roles each is a direct member of. Roles whose name begins with "pg_" are
// PostgreSQL's built-in default roles and are hidden from the panel listing.
const listRolesSQL = `
SELECT r.rolname,
       r.rolcanlogin,
       r.rolsuper,
       COALESCE(
         ARRAY(
           SELECT g.rolname
           FROM pg_catalog.pg_auth_members m
           JOIN pg_catalog.pg_roles g ON g.oid = m.roleid
           WHERE m.member = r.oid
           ORDER BY g.rolname
         ),
         '{}'
       ) AS member_of
FROM pg_catalog.pg_roles r
WHERE r.rolname NOT LIKE 'pg\_%'
ORDER BY r.rolname`

// ListRoles returns the cluster's roles using the read-only pool.
func (m *Manager) ListRoles(ctx context.Context) ([]RoleInfo, error) {
	pool := m.ReadPool()
	if pool == nil {
		return nil, core.InternalError("pg: not connected").
			WithHint("call Connect before ListRoles")
	}

	rows, err := pool.Query(ctx, listRolesSQL)
	if err != nil {
		return nil, core.InternalError("pg: listing roles").Wrap(err)
	}
	defer rows.Close()

	var out []RoleInfo
	for rows.Next() {
		var r RoleInfo
		if err := rows.Scan(&r.Name, &r.CanLogin, &r.IsSuperuser, &r.MemberOf); err != nil {
			return nil, core.InternalError("pg: scanning role row").Wrap(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, core.InternalError("pg: reading roles").Wrap(err)
	}
	return out, nil
}
