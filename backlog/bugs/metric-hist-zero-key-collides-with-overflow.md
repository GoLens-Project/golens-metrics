# Metric: histogram bound 0.0 and the overflow bucket share one counter (`hist[0]`)

**Status: out of scope for the 2026-09-22 histogram fix — pre-existing in `metric.go`, degenerate config only. Recorded during review of that change, which inherits the collision in `orderedBuckets`.**

## Trigger
`newMetric` builds `hist` by inserting a counter per bound, then overwrites `hist[0]` with the overflow (+Inf) counter (`metric.go:97-103`, "key 0 reserved as overflow bucket"). If a caller registers a histogram with `0.0` in `bounds`, the bound-0.0 counter is orphaned and bound 0.0 shares the overflow counter. `Metric.Record` then increments that shared counter for both `v <= 0` and overflow samples.

## Impact
- `Metric.histogramBuckets()` emits the shared count twice (once in the bounds loop, once as the trailing overflow entry).
- `Registry.History` → `orderedBuckets` likewise places the count both at the bound-0.0 position and as the trailing overflow bucket, so the UI's cumulative stack double-reports it.
- Unreachable with `DefaultHistogramBounds` (no 0.0); requires a user-supplied bounds slice containing 0.0.

## Where
`metric.go` (`newMetric`, `Record`, `histogramBuckets`); inherited by `registry.go` (`orderedBuckets`). Fix would reserve a sentinel that cannot collide with real bounds (e.g. +Inf or a separate field for the overflow counter) — same choice must be applied to storage/`orderedBuckets` keying.
