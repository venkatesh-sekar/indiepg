//go:build e2e

package harness

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// SourcePG is a SECOND, standalone Postgres container the migration scenarios use
// as the "source" instance — the database the panel migrates FROM. It is attached
// to the SAME compose network as the panel + MinIO (so the panel can reach it over
// TCP by hostname, and the source itself can reach MinIO for the drop-off push),
// and it is built from the same preinstalled e2e image (which already carries the
// PostgreSQL 17 binaries, pg_dump, psql, curl, sha256sum, AND the MinIO CA in its
// trust store — everything the three migration modes need on a source box).
//
// It is NOT part of the frozen harness core: it is a new, additive helper a
// scenario author owns. The panel reaches it at the network alias sourcePGAlias.
type SourcePG struct {
	t         *testing.T
	env       *Env
	container string // docker container name (globally unique, project-scoped)
	network   string // the compose network it was attached to
}

const (
	// sourcePGAlias is the network alias the panel uses as the migration source
	// host. It is unique per compose network (each scenario has its own), so it
	// never collides across parallel tests.
	sourcePGAlias = "migsource"
	// sourcePGPort is the TCP port the source Postgres listens on inside its
	// container (the standard 5432; published nowhere — only reachable on the
	// private compose network).
	sourcePGPort = "5432"
	// sourceCACert is the MinIO CA path baked into the e2e image's trust store; the
	// drop-off push points curl at it explicitly so the presigned-PUT upload to
	// https://minio verifies regardless of the system bundle state.
	sourceCACert = "/usr/local/share/ca-certificates/indiepg-e2e-minio-ca.crt"
)

// SourceConn is the user-supplied source connection the panel's migrate endpoints
// accept as their `source` object. The json tags match the panel's
// sourceConnRequest wire shape so a scenario can embed it directly in a request
// body. Password is empty: the source uses `trust` auth over TCP, which is
// deterministic for a throwaway test cluster (the migration feature — not libpq
// auth — is what these scenarios exercise).
type SourceConn struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslmode"`
}

// sourceBootScript brings up a fresh, TCP-reachable Postgres inside the container.
// It initdb's a throwaway cluster with `--no-sync` (the full post-initdb fsync
// hangs for minutes on this host's overlay2 filesystem, and durability is
// irrelevant for a disposable test source), opens it to TCP with trust auth, and
// then blocks on `tail -f` so the container stays up. fsync is disabled for speed.
const sourceBootScript = `set -e
PGBIN=/usr/lib/postgresql/17/bin
DATA=/var/lib/postgresql/e2esrc
install -d -o postgres -g postgres /var/run/postgresql
rm -rf "$DATA"; mkdir -p "$DATA"; chown postgres:postgres "$DATA"; chmod 700 "$DATA"
su -s /bin/sh postgres -c "$PGBIN/initdb --no-sync -A trust -U postgres -E UTF8 -D $DATA >/tmp/initdb.log 2>&1"
printf '%s\n' "listen_addresses = '*'" "port = 5432" "fsync = off" "unix_socket_directories = '/var/run/postgresql'" >> "$DATA/postgresql.conf"
echo "host all all all trust" >> "$DATA/pg_hba.conf"
su -s /bin/sh postgres -c "$PGBIN/pg_ctl -D $DATA -l $DATA/server.log -w -t 60 start"
echo SOURCE_PG_READY
exec tail -f /dev/null`

// composeNetwork resolves the docker network name of the env's compose project so
// the source container can be attached to it. It prefers the compose project label
// (robust against compose naming changes) and falls back to the conventional
// "<project>_default".
func (e *Env) composeNetwork() (string, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	out, _, err := runCmd(ctx, "docker", "network", "ls",
		"--filter", "label=com.docker.compose.project="+e.Project,
		"--format", "{{.Name}}")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if n := strings.TrimSpace(line); n != "" {
				return n, nil
			}
		}
	}
	// Fall back to the compose-v2 convention.
	fallback := e.Project + "_default"
	if _, _, ierr := runCmd(ctx, "docker", "network", "inspect", fallback); ierr == nil {
		return fallback, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve compose network for %s: %w", e.Project, err)
	}
	return "", fmt.Errorf("no compose network found for project %s", e.Project)
}

