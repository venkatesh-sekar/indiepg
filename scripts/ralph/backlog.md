# Backlog — pure UI/UX loop (shadcn migration)

One item per iteration, top first. Check off when done + add a line to
`progress-current.md`. Drop an item (with a one-line reason) if it's not worth
doing. Keep it alive — append concrete items when it runs thin.

Goal: every view on shadcn/ui, no hand-rolled component where shadcn has one,
behavior identical, tests green. See `PROMPT.md` + `UI-RULES.md`.

Format: `- [ ] (band) item — acceptance`

## A · Scaffold (must land first) — DONE (set up manually; installs need network the loop's sandbox lacks)
- [x] (A) Initialise shadcn in `web/` (radix base, Nova preset, neutral base color, Tailwind v4 via `@tailwindcss/vite`, `components.json`, tokens in `src/styles.css`, `cn()` in `src/lib/utils.ts`). Fixed the alias bug (CLI wrote a literal `@/` dir → relocated into `src/`, added `paths` to root `tsconfig.json`). Scaffold smoke test proves the `@/` alias resolves.
- [x] (A) Added the core component set to `src/components/ui/` (24): button card badge alert dialog alert-dialog input select switch checkbox label table tabs separator skeleton sonner tooltip sidebar sheet field empty spinner dropdown-menu avatar + `use-mobile` hook.

## B · App shell
- [x] (B) Rebuild `Layout.tsx` as a shadcn `Sidebar` shell — DONE: SidebarProvider/Sidebar/SidebarInset, lucide icons, `data-active` highlight, top-bar page label, footer sign-out (collapses to a Sheet on mobile via SidebarTrigger). Deleted dead Layout-only CSS; added `window.matchMedia` stub to test/setup.ts. 93 web tests green.

## C · Shared primitives (replace hand-rolled)
- [x] (C) Badge family (`Badge`/`ReadOnlyBadge`/`ResultBadge`) → shadcn `Badge` — DONE: extended `badge.tsx` cva with `success`/`warning`/`info` variants (backed by new `--color-*`/`-soft` theme tokens aliasing legacy `--ok/--warn/--info`); ui.tsx wrappers now map tone→variant over `<ShadcnBadge>`; deleted dead `.badge*` CSS; `ui.test.tsx` ResultBadge asserts `data-variant`. Public API unchanged, callsites untouched. 93 tests green.
- [x] (C) Alert family (`Callout`/`ErrorNotice`/`StaleBanner`) → shadcn `Alert` — DONE: extended `alert.tsx` cva with soft-tinted `success`/`warning`/`info` variants (+ destructive soft bg + `border-l-4 border-l-<tone>` accent) backed by the shared `--color-*` tokens; added `data-variant` to `<Alert>`. `Callout`/`ErrorNotice`/`StaleBanner` recomposed over `Alert`+`AlertTitle`/`AlertDescription`, same public API, role="alert" + labelForCode + hint preserved; 40+ callsites untouched. Also migrated two raw callout divs (`ConfirmDialog`, `Login`) to the shared `Callout`. Deleted dead `.callout` container/color CSS (kept `.callout-detail`/`.callout-hint` content classes; scoped hint color inside Alert). Tests migrated off `.callout-*` onto `data-variant` (Backups ×9, ConfirmDialog, ui StaleBanner). 93 tests green.
- [x] (C) `Spinner` → shadcn `Spinner` — DONE: recomposed the `ui.tsx` `Spinner({label})` wrapper over the shadcn `Spinner` primitive (`Loader2Icon`+`animate-spin`), keeping its public API + muted status row (now Tailwind semantic tokens). All 14 callsites untouched. Deleted dead `.loading`/`.spinner`/`@keyframes spin` CSS. A11y: row is an explicit `role="status"` live region (`aria-live=polite`/`aria-atomic`), inner icon decorative (`role=presentation`+`aria-hidden`) to avoid nested status. Added `Spinner` tests. 95 web tests green.
- [x] (C) `Modal.tsx` → shadcn `Dialog` — DONE: recomposed the hand-rolled `Modal({open,title,onClose,children,footer,tone,width})` over shadcn `Dialog`/`DialogContent`/`DialogHeader`/`DialogTitle`/`DialogFooter`, keeping the exact public API so all ~10 callsites (Alerts, Migrate, Backups, RolesDatabases, ConfirmDialog) stay untouched. Radix now owns the focus trap / Escape / backdrop dismiss / focus restore the old version hand-rolled. width→`sm:max-w-md|xl|3xl`; `tone="danger"`→`ring-destructive/30` + `data-tone`; busy-noop `onClose` keeps it open via controlled `open` overriding `onOpenChange`. Body scrolls in an inner `max-h-[65vh] overflow-y-auto` wrapper so the pinned close button isn't clipped (reviewer finding); `aria-describedby={undefined}` to opt out of Radix's dangling description ref (reviewer finding). Deleted dead `.modal*` CSS + orphaned `@keyframes fade/pop` (kept `.confirm-message`). 95 tests green. NOTE: Modal.tsx remains as a thin Dialog shell (same pattern as Layout→Sidebar, Badge/Alert/Spinner wrappers) — full per-callsite decomposition + `.btn`→`Button` is deferred to band D/E when each view is migrated.
- [x] (C) `ConfirmDialog.tsx` → shadcn `AlertDialog` — DONE: rebuilt `ConfirmDialog` + `TypedConfirmDialog` on `AlertDialog` (no click-outside dismiss, focus trap, forced choice). Controlled `open` (parent owns close); action/cancel handlers `preventDefault()` to stop Radix auto-close; Escape ignored while busy. Buttons → `AlertDialogAction`/`AlertDialogCancel`; danger→`variant="destructive"`. Typed-name input → shadcn `Field`+`FieldLabel`+`Input`, gate intact. Message via `AlertDialogDescription asChild`/`<div>` so callsite block content stays valid. `aria-busy` added. Public API unchanged, 6 callsites untouched; test migrated to `data-variant`; dead `.confirm-message` CSS removed. 95 tests green.
- [ ] (C) `Toast.tsx` → `sonner`; mount `<Toaster />` once in the shell; replace `toast` calls — acceptance: Toast.tsx removed, notifications still fire, tests green.

