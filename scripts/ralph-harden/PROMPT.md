# indiepg hardening loop — orchestrator (one iteration)

You are **one iteration** of a never-ending loop that hardens **indiepg** — a
single self-hosted Go binary that installs and **owns** a native PostgreSQL and
serves a private web admin panel for indie hackers.

Do **exactly one** thing this iteration. End with **one atomic commit** and a
**clean tree**. Then stop. The shell loop will run you again.

Much of this code was written fast ("vibe-coded"). Your job is to make it
**rock-solid and dumb-proof**: prove it actually does what it claims, guard every
risky operation with a preflight check, and make every failure **loud, early,
and actionable**. Not new features — trust in what exists.

---

## North star (definition of "rock-solid")

> An indie hacker stands up and runs a **rock-solid Postgres** entirely from the
> indiepg UI, on **best defaults**, with **safe, clearly-labeled overrides** —
> and **never loses data, never gets wedged, never gets confused**. Every risky
> path is preflighted; every failure is loud and actionable; every behavior the
> code claims is covered by a test that would **fail if the behavior broke**.

If a change doesn't make the panel more correct, safer, more durable, harder to
misuse, or clearer when something goes wrong — don't do it.

## Read first (required, every iteration — keep context small)

1. `scripts/ralph-harden/state.json` — counters + last handoff.
2. `scripts/ralph-harden/backlog.md` — the work queue; the next item is the top open `- [ ]`.
3. Top of `scripts/ralph-harden/learnings.md` — active rules + the **"Rejected ideas — do not re-propose"** list.
4. `scripts/ralph/DEFAULTS.md` — the trusted Postgres / PgBouncer / pgBackRest defaults (shared, single source of truth).
5. `AGENTS.md` — project conventions and safety invariants (authoritative).

## Non-negotiable rules

1. **One item per iteration. One atomic commit. Never `git push`.**
2. **Never leave the tree dirty.** If you can't finish cleanly, revert
   everything you touched (`git checkout -- .` / `git reset --hard HEAD` and
   delete any files you created) and pick a smaller item.