// StartSourcePG launches the source Postgres container on the env's compose
// network and waits until it accepts connections. It registers teardown on
// t.Cleanup; because it is called AFTER harness.Up, its cleanup runs BEFORE the
// env's compose-down (t.Cleanup is LIFO), so the source is removed before its
// network is torn down. The returned source has no application databases yet —
// each scenario seeds its own deterministic schema via CreateDatabase/Exec.
func StartSourcePG(t *testing.T, env *Env) *SourcePG {
	t.Helper()

	network, err := env.composeNetwork()
	if err != nil {
		t.Fatalf("resolve source network: %v", err)
	}
	s := &SourcePG{
		t:         t,
		env:       env,
		container: env.Project + "-srcpg",
		network:   network,
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Reuse the preinstalled image: it already carries pg17/pg_dump/psql/curl/
	// sha256sum and trusts the MinIO CA. Override the entrypoint so it runs our
	// standalone Postgres instead of booting systemd + the panel.
	out, stderr, err := runCmd(ctx, "docker", "run", "-d",
		"--name", s.container,
		"--network", s.network,
		"--network-alias", sourcePGAlias,
		"--entrypoint", "/bin/sh",
		PreinstalledImage, "-c", sourceBootScript)
	if err != nil {
		t.Fatalf("docker run source PG failed: %v\nstderr: %s", err, stderr)
	}
	_ = out

	// Wait until the source accepts TCP connections (pg_isready exits 0).
	Await(t, 90*time.Second, time.Second, "source Postgres ready", func() (bool, error) {
		_, err := s.Exec("pg_isready", "-h", "127.0.0.1", "-p", sourcePGPort)
		return err == nil, err
	})
	return s
}

// Exec runs a command inside the source container as root and returns combined
// stdout (stderr folded into the error on failure).
func (s *SourcePG) Exec(argv ...string) (string, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	out, _, err := dockerExec(ctx, s.container, "", argv...)
	return out, err
}

// psqlArgs is the trust-over-TCP psql prefix used for all in-source SQL.
func psqlArgs(db string) []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-tAqX",
		"-U", "postgres", "-h", "127.0.0.1", "-p", sourcePGPort, "-d", db}
}

// Psql runs a single statement against the named source database and returns the
// trimmed scalar output.
func (s *SourcePG) Psql(db, sql string) (string, error) {
	out, err := s.Exec(append(psqlArgs(db), "-c", sql)...)
	return strings.TrimSpace(out), err
}

// CreateDatabase creates a database on the source.
func (s *SourcePG) CreateDatabase(name string) error {
	_, err := s.Psql("postgres", "CREATE DATABASE "+name)
	return err
}

// MustExec runs a statement against a source database, failing the test on error.
func (s *SourcePG) MustExec(db, sql string) {
	s.t.Helper()
	if _, err := s.Psql(db, sql); err != nil {
		s.t.Fatalf("source psql (%s) failed: %v\nsql: %s", db, err, sql)
	}
}

