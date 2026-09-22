# wantsJSON misses composite Accept headers

**Where:** `ui.go`, `wantsJSON`.

**Trigger:** `GET /metrics/data` with `Accept: application/json, */*` (the axios/fetch default in browsers and many HTTP clients).

**Impact:** Only the exact strings `"json"`/`"application/json"` matched, so composite headers silently received the HTML fragment instead of the JSON snapshot.

**Fix:** The Accept header is split on commas, parameters (`;q=…`) stripped, and each media range compared exactly. Bare `*/*` deliberately does not select JSON, so browser navigations keep rendering HTML; `?format=json` behavior is unchanged.
