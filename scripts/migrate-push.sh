#!/bin/sh
# indiepg migrate-push — "drop-off link" source uploader.
#
# Dumps ONE PostgreSQL database from a source the indiepg panel cannot reach
# (behind NAT/a firewall, no inbound, no panel installed, no AWS credentials) and
# uploads it to S3 through two short-lived, single-key presigned PUT URLs the
# panel minted. The panel then imports it with SHA-256 + row-count verification.
# No panel install and no AWS keys are needed on this box.
#
# Usage (copy the exact line from the panel; it has the two upload URLs filled in
# as environment variables, then replace the UPPERCASE placeholders):
#   curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh \
#     | INDIEPG_DUMP_URL='<dump-url>' INDIEPG_META_URL='<meta-url>' \
#       sh -s -- --db DBNAME --docker CONTAINER
#
#   native alternative (no Docker):
#   ... --db DBNAME --host SOURCE_HOST [--port 5432] [--user POSTGRES_USER]
#
# The upload URLs come from INDIEPG_DUMP_URL / INDIEPG_META_URL (env) so they stay
# out of `ps`; --dump-url / --meta-url remain as an explicit fallback.
#
# Password (native mode only): set PGPASSWORD in the environment, or you will be
# prompted on the terminal. It is NEVER passed as a command-line flag (which would
# leak it in the process listing).
#
# Temp space: the whole dump is staged on disk before upload (to checksum it). It
# goes in INDIEPG_TMPDIR if set, else TMPDIR, else /tmp. On distros where /tmp is a
# tmpfs (RAM), a multi-GB dump there can OOM the box — set INDIEPG_TMPDIR to a
# disk-backed directory with room for the dump.
#
# Quiesce the source: row counts are captured AFTER the dump snapshot and frozen in
# meta.json, so writes to the source DURING the push can make the panel's row-count
# verification fail — and because the meta is frozen, re-importing can't fix it; you
# would have to re-run this whole push. Stop writes / put the source read-only first.
#
# Flags:
#   --dump-url URL    presigned PUT URL for the dump (or set INDIEPG_DUMP_URL)
#   --meta-url URL    presigned PUT URL for meta.json (or set INDIEPG_META_URL)
#   --db NAME         source database to migrate                   (required)
#   --docker NAME     run pg_dump/psql inside this docker container
#   --host HOST       connect to a native Postgres at HOST  (XOR --docker)
#   --port PORT       source port (native; default 5432)
#   --user USER       source user (default postgres)
#   --help            show this help and exit

set -eu

REPO_PUSH="indiepg migrate-push"

say()  { printf '%s: %s\n' "$REPO_PUSH" "$1" >&2; }
die()  { printf '%s: error: %s\n' "$REPO_PUSH" "$1" >&2; exit 1; }

usage() {
	cat >&2 <<'EOF'
indiepg migrate-push — "drop-off link" source uploader.

Dumps ONE PostgreSQL database from a source the panel cannot reach and uploads it
to S3 through two short-lived presigned PUT URLs the panel minted. No panel and no
AWS credentials are needed on this box.

Usage (copy the exact line from the panel; it has the two upload URLs filled in as
environment variables, then replace the UPPERCASE placeholders):
  curl -fsSL .../scripts/migrate-push.sh \
    | INDIEPG_DUMP_URL='<dump-url>' INDIEPG_META_URL='<meta-url>' \
      sh -s -- --db DBNAME --docker CONTAINER

  native alternative (no Docker):
    ... --db DBNAME --host SOURCE_HOST [--port 5432] [--user POSTGRES_USER]

The upload URLs come from INDIEPG_DUMP_URL / INDIEPG_META_URL (env) so they stay
out of `ps`; --dump-url / --meta-url remain as an explicit fallback.

Password (native mode only): set PGPASSWORD in the environment, or you will be
prompted on the terminal. It is NEVER passed as a command-line flag.

Temp space: the whole dump is staged on disk before upload. It goes in
INDIEPG_TMPDIR if set, else TMPDIR, else /tmp. If /tmp is a tmpfs (RAM-backed), a
large dump can OOM the box — set INDIEPG_TMPDIR to a disk-backed dir with room for
the dump.

Quiesce the source first: row counts are captured after the dump snapshot and
frozen in meta.json, so writes DURING the push can make the panel's row-count
verification fail unrecoverably (re-importing won't fix a frozen mismatch; you'd
re-run the whole push). Stop writes / set the source read-only before running this.

Flags:
  --dump-url URL    presigned PUT URL for the dump (or set INDIEPG_DUMP_URL)
  --meta-url URL    presigned PUT URL for meta.json (or set INDIEPG_META_URL)
  --db NAME         source database to migrate                   (required)
  --docker NAME     run pg_dump/psql inside this docker container
  --host HOST       connect to a native Postgres at HOST  (XOR --docker)
  --port PORT       source port (native; default 5432)
  --user USER       source user (default postgres)
  --help            show this help and exit
EOF
}

