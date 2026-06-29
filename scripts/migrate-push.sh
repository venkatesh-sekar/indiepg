#!/bin/sh
# indiepg migrate-push — "drop-off link" source uploader.
#
# Dumps ONE PostgreSQL database from a source the indiepg panel cannot reach
# (behind NAT/a firewall, no inbound, no panel installed, no AWS credentials) and
# uploads it to S3 through two short-lived, single-key presigned PUT URLs the
# panel minted. The panel then imports it with SHA-256 + row-count verification.
# No panel install and no AWS keys are needed on this box.
#
# Usage (copy the exact line from the panel; it has the two URLs filled in):
#   curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/migrate-push.sh \
#     | sh -s -- --dump-url '<dump-url>' --meta-url '<meta-url>' --db <database> --docker <container>
#
#   native alternative (no Docker):
#   ... --db <database> --host <host> [--port 5432] [--user postgres]
#
# Password (native mode only): set PGPASSWORD in the environment, or you will be
# prompted. It is NEVER passed as a command-line flag (which would leak it in the
# process listing).
#
# Flags:
#   --dump-url URL    presigned PUT URL for the dump object        (required)
#   --meta-url URL    presigned PUT URL for the meta.json object   (required)
#   --db NAME         source database to migrate                   (required)
#   --docker NAME     run pg_dump/psql inside this docker container
#   --host HOST       connect to a native Postgres at HOST  (XOR --docker)
#   --port PORT       source port (native; default 5432)
#   --user USER       source user (native; default postgres)
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

Usage (copy the exact line from the panel; it has the two URLs filled in):
  curl -fsSL .../scripts/migrate-push.sh | sh -s -- \
    --dump-url '<dump-url>' --meta-url '<meta-url>' --db <database> --docker <container>

  native alternative (no Docker):
    ... --db <database> --host <host> [--port 5432] [--user postgres]

Password (native mode only): set PGPASSWORD in the environment, or you will be
prompted. It is NEVER passed as a command-line flag.

Flags:
  --dump-url URL    presigned PUT URL for the dump object        (required)
  --meta-url URL    presigned PUT URL for the meta.json object   (required)
  --db NAME         source database to migrate                   (required)
  --docker NAME     run pg_dump/psql inside this docker container
  --host HOST       connect to a native Postgres at HOST  (XOR --docker)
  --port PORT       source port (native; default 5432)
  --user USER       source user (native; default postgres)
  --help            show this help and exit
EOF
}

# --- defaults / args ------------------------------------------------------
DUMP_URL=""
META_URL=""
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
[ -n "$DB" ]       || die "--db <source-database> is required"
case "$DB" in
	*'<'*|*'>'*) die "--db still has the <source-database> placeholder; put your real database name there" ;;
esac

if [ -n "$CONTAINER" ] && [ -n "$HOST" ]; then
	die "choose ONE of --docker or --host, not both"
fi
if [ -z "$CONTAINER" ] && [ -z "$HOST" ]; then
	die "specify the source: --docker <container>  OR  --host <host> [--port] [--user]"
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
if [ -z "$CONTAINER" ] && [ -z "${PGPASSWORD:-}" ] && [ -t 0 ]; then
	printf '%s: Postgres password for %s@%s (blank if none): ' "$REPO_PUSH" "$USER_NAME" "$HOST" >&2
	stty -echo 2>/dev/null || true
	# shellcheck disable=SC2162
	read PGPASSWORD || true
	stty echo 2>/dev/null || true
	printf '\n' >&2
	export PGPASSWORD
fi

# --- source command wrappers ----------------------------------------------
# run_pg_dump writes a custom-format (-Fc) dump of $DB to stdout.
run_pg_dump() {
	if [ -n "$CONTAINER" ]; then
		if [ -n "${PGPASSWORD:-}" ]; then
			docker exec -i -e PGPASSWORD "$CONTAINER" pg_dump -Fc -d "$DB"
		else
			docker exec -i "$CONTAINER" pg_dump -Fc -d "$DB"
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
			docker exec -i -e PGPASSWORD "$CONTAINER" psql -tAqX -v ON_ERROR_STOP=1 -d "$DB" -c "$_sql"
		else
			docker exec -i "$CONTAINER" psql -tAqX -v ON_ERROR_STOP=1 -d "$DB" -c "$_sql"
		fi
	else
		psql -tAqX -v ON_ERROR_STOP=1 -h "$HOST" -p "$PORT" -U "$USER_NAME" -d "$DB" -c "$_sql"
	fi
}

# --- temp files (0600, removed on exit) -----------------------------------
DUMP_FILE="$(mktemp)" || die "could not create a temp file"
META_FILE="$(mktemp)" || die "could not create a temp file"
chmod 0600 "$DUMP_FILE" "$META_FILE" 2>/dev/null || true
trap 'rm -f "$DUMP_FILE" "$META_FILE"' EXIT INT TERM

# --- 1. dump --------------------------------------------------------------
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
# for verification), plus pg_version and total_rows. query_to_xml runs count(*)
# per table; xpath extracts it. Postgres builds the JSON object so identifiers and
# the version string are escaped correctly.
META_SQL="WITH counts AS (
  SELECT table_schema AS schema, table_name AS name,
    (xpath('/row/c/text()',
       query_to_xml(format('SELECT count(*) AS c FROM %I.%I', table_schema, table_name),
                    false, true, '')))[1]::text::bigint AS rows
  FROM information_schema.tables
  WHERE table_type = 'BASE TABLE'
    AND table_schema NOT IN ('pg_catalog','information_schema')
)
SELECT json_build_object(
  'schema_version', 1,
  'source_db', current_database(),
  'pg_version', current_setting('server_version'),
  'tables', coalesce((SELECT json_agg(json_build_object('schema', schema, 'name', name, 'rows', rows)
                                       ORDER BY schema, name) FROM counts), '[]'::json),
  'total_rows', coalesce((SELECT sum(rows) FROM counts), 0)
)::text;"

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
	# $1 = url, $2 = file, $3 = label. Capture curl's stderr (discard stdout) so a
	# failure message can be shown with any presigned URL redacted out of it. The
	# `&& return 0` keeps set -e from aborting on the expected non-zero on failure.
	_err="$(curl -fsS -H 'Expect:' --upload-file "$2" "$1" 2>&1 >/dev/null)" && return 0
	printf '%s' "$_err" | sed 's#https\{0,1\}://[^ ]*#<presigned-url>#g' | head -n1 >&2
	die "failed to upload the $3 — the link may have expired; generate a new drop-off link in the panel"
}

say "uploading dump (${SIZE} bytes)..."
upload "$DUMP_URL" "$DUMP_FILE" "dump"

say "uploading metadata..."
upload "$META_URL" "$META_FILE" "metadata"

# --- done -----------------------------------------------------------------
say "upload complete and verifiable (sha256 ${SHA})"
say "return to the indiepg panel and click Start to import."
