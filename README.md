# pgpanel

A single self-hosted binary that installs and **owns** a native PostgreSQL,
serves a private web admin panel to safely browse/query it and run backups, and
pushes Postgres + backup + host metrics to any OTLP collector.

Built for indie hackers running their own database server. It is lightweight,
fast, and **very safe** — it asks for confirmation on anything risky and enforces
read-only access at the database level, not just in the UI.

## What it does

- **Provisions** native Postgres (apt + systemd), reached over the unix socket.
- **Browse & query** read-first: a dedicated read-only role enforces read-only at
  the DB level; auto-LIMIT and statement timeouts protect the query box.
- **Roles & databases** managed via guided, confirmed actions — including
  read-only users done correctly (`ALTER DEFAULT PRIVILEGES`).
- **Backups** via pgBackRest to any S3-compatible target, with rich stats,
  restore, and restore-testing.
- **Migration** across hosts (single-DB, whole-cluster, SSH-less session wizard).
- **Observability** in an in-panel dashboard and exported via OTLP.
- **Alerting** through Pushover and generic webhooks with smart defaults.

Every shared external resource (the S3 backup repo) is claimed with a
single-writer ownership marker and **fails fast** on a foreign owner, so two
panels can never silently corrupt one repository.

## Quick start

Download the binary onto a fresh box and install:

```sh
sudo ./pgpanel install      # provision Postgres, set admin password, generate identity
sudo systemctl enable --now pgpanel
```

Then open the panel on its **private** bind address (localhost / Tailscale /
private net — never `0.0.0.0` unless explicitly forced).

Lost the admin password? From an SSH session on the box:

```sh
sudo pgpanel reset-password
```

## Dev build

Requires Go 1.26 and Node (only to build the SPA once).

```sh
make web      # build the embedded SPA into internal/server/web/dist
make build    # compile the static binary (CGO_ENABLED=0)
make run      # build + serve locally
make test     # run the test suite
make vet      # go vet ./...
```

The frontend is compiled to static files and embedded with `embed.FS`; the
server never needs Node at runtime.

## Safety invariants

1. Read-only is enforced at the DB level via a dedicated read-only role.
2. Destructive operations require an explicit typed-name confirmation.
3. Shared external resources use single-writer ownership markers and fail fast
   on a foreign owner.
4. SQL identifiers are always quoted and escaped when building statements.
5. The panel binds private by default and audit-logs every action.
