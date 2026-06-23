# Learnings

Active rules-of-thumb discovered while working. Keep it short — compact toward
the top, prune stale entries. One line each. Newest at the bottom of each group.

## Build / test / verify
- Verify gates: `gofmt -l $(git ls-files '*.go')` (must be empty), `go vet ./...`,
  `go test ./... -count=1`, `CGO_ENABLED=0 go build ./cmd/indiepg`. Web: `npm run
  typecheck && npm run build` (and `npm test` once vitest is added).
- The tracked tree must be clean at the end of every iteration; untracked
  sandbox dotfiles are ignored by the loop and must not be deleted.

## Conventions
- Single trusted operator, private instance. Most-secure option always wins.
- Best-defaults-first; overrides optional and labeled with their effect.
- Trusted Postgres/PgBouncer/pgBackRest defaults live in DEFAULTS.md (ported
  from the `sm` CLI at /primary01/git/server-management/src/sm/).

## Gotchas
- (none yet)
