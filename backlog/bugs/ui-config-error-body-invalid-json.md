# Config PUT/DELETE error bodies are hand-concatenated JSON

**Where:** `dashboard_config.go`, `dashboardConfigHandler` PUT and DELETE branches: `http.Error(w, '{"error":"'+err.Error()+'"}', …)`.

**Trigger:** Persist fails with an error message containing `"` or a control character (e.g. an operator-configured `dashboard_config` path containing a quote, or OS-specific error text).

**Impact:** The `err.Error()` string is interpolated into a JSON string literal without escaping, producing an invalid JSON body — clients calling `r.json()` on the failure response throw.

**Fix:** Build the error body with `json.Marshal` (or a small struct + `writeJSON`-style encoder) instead of string concatenation.
