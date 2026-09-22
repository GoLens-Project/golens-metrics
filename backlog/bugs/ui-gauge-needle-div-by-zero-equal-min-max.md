# Gauge needle divides by zero when GaugeMin == GaugeMax (nonzero)

**Where:** `ui.go`, `applyGaugeScale` (logic moved verbatim from the pre-refactor duplicated blocks in `buildCards`/`buildHookCards`).

**Trigger:** Register a gauge with `GaugeMin == GaugeMax != 0` (e.g. both 5). `maxVal != 0` skips the smart defaults, so `maxVal - minVal == 0`.

**Impact:** `NeedlePct = int((Value - minVal) / (maxVal - minVal) * 100)` divides by zero; the float→int conversion of NaN/±Inf is implementation-defined in Go (on amd64 it yields the minimum int, which the `< 0` clamp then pins to 0). No crash, but the needle is silently wrong for a misconfigured scale instead of surfacing the config error. Pre-existing behavior, surfaced verbatim during the dedup refactor.

**Fix:** Guard `maxVal > minVal` before computing the needle (and/or validate GaugeMin/GaugeMax at registration).
