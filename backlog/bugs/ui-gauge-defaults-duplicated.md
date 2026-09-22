# Gauge smart-default logic duplicated (+ dead code inside it)

**Where:** `ui.go` — was duplicated across `buildCards` and `buildHookCards` (~40 identical lines each); now extracted into `applyCardDefaults`/`applyGaugeScale`.

**Trigger:** Any change to gauge scale defaults had to be made twice; the two copies could diverge per name-pattern (`grid_cpu_percent` vs `hook_cpu_percent`).

**Impact:** Maintenance hazard: a fix applied to one builder silently missed the other. Additionally, the `temp` min-default exception (`if minVal == 0 && !strings.Contains(..., "temp")`) was a provable no-op in all cases: with `GaugeMin == 0` both branches left min at 0, and with `GaugeMin != 0` it never fired. Removed without observable change.

**Fix:** One shared `applyCardDefaults` (badge/styling per type) + `applyGaugeScale` (min/max auto-detection and needle math) used by both builders.
