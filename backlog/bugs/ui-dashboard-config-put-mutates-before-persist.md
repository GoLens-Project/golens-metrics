# UI: /metrics/config PUT mutated in-memory state before persisting

**Status: fixed in this change (2026-09-22) — both the PUT and DELETE branches now persist first under `dashMu` and assign `r.dash` only on success, so a failed persist leaves memory and disk consistent (GET keeps serving the last saved document).**

## Trigger
`PUT /metrics/config` with a document that passes validation while the on-disk persist fails (e.g. `dashboard_config` path is a directory or read-only location, so `persistDashboardConfig` fails on write/rename).

## Impact
The handler used to assign `r.dash = &d` *before* calling `persistDashboardConfig`. On persist failure the client received a 500 "not saved" response, yet a subsequent `GET /metrics/config` returned the rejected document: memory and disk silently diverged, and the failure was invisible to any client that only reads back. A restart then reverted the layout with no trace of why.

## Where
`dashboard_config.go`, `dashboardConfigHandler` — the fix reorders both branches to persist-first/mutate-on-success within the single lock hold.