// CountRows returns the row count of a relation in a source database — the source
// ground truth the scenario compares the migrated target against.
func (s *SourcePG) CountRows(db, table string) (int, error) {
	out, err := s.Psql(db, "SELECT count(*) FROM "+table)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Conn returns the SourceConn the panel uses to reach this source for the given
// database (host = the network alias, trust auth so no password, sslmode disabled
// so libpq never negotiates TLS against the plain test cluster).
func (s *SourcePG) Conn(database string) SourceConn {
	return SourceConn{
		Host:     sourcePGAlias,
		Port:     sourcePGPort,
		User:     "postgres",
		Password: "",
		Database: database,
		SSLMode:  "disable",
	}
}

// dropPushScript replicates scripts/migrate-push.sh's source-side steps directly
// (the panel's copy-paste command pipes a script from raw.githubusercontent.com,
// which the isolated test network cannot reach). It dumps $DB with `pg_dump -Fc`,
// checksums it with sha256sum, builds meta.json with the SAME server-side
// pg_temp.indiepg_meta() function the real script uses (so the per-table (schema,
// name, rows) entries are byte-identical to what the panel's RowCountsByTable
// enumerates), splices in sha256/byte_size/created_at, then PUTs the dump FIRST
// and meta.json SECOND to the two presigned URLs (meta-last == "upload complete").
// DB/DUMP_URL/META_URL arrive as env (kept out of argv, mirroring the real script).
const dropPushScript = `set -eu
DUMP=/tmp/drop.dump
META=/tmp/drop.meta.json
pg_dump -Fc -U postgres -h 127.0.0.1 -p 5432 -d "$DB" > "$DUMP"
SIZE=$(wc -c < "$DUMP" | tr -d ' ')
[ "$SIZE" -gt 0 ] || { echo "EMPTY_DUMP" >&2; exit 1; }
SHA=$(sha256sum "$DUMP" | awk '{print $1}')
META_SQL="CREATE FUNCTION pg_temp.indiepg_meta() RETURNS text AS \$indiepg\$
DECLARE
  r record; n bigint; tbls json[] := '{}'; total bigint := 0;
BEGIN
  FOR r IN
    SELECT table_schema AS schema, table_name AS name
    FROM information_schema.tables
    WHERE table_type = 'BASE TABLE'
      AND table_schema NOT IN ('pg_catalog','information_schema')
    ORDER BY table_schema, table_name
  LOOP
    EXECUTE format('SELECT count(*) FROM %I.%I', r.schema, r.name) INTO n;
    tbls := tbls || json_build_object('schema', r.schema, 'name', r.name, 'rows', n);
    total := total + n;
  END LOOP;
  RETURN json_build_object('schema_version', 1, 'source_db', current_database(), 'pg_version', current_setting('server_version'), 'tables', coalesce(array_to_json(tbls), '[]'::json), 'total_rows', total)::text;
END;
\$indiepg\$ LANGUAGE plpgsql;
SELECT pg_temp.indiepg_meta();"
SRC=$(psql -tAqX -v ON_ERROR_STOP=1 -U postgres -h 127.0.0.1 -p 5432 -d "$DB" -c "$META_SQL")
case "$SRC" in '{'*'}') : ;; *) echo "BAD_META:$SRC" >&2; exit 1 ;; esac
INNER=${SRC#\{}
INNER=${INNER%\}}
CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
printf '{"sha256":"%s","byte_size":%s,"created_at":"%s",%s}' "$SHA" "$SIZE" "$CREATED" "$INNER" > "$META"
curl -fsS -H 'Expect:' --cacert ` + sourceCACert + ` --upload-file "$DUMP" "$DUMP_URL"
curl -fsS -H 'Expect:' --cacert ` + sourceCACert + ` --upload-file "$META" "$META_URL"
printf 'PUSH_OK sha=%s size=%s\n' "$SHA" "$SIZE"`

// PushDropoff performs the drop-off source push for database db against the two
// presigned PUT URLs the panel minted, exactly as a real source box would. It
// returns the dump's hex SHA-256 and byte size (parsed from the script's PUSH_OK
// line) so the scenario can cross-check the panel's own verification.
func (s *SourcePG) PushDropoff(db, dumpURL, metaURL string) (sha string, size int64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, stderr, err := dockerExec(ctx, s.container, "",
		"env", "DB="+db, "DUMP_URL="+dumpURL, "META_URL="+metaURL,
		"sh", "-c", dropPushScript)
	if err != nil {
		return "", 0, fmt.Errorf("drop-off push failed: %w\nstdout: %s\nstderr: %s", err, out, stderr)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "PUSH_OK ") {
			continue
		}
		for _, f := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(f, "sha="):
				sha = strings.TrimPrefix(f, "sha=")
			case strings.HasPrefix(f, "size="):
				size, _ = strconv.ParseInt(strings.TrimPrefix(f, "size="), 10, 64)
			}
		}
	}
	if sha == "" || size == 0 {
		return "", 0, fmt.Errorf("drop-off push did not report sha/size; output: %s", out)
	}
	return sha, size, nil
}

// Close removes the source container (idempotent), dumping its logs on a failed
// test for diagnosis.
func (s *SourcePG) Close() {
	if s.container == "" {
		return
	}
	if s.t.Failed() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if out, _, err := runCmd(ctx, "docker", "logs", "--tail", "40", s.container); err == nil {
			s.t.Logf("=== source PG logs [%s] ===\n%s", s.container, out)
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _ = runCmd(ctx, "docker", "rm", "-f", s.container)
}
