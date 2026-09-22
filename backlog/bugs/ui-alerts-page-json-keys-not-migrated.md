# Alerts page metric picker broken by lowercase JSON keys (missed consumer)

**Where:** `alert_html.go:364` (`openAlert` in the alerts page template), consuming `/metrics/data`.

**Trigger:** Open the Alerts page, click "create alert" — the modal fetches `/metrics/data` with `Accept: application/json`.

**Impact:** The diff added lowercase JSON tags to `MetricSnapshot` (`Name`→`name`, `Type`→`type`, …) and migrated the dashboard's own alert modal (`ui.go:1520`), but the standalone alerts page still maps `s.Name`/`s.Type`, which are now `undefined`. `alertMetrics` becomes a list of `{name: undefined, type: undefined}`, and `filteredMetrics` (`alert_html.go:178`) throws on `m.name.toLowerCase()`. The metric picker in the alerts modal is broken.

**Fix:** Change the mapping to `{name:s.name, type:s.type}` — same one-line migration already applied in `ui.go`.
