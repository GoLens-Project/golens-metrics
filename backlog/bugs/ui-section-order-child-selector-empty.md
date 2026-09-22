# Section reorder/filter queries select zero nodes (`:scope >` vs nested cards)

**Where:** `ui.go` dashboard template JS — `metricNames()`, `applyFilter()`, `sortCharts()`, `hooksSection().drop`/`setupDnD`, `applyHookOrder()`.

**Trigger:** Use the header search box, drag-reorder All Metrics cards or hook cards, or reload with a persisted section order.

**Impact:** The All Metrics and Hooks cards are **grandchildren** of their section containers (`#charts`/`#hooks` render an inner `<div class="grid …">` wrapper that holds the cards), but the queries used child selectors (`#charts > [data-metric]`, `:scope > [data-metric]`). They therefore always matched zero nodes, so:

- the search filter hid nothing (`applyFilter` iterated an empty list),
- custom ordering was never applied and `saveOrder`/hooks-order persistence saved empty arrays,
- `sortCharts`'s `grid.appendChild` would (once cards were found) have moved cards out of the grid wrapper into the section container, breaking the layout — reordering now re-appends to the cards' real parent.

Found while wiring §2 persistence, which routes through these functions. Same bug family as `ui-runtime-drag-reorder-wrong-index.md`.

**Fix:** Descendant selectors (`#charts [data-metric]`, `grid.querySelectorAll('[data-metric]')`) — no `[data-metric]` element nests inside another, so the queries are unambiguous — and reorder re-appends to `cards[0].parentNode`.
