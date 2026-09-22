# UI: failed dashboard config rename leaves a stale .tmp file

## Trigger
`persistDashboardConfig` (dashboard_config.go:76-89) succeeds at `os.WriteFile(path+".tmp")` but fails at `os.Rename(tmp, path)` (e.g. the target path becomes a directory or crosses a filesystem boundary between the two calls).

## Impact
Cosmetic only: the `.tmp` file is left next to the config file and is never cleaned up. The next successful write overwrites it, so no functional or data impact.

## Where
`dashboard_config.go`, `persistDashboardConfig` — a `defer os.Remove(tmp)` before the rename (or removing `tmp` on the rename error path) would close it.
