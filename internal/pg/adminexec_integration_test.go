//go:build integration

// Integration test for the real AdminNewApp execution path against a live
// Postgres reachable over a unix socket. It is gated behind the "integration"
// build tag and additionally skips unless INDIEPG_TEST_SOCKET points at a
// cluster that already has the indiepg_admin and indiepg_readonly login roles.
//
// Run via the harness in the repo (a throwaway cluster), e.g.:
//
//	go test -tags integration ./internal/pg/ -run TestAdminNewApp_Integration -v
package pg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/pg/admin"
)

func socketDSN(sock, user, database string) string {
	// Trust auth on the throwaway cluster: no password needed.
	return fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable", sock, user, database)
}

func TestAdminNewApp_Integration(t *testing.T) {
	sock := os.Getenv("INDIEPG_TEST_SOCKET")
	if sock == "" {
		t.Skip("set INDIEPG_TEST_SOCKET to run the live provisioning test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := New(Options{Config: config.Config{PGSocketDir: sock}})
	require.NoError(t, m.Connect(ctx))
	defer m.Close()

	plan := admin.NewAppPlan{
		Database:      "athenacover",
		OwnerRole:     "athenacover_owner",
		ReadwriteUser: "athenacover_readwrite",
		ReadwritePass: "rwpass123",
		ReadonlyUser:  "athenacover_readonly",
		ReadonlyPass:  "ropass123",
	}

	// The actual reported bug: this used to fail with
	// "must be member of role athenacover_owner", orphaning the owner role.
	res, err := m.AdminNewApp(ctx, plan)
	require.NoError(t, err, "provisioning a new app should succeed")
	t.Logf("recorded statements:\n%s", strings.Join(res.Statements, "\n"))

	// As the read-write user: create a table AFTER provisioning and write to it.
	rw, err := pgx.Connect(ctx, socketDSN(sock, plan.ReadwriteUser, plan.Database))
	require.NoError(t, err)
	defer rw.Close(ctx)
	_, err = rw.Exec(ctx, "CREATE TABLE orders(id serial primary key, total int)")
	require.NoError(t, err, "read-write user must be able to create tables")
	_, err = rw.Exec(ctx, "INSERT INTO orders(total) VALUES (42)")
	require.NoError(t, err, "read-write user must be able to write")

	// As the read-only user: must be able to READ the table rw just created
	// (this is the case that was silently broken — default privileges had been
	// attached to the admin role instead of the read-write creator).
	ro, err := pgx.Connect(ctx, socketDSN(sock, plan.ReadonlyUser, plan.Database))
	require.NoError(t, err)
	defer ro.Close(ctx)
	var n int
	require.NoError(t, ro.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&n),
		"read-only user must be able to read a table the app created after provisioning")
	require.Equal(t, 1, n)

	// The read-only guarantee: ro must NOT be able to write (privilege denial,
	// independent of any session GUC).
	_, werr := ro.Exec(ctx, "INSERT INTO orders(total) VALUES (99)")
	require.Error(t, werr, "read-only user must be refused writes")

	// Re-provisioning the same name now reports a clean conflict rather than a
	// mid-sequence failure, because provisioning no longer orphans objects.
	_, err = m.AdminNewApp(ctx, plan)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already present")
}
