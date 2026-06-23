# Progress (rolling)

Newest first. One short entry per iteration: date, band, what changed, why.
Keep ~20 entries; archive older ones if this grows large.

<!-- iterations will be prepended here -->

## 2026-06-24 · band 0 (foundation) · executable verify gate
Closed the last foundation item: verified the web gate is green from a fresh
`npm ci` (typecheck/build/test all pass) and confirmed the build is
deterministic — the committed `internal/server/web/dist` is byte-identical after
a rebuild, so running the gate never dirties the tracked tree. Turned the gate
from prose into one reproducible command: added `make verify`
(fmt-check → vet → test → static build), `make verify-web`
(npm ci → typecheck → build → test), and `make fmt-check` — the latter runs the
`gofmt -l` "must print nothing" check that `go fmt` cannot do (it rewrites
rather than reports). AGENTS.md now points at these targets. Why: the verify
gate was re-typed by hand each iteration and free to drift from the docs; an
executable gate keeps every iteration consistent and is the literal meaning of
"wire the verify gate into the loop reality." Reviewed by
feature-dev:code-reviewer — fixed its one blocking finding (`fmt-check`
discarded `gofmt`'s non-zero exit, so a syntactically-broken file would silently
pass; now it captures `$?` and fails). `make verify` green (exit 0); web gate
green; tree clean. Foundation band done → moving to band 1 (security).

## 2026-06-24 · band 0 (foundation) · root AGENTS.md
Added a root `AGENTS.md` so every iteration (and any human) shares one
consistent set of build/test/run commands and conventions. It documents the
`make` targets (run/reset/test/vet/fmt/build/web/tidy), the web verify gate
(`cd web` → `npm ci`/`typecheck`/`build`/`test`, vitest+RTL+jsdom), the full
Go verify gate, and the project conventions (single trusted operator,
read-only enforced at the DB level, confirm-on-risky, best-defaults-first,
secrets never logged, atomic config writes, single-writer S3 ownership,
YAGNI/KISS) — linking `scripts/ralph/DEFAULTS.md` as the source of trusted
Postgres/PgBouncer/pgBackRest defaults. Why: closes a band-0 foundation item;
keeps future iterations aligned without re-deriving conventions. Reviewed by
feature-dev:code-reviewer — every documented command/path verified accurate;
applied its one fix (made the `web/` shell block's working directory explicit).
All Go gates green (gofmt clean, vet, test, build).

## 2026-06-24 · band 0 (foundation) · vitest + RTL test runner
Added vitest + React Testing Library + jsdom to `web/` and wired `npm test`
(`vitest run`, CI-less one-shot) plus `test:watch`. Config lives inline in
`vite.config.ts` (jsdom env, `src/test/setup.ts` setup with jest-dom matchers +
RTL cleanup, include `src/**/*.{test,spec}.{ts,tsx}`). Added a real component
test for `ui.tsx` covering `ResultBadge` tone mapping and `ErrorNotice`
ApiError-vs-plain-Error rendering (6 tests green). Why: unblocks the "every
frontend change is tested" north-star requirement — the web verify gate
(`npm test`) is now real. Reviewed by feature-dev:code-reviewer (no blocking
findings); typecheck/build/test all green; Go gates unaffected and green.
