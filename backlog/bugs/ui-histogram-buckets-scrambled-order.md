# UI: stored histogram bucket slices in random (map-iteration) order

**Status: fixed in this change (2026-09-22) — `Registry.History` now reorders each point's buckets to the metric's bounds order (overflow bucket last) at read time, which also normalizes data already at rest in SQLite.**

## Trigger
`aggregator.flushAll` converted the `map[float64]int64` bucket counts to `[]HistogramBucket` via map iteration, so every stored point's bucket slice was in random order (verified live: `1, 2.5, 0, 0.1, 0.5` against bounds `[0.1, 0.5, 1, 2.5]`).

## Impact
The client stacks buckets by index against `histogram_bounds`, so even when charts drew, the distribution shown was wrong — arbitrary buckets stacked as if they were arbitrary bounds. SQLite rows stored the scrambled order permanently.

## Where
`registry.go` (`History` + `orderedBuckets`); the scramble originated in `aggregator.go` (`flushAll`/`window.buckets`).
