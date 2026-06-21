package admin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

func TestParseAccessLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    AccessLevel
		wantErr bool
	}{
		{"readonly", AccessReadonly, false},
		{"readwrite", AccessReadwrite, false},
		{"owner", AccessOwner, false},
		{"READONLY", "", true},
		{"admin", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseAccessLevel(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, core.CodeValidation, core.CodeOf(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCreateRole(t *testing.T) {
	t.Run("login role", func(t *testing.T) {
		got, err := CreateRole("app_user", "s3cr3t", true)
		require.NoError(t, err)
		require.Equal(t, `CREATE ROLE "app_user" LOGIN PASSWORD 's3cr3t';`, got)
	})
	t.Run("nologin role ignores password", func(t *testing.T) {
		got, err := CreateRole("app_owner", "", false)
		require.NoError(t, err)
		require.Equal(t, `CREATE ROLE "app_owner" NOLOGIN;`, got)
	})
	t.Run("password is literal-quoted against injection", func(t *testing.T) {
		got, err := CreateRole("app_user", "p'; DROP TABLE x; --", true)
		require.NoError(t, err)
		require.Equal(t, `CREATE ROLE "app_user" LOGIN PASSWORD 'p''; DROP TABLE x; --';`, got)
	})
	t.Run("backslash password uses E-string", func(t *testing.T) {
		got, err := CreateRole("app_user", `pa\ss`, true)
		require.NoError(t, err)
		require.Equal(t, `CREATE ROLE "app_user" LOGIN PASSWORD E'pa\\ss';`, got)
	})
	t.Run("invalid role name", func(t *testing.T) {
		_, err := CreateRole("2bad", "pw", true)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("empty password for login role", func(t *testing.T) {
		_, err := CreateRole("app_user", "", true)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("injection in role name is rejected", func(t *testing.T) {
		_, err := CreateRole(`x"; DROP ROLE y; --`, "pw", true)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestCreateReadonlyUser(t *testing.T) {
	got, err := CreateReadonlyUser("ro_user", "pw123", "appdb", "public")
	require.NoError(t, err)
	require.Len(t, got, 7)

	require.Equal(t, `CREATE ROLE "ro_user" LOGIN PASSWORD 'pw123';`, got[0])
	require.Equal(t, `GRANT CONNECT ON DATABASE "appdb" TO "ro_user";`, got[1])
	require.Equal(t, `GRANT USAGE ON SCHEMA "public" TO "ro_user";`, got[2])
	require.Equal(t, `GRANT SELECT ON ALL TABLES IN SCHEMA "public" TO "ro_user";`, got[3])
	require.Equal(t, `GRANT SELECT ON ALL SEQUENCES IN SCHEMA "public" TO "ro_user";`, got[4])

	// The defining feature: ALTER DEFAULT PRIVILEGES must be present so future
	// tables/sequences are covered automatically.
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "ALTER DEFAULT PRIVILEGES IN SCHEMA \"public\" GRANT SELECT ON TABLES TO \"ro_user\";")
	require.Contains(t, joined, "ALTER DEFAULT PRIVILEGES IN SCHEMA \"public\" GRANT SELECT ON SEQUENCES TO \"ro_user\";")

	// A read-only user must never get any write privilege.
	require.NotContains(t, joined, "INSERT")
	require.NotContains(t, joined, "UPDATE")
	require.NotContains(t, joined, "DELETE")
}

func TestCreateReadonlyUserValidation(t *testing.T) {
	tests := []struct {
		name                         string
		user, pass, database, schema string
	}{
		{"bad user", "1bad", "pw", "appdb", "public"},
		{"empty pass", "ro", "", "appdb", "public"},
		{"bad database", "ro", "pw", "select", "public"},
		{"bad schema", "ro", "pw", "appdb", "2bad"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateReadonlyUser(tc.user, tc.pass, tc.database, tc.schema)
			require.Error(t, err)
			require.Equal(t, core.CodeValidation, core.CodeOf(err))
		})
	}
}

func TestCreateReadonlyUserCustomSchema(t *testing.T) {
	got, err := CreateReadonlyUser("ro", "pw", "appdb", "reporting")
	require.NoError(t, err)
	require.Contains(t, strings.Join(got, "\n"), `GRANT USAGE ON SCHEMA "reporting" TO "ro";`)
}

func TestCreateDatabase(t *testing.T) {
	t.Run("with owner", func(t *testing.T) {
		got, err := CreateDatabase("appdb", "app_owner")
		require.NoError(t, err)
		require.Equal(t, `CREATE DATABASE "appdb" OWNER "app_owner";`, got)
	})
	t.Run("without owner", func(t *testing.T) {
		got, err := CreateDatabase("appdb", "")
		require.NoError(t, err)
		require.Equal(t, `CREATE DATABASE "appdb";`, got)
	})
	t.Run("bad database name", func(t *testing.T) {
		_, err := CreateDatabase("bad-name", "")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("bad owner name", func(t *testing.T) {
		_, err := CreateDatabase("appdb", "bad owner")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestGrant(t *testing.T) {
	t.Run("readonly", func(t *testing.T) {
		got, err := Grant(AccessReadonly, "ro", "appdb", "public")
		require.NoError(t, err)
		require.Len(t, got, 6)
		require.Equal(t, `GRANT CONNECT ON DATABASE "appdb" TO "ro";`, got[0])
	})
	t.Run("readwrite includes readonly plus writes", func(t *testing.T) {
		got, err := Grant(AccessReadwrite, "rw", "appdb", "public")
		require.NoError(t, err)
		joined := strings.Join(got, "\n")
		require.Contains(t, joined, `GRANT CONNECT ON DATABASE "appdb" TO "rw";`)
		require.Contains(t, joined, `GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "public" TO "rw";`)
		require.Contains(t, joined, `GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA "public" TO "rw";`)
		require.Contains(t, joined, `ALTER DEFAULT PRIVILEGES IN SCHEMA "public" GRANT INSERT, UPDATE, DELETE ON TABLES TO "rw";`)
	})
	t.Run("owner", func(t *testing.T) {
		got, err := Grant(AccessOwner, "owner_role", "appdb", "public")
		require.NoError(t, err)
		require.Equal(t, []string{
			`ALTER DATABASE "appdb" OWNER TO "owner_role";`,
			`ALTER SCHEMA "public" OWNER TO "owner_role";`,
		}, got)
	})
	t.Run("invalid level", func(t *testing.T) {
		_, err := Grant(AccessLevel("god"), "r", "appdb", "public")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("invalid role", func(t *testing.T) {
		_, err := Grant(AccessReadonly, "bad-role", "appdb", "public")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestRevoke(t *testing.T) {
	t.Run("readonly", func(t *testing.T) {
		got, err := Revoke(AccessReadonly, "ro", "appdb", "public")
		require.NoError(t, err)
		joined := strings.Join(got, "\n")
		require.Contains(t, joined, `REVOKE CONNECT ON DATABASE "appdb" FROM "ro";`)
		require.Contains(t, joined, `REVOKE SELECT ON ALL TABLES IN SCHEMA "public" FROM "ro";`)
		require.Contains(t, joined, `ALTER DEFAULT PRIVILEGES IN SCHEMA "public" REVOKE SELECT ON TABLES FROM "ro";`)
	})
	t.Run("readwrite drops writes then readonly", func(t *testing.T) {
		got, err := Revoke(AccessReadwrite, "rw", "appdb", "public")
		require.NoError(t, err)
		joined := strings.Join(got, "\n")
		require.Contains(t, joined, `REVOKE INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "public" FROM "rw";`)
		require.Contains(t, joined, `REVOKE CONNECT ON DATABASE "appdb" FROM "rw";`)
	})
	t.Run("owner revoke is rejected", func(t *testing.T) {
		_, err := Revoke(AccessOwner, "owner_role", "appdb", "public")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("invalid role", func(t *testing.T) {
		_, err := Revoke(AccessReadonly, "bad role", "appdb", "public")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestRotatePassword(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		got, err := RotatePassword("app_user", "newpw")
		require.NoError(t, err)
		require.Equal(t, `ALTER ROLE "app_user" PASSWORD 'newpw';`, got)
	})
	t.Run("literal quoted", func(t *testing.T) {
		got, err := RotatePassword("app_user", "a'b")
		require.NoError(t, err)
		require.Equal(t, `ALTER ROLE "app_user" PASSWORD 'a''b';`, got)
	})
	t.Run("empty password rejected", func(t *testing.T) {
		_, err := RotatePassword("app_user", "")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("bad role rejected", func(t *testing.T) {
		_, err := RotatePassword("bad-role", "pw")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestDropRole(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		got, err := DropRole("app_user", "app_user")
		require.NoError(t, err)
		require.Equal(t, `DROP ROLE "app_user";`, got)
	})
	t.Run("unconfirmed is a safety error", func(t *testing.T) {
		_, err := DropRole("app_user", "wrong")
		require.Error(t, err)
		require.Equal(t, core.CodeSafety, core.CodeOf(err))
		var se *core.SafetyError
		require.ErrorAs(t, err, &se)
		require.Equal(t, "drop role", se.Operation)
	})
	t.Run("empty confirm is a safety error", func(t *testing.T) {
		_, err := DropRole("app_user", "")
		require.Error(t, err)
		require.Equal(t, core.CodeSafety, core.CodeOf(err))
	})
	t.Run("invalid role name is validation error before safety", func(t *testing.T) {
		_, err := DropRole("bad-role", "bad-role")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestDropDatabase(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		got, err := DropDatabase("appdb", "appdb")
		require.NoError(t, err)
		require.Equal(t, `DROP DATABASE "appdb";`, got)
	})
	t.Run("unconfirmed is a safety error", func(t *testing.T) {
		_, err := DropDatabase("appdb", "nope")
		require.Error(t, err)
		require.Equal(t, core.CodeSafety, core.CodeOf(err))
		var se *core.SafetyError
		require.ErrorAs(t, err, &se)
		require.Equal(t, "drop database", se.Operation)
	})
	t.Run("invalid db name", func(t *testing.T) {
		_, err := DropDatabase("bad-db", "bad-db")
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

func TestBuildNewApp(t *testing.T) {
	plan := NewAppPlan{
		Database:      "shop",
		OwnerRole:     "shop_owner",
		ReadwriteUser: "shop_rw",
		ReadwritePass: "rwpass",
		ReadonlyUser:  "shop_ro",
		ReadonlyPass:  "ropass",
	}
	got, err := BuildNewApp(plan)
	require.NoError(t, err)
	require.NotEmpty(t, got)

	require.Equal(t, `CREATE ROLE "shop_owner" NOLOGIN;`, got[0])
	require.Equal(t, `CREATE DATABASE "shop" OWNER "shop_owner";`, got[1])
	require.Equal(t, `CREATE ROLE "shop_rw" LOGIN PASSWORD 'rwpass';`, got[2])

	joined := strings.Join(got, "\n")
	// Read-write user gets write privileges.
	require.Contains(t, joined, `GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "public" TO "shop_rw";`)
	// Read-only user is created and granted SELECT + default privileges.
	require.Contains(t, joined, `CREATE ROLE "shop_ro" LOGIN PASSWORD 'ropass';`)
	require.Contains(t, joined, `ALTER DEFAULT PRIVILEGES IN SCHEMA "public" GRANT SELECT ON TABLES TO "shop_ro";`)

	// CREATE DATABASE appears exactly once.
	require.Equal(t, 1, strings.Count(joined, "CREATE DATABASE"))
	// Three roles created.
	require.Equal(t, 3, strings.Count(joined, "CREATE ROLE"))
}

func TestBuildNewAppValidation(t *testing.T) {
	base := NewAppPlan{
		Database:      "shop",
		OwnerRole:     "shop_owner",
		ReadwriteUser: "shop_rw",
		ReadwritePass: "rwpass",
		ReadonlyUser:  "shop_ro",
		ReadonlyPass:  "ropass",
	}
	t.Run("bad database", func(t *testing.T) {
		p := base
		p.Database = "bad-db"
		_, err := BuildNewApp(p)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("empty rw password", func(t *testing.T) {
		p := base
		p.ReadwritePass = ""
		_, err := BuildNewApp(p)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("empty ro password", func(t *testing.T) {
		p := base
		p.ReadonlyPass = ""
		_, err := BuildNewApp(p)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
	t.Run("duplicate role names rejected", func(t *testing.T) {
		p := base
		p.ReadonlyUser = p.ReadwriteUser
		_, err := BuildNewApp(p)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})
}

// TestNoRawInterpolation is a guard: every statement produced by the builders
// must double-quote its identifiers, never embed them raw. We assert that a
// payload-laden (but identifier-valid) input never escapes quoting by checking
// the literal-quoted password forms.
func TestPasswordNeverBreaksOutOfLiteral(t *testing.T) {
	evil := `'; DROP DATABASE postgres; --`
	got, err := CreateRole("safe_user", evil, true)
	require.NoError(t, err)
	// The leading single quote of the payload is doubled, so the password stays
	// inside one string literal and cannot terminate the statement early.
	require.Equal(t, `CREATE ROLE "safe_user" LOGIN PASSWORD '''; DROP DATABASE postgres; --';`, got)
}