## D · Views (one per iteration, behavior identical)
- [ ] (D) Login → shadcn (Card + Field/FieldGroup + Input + Button) — acceptance: login works, errors render via Alert/Field validation, test green.
- [ ] (D) Dashboard → shadcn (Cards, Badges, Skeleton loaders, Empty states; Table where used) — acceptance: same data, test green.
- [ ] (D) RolesDatabases → shadcn (Table + Dialog/AlertDialog actions + forms via Field) — acceptance: same actions/confirms, test green.
- [ ] (D) Query → shadcn (Textarea/Input, Button, Table for results, Alert for errors) — acceptance: read-first behavior + auto-LIMIT messaging intact, test green.
- [ ] (D) Backups → shadcn (Cards, Badges for state, AlertDialog for restore, Empty when none) — acceptance: last-good-backup surfacing intact, test green.
- [ ] (D) Migrate → shadcn (Tabs/steps, Field forms, Alert, progress) — acceptance: wizard flow + validation intact, test green.
- [ ] (D) Alerts → shadcn (Table/Cards, Switch to enable, Field forms for channels) — acceptance: same behavior, test green.
- [ ] (D) Settings → shadcn (Tabs + Card + Field forms) — acceptance: same behavior, test green.
- [ ] (D) Pooler → shadcn (Switch toggle, Card, Field; connection string display) — acceptance: opt-in toggle + off-by-default intact, test green.
- [ ] (D) DatabaseTuning → shadcn (Select for workload profile, Field, Table/Card of computed values) — acceptance: tuning preview intact, test green.

## E · Cleanup & consistency
- [ ] (E) Delete the hand-rolled `styles.css` design tokens (keep only minimal unavoidable globals); ensure nothing references removed vars — acceptance: build green, app visually consistent.
- [ ] (E) Consistency sweep: every empty state uses `Empty`, every loader uses `Skeleton`, every status uses `Badge`, every callout uses `Alert`; no orphaned components remain — acceptance: grep finds no hand-rolled equivalents; then write COMPLETE.md.
- [ ] (E) Token: `--info-soft` aliases the same value as `--primary-soft` (#e7efff / dark #1e2a44), so `info` badges are indistinguishable from primary-soft surfaces (e.g. Migrate step-active bg). Pre-existing from the legacy tokens (preserved as-is during the Badge migration for parity). Give `info`/`info-soft` a perceptually distinct blue in both light+dark `:root` once views are migrated — acceptance: info badge reads distinct from primary-soft surfaces.