# --- defaults / args ------------------------------------------------------
# The two presigned upload URLs default from the environment (INDIEPG_DUMP_URL /
# INDIEPG_META_URL) — the form the panel's copy-paste command uses — so they stay
# OUT of this process's argv and cannot be read from `ps` by other local users
# (the same protection PGPASSWORD gets). The --dump-url / --meta-url flags remain
# as an explicit override/fallback.
DUMP_URL="${INDIEPG_DUMP_URL:-}"
META_URL="${INDIEPG_META_URL:-}"
DB=""
CONTAINER=""
HOST=""
PORT="5432"
USER_NAME="postgres"

while [ $# -gt 0 ]; do
	case "$1" in
		--dump-url) [ $# -ge 2 ] || die "--dump-url needs a value"; DUMP_URL="$2"; shift 2 ;;
		--meta-url) [ $# -ge 2 ] || die "--meta-url needs a value"; META_URL="$2"; shift 2 ;;
		--db)       [ $# -ge 2 ] || die "--db needs a value"; DB="$2"; shift 2 ;;
		--docker)   [ $# -ge 2 ] || die "--docker needs a value"; CONTAINER="$2"; shift 2 ;;
		--host)     [ $# -ge 2 ] || die "--host needs a value"; HOST="$2"; shift 2 ;;
		--port)     [ $# -ge 2 ] || die "--port needs a value"; PORT="$2"; shift 2 ;;
		--user)     [ $# -ge 2 ] || die "--user needs a value"; USER_NAME="$2"; shift 2 ;;
		-h|--help)  usage; exit 0 ;;
		*)          die "unknown argument: $1 (try --help)" ;;
	esac
done

# --- validate args --------------------------------------------------------
[ -n "$DUMP_URL" ] || die "--dump-url is required (copy the full command from the panel)"
[ -n "$META_URL" ] || die "--meta-url is required (copy the full command from the panel)"
[ -n "$DB" ]       || die "--db DBNAME is required"
# Catch an unsubstituted placeholder (the panel emits literal DBNAME/CONTAINER/...
# tokens, chosen to be shell-safe so a verbatim paste reaches here instead of
# triggering shell redirection on <…>).
case "$DB" in
	DBNAME|*'<'*|*'>'*) die "--db is still the DBNAME placeholder — replace it with your real database name" ;;
esac
if [ "$CONTAINER" = "CONTAINER" ]; then
	die "--docker is still the CONTAINER placeholder — replace it with your Postgres container name"
fi
if [ "$HOST" = "SOURCE_HOST" ]; then
	die "--host is still the SOURCE_HOST placeholder — replace it with your Postgres host"
fi
if [ "$USER_NAME" = "POSTGRES_USER" ]; then
	die "--user is still the POSTGRES_USER placeholder — replace it with your Postgres user (often 'postgres')"
fi

if [ -n "$CONTAINER" ] && [ -n "$HOST" ]; then
	die "choose ONE of --docker or --host, not both"
fi
if [ -z "$CONTAINER" ] && [ -z "$HOST" ]; then
	die "specify the source: --docker CONTAINER  OR  --host SOURCE_HOST [--port] [--user]"
fi

