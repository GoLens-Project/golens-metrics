# Histogram history bucket arrays are order-nondeterministic (stacked chart misaligned)

**Where:** `aggregator.go:73-81` — `flushAll` converts the per-window `map[float64]int64` bucket counts to `[]HistogramBucket` by ranging over the map, so Go's randomized map iteration emits a different order per flush. Consumed positionally by `ui.go:2252-2272`: `renderHistogramChart` stacks `pt.histogram_buckets[bucketIdx]` against `metric.histogram_bounds`, which is the sorted `m.value.bounds` slice (`registry.go:474-476`) and excludes the +Inf/0-bound overflow entry.

**Trigger:** Any histogram metric with stored history. Every flushed window carries its own random bucket order.

**Evidence:** throwaway probe (two identical sample batches flushed as two windows over bounds `{1, 2.5, 5}`):

```
window 1 buckets (order as stored): 2.5:0 5:0 0:0 1:0
window 2 buckets (order as stored): 1:0 2.5:0 5:0 0:0
```

Position 0 is bound 2.5 in the first window and bound 1 in the second; the overflow (0) entry lands at position 2 vs 3. (Probe was run at HEAD `011aca5` and deleted; repro by calling `agg.add` + `flushAll` twice and printing `AggregatedMetric.HistogramBuckets`.)

**Impact:** The stacked time-series assumes `histogram_buckets[i]` corresponds to `histogram_bounds[i]`. With per-window random order each bucket's count is plotted under a different bound in every window, so the stacked areas are scrambled; the `len(bounds)+1`-entry array (overflow included) additionally pollutes whatever position it lands in. A per-window delta pass added in `Registry.History` (round-2 report §4.3 / P0 item 3) must re-align buckets by `UpperBound` first, or it will diff across mismatched bounds.

**Fix:** Either sort `histogramBuckets` deterministically at flush time (bounds ascending, overflow/0 last — matching what `Metric.histogramBuckets()` already does for snapshots), or align by `UpperBound` in `History`/the client instead of positional index.
