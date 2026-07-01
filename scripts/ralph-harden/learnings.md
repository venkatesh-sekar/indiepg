# Learnings — hardening loop

Durable memory across iterations. Newest at top. Keep entries one or two lines.
When a pattern recurs (≥2×), promote it to a short rule here so future
iterations don't rediscover it.

## Active rules

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
