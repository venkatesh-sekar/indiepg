package guard

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

func TestClassString(t *testing.T) {
	require.Equal(t, "read", ClassRead.String())
	require.Equal(t, "write", ClassWrite.String())
	require.Equal(t, "ddl", ClassDDL.String())
	require.Equal(t, "utility", ClassUtility.String())
	require.Equal(t, "unknown", ClassUnknown.String())
	require.Equal(t, "unknown", Class(99).String())
}

func TestClassIsReadOnly(t *testing.T) {
	require.True(t, ClassRead.IsReadOnly())
	require.False(t, ClassWrite.IsReadOnly())
	require.False(t, ClassDDL.IsReadOnly())
	require.False(t, ClassUtility.IsReadOnly())
	require.False(t, ClassUnknown.IsReadOnly())
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		wantClass Class
		wantLimit bool
	}{
		// reads
		{"plain select", "SELECT * FROM users", ClassRead, false},
		{"select lower", "select id from t", ClassRead, false},
		{"select with limit", "SELECT * FROM users LIMIT 10", ClassRead, true},
		{"select trailing semicolon", "SELECT 1;", ClassRead, false},
		{"select leading whitespace", "   \n  SELECT 1", ClassRead, false},
		{"select with comment", "-- a comment\nSELECT 1", ClassRead, false},
		{"select block comment", "/* hi */ SELECT 1", ClassRead, false},
		{"table cmd", "TABLE users", ClassRead, false},
		{"values", "VALUES (1),(2)", ClassRead, false},
		{"with select", "WITH t AS (SELECT 1) SELECT * FROM t", ClassRead, false},
		{"with select limit", "WITH t AS (SELECT 1) SELECT * FROM t LIMIT 5", ClassRead, true},
		{"explain select", "EXPLAIN SELECT * FROM t", ClassRead, false},
		{"explain verbose select", "EXPLAIN VERBOSE SELECT * FROM t", ClassRead, false},
		{"explain options no analyze", "EXPLAIN (FORMAT JSON) SELECT 1", ClassRead, false},

		// explain analyze runs the inner stmt -> not read for a writing inner
		{"explain analyze select", "EXPLAIN ANALYZE SELECT 1", ClassRead, false},
		{"explain analyze delete", "EXPLAIN ANALYZE DELETE FROM t", ClassWrite, false},
		{"explain options analyze delete", "EXPLAIN (ANALYZE, BUFFERS) DELETE FROM t WHERE id=1", ClassWrite, false},

		// writes
		{"insert", "INSERT INTO t VALUES (1)", ClassWrite, false},
		{"update", "UPDATE t SET x=1 WHERE id=2", ClassWrite, false},
		{"delete", "DELETE FROM t WHERE id=2", ClassWrite, false},
		{"merge", "MERGE INTO t USING s ON t.id=s.id", ClassWrite, false},
		{"copy from", "COPY t FROM '/tmp/x.csv'", ClassWrite, false},
		{"copy to stdout is read", "COPY t TO STDOUT", ClassRead, false},
		{"copy query to stdout is read", "COPY (SELECT * FROM t) TO STDOUT", ClassRead, false},
		{"copy to file is not read", "COPY t TO '/tmp/x.csv'", ClassWrite, false},
		{"copy to program is not read", "COPY t TO PROGRAM 'cat > /tmp/x'", ClassWrite, false},
		{"copy from program is write", "COPY t FROM PROGRAM 'curl http://x'", ClassWrite, false},
		{"with delete is write", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", ClassWrite, false},
		{"with insert is write", "WITH x AS (INSERT INTO t VALUES (1) RETURNING id) SELECT * FROM x", ClassWrite, false},

		// ddl
		{"create table", "CREATE TABLE t (id int)", ClassDDL, false},
		{"alter table", "ALTER TABLE t ADD COLUMN c int", ClassDDL, false},
		{"drop table", "DROP TABLE t", ClassDDL, false},
		{"truncate", "TRUNCATE t", ClassDDL, false},
		{"grant", "GRANT SELECT ON t TO r", ClassDDL, false},
		{"revoke", "REVOKE SELECT ON t FROM r", ClassDDL, false},
		{"vacuum", "VACUUM ANALYZE t", ClassDDL, false},
		{"reindex", "REINDEX TABLE t", ClassDDL, false},

		// utility
		{"set", "SET search_path TO public", ClassUtility, false},
		{"reset", "RESET ALL", ClassUtility, false},
		{"begin", "BEGIN", ClassUtility, false},
		{"commit", "COMMIT", ClassUtility, false},
		{"rollback", "ROLLBACK", ClassUtility, false},
		{"show", "SHOW work_mem", ClassUtility, false},
		{"discard", "DISCARD ALL", ClassUtility, false},

		// unknown / edge
		{"empty", "", ClassUnknown, false},
		{"only comment", "-- nothing here", ClassUnknown, false},
		{"only whitespace", "   \t\n ", ClassUnknown, false},
		{"gibberish", "FROBNICATE all the things", ClassUnknown, false},

		// LIMIT detection nuances
		{"limit only in subquery is not top level", "SELECT * FROM (SELECT 1 LIMIT 1) q", ClassRead, false},
		{"limit in string not counted", "SELECT 'LIMIT 5' AS s", ClassRead, false},
		{"limit in dollar string not counted", "SELECT $x$ LIMIT 1 $x$ AS s", ClassRead, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.sql)
			require.Equal(t, tc.wantClass, c.Class, "class for %q", tc.sql)
			require.Equal(t, tc.wantLimit, c.HasLimit, "hasLimit for %q", tc.sql)
		})
	}
}

