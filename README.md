# indiepg

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

One command on a fresh Debian/Ubuntu box. It downloads indiepg, provisions
Postgres, installs a systemd service, and prints your panel URL and a one-time
admin password:

```sh
curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/install.sh | sudo sh
```

Already have the binary on the box? Skip the download — `install` does the rest,
including writing and starting the service:

```sh
sudo indiepg install
```

Either way it ends with everything you need:

```
============================================================
  indiepg is installed.
  Running now as a systemd service (auto-starts on boot).

  Panel URL       http://127.0.0.1:8443
  Admin password  <48-char password>   (shown once — save it now)
  Reset it later  sudo indiepg reset-password

  The panel binds a PRIVATE address — reach it over localhost,
  Tailscale, or your private network.
============================================================
```

No second `systemctl` step, no hand-written unit file, no guessing the URL.
The panel never binds `0.0.0.0` unless you explicitly force it.

**Lost the password?** From an SSH session on the box, generate a fresh one —
no flags, nothing to remember:

```sh
sudo indiepg reset-password                       # prints a new password, once
sudo indiepg reset-password --password 'my-pick'  # or set your own
```

**Updating.** Swap in the latest release binary and restart the service in one
step. It's a binary-only upgrade — your admin password, config, and databases
are left exactly as they are:

```sh
sudo indiepg update                  # latest release
sudo indiepg update --version v1.2.3 # pin a specific tag
```

**Start / stop / restart.** Thin wrappers over the systemd unit, so you don't
have to remember the service name:

```sh
sudo indiepg stop                    # stop now
sudo indiepg start                   # start again
sudo indiepg restart                 # restart
sudo systemctl disable indiepg       # stop auto-start on boot (systemd directly)
```

**Uninstalling.** Remove indiepg entirely with `uninstall`:

```sh
sudo indiepg uninstall               # stop+disable+remove the service
sudo indiepg uninstall --purge       # also delete the state DB and the binary
```

> `uninstall` never touches PostgreSQL or the databases it manages — that data
> stays put. Remove Postgres yourself if you really want it gone.

> The one-liner pulls the latest [GitHub release](https://github.com/venkatesh-sekar/indiepg/releases).
> Until you've cut one, build locally (see **Dev build**) and run
> `sudo ./indiepg install`.

## Dev build

Requires Go 1.26. Node is only needed to rebuild the SPA — the built assets are
committed, so a plain `make run` works without it.

```sh
make run      # build + serve locally; prints a generated login password
make reset    # wipe local dev state for a clean slate
make test     # run the test suite
make vet      # go vet ./...

make web      # (optional) rebuild the embedded SPA after editing web/ sources
make build    # compile the static binary (CGO_ENABLED=0)
```

`make run` serves against `./indiepg-dev.db` and, on first run, prints a
generated admin password — copy it to log in. `make reset` deletes that local
state so the next run starts fresh. The frontend is compiled to static files and
embedded with `embed.FS`; the server never needs Node at runtime.

## Safety invariants

1. Read-only is enforced at the DB level via a dedicated read-only role.
2. Destructive operations require an explicit typed-name confirmation.
3. Shared external resources use single-writer ownership markers and fail fast
   on a foreign owner.
4. SQL identifiers are always quoted and escaped when building statements.
5. The panel binds private by default and audit-logs every action.
