# Progress (rolling)

Newest first. One short entry per iteration: date, band, what changed, why.
Keep ~20 entries; archive older ones if this grows large.

<!-- iterations will be prepended here -->

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
