package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// localConn is a peer-auth local connection over the default socket dir.
func localConn() ConnInfo {
	return ConnInfo{Host: "/var/run/postgresql", Port: "5432", Database: "appdb"}
}

// remoteConn is a TCP connection with a password (the secret that must never
// appear in argv or error text).
func remoteConn() ConnInfo {
	return ConnInfo{
		Host:     "db.example.com",
		Port:     "5433",
		User:     "migrator",
		Password: "s3cr3t-pass",
		SSLMode:  "require",
		Database: "appdb",
	}
}

func newEngine() (*engine, *exec.FakeRunner) {
	r := exec.NewFakeRunner()
	return NewEngine(r, core.Discard()).(*engine), r
}

// ---- ConnInfo unit tests -------------------------------------------------

func TestConnInfo_Local(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", true},
		{"/var/run/postgresql", true},
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},
		{"db.example.com", false},
		{"10.0.0.9", false},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, ConnInfo{Host: c.host}.Local(), "host=%q", c.host)
	}
}

func TestConnInfo_Redacted_hidesPassword(t *testing.T) {
	r := remoteConn().Redacted()
	require.Equal(t, "migrator@db.example.com:5433/appdb", r)
	require.NotContains(t, r, "s3cr3t-pass")
}

func TestConnInfo_Redacted_localDefaults(t *testing.T) {
	require.Equal(t, "postgres@local:5432/", ConnInfo{}.Redacted())
}

func TestConnInfo_connArgs_local(t *testing.T) {
	args, asUser, env, sensitive := localConn().connArgs()
	require.Equal(t, []string{"-h", "/var/run/postgresql", "-p", "5432"}, args)
	require.Equal(t, "postgres", asUser)
	require.Empty(t, env)
	require.False(t, sensitive)
}

func TestConnInfo_connArgs_localDefaultSocket(t *testing.T) {
	args, asUser, _, _ := ConnInfo{}.connArgs()
	require.Equal(t, []string{"-h", "/var/run/postgresql", "-p", "5432"}, args)
	require.Equal(t, "postgres", asUser)
}

func TestConnInfo_connArgs_remote(t *testing.T) {
	args, asUser, env, sensitive := remoteConn().connArgs()
	require.Equal(t, []string{"-h", "db.example.com", "-p", "5433", "-U", "migrator"}, args)
	require.Equal(t, "", asUser)
	require.Contains(t, env, "PGPASSWORD=s3cr3t-pass")
	require.Contains(t, env, "PGSSLMODE=require")
	require.True(t, sensitive, "remote with password must mark RunSpec Sensitive")
}

func TestConnInfo_connArgs_remoteNoPassword(t *testing.T) {
	c := remoteConn()
	c.Password = ""
	_, _, env, sensitive := c.connArgs()
	require.NotContains(t, strings.Join(env, " "), "PGPASSWORD")
	require.False(t, sensitive)
}

// ---- Version / psql plumbing --------------------------------------------

func TestVersion_localArgv(t *testing.T) {
	e, r := newEngine()
	r.On("SHOW server_version", exec.FakeResponse{Stdout: "16.2 (Debian)\n"})

	v, err := e.Version(context.Background(), localConn())
	require.NoError(t, err)
	require.Equal(t, "16.2 (Debian)", v)

	call := r.Calls()[0]
	require.Equal(t, "psql", call.Name)
	require.Equal(t, "postgres", call.AsUser, "local psql runs as the postgres OS user")
	require.Empty(t, call.Env)
	require.False(t, call.Sensitive)
	require.Equal(t, []string{
		"-h", "/var/run/postgresql", "-p", "5432",
		"-v", "ON_ERROR_STOP=1", "-tAqX", "-d", "appdb", "-c", "SHOW server_version",
	}, call.Args)
}

