# Learnings — hardening loop

Durable memory across iterations. Newest at top. Keep entries one or two lines.
When a pattern recurs (≥2×), promote it to a short rule here so future
iterations don't rediscover it.

## Active rules

- SQL-rewriting/classification guards must handle EVERY syntactic form of a
  construct, not just the common keyword. A top-level row bound is `LIMIT` *or*
  `FETCH FIRST/NEXT ... ROWS` (PostgreSQL rejects both in one query), so an
  auto-LIMIT gate keyed on LIMIT alone breaks a valid FETCH read. When testing
  such a guard, cover lower/mixed-case and quoted-identifier variants: a
  case-sensitive or keyword-only match is a plausible one-line regression the
  uppercase-only test won't catch (the test-skeptic found exactly this). (Iter #2)
- Run `gofmt -l $(git ls-files '*.go')` FIRST, before picking work. The committed
  tree can carry gofmt drift independent of your change (doc-comment list reflow,
  struct-field alignment under gofmt ≥1.19) that reds the fmt gate. It's a
  priority-0 red gate — fix it (a plain `gofmt -w`, no behavior change) as its own
  small commit. Seen Iter #1: server.go + 2 e2e files were drifted at HEAD.
- `make` here is an unresolved zsh autoload stub — run the explicit gate commands
  from AGENTS.md directly (gofmt -l / go vet / go test ./... / CGO build). Build to
  /tmp (`go build -o /tmp/...`) so no stray binary dirties the tree.
- Preflights that gate recovery/restore should fail OPEN on uncertainty (can't
  enumerate the repo, missing metadata) and reject ONLY the provably-impossible
  case. Blocking recovery — the most data-critical op — on a transient read is
  worse than a late pgBackRest error. pgBackRest stays the final arbiter. (Iter #1)
- Prefer make targets: `make verify` (backend gate) and `make verify-web` (web
  gate). They encode the right flags.
- A test only counts if it would **fail** when the behavior it protects breaks.
  If a one-line source mutation wouldn't fail the test, the test is too weak.
- Sandbox: `go`/`make` can be blocked by snap-confine (`cap_dac_override`). Run
  them outside the sandbox; never commit unverified.
- Shared defaults are `scripts/ralph/DEFAULTS.md`; real behavior reference is the
  `sm` CLI at `/primary01/git/server-management/src/sm/`.
- e2e (`test/e2e`, `//go:build e2e`) is Docker/systemd-based and heavy; run one
  scenario with `make e2e SCENARIO=TestName`. Skip if Docker is unavailable, but
  say so in the progress note.

## Rejected ideas — do not re-propose

(none yet — when the loop drops a backlog item as not-worth-doing, record it
here with a one-line reason so it never comes back.)
