# Best defaults — ported from the `sm` CLI

These are the trusted Postgres / PgBouncer / pgBackRest defaults from the
original `server-management` (`sm`) CLI. They encode deliberate "best default"
decisions for a single self-hosted box. Match them in indiepg unless there's a
documented reason to deviate. Source of truth:
`/primary01/git/server-management/src/sm/`.

## Postgres tuning (host-sized) — `templates/postgresql/tuning.conf.j2`, `services/tuning.py`

Size to the box's RAM/CPU at provision time. Indie hackers never tune by hand.

```
shared_buffers          = <sized to RAM>     # restart required
effective_cache_size    = <sized to RAM>
maintenance_work_mem    = <sized to RAM>
work_mem                = <sized to RAM/conns>
wal_buffers             = -1                  # auto
min_wal_size            = 1GB
max_wal_size            = 4GB
checkpoint_completion_target = 0.9
random_page_cost        = 1.1                 # SSD; use higher for HDD
default_statistics_target = 100
max_connections         = <sized>            # restart required
```

Workload profiles (affect pool sizing + connection math): `oltp` (web apps,
high concurrency), `olap` (analytics, long queries), `mixed` (default,
balanced). Parameters needing a restart: `shared_buffers`, `max_connections`,
`max_worker_processes`, `max_parallel_workers`, `wal_buffers`, `huge_pages`.

## Client auth lockdown — `templates/postgresql/pg_hba.conf.j2`

Most-secure default: local socket + loopback only, **no remote auth**.

```
local   all             postgres                                peer
local   all             all                                     peer
host    all             all             127.0.0.1/32            scram-sha-256
host    replication     postgres        127.0.0.1/32            scram-sha-256
```

## WAL / archiving for PITR — `templates/postgresql/pgbackrest.conf.j2`

```
wal_level        = replica
archive_mode     = on
archive_command  = 'pgbackrest --stanza=<stanza> archive-push %p'
max_wal_senders  = 3
wal_compression  = on
```

## PgBouncer (opt-in pooler) — `templates/pgbouncer/pgbouncer.ini.j2`, `services/pgbouncer.py`

Off by default. When enabled, ship with:

```
auth_type           = scram-sha-256          # never trust/plain
server_reset_query  = DISCARD ALL
pool_mode           = transaction            # default
[databases] * = host=127.0.0.1 port=5432     # loopback to local PG
```

Pool sizing math (coordinated with PG `max_connections`):

- `available = pg_max_connections - 5` (5 reserved for admin/superuser)
- `default_pool_size = round(available * util)`, util = oltp 0.80 / mixed 0.70 / olap 0.60 (floor 20)
- `min_pool_size = default_pool_size / 4` (floor 5)  — kept ready
- `reserve_pool_size = default_pool_size / 5` (floor 5)  — burst overflow
- `max_client_conn = default_pool_size * multiplex`, multiplex = oltp 20 / mixed 10 / olap 5
- `server_idle_timeout = 300` (oltp/mixed) / `600` (olap)

Config writes: atomic, owner `pgbouncer`, perms `0640`, reload via SIGHUP
(`systemctl reload`), restart as fallback; verify it's still running after.

## Safety conventions from `sm` worth preserving

- **Atomic file writes** with preserved ownership for every config change.
- **Backup-before-modify** for config files (so a bad change can roll back).
- **Dry-run / preview** of changes before applying.
- **SCRAM-SHA-256** everywhere for passwords; reject plain/trust.
- Secrets/config files written `0600`/`0640`, never world-readable, never logged.