func TestClassifyDestructive(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantDestr  bool
		wantTarget string
	}{
		{"drop table", "DROP TABLE users", true, "users"},
		{"drop table if exists", "DROP TABLE IF EXISTS users", true, "users"},
		{"drop table qualified", "DROP TABLE public.users", true, "public.users"},
		{"drop index", "DROP INDEX idx_users_email", true, "idx_users_email"},
		{"drop materialized view", "DROP MATERIALIZED VIEW mv_stats", true, "mv_stats"},
		{"drop database", "DROP DATABASE app", true, "app"},
		{"drop role", "DROP ROLE app_user", true, "app_user"},
		{"drop quoted", `DROP TABLE "My Table"`, true, "My Table"},
		{"truncate", "TRUNCATE users", true, "users"},
		{"truncate table kw", "TRUNCATE TABLE users", true, "users"},
		{"truncate only", "TRUNCATE TABLE ONLY users", true, "users"},
		{"delete no where", "DELETE FROM logs", true, "logs"},
		{"delete only no where", "DELETE FROM ONLY logs", true, "logs"},
		{"delete with where not destructive", "DELETE FROM logs WHERE id=1", false, ""},
		{"delete where in subquery still has top where", "DELETE FROM logs WHERE id IN (SELECT id FROM t)", false, ""},
		{"update no where", "UPDATE accounts SET active=false", true, "accounts"},
		{"update with where not destructive", "UPDATE accounts SET active=false WHERE id=1", false, ""},
		{"alter drop column", "ALTER TABLE users DROP COLUMN email", true, "users"},
		{"alter drop constraint", "ALTER TABLE users DROP CONSTRAINT uq_email", true, "users"},
		{"alter add column not destructive", "ALTER TABLE users ADD COLUMN nick text", false, ""},
		{"alter rename not destructive", "ALTER TABLE users RENAME TO members", false, ""},
		{"create not destructive", "CREATE TABLE t (id int)", false, ""},
		{"grant not destructive", "GRANT SELECT ON t TO r", false, ""},
		{"insert not destructive", "INSERT INTO t VALUES (1)", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.sql)
			require.Equal(t, tc.wantDestr, c.IsDestructive, "destructive for %q", tc.sql)
			require.Equal(t, tc.wantTarget, c.Target, "target for %q", tc.sql)
		})
	}
}

func TestClassifyDeleteWhereInSubqueryOnly(t *testing.T) {
	// A WHERE that appears only inside a parenthesized subquery does NOT bound
	// the outer DELETE; such a statement is whole-table destructive.
	c := Classify("DELETE FROM t USING (SELECT id FROM other WHERE x=1) s")
	require.True(t, c.IsDestructive)
	require.Equal(t, "t", c.Target)
}

func TestCheckReadOnlyAllowsReads(t *testing.T) {
	g := New(Options{ReadOnly: true})
	out, cls, err := g.Check("SELECT 1")
	require.NoError(t, err)
	require.Equal(t, ClassRead, cls.Class)
	require.Equal(t, "SELECT 1", out)
}

func TestCheckReadOnlyRejectsWrites(t *testing.T) {
	g := New(Options{ReadOnly: true})
	for _, sql := range []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET x=1 WHERE id=2",
		"DELETE FROM t WHERE id=2",
		"DROP TABLE t",
		"TRUNCATE t",
		"SET search_path TO public", // utility is not read-only
		"GRANT SELECT ON t TO r",
	} {
		_, _, err := g.Check(sql)
		require.Error(t, err, "expected rejection for %q", sql)
		require.Equal(t, core.CodeSafety, core.CodeOf(err), "code for %q", sql)
	}
}

