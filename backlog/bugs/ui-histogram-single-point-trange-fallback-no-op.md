# UI: histogram single-point `tRange` fallback is a no-op — the point still renders invisible

**Status: fixed in this change (2026-09-22) — `renderHistogramChart` now anchors the x domain at `[tMax - durSec, tMax]` when `tMax === tMin`, so the point lands at the right edge of a full-width domain that `xP` and the axis labels share.**

## Trigger
`renderHistogramChart` computes `const tRange = (tMax - tMin) || (durSec || 1)` and every x position via `xP(t) = pad.left + ((t - tMin) / tRange) * cw`. With a single point, `tMax - tMin === 0`, so the fallback selects `durSec` — but the numerator `t - tMin` is also 0 for the only point, so `xP` returns `pad.left` no matter what `tRange` is. The stacked-area path (`moveTo(xP(tMin))`, `lineTo(xP(pt.t))`, `lineTo(xP(tMax))`, `fill`) is a zero-width shape at the left edge and the canvas fill paints nothing.

## Impact
The single-point case renders identically before and after the change: only axes are drawn, the data point is invisible. The code comment ("fall back to the selected window's seconds so it still renders visibly") and the "fixed" status of `backlog/bugs/ui-histogram-history-missing-partial-window.md` for the client part contradict actual behavior. The Go-side part of that fix (`aggregator.partial` feeding `History`) is real and correct — the panel now receives a point; it just still cannot be seen.

## Where
`ui.go` (`renderHistogramChart`, the `durSec`/`tRange` lines). A working fix must spread the x domain — e.g. treat the span as `[tMin - durSec, tMax]` when `tMax === tMin` (adjusting `xP` and the axis labels), or draw an explicit marker for single/degenerate points.