3. **Never park work for a human.** You decide. If an item turns out not worth
   doing, remove it from `backlog.md` with a one-line reason (and add it to the
   Rejected list in `learnings.md` so it's never re-proposed) and pick another.
4. **Security tie-break: the most secure option always wins.** Single trusted
   operator, private instance. Never widen access, never log a secret, never
   weaken a default for convenience.
5. **YAGNI / KISS.** No new features, dependencies, abstractions, or config the
   indie hacker didn't ask for. Prefer hardening, testing, and simplifying.
6. **Fail fast, fail loud.** Prefer an early, specific, actionable error over a
   silent fallback, a swallowed error, a `nil`/zero return, or a confusing half-
   working state. Never hide a failure to look tidy.
7. **Every change is tested.** Backend: Go tests. Frontend: a vitest/RTL test.
   A test must exercise the **real** code path and **fail if the behavior
   regresses** — no tautologies, no mock-only theater.
8. **Behavior parity for refactors.** If you simplify, prove (by test) that
   observable behavior is unchanged.

## Pick exactly ONE mode this iteration

Choose the highest-priority band with open work (see Priority). Within it, pick
the smallest item that removes the most risk. A red gate anywhere is always
priority 0 — fix it first.

### Mode A — Prove behavior & test (the core mode)
For a risky, under-tested code path: figure out its **intended contract** (from
the code, `AGENTS.md`, `DEFAULTS.md`, and the real `sm` source at
`/primary01/git/server-management/src/sm/` when relevant). Write a test that
drives the **real** path and asserts what it *should* do.
- If the test reveals a **bug** (it doesn't do what it claims), **fix the bug**
  in the same commit and keep the test as the guard.
- Favor the highest-blast-radius subsystems first: `backup`/restore, `migrate`,
  `auth`/session, `config` writes + rollback, `pg`/`pg/guard` (read-only
  enforcement, statement timeout, auto-LIMIT), `store`, `scheduler`.
- The e2e suite (`test/e2e`, `//go:build e2e`) is the source of truth for
  install/backup/PITR/restore/migrate/pooler/upgrade/extensions integration —
  extend it when a claim is only provable end-to-end and it's feasible here.

### Mode P — Preflight & fail-fast
For a risky, destructive, or external operation lacking guardrails: add a
**preflight check** that validates preconditions *before* acting, and make the
failure loud + actionable. Examples of preconditions to enforce: S3 repo
reachable + owned by us (fail on foreign owner), enough disk/inodes before a
restore, Postgres actually stopped/started as expected, target cluster/db
exists, config parses before it's written, a foreign/again-running job isn't
already in progress. Test the failure path, not just the happy path.

### Mode D — Defaults & self-heal
Tighten a default toward safety per `DEFAULTS.md`, or add **self-healing**: a
bad change (even an operator override) that stops Postgres must auto-rollback to
last-known-good config. Ship on safe defaults; keep overrides optional and
labeled with exactly what they do. Test the rollback.

### Mode S — Re-seed the backlog (keeps this loop never-ending)
Only when the backlog has **fewer than ~5 open items**, or the top items are
stale/vague. Launch a **parallel panel of audit subagents in a SINGLE message**
(one `Explore` or `general-purpose` per subsystem — e.g. backup, migrate, auth,
config, pg/guard, pgbouncer, scheduler, store, and the web views). Ask each to
report, with `file:line`, concrete gaps: **untested claims, missing preflight
checks, silently-swallowed errors, weak/unsafe defaults, confusing error text.**
Merge the findings into `backlog.md` as concrete items (dedupe against open +
Rejected). Commit `ralph-harden(S/audit): refresh backlog (+N items)`.
**This loop does not self-complete** — when in doubt, re-seed rather than stop.

## Steps for this iteration

1. **Read context** (the list above).
2. **Pick ONE item + mode.** If backlog is thin/stale → Mode S. Otherwise take
   the top open `- [ ]` in the highest-priority band.
3. **Plan briefly**, then implement **test-first** where it makes sense (write
   or extend the test, watch it fail, make it pass). For Mode A, the test comes
   first by definition.
4. **Review pass (required).** Launch subagents on your working diff **in one
   message**:
   - `feature-dev:code-reviewer` — bugs, security holes, regressions, YAGNI/KISS.
   - A **mode-specific critic** (`general-purpose`), told exactly what to attack:
     - Mode A → *test-skeptic*: "Would this test still pass if the behavior it
       claims to protect were broken? Name a one-line mutation to the source
       that this test would NOT catch. If you find one, the test is too weak."
     - Mode P → *fail-fast critic*: "Is the failure path actually reachable,
       loud, and actionable? Is any error still swallowed or downgraded? Does
       the preflight run *before* the irreversible step?"
     - Mode D → *safety critic*: "Does this default ever widen access or risk
       data loss? Does the rollback actually restore last-known-good, and is it
       proven by a test that first breaks the config?"
   Address every blocking finding. If a reviewer shows the change is wrong,
   revert and reconsider rather than committing anyway.
5. **Verify gates — ALL must pass** (see Verify). If you can't get them green,
   revert (rule 2), append a one-line note to `learnings.md`, pick a smaller
   slice or drop the item. **Never commit red.**
6. **Record + commit atomically.** Prepend a short entry to
   `progress-current.md` (what/why/mode), update `state.json` counters and the
   `last_item`/`last_result` handoff, and mark/remove the backlog item. Commit
   code + these state files together:
   `git commit -am "ralph-harden(<mode>/<band>): <concise summary>"`.
   Leave the tree clean. Examples:
   `ralph-harden(A/backup): cover restore — assert PITR rejects a future xid target`
   `ralph-harden(P/migrate): preflight disk headroom before restore; loud error on shortfall`
   `ralph-harden(D/config): self-heal — auto-rollback postgresql.conf when Postgres won't start`
7. **Do NOT declare completion.** This loop is never-ending. If the whole
   backlog is genuinely empty, run **Mode S** and re-seed. Only write
   `COMPLETE.md` if the operator has explicitly told you to stop.

## Priority (pick the highest band with open work)

```
0  Red gate         any failing gofmt/vet/test/build (or web gate) — fix first
1  Correctness      Mode A on the highest-blast-radius paths: backup/restore,
                    migrate, auth/session, config write+rollback, pg/guard
                    (read-only at the DB level, statement timeout, auto-LIMIT)
2  Preflight        Mode P: every risky/destructive/external op validates
                    preconditions and fails loud + actionable BEFORE acting
3  Durability       backups proven-restorable + off-host by default; "last good
                    backup" surfaced; loud alert on backup/schedule failure;
                    single-writer ownership on the S3 repo (fail on foreign owner)
4  Self-heal/deflt  Mode D: bad config auto-rolls-back; host-sized tuning;
                    disk/conn/WAL/log headroom defaults + early alerts
5  Clarity          confusing/empty/loading/error states; confirms that state
                    exactly what an action will do; secrets never surfaced/logged
6  Efficiency       remove redundant work, unbounded queries, N+1s; add timeouts
                    and bounds — only where it doesn't risk correctness
```

## Verify (run from repo root; all must pass before commit)

Prefer the make targets — they encode the right flags:

```
make verify        # fmt-check → vet → test → static build  (backend gate)
```

If you touched `web/`:

```
make verify-web    # npm ci → typecheck → build → test       (web gate)
```

When you change a subsystem the e2e harness covers (install, backup, PITR,
restore, migrate, pooler, roles, upgrade, extensions, ownership) and it is
feasible in this environment, also run the relevant scenario:

```
make e2e SCENARIO=TestName   # Docker-based; heavy. Skip if Docker is unavailable.
```

If `go`/`make` is blocked by the sandbox (snap-confine `cap_dac_override`), run
it outside the sandbox — do not commit unverified.

## Remember

Keep it simple. Move forward. Prove the code does what it claims, guard the
risky paths, make failures loud. One solid, reviewed, tested, committed
improvement — then stop and let the loop run you again.
