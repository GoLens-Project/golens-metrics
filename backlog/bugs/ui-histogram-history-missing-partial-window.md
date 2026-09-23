# UI: Histogram Time-Series panel empty until first flush window closes

**Status: fixed in this change (2026-09-22) — `Registry.History` appends the aggregator's in-flight window via a new `aggregator.partial(name)` accessor, and the client anchors the x domain at `[tMax - durSec, tMax]` when the series has a single timestamp, so that point renders across the full chart width (a plain `tRange` fallback is a no-op; see `ui-histogram-single-point-trange-fallback-no-op.md`).**

## Trigger
Dashboard opened (or traffic arriving sparsely) before the first `flush_interval` window closes. Default `flush_interval` is 30s; `Registry.History` only read flushed windows from storage, so `/metrics/history` returned `"points": null` for the first window of a process's life.

## Impact
The Histogram Time-Series panel is purely history-driven (unlike the snapshot-fed cards), so it rendered nothing at all on a fresh process. With sparse traffic a single stored point drew as a zero-width triangle because `tRange = tMax - tMin || 1` collapses all x positions onto one pixel column.

## Where
`registry.go` (`History`) and `aggregator.go` (`partial`, snapshotting the open window under `a.mu`); `ui.go` (`renderHistogramChart`, single-point `tRange` fallback).