# --- preflight ------------------------------------------------------------
command -v curl >/dev/null 2>&1 || die "curl is required to upload the dump"
if [ -n "$CONTAINER" ]; then
	command -v docker >/dev/null 2>&1 || die "docker is required for --docker mode"
else
	command -v pg_dump >/dev/null 2>&1 || die "pg_dump not found — install the postgresql-client package"
	command -v psql    >/dev/null 2>&1 || die "psql not found — install the postgresql-client package"
fi

if command -v sha256sum >/dev/null 2>&1; then
	SHA_TOOL="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SHA_TOOL="shasum"
else
	die "need sha256sum or shasum to checksum the dump"
fi

# --- password (native mode only) -----------------------------------------
# Read from PGPASSWORD or an interactive prompt ONLY. Never a flag — a --password
# argument would be visible to every user via `ps`.
#
# The prompt reads from the controlling terminal (/dev/tty), NOT stdin: in the
# documented `curl ... | sh -s -- ...` invocation, fd 0 is the pipe carrying the
# script, so a `[ -t 0 ]`/`read` would never fire (and would eat the script). Going
# through /dev/tty makes the masked prompt actually work under curl|sh.
if [ -z "$CONTAINER" ] && [ -z "${PGPASSWORD:-}" ]; then
	if [ -e /dev/tty ]; then
		printf '%s: Postgres password for %s@%s (blank if none): ' "$REPO_PUSH" "$USER_NAME" "$HOST" >/dev/tty
		# Restore echo even if interrupted. stty -echo mutes the terminal for the
		# masked read; a Ctrl-C / SIGTERM delivered while the read is waiting would
		# otherwise leave the operator's terminal permanently echo-disabled (until a
		# manual `stty echo` / `reset`). This dedicated trap re-enables echo and then
		# aborts (130 = SIGINT) before the temp-file EXIT trap below is even armed.
		trap 'stty echo </dev/tty 2>/dev/null || true; exit 130' INT TERM
		stty -echo </dev/tty 2>/dev/null || true
		# IFS= and -r are REQUIRED for correctness: a bare `read PGPASSWORD` strips
		# leading/trailing IFS whitespace and lets a backslash escape the next char,
		# silently corrupting an otherwise valid password. `IFS= read -r` reads the
		# line verbatim.
		IFS= read -r PGPASSWORD </dev/tty || true
		stty echo </dev/tty 2>/dev/null || true
		trap - INT TERM
		printf '\n' >/dev/tty
		export PGPASSWORD
	else
		say "no PGPASSWORD set and no terminal to prompt on; relying on the source's local auth (export PGPASSWORD if the connection fails)"
	fi
fi

# --- source command wrappers ----------------------------------------------
# run_pg_dump writes a custom-format (-Fc) dump of $DB to stdout. -U is passed in
# docker mode too: `docker exec` on the standard postgres image runs as root, and
# without -U libpq would default the role to the OS user 'root' (which is not a
# Postgres role) and fail with `role "root" does not exist`. USER_NAME defaults to
# 'postgres'.
run_pg_dump() {
	if [ -n "$CONTAINER" ]; then
		if [ -n "${PGPASSWORD:-}" ]; then
			docker exec -i -e PGPASSWORD "$CONTAINER" pg_dump -Fc -U "$USER_NAME" -d "$DB"
		else
			docker exec -i "$CONTAINER" pg_dump -Fc -U "$USER_NAME" -d "$DB"
		fi
	else
		pg_dump -Fc -h "$HOST" -p "$PORT" -U "$USER_NAME" -d "$DB"
	fi
}

# run_psql runs the SQL in $1 against $DB with tuples-only/unaligned output. The
# SQL is static (no user interpolation); $DB rides in -d (argv value position).
run_psql() {
	_sql="$1"
	if [ -n "$CONTAINER" ]; then
		if [ -n "${PGPASSWORD:-}" ]; then
			docker exec -i -e PGPASSWORD "$CONTAINER" psql -tAqX -v ON_ERROR_STOP=1 -U "$USER_NAME" -d "$DB" -c "$_sql"
		else
			docker exec -i "$CONTAINER" psql -tAqX -v ON_ERROR_STOP=1 -U "$USER_NAME" -d "$DB" -c "$_sql"
		fi
	else
		psql -tAqX -v ON_ERROR_STOP=1 -h "$HOST" -p "$PORT" -U "$USER_NAME" -d "$DB" -c "$_sql"
	fi
}

