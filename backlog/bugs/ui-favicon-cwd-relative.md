# Favicon resolves against process CWD

**Where:** `ui.go`, `faviconHandler` (was `http.ServeFile(w, req, "favicon.ico")`).

**Trigger:** Launch the binary from any directory other than the repo root and load the dashboard.

**Impact:** 404 on `/favicon.ico` because the relative path resolved against the process working directory, not the module.

**Fix:** `favicon.ico` is embedded at compile time (`//go:embed favicon.ico`) and served with `http.ServeContent`, so it resolves regardless of CWD (and works in air-gapped deployments once the remaining CDN assets are vendored).
