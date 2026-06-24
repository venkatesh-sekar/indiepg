# UI rules (shadcn) — the non-negotiables

This loop rebuilds the panel on shadcn/ui. These are the rules every iteration
must follow. They mirror the `shadcn` skill's Critical Rules — invoke that skill
for component docs/examples; this file is the always-on checklist.

## The one rule above all
**Never roll your own component when shadcn has one.** Check the table below and
`web/src/components/ui/` before writing any styled `div`. If shadcn has it, use it.

| Need | Use (shadcn) |
|---|---|
| action / button | `Button` (+ `variant`, `size`) |
| status pill | `Badge` (variants) — not a styled span |
| callout / error | `Alert` — not a custom colored div |
| empty state | `Empty` — not custom markup |
| loading placeholder | `Skeleton` — not `animate-pulse` divs |
| spinner | `Spinner` |
| modal | `Dialog` (needs `DialogTitle`) |
| confirm destructive | `AlertDialog` |
| side panel | `Sheet` |
| toast | `sonner` (`toast()`) |
| form field | `Field` + `FieldGroup` + `FieldLabel` — never `div` + `space-y` |
| inputs | `Input`, `Select`, `Switch`, `Checkbox`, `RadioGroup`, `Textarea` |
| 2–5 options | `ToggleGroup` |
| tabs | `Tabs` (`TabsTrigger` inside `TabsList`) |
| table | `Table` |
| card | full `Card` composition (Header/Title/Description/Content/Footer) |
| nav shell | `Sidebar` |
| separator | `Separator` — not `<hr>` / border div |
| tooltip/info | `Tooltip` / `HoverCard` / `Popover` |

## Styling
- **Semantic tokens only:** `bg-background`, `text-muted-foreground`, `bg-primary`,
  `border`, `bg-destructive`. Never raw colors (`bg-blue-500`, hex).
- **`className` is for layout, not for restyling components.** Don't override a
  component's colors/typography.
- **Spacing: `gap-*` with flex/grid.** Never `space-x-*` / `space-y-*`.
- **Equal width/height: `size-*`** (e.g. `size-10`), not `w-10 h-10`.
- **`truncate`** shorthand, not the long form.
- **No manual `dark:` color overrides** — semantic tokens handle dark mode.
- **No manual `z-index`** on overlays (Dialog/Sheet/Popover manage stacking).
- **`cn()`** for conditional classes.

## Composition
- Items inside their group: `SelectItem`→`SelectGroup`, `DropdownMenuItem`→
  `DropdownMenuGroup`, etc.
- Dialog/Sheet/Drawer always need a Title (use `sr-only` if visually hidden).
- Icons in buttons use `data-icon="inline-start|inline-end"`, no size classes.
- `Avatar` always has `AvatarFallback`.
- Buttons have no `isLoading` prop — compose `Spinner` + `disabled`.

## This project's context
- **Vite SPA, React 18** — not RSC, so no `"use client"` needed; hooks/handlers
  are fine anywhere. Import alias is whatever `components.json` sets (use it; don't
  hardcode `@/`).
- **Package manager: npm** (`web/package-lock.json`). Run shadcn via
  `npx shadcn@latest ...` from inside `web/`.
- **Default look:** base style, neutral base color, clean and minimal — this is an
  admin panel, not a marketing site. The operator can re-theme later via
  `npx shadcn@latest apply <preset>`.

## Process
- Before using a component, `npx shadcn@latest docs <component>` and fetch the URL.
- After `add`ing, READ the generated files and verify composition + imports + icon
  library before using them.
- Keep each view's test in lockstep with its markup — migrate the test in the same
  commit as the view. Tests must stay green.