# --- temp files (0600, removed on exit) -----------------------------------
# The ENTIRE dump is staged on disk here before upload (to checksum it and learn
# its size). On many distros /tmp is a tmpfs (RAM-backed); a multi-GB dump staged
# there can OOM-kill a small source box — exactly the kind of constrained host this
# mode targets. So stage in INDIEPG_TMPDIR if set (point it at a disk-backed dir
# with room for the dump), else TMPDIR, else /tmp. Pick the dir explicitly rather
# than relying on mktemp's default so the override is honored on every platform.
WORK_DIR="${INDIEPG_TMPDIR:-${TMPDIR:-/tmp}}"
[ -d "$WORK_DIR" ] || die "temp directory '$WORK_DIR' does not exist — set INDIEPG_TMPDIR to a writable, disk-backed directory with room for the dump"
[ -w "$WORK_DIR" ] || die "temp directory '$WORK_DIR' is not writable — set INDIEPG_TMPDIR to a writable, disk-backed directory with room for the dump"
DUMP_FILE="$(mktemp "$WORK_DIR/indiepg-dump.XXXXXX")" || die "could not create a temp file in '$WORK_DIR'"
META_FILE="$(mktemp "$WORK_DIR/indiepg-meta.XXXXXX")" || die "could not create a temp file in '$WORK_DIR'"
# A reusable 0600 curl config file: each upload writes its presigned URL HERE (via
# `curl --config`) instead of passing it as an argv argument, so the upload URLs —
# bucket-write bearer tokens — never appear in `ps`/​/proc/<pid>/cmdline to other
# local users (the same anti-`ps` protection PGPASSWORD gets). Owner-only and removed
# on exit alongside the dump/meta temp files.
CURL_CFG="$(mktemp "$WORK_DIR/indiepg-curlcfg.XXXXXX")" || die "could not create a temp file in '$WORK_DIR'"
chmod 0600 "$DUMP_FILE" "$META_FILE" "$CURL_CFG" 2>/dev/null || true
trap 'rm -f "$DUMP_FILE" "$META_FILE" "$CURL_CFG"' EXIT INT TERM
say "staging the dump under '$WORK_DIR' (needs free disk room for the whole dump; set INDIEPG_TMPDIR if /tmp is small or RAM-backed)"

# --- 1. dump --------------------------------------------------------------
# Row counts are captured after this dump's snapshot and frozen in meta.json, so a
# source taking writes during the push can fail the panel's verification in a way a
# re-import cannot fix. Remind the operator to quiesce the source.
say "note: the source should be idle / read-only during this push (counts are frozen for verification)"
say "dumping database '$DB'..."
if ! run_pg_dump > "$DUMP_FILE"; then
	die "pg_dump failed — check the database name, the container/host, and the credentials"
fi

SIZE="$(wc -c < "$DUMP_FILE" | tr -d ' ')"
[ "$SIZE" -gt 0 ] || die "the dump is empty (0 bytes) — refusing to upload; check the database name and permissions"
# 5 GiB single-PUT ceiling, matching the panel. 5368709120 = 5 * 1024^3.
if [ "$SIZE" -gt 5368709120 ]; then
	die "the dump is ${SIZE} bytes, over the 5 GiB single-PUT limit — use the direct-pull migration instead"
fi
say "dump is $SIZE bytes"

# --- 2. checksum ----------------------------------------------------------
if [ "$SHA_TOOL" = "sha256sum" ]; then
	SHA="$(sha256sum "$DUMP_FILE" | awk '{print $1}')"
else
	SHA="$(shasum -a 256 "$DUMP_FILE" | awk '{print $1}')"
fi
[ -n "$SHA" ] || die "could not compute the dump checksum"

