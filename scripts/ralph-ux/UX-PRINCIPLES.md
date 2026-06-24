# UX principles — what "good UX" means here

The audience is **indie hackers**: solo or tiny-team builders running their own
Postgres. They are technical enough to use a database, but they are busy, often
context-switching, and they did NOT buy this panel to learn it. They want to do the
obvious thing fast, trust that it worked, and get back to building.

## The bar for shipping any change

A change ships only if it makes a real task **easier, faster, clearer, or safer**
for that user. "Looks nicer" is not enough. "More powerful" is not enough if it adds
clutter. If you can't name the specific task it improves and the user it helps, don't
ship it.

## Principles (in priority order)

1. **Clarity over cleverness.** The user should always know where they are, what
   just happened, and what to do next. Plain labels beat jargon. Inline help beats a
   docs link.
2. **Co-locate what's used together.** Config and the action it configures belong
   near each other. Don't make someone bounce between routes to finish one job.
   (The canonical fix: backup config ↔ backup operations.)
3. **Safe by default, scary actions gated.** Destructive actions (restore, drop,
   overwrite) need a clear confirm and an "are you sure" that matches the blast
   radius. Everything else should be one obvious click.
4. **First-run should never be a blank wall.** Empty states tell the user what this
   screen is for and the single next step to take. Sensible defaults over empty
   forms.
5. **Show state honestly.** Loading, empty, error, stale, success — each has a clear
   visual. Never a spinner with no end, never a silent failure.
6. **Consistency is a feature.** The same concept looks and behaves the same on
   every screen. A user who learns one view should already understand the next.
7. **Minimal surface area.** Fewer, well-chosen controls beat many. Every control
   earns its place. Progressive disclosure (advanced behind a toggle) over walls of
   options.

## Anti-over-design — the things this loop must NOT do

This is an open-ended loop with freedom to change anything, which is exactly how UIs
get over-built. Guard against it:

- **No net-new features** unless the audit proves a clear, specific user need. UX
  only — reshape what exists; don't grow scope.
- **No decoration for its own sake.** No gratuitous animation, illustration,
  gradients, or "delight" that doesn't aid a task.
- **No premature configurability.** Don't add settings/toggles/themes for
  hypothetical needs. A good default beats an option.
- **No redesign churn.** Don't re-relayout a screen that already works well just to
  make it different. Stable and good > novel.
- **Don't fight the platform.** Use shadcn defaults; don't restyle components.
- **"Make no change" is a valid, frequent outcome.** If the best available item is
  low-value, reject it and let the loop converge.

When two designs are equally good, ship the **simpler** one.
