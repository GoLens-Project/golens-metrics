# Runtime Health drag-reorder never reorders cards

**Where:** `ui.go`, `runtimeHealth().drop` (dashboard template JS).

**Trigger:** Drag any Runtime Health chart (Memory Overview, CPU, Goroutines) onto another and drop.

**Impact:** The drop handler queried `:scope > div, :scope > div > div`, which matched the section's inner grid wrapper, card headers, and legend divs — a 9-node list that never aligned with the wired `drop(0..2)` indexes. Symptoms ranged from `HierarchyRequestError` (inserting a card into its own subtree) to silently extracting header/legend divs out of cards. Reordering never worked.

**Fix:** Cards are tagged with an `rt-card` class; the handler selects `.rt-card` elements only and moves the dragged card with `target.after(dragged)` / `target.before(dragged)`, which are valid across the two different parent containers (memory card is a direct child of `#runtime-charts`; CPU/goroutine cards sit in the inner grid).
