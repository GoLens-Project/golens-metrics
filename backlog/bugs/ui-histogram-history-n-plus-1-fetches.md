# Histogram time-series N+1 sequential fetches

**Where:** `ui.go`, `histogramTimeSeries().refresh` (dashboard template JS); fixed together with the batch endpoint in `metricsHistoryHandler`.

**Trigger:** Dashboard with N histogram metrics open; every poll interval.

**Impact:** One blocking `await fetch('/metrics/history?name=…')` per histogram metric inside the poll loop — 10 histograms at a 5 s poll meant 10 serial round-trips per interval.

**Fix:** `GET /metrics/history?names=a,b,c&duration=…` returns `{name: HistorySeries}` in a single request; the histogram component fetches the batch endpoint once per refresh and the per-metric await loop is gone.
