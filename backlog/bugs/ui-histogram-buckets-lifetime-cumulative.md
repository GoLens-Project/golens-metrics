# UI: histogram bucket counts are lifetime-cumulative, not per-window deltas

**Status: fixed in this change (2026-09-22) — the aggregator tracks last-seen lifetime counter values per bucket (`aggregator.lastHist`, persists across flushes) and records the delta, treating a negative delta as a counter reset. The reset branch is reachable in-process only (metric evicted and re-registered, counters restarting at 0); `lastHist` entries linger for evicted metrics and grow one entry per distinct name ever seen, accepted at small-map scale.**

## Trigger
`aggregator.add` overwrote `w.histogramBuckets[bound] = counter.Load()` — the metric's lifetime counter at the last sample of the window, not the window's share.

## Impact
Every window showed ~the same monotonically growing totals, so the "bucket distribution evolution" chart degenerated into a static plateau, and the client (which sums counts per bucket, i.e. expects per-window counts) double-cumulated the growth.

## Where
`aggregator.go` (`add`, `lastHist`).
