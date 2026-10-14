# UI: HistogramBucket missed the snake_case JSON-tag migration

**Status: fixed in this change (2026-09-22) — `HistogramBucket` now carries `json:"upper_bound"` / `json:"count"` tags and the client JS reads the lowercase keys. `encoding/json` unmarshals case-insensitively, so existing SQLite rows keep parsing.**

## Trigger
The `/metrics/data` snake_case JSON migration added tags to every public snapshot type except `storage.go`'s `HistogramBucket`, so `/metrics/history` emitted `"UpperBound"` / `"Count"`.

## Impact
Not yet fatal — the client JS read `b.Count` and accidentally matched — but it pinned the wire format to Go field names and contradicted the API contract the rest of the branch established (same missed-consumer family as the alerts-page `Name`/`Type` regression, which did break).

## Where
`storage.go` (`HistogramBucket`); `ui.go` (`renderHistogramChart` client reads).
