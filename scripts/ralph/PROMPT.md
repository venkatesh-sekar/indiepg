# indiepg Ralph loop — UI/UX orchestrator (one iteration)

You are **one iteration** of a loop whose ONLY job is to rebuild the indiepg web
panel on **shadcn/ui**. This is a **pure UI/UX loop** — do not touch Go/backend
behavior, the API shape, or business logic. Frontend only (`web/`).

Do **exactly one** thing this iteration. End with **one atomic commit** and a
**clean tree**. Then stop. The shell loop will run you again.

---

## Goal (definition of done)

> Every view in the panel is rebuilt with **shadcn/ui components**, composed and
> styled the shadcn way. **No hand-rolled component survives where a shadcn one
> exists.** The panel looks clean, simple, and consistent; behavior is identical
> to before; every change is tested.

You may only write `COMPLETE.md` when ALL of these hold:
1. shadcn is initialised (`web/components.json` + Tailwind configured).
2. Every view (`web/src/views/*.tsx`) is rebuilt with shadcn components.
3. The hand-rolled components are **gone**, replaced by shadcn equivalents:
   `ui.tsx`→Button/Badge/Alert/etc · `Modal.tsx`→Dialog · `ConfirmDialog.tsx`→
   AlertDialog · `Toast.tsx`→sonner · `Layout.tsx`→Sidebar shell.
4. The hand-rolled `styles.css` design-token CSS is removed in favor of Tailwind
   + shadcn semantic tokens (keep only minimal unavoidable globals).
5. `npm run typecheck`, `npm test`, `npm run build`, and `go build` are all green;
   no behavior regressions.

## Non-negotiable rules

1. **One item per iteration. One atomic commit. Never `git push`.**
2. **Never leave the tree dirty.** If you can't finish cleanly, revert
   everything (`git checkout -- .` / `git reset --hard HEAD`, delete new files)
   and pick a smaller item.
3. **Never park work.** You decide. Drop an item from `backlog.md` with a
   one-line reason if it's not worth doing, and pick another.
4. **Use shadcn components — never roll your own when shadcn has one.** This is
   the whole point. Before building any UI, check the shadcn component table and
   the installed `web/src/components/ui/` dir. See `scripts/ralph/UI-RULES.md`.
5. **Use the `shadcn` skill** for every component task: `npx shadcn@latest
   search`, `add`, and `docs <component>` (fetch the docs URLs before using a
   component). Follow its Critical Rules exactly.
6. **Behavior parity.** The migrated view must do exactly what it did before —
   same data, same actions, same confirmations. Update that view's test to match
   and keep it green. UI text must still say what an action will do.
7. **KISS.** Clean, simple, consistent. No new features, no decorative
   complexity, no extra dependencies beyond what shadcn pulls in.

## Steps for this iteration

1. **Read context:** `scripts/ralph/state.json`, `scripts/ralph/backlog.md`,
   the top of `scripts/ralph/learnings.md`, and `scripts/ralph/UI-RULES.md`.
2. **Pick ONE item** — the highest one open in `backlog.md` (scaffold first, then
   the shared shell, then views one at a time, then cleanup). One view or one
   shared component per iteration — keep the diff reviewable.
3. **Do it the shadcn way:** invoke the `shadcn` skill; `add` the components you
   need (don't re-add installed ones); fetch their docs; compose them. Match the
   `frontend-design` skill's guidance for a clean, intentional look. Replace the
   hand-rolled markup; delete the dead component/CSS it supersedes.
4. **Update the view's test** to the new markup and keep it green. Add a test if
   one is missing for what you touched.
5. **Review pass (required).** Use the Task tool to launch the
   `ui-heuristics-reviewer` subagent on the changed view (UX heuristics: clarity,
   error/empty/loading states, affordances, consistency) and address blocking
   findings. Then self-check against `UI-RULES.md` (no hand-rolled where shadcn
   exists; semantic tokens; `gap-*` not `space-*`; forms use `Field`/`FieldGroup`).
6. **Verify gates — ALL must pass** (see Verify). If you can't get them green,
   revert (rule 2), note a one-line learning, pick a smaller slice.
7. **Record + commit atomically.** Prepend a short entry to
   `progress-current.md`, update `state.json`, and
   `git commit -am "ralph(ui): <view/component> → shadcn — <note>"`.
8. **Completion check.** Only if every condition in "Goal" above holds, write
   `scripts/ralph/COMPLETE.md` and stop.

## Order of work (bands)

```
A  Scaffold     shadcn init (base style, neutral) + Tailwind into the Vite app;
                wire globals/tokens; add a pilot component; keep build green
B  App shell    Layout.tsx → shadcn Sidebar + header; nav, active state, sign-out
C  Primitives   replace shared hand-rolled: ui.tsx → Button/Badge/Alert/Spinner;
                Modal → Dialog; ConfirmDialog → AlertDialog; Toast → sonner
D  Views        migrate one per iteration: Login, Dashboard, RolesDatabases,
                Query, Backups, Migrate, Alerts, Settings, Pooler, DatabaseTuning
E  Cleanup      delete dead styles.css tokens + any orphaned components; final
                consistency pass (empty states → Empty, loaders → Skeleton)
```

Within a band, smallest reviewable slice first. Scaffold (band A) must land
before anything else — nothing imports shadcn until `components.json` exists.

## Verify (run from repo root; all must pass before commit)

```
cd web && npm run typecheck
cd web && npm test
cd web && npm run build
CGO_ENABLED=0 go build ./cmd/indiepg     # the SPA is embedded; build must still pass
```

(Backend Go tests are out of scope — you are not changing Go. `go build` only
confirms the freshly-built SPA still embeds.)

## Remember

shadcn components, composed cleanly. One view at a time. Behavior identical,
tests green, tree clean — then stop and let the loop run you again.