func TestVersion_remoteArgvSensitive(t *testing.T) {
	e, r := newEngine()
	r.On("SHOW server_version", exec.FakeResponse{Stdout: "15.6\n"})

	_, err := e.Version(context.Background(), remoteConn())
	require.NoError(t, err)

	call := r.Calls()[0]
	require.Equal(t, "", call.AsUser, "remote runs as the panel user, not postgres")
	require.True(t, call.Sensitive, "PGPASSWORD-bearing spec must be Sensitive")
	require.Contains(t, call.Env, "PGPASSWORD=s3cr3t-pass")
	require.Equal(t, []string{
		"-h", "db.example.com", "-p", "5433", "-U", "migrator",
		"-v", "ON_ERROR_STOP=1", "-tAqX", "-d", "appdb", "-c", "SHOW server_version",
	}, call.Args)
}

func TestPsql_errorScrubsPasswordInStderr(t *testing.T) {
	e, r := newEngine()
	// Simulate a tool echoing the password back in its stderr.
	r.On("SHOW server_version", exec.FakeResponse{
		Stderr:   "FATAL: auth failed for password s3cr3t-pass",
		ExitCode: 1,
		Err:      core.ExecError("boom"),
	})
	_, err := e.Version(context.Background(), remoteConn())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "s3cr3t-pass", "password must be scrubbed from error text")
	ce, ok := core.AsError(err)
	require.True(t, ok)
	require.Equal(t, core.CodeExec, ce.Code)
}

// ---- ListDatabases parsing ----------------------------------------------

func TestListDatabases_parses(t *testing.T) {
	e, r := newEngine()
	r.On("pg_database_size", exec.FakeResponse{
		Stdout: "appdb|10485760|10 MB\nshop|2048|2048 bytes\n",
	})
	dbs, err := e.ListDatabases(context.Background(), localConn())
	require.NoError(t, err)
	require.Equal(t, []DatabaseSize{
		{Name: "appdb", SizeBytes: 10485760, SizePretty: "10 MB"},
		{Name: "shop", SizeBytes: 2048, SizePretty: "2048 bytes"},
	}, dbs)
	// Verify it queries the maintenance database, not conn.Database.
	require.Contains(t, strings.Join(r.Calls()[0].Args, " "), "-d postgres")
}

func TestListDatabases_empty(t *testing.T) {
	e, r := newEngine()
	r.On("pg_database_size", exec.FakeResponse{Stdout: "\n"})
	dbs, err := e.ListDatabases(context.Background(), localConn())
	require.NoError(t, err)
	require.Empty(t, dbs)
}

