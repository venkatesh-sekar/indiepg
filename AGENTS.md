# AGENTS.md — working conventions for indiepg

Guidance for any agent (or human) making changes here. Keep changes small,
tested, and aligned with the project's safety invariants.

## What this is

A single self-hosted Go binary that installs and **owns** a native PostgreSQL
and serves a private web admin panel for indie hackers. The frontend is a Vite +
React SPA built at compile time and embedded with `embed.FS`; the server needs
no Node at runtime. Built artifacts under `internal/server/web/dist` are
committed, so a plain `make run` works without Node.

Layout: Go backend in `internal/` (one package per concern: `auth`, `pg`,
`backup`, `migrate`, `config`, `store`, `server`, `alert`, …), entrypoint in
`cmd/indiepg`, SPA sources in `web/`.

## Build / test / run

Prefer the `make` targets — they encode the right flags.

```sh
make run      # build + serve against ./indiepg-dev.db; prints a one-time admin password
make reset    # wipe local dev state (the SQLite db + WAL) for a clean slate
make test     # go test ./... -count=1
make vet      # go vet ./...
make fmt      # go fmt ./...
make build    # CGO_ENABLED=0 static binary -> ./indiepg
make web      # rebuild the embedded SPA (npm ci && npm run build) after editing web/
make tidy     # go mod tidy
```

Requires Go 1.26. Node is only needed for `make web`.

### Web (only when you touch `web/`)

Run all of these from the `web/` directory:

```sh
cd web
npm ci                    # fresh, lockfile-faithful install
npm run typecheck         # tsc -b --noEmit
npm run build             # tsc -b && vite build
npm test                  # vitest run (one-shot); npm run test:watch to iterate
```

Tests use vitest + React Testing Library + jsdom. Config is inline in
`web/vite.config.ts`; setup in `web/src/test/setup.ts`. Name tests
`*.test.ts(x)` / `*.spec.ts(x)` under `web/src/`.

## Verify gate (all must pass before committing)

One command runs the whole backend gate (fmt-check → vet → test → static build):

```sh
make verify
```

It is exactly the explicit gate below. `make fmt-check` checks `gofmt` without
rewriting — it fails and lists any file that isn't gofmt-clean:

```sh
gofmt -l $(git ls-files '*.go')        # must print NOTHING
go vet ./...
go test ./... -count=1
CGO_ENABLED=0 go build ./cmd/indiepg
```

If `web/` changed, also run the web gate (needs Node):

```sh
make verify-web                        # npm ci → typecheck → build → test
```

Never commit with a red gate, and never leave the tracked tree dirty.

## Conventions

- **Single trusted operator, private instance.** When two designs differ on
  security, the most secure one wins. Never widen access, never weaken a default
  for convenience.
- **Read-only enforced at the DB level**, not just the UI — via a dedicated
  read-only Postgres role. Query box has auto-LIMIT and a statement timeout.
- **Confirm-on-risky.** Destructive actions require an explicit typed-name
  confirmation, and UI text must say *what an action will do* before it does it.
- **Best-defaults-first.** New capability ships working on safe defaults;
  overrides are optional and clearly labeled with their effect. The trusted
  Postgres / PgBouncer / pgBackRest defaults are in
  [`scripts/ralph/DEFAULTS.md`](scripts/ralph/DEFAULTS.md) (ported from the `sm`
  CLI at `/primary01/git/server-management/src/sm/`) — match them unless there's
  a documented reason to deviate.
- **Secrets never logged**, never weakened: S3 creds, Pushover tokens, and the
  admin hash live in the state DB (`0600`) and must stay out of logs.
- **Atomic config writes** with preserved ownership; back up a config before
  modifying it so a bad change can roll back. SQL identifiers are always quoted
  and escaped.
- **Single-writer ownership** on shared external resources (the S3 backup repo):
  claim with an ownership marker and fail fast on a foreign owner.
- **YAGNI / KISS.** No new dependencies, abstractions, or config the indie
  hacker didn't ask for. Prefer hardening, testing, and simplifying what exists.
- **Every change is tested.** Go tests for backend, a vitest/RTL test for
  frontend.

See `README.md` for the user-facing safety invariants and install flow.