# --- 3. source metadata (Postgres emits the JSON for correct escaping) -----
# Exact per-table counts of every user BASE TABLE (the SAME set the panel counts
# for verification), plus pg_version and total_rows. A pg_temp PL/pgSQL function
# loops those tables and runs count(*) per table via dynamic SQL, assembling the
# JSON object server-side so identifiers and the version string are escaped
# correctly. This deliberately avoids query_to_xml, which only works on a Postgres
# built with libxml2 — absent on minimal/custom builds, where it would error ONLY
# AFTER the costly multi-GB dump had already been staged. count(*) and json_*()
# need no build-time options, so the common docker/native paths AND a no-libxml2
# build all work. psql -c returns only the LAST statement's result, so the CREATE
# FUNCTION is silent and we read back just the JSON. pg_temp is the session-local
# temp schema (auto-dropped at disconnect; creating there needs only the TEMP
# privilege PUBLIC holds by default). The $indiepg$ dollar-quote tags are
# backslash-escaped so this double-quoted shell string does not expand them.
META_SQL="CREATE FUNCTION pg_temp.indiepg_meta() RETURNS text AS \$indiepg\$
DECLARE
  r record;
  n bigint;
  tbls json[] := '{}';
  total bigint := 0;
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
  RETURN json_build_object(
    'schema_version', 1,
    'source_db', current_database(),
    'pg_version', current_setting('server_version'),
    'tables', coalesce(array_to_json(tbls), '[]'::json),
    'total_rows', total
  )::text;
END;
\$indiepg\$ LANGUAGE plpgsql;
SELECT pg_temp.indiepg_meta();"

say "collecting source row counts..."
SOURCE_META="$(run_psql "$META_SQL")" || die "could not read source row counts from the database"
case "$SOURCE_META" in
	'{'*'}') : ;;
	*) die "unexpected metadata from the source database (is --db correct?)" ;;
esac

CREATED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Splice the shell-side fields (sha256/byte_size/created_at) into the
# Postgres-built object by stripping its outer braces. Order is irrelevant in JSON.
INNER="${SOURCE_META#\{}"
INNER="${INNER%\}}"
printf '{"sha256":"%s","byte_size":%s,"created_at":"%s",%s}' \
	"$SHA" "$SIZE" "$CREATED" "$INNER" > "$META_FILE"

# --- 4. upload the dump FIRST, then meta.json (presence == verifiable) ----
# curl -f fails on any HTTP error; -H 'Expect:' avoids the 100-continue some S3
# gateways reject; no extra signed headers, so the presigned signature holds. The
# full presigned URL is a secret, so failures print a generic message, never the URL.
upload() {
	# $1 = url, $2 = file, $3 = label. The presigned URL is a bucket-write bearer
	# token, so it is fed to curl through the 0600 $CURL_CFG file (`url = "..."`) —
	# NEVER as an argv argument, where `ps`/​/proc/<pid>/cmdline would expose it to
	# other local users (who could then overwrite the dump/meta). The file path in
	# --upload-file is not sensitive, so it stays on the command line. Capture curl's
	# stderr (discard stdout) so a failure message can be shown with any presigned URL
	# redacted out of it. The `&& return 0` keeps set -e from aborting on the expected
	# non-zero on failure.
	printf 'url = "%s"\n' "$1" > "$CURL_CFG"
	_err="$(curl -fsS -H 'Expect:' --config "$CURL_CFG" --upload-file "$2" 2>&1 >/dev/null)" && return 0
	printf '%s' "$_err" | sed 's#https\{0,1\}://[^ ]*#<presigned-url>#g' | head -n1 >&2
	die "failed to upload the $3 — the link may have expired, or the panel's S3 credentials/bucket are misconfigured or unreachable; check the error above, then generate a new drop-off link in the panel"
}

say "uploading dump (${SIZE} bytes)..."
upload "$DUMP_URL" "$DUMP_FILE" "dump"

say "uploading metadata..."
upload "$META_URL" "$META_FILE" "metadata"

# --- done -----------------------------------------------------------------
say "upload complete and verifiable (sha256 ${SHA})"
say "return to the indiepg panel and click Start to import."