func TestListDatabases_malformed(t *testing.T) {
	e, r := newEngine()
	r.On("pg_database_size", exec.FakeResponse{Stdout: "appdb|notanumber|10 MB\n"})
	_, err := e.ListDatabases(context.Background(), localConn())
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// ---- DatabaseExists / DatabaseNonEmpty ----------------------------------

func TestDatabaseExists(t *testing.T) {
	e, r := newEngine()
	r.On("FROM pg_database WHERE datname", exec.FakeResponse{Stdout: "1\n"})
	ok, err := e.DatabaseExists(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.True(t, ok)

	r.Reset()
	r.On("FROM pg_database WHERE datname", exec.FakeResponse{Stdout: "\n"})
	ok, err = e.DatabaseExists(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestDatabaseExists_invalidName(t *testing.T) {
	e, _ := newEngine()
	_, err := e.DatabaseExists(context.Background(), localConn(), "bad name!")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestDatabaseNonEmpty_true(t *testing.T) {
	e, r := newEngine()
	r.On("FROM pg_database WHERE datname", exec.FakeResponse{Stdout: "1\n"})
	r.On("information_schema.tables", exec.FakeResponse{Stdout: "3\n"})
	ne, err := e.DatabaseNonEmpty(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.True(t, ne)
}

func TestDatabaseNonEmpty_existsButEmpty(t *testing.T) {
	e, r := newEngine()
	r.On("FROM pg_database WHERE datname", exec.FakeResponse{Stdout: "1\n"})
	r.On("information_schema.tables", exec.FakeResponse{Stdout: "0\n"})
	ne, err := e.DatabaseNonEmpty(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.False(t, ne)
}

func TestDatabaseNonEmpty_absentIsEmpty(t *testing.T) {
	e, r := newEngine()
	r.On("FROM pg_database WHERE datname", exec.FakeResponse{Stdout: "\n"})
	ne, err := e.DatabaseNonEmpty(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.False(t, ne)
	require.Equal(t, 1, r.CallCount(), "must not query tables when the database is absent")
}

// ---- CreateDatabase / DropDatabase --------------------------------------

func TestCreateDatabase_withOwner(t *testing.T) {
	e, r := newEngine()
	require.NoError(t, e.CreateDatabase(context.Background(), localConn(), "appdb", "appowner"))
	sql := lastCArg(t, r)
	require.Equal(t, `CREATE DATABASE "appdb" OWNER "appowner"`, sql)
}

func TestCreateDatabase_noOwner(t *testing.T) {
	e, r := newEngine()
	require.NoError(t, e.CreateDatabase(context.Background(), localConn(), "appdb", ""))
	require.Equal(t, `CREATE DATABASE "appdb"`, lastCArg(t, r))
}

func TestCreateDatabase_invalidName(t *testing.T) {
	e, _ := newEngine()
	err := e.CreateDatabase(context.Background(), localConn(), "1bad", "")
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestDropDatabase_terminatesThenDrops(t *testing.T) {
	e, r := newEngine()
	require.NoError(t, e.DropDatabase(context.Background(), localConn(), "appdb"))
	require.Equal(t, 2, r.CallCount())
	require.Contains(t, cArg(r.Calls()[0]), "pg_terminate_backend")
	require.Equal(t, `DROP DATABASE IF EXISTS "appdb"`, cArg(r.Calls()[1]))
}

// ---- Dump ---------------------------------------------------------------

func TestDump_argvAndChecksum(t *testing.T) {
	e, r := newEngine()
	dir := t.TempDir()
	out := filepath.Join(dir, "dump.bin")
	payload := []byte("PGDMP fake custom dump bytes")
	// FakeRunner does not actually write the file, so write it as a side effect
	// the way pg_dump would: we pre-create it then assert the engine stats it.
	require.NoError(t, os.WriteFile(out, payload, 0o600))

	info, err := e.Dump(context.Background(), remoteConn(), "appdb", out, DumpOpts{CompressionLevel: 9, ExcludeTables: []string{"audit_log"}})
	require.NoError(t, err)
	require.Equal(t, out, info.Path)
	require.Equal(t, "appdb", info.Database)
	require.Equal(t, "custom", info.Format)
	require.Equal(t, int64(len(payload)), info.SizeBytes)

	want := sha256.Sum256(payload)
	require.Equal(t, hex.EncodeToString(want[:]), info.Checksum)

	call := r.Calls()[0]
	require.Equal(t, "pg_dump", call.Name)
	require.True(t, call.Sensitive)
	require.Equal(t, []string{
		"-h", "db.example.com", "-p", "5433", "-U", "migrator",
		"-Fc", "-Z", "9",
		"--exclude-table", "audit_log",
		"-d", "appdb", "-f", out,
	}, call.Args)
}

func TestDump_defaultCompression(t *testing.T) {
	e, r := newEngine()
	dir := t.TempDir()
	out := filepath.Join(dir, "dump.bin")
	require.NoError(t, os.WriteFile(out, []byte("x"), 0o600))

	_, err := e.Dump(context.Background(), localConn(), "appdb", out, DumpOpts{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(r.Calls()[0].Args, " "), "-Z 6")
}

func TestDump_missingOutputFile(t *testing.T) {
	e, _ := newEngine()
	out := filepath.Join(t.TempDir(), "never-created.bin")
	_, err := e.Dump(context.Background(), localConn(), "appdb", out, DumpOpts{})
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestDump_runnerFailureScrubs(t *testing.T) {
	e, r := newEngine()
	r.On("pg_dump", exec.FakeResponse{Stderr: "auth failed s3cr3t-pass", ExitCode: 1, Err: core.ExecError("boom")})
	_, err := e.Dump(context.Background(), remoteConn(), "appdb", filepath.Join(t.TempDir(), "d.bin"), DumpOpts{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "s3cr3t-pass")
	require.Contains(t, err.Error(), "migrator@db.example.com:5433/appdb")
}

// ---- DumpGlobals --------------------------------------------------------

func TestDumpGlobals_argv(t *testing.T) {
	e, r := newEngine()
	out := filepath.Join(t.TempDir(), "globals.sql")
	require.NoError(t, e.DumpGlobals(context.Background(), localConn(), out))
	call := r.Calls()[0]
	require.Equal(t, "pg_dumpall", call.Name)
	require.Equal(t, "postgres", call.AsUser)
	require.Equal(t, []string{"-h", "/var/run/postgresql", "-p", "5432", "-g", "-f", out}, call.Args)
}

// ---- RestoreGlobals -----------------------------------------------------

func TestRestoreGlobals_localArgvAndStdin(t *testing.T) {
	e, r := newEngine()
	path := filepath.Join(t.TempDir(), "globals.sql")
	require.NoError(t, os.WriteFile(path, []byte("CREATE ROLE app;"), 0o600))

	require.NoError(t, e.RestoreGlobals(context.Background(), localConn(), path))
	call := r.Calls()[0]
	require.Equal(t, "psql", call.Name)
	require.Equal(t, "postgres", call.AsUser, "local globals replay runs as the postgres OS user (peer auth)")
	require.Equal(t, []string{"-h", "/var/run/postgresql", "-p", "5432", "-tAqX", "-d", "postgres"}, call.Args)
	require.Equal(t, "CREATE ROLE app;", call.Stdin, "the globals SQL is piped via stdin")
	require.False(t, call.Sensitive, "a local (peer-auth) command carries no password")
}

func TestRestoreGlobals_remoteIsSensitive(t *testing.T) {
	e, r := newEngine()
	path := filepath.Join(t.TempDir(), "globals.sql")
	require.NoError(t, os.WriteFile(path, []byte("CREATE ROLE app;"), 0o600))

	require.NoError(t, e.RestoreGlobals(context.Background(), remoteConn(), path))
	call := r.Calls()[0]
	require.True(t, call.Sensitive, "a remote command carrying PGPASSWORD must be Sensitive")
	require.Contains(t, call.Env, "PGPASSWORD=s3cr3t-pass")
}

func TestRestoreGlobals_warningsAreNotFatal(t *testing.T) {
	e, r := newEngine()
	path := filepath.Join(t.TempDir(), "globals.sql")
	require.NoError(t, os.WriteFile(path, []byte("CREATE ROLE app;"), 0o600))
	// Replaying globals onto a target that already has the roles produces benign
	// "already exists" notices on a non-zero exit -> still success.
	r.On("psql", exec.FakeResponse{
		Stderr:   `psql:globals.sql:1: NOTICE: role "app" already exists, skipping`,
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	require.NoError(t, e.RestoreGlobals(context.Background(), localConn(), path))
}

func TestRestoreGlobals_realErrorIsFatal(t *testing.T) {
	e, r := newEngine()
	path := filepath.Join(t.TempDir(), "globals.sql")
	require.NoError(t, os.WriteFile(path, []byte("CREATE ROLE app;"), 0o600))
	r.On("psql", exec.FakeResponse{
		Stderr:   "psql:globals.sql:1: error: syntax error at or near \"CRATE\"",
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	err := e.RestoreGlobals(context.Background(), localConn(), path)
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestRestoreGlobals_missingFile(t *testing.T) {
	e, _ := newEngine()
	err := e.RestoreGlobals(context.Background(), localConn(), filepath.Join(t.TempDir(), "nope.sql"))
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// ---- Restore: warning vs fatal classification ---------------------------

func TestRestore_argvWithClean(t *testing.T) {
	e, r := newEngine()
	dump := filepath.Join(t.TempDir(), "d.bin")
	require.NoError(t, e.Restore(context.Background(), localConn(), dump, "appdb",
		RestoreOpts{Jobs: 8, Clean: true, NoOwner: true, Create: true}))
	call := r.Calls()[0]
	require.Equal(t, "pg_restore", call.Name)
	require.Equal(t, []string{
		"-h", "/var/run/postgresql", "-p", "5432",
		"-j", "8", "-d", "appdb",
		"--clean", "--if-exists", "--no-owner", "--create",
		dump,
	}, call.Args)
}

func TestRestore_defaultJobs(t *testing.T) {
	e, r := newEngine()
	dump := filepath.Join(t.TempDir(), "d.bin")
	require.NoError(t, e.Restore(context.Background(), localConn(), dump, "appdb", RestoreOpts{}))
	require.Contains(t, strings.Join(r.Calls()[0].Args, " "), "-j 4")
}

func TestRestore_warningsAreNotFatal(t *testing.T) {
	e, r := newEngine()
	// pg_restore exits non-zero but stderr has only warnings -> success.
	r.On("pg_restore", exec.FakeResponse{
		Stderr:   "pg_restore: warning: errors ignored on restore: 0\nWARNING: no privileges could be revoked",
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	err := e.Restore(context.Background(), localConn(), "/tmp/d.bin", "appdb", RestoreOpts{})
	require.NoError(t, err, "non-zero exit with only warnings must be treated as success")
}

func TestRestore_realErrorIsFatal(t *testing.T) {
	e, r := newEngine()
	r.On("pg_restore", exec.FakeResponse{
		Stderr:   "pg_restore: error: could not execute query: relation already exists",
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	err := e.Restore(context.Background(), localConn(), "/tmp/d.bin", "appdb", RestoreOpts{})
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestRestore_fatalLineIsFatal(t *testing.T) {
	e, r := newEngine()
	r.On("pg_restore", exec.FakeResponse{
		Stderr:   "pg_restore: [archiver] fatal: corrupt archive",
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	err := e.Restore(context.Background(), localConn(), "/tmp/d.bin", "appdb", RestoreOpts{})
	require.Error(t, err)
}

func TestRestore_passwordScrubbedFromFatal(t *testing.T) {
	e, r := newEngine()
	r.On("pg_restore", exec.FakeResponse{
		Stderr:   "pg_restore: error: connection failed: password=s3cr3t-pass rejected",
		ExitCode: 1,
		Err:      core.ExecError("exit status 1"),
	})
	err := e.Restore(context.Background(), remoteConn(), "/tmp/d.bin", "appdb", RestoreOpts{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "s3cr3t-pass")
}

// ---- RowCounts ----------------------------------------------------------

func TestRowCounts_parses(t *testing.T) {
	e, r := newEngine()
	r.On("information_schema.tables", exec.FakeResponse{
		Stdout: "public|users\npublic|orders\nshop|items\n",
	})
	r.On(`count(*) FROM "public"."users"`, exec.FakeResponse{Stdout: "42\n"})
	r.On(`count(*) FROM "public"."orders"`, exec.FakeResponse{Stdout: "7\n"})
	r.On(`count(*) FROM "shop"."items"`, exec.FakeResponse{Stdout: "0\n"})

	counts, err := e.RowCounts(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.Equal(t, map[string]int64{
		"public.users":  42,
		"public.orders": 7,
		"shop.items":    0,
	}, counts)
}

func TestRowCounts_emptyDatabase(t *testing.T) {
	e, r := newEngine()
	r.On("information_schema.tables", exec.FakeResponse{Stdout: "\n"})
	counts, err := e.RowCounts(context.Background(), localConn(), "appdb")
	require.NoError(t, err)
	require.Empty(t, counts)
}

func TestRowCounts_malformedCount(t *testing.T) {
	e, r := newEngine()
	r.On("information_schema.tables", exec.FakeResponse{Stdout: "public|users\n"})
	r.On(`count(*) FROM "public"."users"`, exec.FakeResponse{Stdout: "notanumber\n"})
	_, err := e.RowCounts(context.Background(), localConn(), "appdb")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

// ---- helpers ------------------------------------------------------------

// cArg returns the value of the -c flag (the SQL) from a psql RunSpec.
func cArg(spec exec.RunSpec) string {
	for i, a := range spec.Args {
		if a == "-c" && i+1 < len(spec.Args) {
			return spec.Args[i+1]
		}
	}
	return ""
}

// lastCArg returns the -c SQL from the most recent call.
func lastCArg(t *testing.T, r *exec.FakeRunner) string {
	t.Helper()
	calls := r.Calls()
	require.NotEmpty(t, calls)
	return cArg(calls[len(calls)-1])
}