func TestCheckReadOnlyRejectsUnknown(t *testing.T) {
	g := New(Options{ReadOnly: true})
	_, cls, err := g.Check("FROBNICATE everything")
	require.Error(t, err)
	require.Equal(t, ClassUnknown, cls.Class)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestCheckRejectsMultiStatement(t *testing.T) {
	// Multi-statement input must be rejected in the query box regardless of
	// read-only mode, with code CodeSafety, and must never be limit-rewritten.
	for _, ro := range []bool{true, false} {
		g := New(Options{ReadOnly: ro, AutoLimit: 1000})
		for _, sql := range []string{
			"SELECT 1; DROP TABLE x",
			"SELECT 1; UPDATE accounts SET active=false",
			"SELECT * FROM t; INSERT INTO audit VALUES (1)",
			"SELECT 1; SELECT 2", // even two reads are a batch
			"SELECT 1; -- trailing comment after a semicolon\nSELECT 2",
		} {
			out, _, err := g.Check(sql)
			require.Error(t, err, "expected rejection for %q (readOnly=%v)", sql, ro)
			require.Equal(t, core.CodeSafety, core.CodeOf(err), "code for %q", sql)
			// LIMIT must never be appended to a multi-statement string.
			require.NotContains(t, out, "LIMIT 1000", "no auto-limit for %q", sql)
		}
	}
}

func TestCheckAllowsSingleStatementWithEmbeddedSemicolons(t *testing.T) {
	g := New(Options{ReadOnly: true, AutoLimit: 1000})
	tests := []struct {
		name string
		sql  string
	}{
		// A trailing comment after the (only) statement's semicolon is not a
		// second statement.
		{"trailing comment after semicolon", "SELECT 1; -- just a comment\n"},
		// A semicolon inside a dollar-quoted body stays within one statement.
		{"dollar-quoted semicolon", "SELECT $x$ a; b; c $x$ AS s"},
		// A semicolon inside a single-quoted string stays within one statement.
		{"string semicolon", "SELECT 'a; b' AS s"},
		// A semicolon inside parentheses is not a top-level separator.
		{"paren semicolon", "SELECT * FROM (SELECT 1) q"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cls, err := g.Check(tc.sql)
			require.NoError(t, err, "expected single-statement allow for %q", tc.sql)
			require.Equal(t, ClassRead, cls.Class)
		})
	}
}

func TestCountStatements(t *testing.T) {
	cases := []struct {
		sql  string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{";", 0},
		{";;;", 0},
		{"SELECT 1", 1},
		{"SELECT 1;", 1},
		{"SELECT 1;;", 1},
		{"SELECT 1; -- comment\n", 1},
		{"SELECT $x$ a; b $x$", 1},
		{"SELECT 'a; b'", 1},
		{"SELECT * FROM (SELECT 1; ) q", 1},
		{"SELECT 1; DROP TABLE x", 2},
		{"SELECT 1; SELECT 2; SELECT 3", 3},
		{"SELECT 1 ;; SELECT 2", 2},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, countStatements(tokenize(trimStatement(tc.sql))), "count for %q", tc.sql)
	}
}

func TestCheckRejectsMultiStatementWithCopyToProgram(t *testing.T) {
	// COPY ... TO PROGRAM is not a read; it must be rejected in read-only mode.
	g := New(Options{ReadOnly: true, AutoLimit: 1000})
	for _, sql := range []string{
		"COPY t TO PROGRAM 'cat > /tmp/x'",
		"COPY t FROM PROGRAM 'curl http://x'",
		"COPY t TO '/tmp/x.csv'",
	} {
		_, cls, err := g.Check(sql)
		require.Error(t, err, "expected rejection for %q", sql)
		require.Equal(t, core.CodeSafety, core.CodeOf(err), "code for %q", sql)
		require.Equal(t, ClassWrite, cls.Class, "class for %q", sql)
	}

	// COPY ... TO STDOUT remains a read and is allowed.
	_, cls, err := g.Check("COPY t TO STDOUT")
	require.NoError(t, err)
	require.Equal(t, ClassRead, cls.Class)
}

func TestCheckNonReadOnlyAllowsEverything(t *testing.T) {
	g := New(Options{ReadOnly: false})
	out, cls, err := g.Check("DELETE FROM t")
	require.NoError(t, err)
	require.Equal(t, ClassWrite, cls.Class)
	require.Equal(t, "DELETE FROM t", out)
}

