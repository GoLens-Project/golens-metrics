# Aggregator: histogram per-window delta undercounts after re-registration when the new counter reaches the last-seen value

**Status: fixed in this change (2026-09-22) — `lastHist` now carries the metric identity (`lastHist map[string]*histState` with `histState{m *Metric, last map[float64]int64}`); when `add` sees a different `*Metric` pointer for a name (eviction + re-registration always yields a fresh instance), the baseline resets and every bound takes its full current count as the delta. The `delta < 0` clamp remains as defense only.**

## Trigger
`aggregator.add` computes bucket deltas as `cur - last[bound]` and clamps only when the delta is negative (`aggregator.go`, the `delta < 0` branch, "metric evicted and re-registered"). If a histogram metric is evicted and re-registered, its counters restart at 0, but the clamp only produces the correct count when `cur < last` at the first observation. When the new counter is equal to or has grown past the old last-seen value, `cur - last` is a small positive number that no longer equals the number of new samples.

Example: old metric had 1 hit in a bound (`last = 1`); after re-registration the new metric records 5 hits (`cur = 5`); the window records `5 - 1 = 4` instead of 5. With exactly 1 new hit it records 0 instead of 1.

## Impact
Per-window histogram counts are silently undercounted for windows following a re-registration whose new counter value is >= the previous last-seen value. No data loss for the common path (long-lived metrics, monotonic counters); confined to the evict/re-register path the clamp was written for, and only when the old lifetime count is small relative to the new one.

## Where
`aggregator.go` (`add`, `lastHist`/`histState` delta accounting). The fix implements that identity signal via `*Metric` pointer comparison in `histState.m`: `process` re-reads the metrics map each sample and `Register` returns the existing pointer, so a re-registered metric always presents a different pointer, and the retained old `*Metric` has the same lifetime bound as the entry it replaces.