func TestCheckAutoLimit(t *testing.T) {
	g := New(Options{ReadOnly: true, AutoLimit: 1000})
	tests := []struct {
		name      string
		sql       string
		wantSQL   string
		wantLimit bool
	}{
		{"unbounded select gets limit", "SELECT * FROM users", "SELECT * FROM users LIMIT 1000", true},
		{"select with limit untouched", "SELECT * FROM users LIMIT 10", "SELECT * FROM users LIMIT 10", true},
		{"trailing semicolon trimmed then limited", "SELECT * FROM users;", "SELECT * FROM users LIMIT 1000", true},
		{"values not limited", "VALUES (1)", "VALUES (1) LIMIT 1000", true},
		{"with select limited", "WITH t AS (SELECT 1) SELECT * FROM t", "WITH t AS (SELECT 1) SELECT * FROM t LIMIT 1000", true},
		{"subquery limit does not block top limit", "SELECT * FROM (SELECT 1 LIMIT 1) q", "SELECT * FROM (SELECT 1 LIMIT 1) q LIMIT 1000", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, cls, err := g.Check(tc.sql)
			require.NoError(t, err)
			require.Equal(t, tc.wantSQL, out)
			require.Equal(t, tc.wantLimit, cls.HasLimit)
		})
	}
}

func TestCheckAutoLimitDisabled(t *testing.T) {
	g := New(Options{ReadOnly: true, AutoLimit: 0})
	out, _, err := g.Check("SELECT * FROM users")
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM users", out)
}

func TestCheckAutoLimitOnlyReads(t *testing.T) {
	// auto-limit must never touch a non-read statement even when not read-only.
	g := New(Options{ReadOnly: false, AutoLimit: 100})
	out, _, err := g.Check("DELETE FROM t")
	require.NoError(t, err)
	require.Equal(t, "DELETE FROM t", out)
}

func TestEnsureLimit(t *testing.T) {
	g := New(Options{AutoLimit: 500})
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"adds limit", "SELECT * FROM t", "SELECT * FROM t LIMIT 500"},
		{"keeps existing", "SELECT * FROM t LIMIT 5", "SELECT * FROM t LIMIT 5"},
		{"trailing whitespace", "SELECT * FROM t   ", "SELECT * FROM t LIMIT 500"},
		{"trailing semicolon", "SELECT * FROM t;", "SELECT * FROM t LIMIT 500"},
		{"non-read unchanged", "DELETE FROM t", "DELETE FROM t"},
		{"ddl unchanged", "DROP TABLE t", "DROP TABLE t"},
		{"subquery-limit gets top limit", "SELECT * FROM (SELECT 1 LIMIT 1) q", "SELECT * FROM (SELECT 1 LIMIT 1) q LIMIT 500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, g.EnsureLimit(tc.sql))
		})
	}
}

func TestEnsureLimitDisabled(t *testing.T) {
	g := New(Options{AutoLimit: 0})
	require.Equal(t, "SELECT * FROM t", g.EnsureLimit("SELECT * FROM t"))
}

func TestNewClampsNegativeLimit(t *testing.T) {
	g := New(Options{AutoLimit: -5})
	require.Equal(t, 0, g.Options().AutoLimit)
	require.Equal(t, "SELECT 1", g.EnsureLimit("SELECT 1"))
}

func TestNewDestructiveError(t *testing.T) {
	de := NewDestructiveError("users", "drop table")
	require.NotNil(t, de)
	require.Equal(t, "users", de.Object)

	// It must carry CodeSafety through the chain.
	require.Equal(t, core.CodeSafety, core.CodeOf(de))

	// It is an error and unwraps to the embedded *core.SafetyError.
	var se *core.SafetyError
	require.True(t, errors.As(de, &se))
	require.Equal(t, "drop table", se.Operation)
	require.Contains(t, se.RequiredFlags, "confirm=users")

	// The message names the operation and the object to type.
	require.Contains(t, de.Error(), "users")
	require.Contains(t, de.Error(), "drop table")
}

func TestDestructiveErrorIsError(t *testing.T) {
	var err error = NewDestructiveError("app", "drop database")
	require.Error(t, err)
	require.Equal(t, core.CodeSafety, core.CodeOf(err))
}

func TestReadOnlyRejectionIsSafetyError(t *testing.T) {
	g := New(Options{ReadOnly: true})
	_, _, err := g.Check("DROP TABLE t")
	require.Error(t, err)
	var se *core.SafetyError
	require.True(t, errors.As(err, &se))
	require.Equal(t, "read-only query", se.Operation)
}
