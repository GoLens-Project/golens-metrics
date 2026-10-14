# GoLens Review Report — Round 2

Date: 2026-10-14 · Scope: HEAD `011aca5` ("daedalus: issue overall-improvements") · Docs-only investigation; no code changed.

Round 1's report lives on a separate branch; this report is self-contained and covers the current state after the overall-improvements work merged (server-side dashboard-config persistence, batch history endpoint, shared card defaults, hidden-tab poll pause, snake_case JSON are all present and working). Where a finding duplicates an existing `backlog/bugs/` record, the backlog file is named.

---

## 1. What GoLens is

A single-module Go library that embeds observability into a service: RED metrics via HTTP middleware, counters/gauges/histograms in a fixed-point in-memory registry (optional SQLite roll-ups), OTLP/HTTP push export, threshold alerting with cooldown + pluggable email notifier, and an embedded HTMX/Alpine dashboard at `/metrics` with server-side layout persistence (`/metrics/config`). Its differentiator is the "Swagger-like" zero-config developer experience: one import, one middleware, a live dashboard with no external infrastructure.

**The single most consequential design decision** (metric.go:68-78, registry.go:341): there is exactly **one value series per metric name**. Labels are display-only — `setLabels` keeps the last-seen label set for rendering. Every HTTP status code, method, and path collapses into one counter. This bounds memory and cardinality by construction, but it is the root cause of several findings below (§4.1) and the widest gap versus every compared tool (§3).

## 2. Feature inventory (as implemented)

| Area | Features |
|---|---|
| Collection | RED middleware (`middleware.go`), fluent hook builder + 3 granular middlewares (`hooks.go`), manual `Register`/`Record`, Go runtime gauges + CPU% (`runtime_metrics.go`), per-endpoint p50/p95/p99 tracker (`endpoint_latency.go`) |
| Cardinality | path normalization (`labels.go:35`), label truncation, per-metric series cap with drop counter, `/metrics/cardinality` introspection endpoint + dashboard section |
| Storage | in-memory tumbling-window roll-ups with TTL + 100k ring (`storage_memory.go`), optional SQLite (`storage_sqlite.go`), 7 fixed history durations (5m–1w), batch history `?names=` |
| Export | OTLP/HTTP JSON, cumulative counters, sparse histograms (`otlp.go`) |
| Alerting | threshold rules (gt/gte/lt/lte/eq), cooldown, integrated/standalone modes, rules CRUD API, firing log, email templates, pluggable `EmailNotifier`, `/metrics/alerts` page |
| Dashboard | server-rendered cards, add-chart modal with search, drag-reorder per section, pinned "My Charts", histogram stacked time-series, runtime health charts, endpoint latency, cardinality panel, dark/light theme, pause, poll cooldown ring, HTTP basic auth, server-persisted layout (`/metrics/config`) with localStorage fallback + migration |

## 3. Comparison with similar tools

Landscape verified via web research (2025-2026 state). Closest peers: **Netdata** (embedded auto-discovering dashboard + local alarms), **Grafana** (the dashboard/alerting reference), **Prometheus's own UI / VictoriaMetrics vmui** (TSDB-embedded explore UIs), **Uptime Kuma** (self-hosted alerting UX), and for Go libraries specifically **prometheus/client_golang** (the canonical collector GoLens partially duplicates).

| Capability | GoLens | Netdata | Grafana | vmui/Prom UI | Uptime Kuma |
|---|---|---|---|---|---|
| Embedded, zero-dep dashboard | ✅ | ✅ | ❌ (separate) | ✅ | ✅ |
| Auto-instrumentation on import | ✅ (RED) | ✅ | ❌ | ❌ | ❌ |
| Per-label series / group-by | ❌ | ✅ | ✅ | ✅ | n/a |
| Chart hover tooltip / crosshair | ❌ (CSS bars only) | ✅ | ✅ | ✅ | ✅ |
| Drag-n-drop dashboard layout | ✅ (order only) | ✅ | ✅ (+resize) | ❌ | n/a |
| Panel resize / size presets | ❌ | ✅ | ✅ (12-col grid) | ❌ | n/a |
| Global time-range picker | ❌ (per-section, partial) | ✅ | ✅ (shareable URL) | ✅ | ✅ |
| Templating variables / repeating panels | ❌ | ❌ | ✅ | ❌ | n/a |
| Annotations (deploys/incidents) | ❌ | ✅ | ✅ | ❌ | ❌ |
| CSV/JSON export from UI | ❌ | ✅ (CSV/PNG/PDF) | ✅ (panel inspect) | ✅ | ✅ |
| Alerting: rate/window functions | ❌ (lifetime totals/avgs) | ✅ | ✅ | n/a | ✅ (per-check) |
| Alerting: `for` duration, severity, recovery notices, silences | ❌ | ✅ | ✅ | n/a | partial |
| Notification channels | email interface (DI) | dozens built-in | dozens | n/a | 90+ built-in |
| Anomaly detection | ❌ | ✅ (ML, local) | ✅ (ML in cloud) | ❌ | ❌ |
| Cardinality guard | ✅ (preventive) | ❌ | ❌ | vmui: explorer (retrospective) | n/a |
| Status of series model | single series/name | multi-series | multi-series | multi-series | n/a |

**Missing features that matter most for the stated "Grafana-like" ambition**, in rough order of user-visible impact:

1. **Per-series breakdown** — no group-by label, no per-status/per-endpoint series. Every competitor models this. (Design choice, but it silently breaks the status-code card, §4.1.)
2. **Chart hover/tooltip on canvas charts** — universal expectation; CSS bars have it, line/stacked-area charts don't.
3. **Global time-range control + shareable URLs** — currently two sections have independent duration buttons; search/filter/time-range are not in the URL, so no state can be shared or bookmarked.
4. **Rate/interval functions for alerts** — alerting on lifetime totals (e.g. `http_requests_total > 1000` fires forever after the first 1000 requests) is not comparable to Grafana/Netdata semantics; `rate()`/per-window deltas are the missing primitive (see §4.3).
5. **Panel resizing + per-panel chart-type override** — the server hardcodes chart type per metric kind (metric.go:32-43); the user cannot pick bar vs line vs area per panel.
6. **Alert delivery breadth** — webhook/Slack at minimum; email-only with a user-supplied notifier is below even hobby-grade expectations (Uptime Kuma ships 90+ integrations).
7. **CSV/JSON export buttons, section collapse, and a self-describing OpenAPI spec** — ironic gap: GoLens markets a "Swagger-like" experience but ships no swagger.json/OpenAPI document for its own JSON API.
8. **Annotations and dashboards-as-code** — fine to skip for this scope, but worth listing since Grafana 12 made both mainstream.

Where GoLens is genuinely ahead of peers: the **preventive cardinality guard with drop accounting** (vmui's explorer is diagnostic, not preventive), the **first-class Go middleware ingestion** (no scraper/agent hop), and **single-binary zero-config operation** (Netdata now caps its local dashboard at 5 nodes to push cloud; GoLens stays single-process).

## 4. Findings — correctness bugs (ranked)

### 4.1 The HTTP status-breakdown card cannot show what it claims (high)

`buildHTTPCard` (ui.go:574-672) categorizes the request counter into 2xx/3xx/4xx/5xx columns by scanning `s.LabelValues` — which holds **only the labels of the most recent sample** (metric.go:141-150). The whole counter value lands in whichever bucket the last request happened to fall into; the other columns read zero. Under normal traffic the card shows "all requests are 2xx" almost always, and lurches to "all 5xx" the moment a single error occurs. Since one series per name is a core design decision (§1), per-status accounting cannot be fixed without either per-label series or per-status child metrics; today the card is misleading and its test (`TestBuildHTTPCardStatusCodes`, ui_test.go:484) cements the wrong semantics by constructing snapshots that mirror the flawed derivation. **Recommendation:** either drop the per-status columns and show totals + errors, or emit `http_requests_total_{2xx,3xx,4xx,5xx}` child counters in the middleware (bounded, no cardinality risk).

### 4.2 Gauge needle math contradicts the dial it sits on (high)

Both gauge templates map `NeedlePct` (0-100) directly to degrees: `transform="rotate({{.NeedlePct}} 50 50)"` (ui.go:1050 and ui.go:2413). The needle starts pointing at 12 o'clock and the dial arc spans ±90° (9 o'clock → 3 o'clock, red 0-50% left, green right). Rotating 0-100° sweeps only the right half: a value of 25% (mid red zone) renders pointing up-right into the visually "safe" region, and 100% overshoots the arc by ~10°. The mapping should be `rotate(NeedlePct*1.8 - 90 50 50)`. Related open backlog: `backlog/bugs/ui-gauge-needle-div-by-zero-equal-min-max.md` (min==max ⇒ NaN needle) is still unguarded at `applyGaugeScale` (ui.go:505-513); it happens to clamp on amd64 but only by accident of `int(NaN)` behavior.

### 4.3 Histogram time-series plots lifetime totals, not per-window distribution (high)

`aggregator.add` copies the **cumulative lifetime** bucket counters into every flushed window (aggregator.go:50-54, `counter.Load()`), `Registry.History` returns those rows verbatim (registry.go:459-498, no delta computation), and the client stacks them per window as if they were window-local counts (ui.go:2261-2272). The result is a set of monotonically increasing plateaus that plateau as traffic dies — you cannot see distribution evolution, which is exactly what the section title and README claim (README:60, 269-270). A per-window delta pass (row N − row N−1, handling restart resets) at read time in `History` would fix the semantics without schema changes. One prerequisite: the stored rows' `HistogramBuckets` arrays are emitted in Go's randomized map-iteration order (probe-verified; `backlog/bugs/ui-histogram-history-bucket-order-random.md`), so the delta pass must realign buckets by `UpperBound` against `HistogramBounds` before differencing — a naive row-N − row-N−1 diff would subtract counts across mismatched bounds.

### 4.4 Auth silently disables itself on misconfiguration (high, security-adjacent)

`resolveAuth` (auth.go:31-55) returns an inactive `authState` — with no log line and no error — when (a) `bcrypt.GenerateFromPassword` fails (which happens for passwords > 72 bytes, the bcrypt limit), or (b) username is empty. A user who configures a 100-char password or forgets the username gets an **open dashboard** with zero indication. Same class: invalid `include_patterns`/`exclude_patterns` regexes are silently dropped at registry.go:92-101. Both paths should at minimum `log.Printf` at startup, ideally fail `New()` for the regex case.

### 4.5 Alert handlers still emit hand-concatenated JSON (medium)

`alertRulesHandler` and `alertRuleDeleteHandler` build error bodies by string concatenation: `http.Error(w, `{"error":"`+err.Error()+`"}`, ...)` (ui.go:362, 389). Any error text containing a quote produces invalid JSON, and the client already parses `e.error` (ui.go:1576, alert_html.go:415). `writeJSONError` exists precisely for this (dashboard_config.go:94-98) — the alert handlers just don't use it. (Sibling of the fixed `backlog/bugs/ui-config-error-body-invalid-json.md`.)

### 4.6 "LRU" eviction is FIFO-by-registration and evicts the RED metrics first (medium)

`evictLocked` walks `r.order` for an idle metric, and when **nothing is idle** drops `r.order[0]` (registry.go:220-223). The order slice starts with the pre-registered RED metrics (registry.go:162-165), so under sustained `MaxMetrics` pressure with all metrics active, the eviction victim is `http_requests_total` — the primary card — while newest junk metrics survive. README:35 documents this as "LRU eviction"; it is neither LRU nor TTL-safe in that corner. Also compounds with alerting: an evicted metric silently stops matching rules (`snapMap` miss, alert.go:411-413) — no "no data" alert exists.

### 4.7 Config-seeded alert rules can never be edited (medium)

`SeedFromConfig` inserts rules **only when the ID is absent** from the store (alert_store.go:69-84), and `New` loads stored rules over the in-memory map afterwards (registry.go:145-150). Consequence: with a persistent store (`storage.backend: sqlite`) the rule is frozen after the first run — editing its threshold/condition in `golens.yaml` silently has no effect and the stored copy wins forever with no warning. With the default in-memory backend the store dies with the process, so `SeedFromConfig` finds nothing on the next start and config edits do take effect; the freeze only bites once rules persist. Either upsert-and-report conflicts, or document that config rules are seed-only under sqlite.

### 4.8 Smaller correctness items

- **`Start(nil)` panics**: the nil-ctx guard in `loop` is dead code — `done := r.ctx.Done()` is evaluated *before* `if r.ctx == nil` (registry.go:290-293).
- **Integrated alert ticker never stopped**: `C()` lazily creates a ticker with no `Stop` anywhere (alert.go:363-368).
- **Register name-collision ignores type**: re-registering an existing name returns the old metric even if the declared type differs (registry.go:179-184) — a histogram registration over an auto-created counter silently yields a counter without bounds. Hot-path `Record` on an unregistered name also auto-creates a counter unconditionally (registry.go:334), so the common typo of recording before registering degrades silently.
- **Fixed-point counter overflow**: `int64(v * 1e6)` (metric.go:80, 111) caps counters at ~9.2e12 (≈9.2 TB-scale totals) and truncates sub-microsecond additions to zero. Document or switch to float64/int128-free accumulation.
- **`statusRecorder` drops `Flusher`/`Hijacker`** (middleware.go:10-18): SSE/streaming/websocket handlers behind `Middleware` lose flush/hijack. Embed-and-forward the interfaces.
- **Error classification**: every status ≥ 400 counts as an error including 404 noise (middleware.go:44); no configurable threshold (5xx-only is the common default).
- **Double snapshot per page render**: `metricsPageHandler` calls `r.Snapshots()` twice (ui.go:187-188).
- **Memory-vs-SQLite alert-log limit inconsistency**: `ListLog` defaults to 100 rows in SQLite but "all entries" in memory (alert_store.go:134-137 vs 230-231).
- **Histogram alert value = lifetime average** (alert.go:416-420); see §4.3 — same cumulative-vs-window root cause, so alerts fire on process-lifetime means rather than recent behavior.

## 5. Findings — design & architecture issues

1. **Hot-path mutex contradicts the "Zero-Blocking" claim** (README:57). `endpointTracker.Observe` takes a global mutex, allocates via `normalizePath`, and binary-searches buckets **synchronously in the request path** (endpoint_latency.go:38-56, middleware.go:56). The ingest channel is non-blocking, but this is not. Fix: reuse the async queue, or shard/sync.Map + precomputed normalized routes.
2. **Aggregator serializes all samples** through one mutex with an O(buckets) map copy per histogram sample (aggregator.go:31-56). Fine at small scale; a documented ceiling for high-throughput services.
3. **SQLite grows forever.** `StorageConfig.TTL` is applied only by the memory backend (storage_memory.go:26-31); the SQLite roll-up table and `alert_log` have no retention/pruning at all (storage_sqlite.go, alert_store.go:42-53). Long-running deployments fill disk.
4. **In-memory history depth is unconfigurable and shallow at scale**: the 100k-bucket ring is hardcoded (storage_memory.go:20); at `MaxMetrics=10_000` and 30s flush that is ~10 windows per metric (~5 minutes) once full. The TTL/cap interaction is undocumented.
5. **OTLP export quality gaps** (otlp.go): no retry/backoff — one blip silently loses the batch (124-132); `Resource.Attributes` is never populated, so metrics land in Grafana with **no `service.name`** even though `ProjectName` exists (102-110); cumulative points omit `startTimeUnixNano`, weakening reset detection (59-64).
6. **Portability: the package does not compile on Windows.** `syscall.Getrusage` in runtime_metrics.go:87-95 has no `//go:build unix` constraint, and CI runs Linux-only so nothing catches it.
7. **The library module depends on Gin** (go.mod): examples live in the same module, so every `go get golens` pulls gin + its tree (sonic, protobuf, validator). README:108 states the runtime depends on "only two pure-Go libraries" — true at link time for consumers who don't import examples, but false as a dependency statement. Moving `examples/` to a nested module is a one-file change.
8. **The dashboard requires public CDNs** (ui.go:792-798, alert_html.go:12-18): `cdn.tailwindcss.com` (the JIT dev script Tailwind explicitly labels not-for-production), unpkg htmx/alpine, Google Fonts. Air-gapped or locked-down deployments get a dead dashboard; unpkg is also a supply-chain surface. Known leftover of the round-1 plan (§4.1 vendoring); still the largest deployment blocker.
9. **Config trap: user `exclude_patterns` replaces the default `^/metrics`** (config.go:177 sets it only in `DefaultConfig`; `applyDefaults` never re-appends). Setting any exclude list re-instruments the dashboard's own polling, reintroducing self-instrumentation feedback. Merge or document.
10. **No request-body limits** on PUT `/metrics/config` (dashboard_config.go:113) or POST alert rules (ui.go:352); `?names=` batches are unbounded (ui.go:256-264). Basic-auth-protected, but cheap to bound with `http.MaxBytesReader`.
11. **Name-sniffing classification hides user metrics**: `isHTTPMetric` (contains "http"/"request"/"response") and `isHookMetric` (contains "hook"/"webhook") remove matching user metrics from the All Metrics grid and merge them into the HTTP card / hooks section (ui.go:562-571, 518-531). A user counter named `queue_request_latency` or `webhooks_sent` silently vanishes from the main list. At minimum document; better, classify only the canonical RED names or make it configurable.
12. **`mustRegister` neither checks duplicates nor panics** (registry.go:170-176) — the `must` prefix implies a guarantee it doesn't have.

## 6. UI review

### 6.1 Hovering
- CSS bar charts (histogram cards ui.go:2386-2388, endpoint percentile bars 1153-1154) have hover tooltips — good.
- **Every canvas chart has zero hover behavior**: `drawChart` (ui.go:1885-1992) and `renderHistogramChart` (2195-2313) register no mouse handlers — no crosshair, no point tooltip, no legend interaction. This is the single biggest day-to-day UX gap versus Grafana/Netdata.
- Card hover is limited to a border tint. The label-value chips and Internals table have no hover affordances (the table rows do, 1215).

### 6.2 Drag-n-drop
- Works per-section (All Metrics delegated on `#charts`, hooks delegated, runtime via hardcoded indices 0/1/2, pinned + endpoints via Alpine), and orders persist server-side — genuinely good coverage.
- **Whole card is the drag handle** (`draggable="true"`, cursor-grab on the card): text selection inside cards is impossible, and HTML5 DnD is unsupported on touch — no mobile reordering at all. A dedicated handle would fix both.
- **Gap-drop desync**: `dragover` mutates the DOM immediately, but `drop` only persists if released over a card (ui.go:1668-1681); release over a grid gap reverts the visual move on the next poll. Persist in `dragend` instead.
- Four parallel implementations of the same interaction (delegated listeners ×2, Alpine x-data ×2, plus dead `hooksSection.start/drop` methods at ui.go:1731-1749) — consolidation candidate.

### 6.3 UI toggles
- Theme toggle: dark default with pre-paint FOUC guard (ui.go:801-807) — good. The light theme is a hand-rolled `!important` remap of dark utility classes (809-836, alert_html.go:28-51); every new dark shade used anywhere needs a manual override — fragile, and the round-1 "token" plan remains the right end state.
- Pause: halts HTMX and Alpine polling (ui.go:1002, 1126), pauses on hidden tabs (1832-1836), freezes the cooldown ring — well done. Pause state is lost on reload (minor).
- Missing toggles: per-section collapse (only Internals is collapsible), per-card hide, compact density, refresh-interval selector in the UI (config-file only), and a visible "paused" state after reload.

### 6.4 Alerting UX
- Two-step create wizard with metric search and type badges is good; template dropdown exists but only for HTML format.
- **No rule editing** — only create/toggle/delete; changing a threshold means delete + recreate.
- Delete has no confirmation (alert_html.go:490-498).
- Rules never auto-refresh (log does, 10s) and the log is capped at 200 rows with no pagination (ui.go:397-400); `fmtTime` shows time-of-day only, so multi-day-old entries are ambiguous (alert_html.go:531-535).
- No "send test notification" button — the fastest way to validate mailer wiring.
- Server-side alerting gaps that cap the UX regardless of polish: lifetime-window semantics (§4.3/§4.8), no `for`-duration, no recovery/resolved notifications, no severity, no webhook channel, invalid conditions accepted silently (§4.7 backlog-adjacent — `AddRule` validates only name/metric, ui.go:356-359).

### 6.5 Customization
- Add-chart modal with live search; pinned "My Charts" persist to `/metrics/config` with localStorage fallback and first-connect promotion — solid design (dashboard_config.go; client ui.go:1436-1497).
- **Pinning is limited to what `#charts` renders**: runtime, HTTP, and hook metrics are filtered out of the grid (ui.go:518-531), so pinning them yields the "metric unavailable" placeholder (ui.go:1622-1625). Pinned cards also *duplicate* rather than move the card — two copies of the same metric on one page.
- No per-panel overrides: chart type, title, color, threshold line, and unit are all server-decided (`DashboardPanel` is `{id, metric}` only, dashboard_config.go:26-29 — the round-1 decision to defer fields; panel customization is now the feature that needs them back).

### 6.6 Chart types & resizing
- Types in use: big-number (counters), needle gauge, CSS histogram bars, canvas line + min/max band (runtime), canvas stacked area (histograms), CSS grouped bars (endpoints, HTTP card).
- **No per-panel resize** — the Tailwind grid fixes sizes (histograms span 2 cols, HTTP card 3, ui.go:2362); canvases have fixed height attributes; no size presets, no full-screen/expand.
- No chart-type choice per panel, no log scale, no unit override (formatting is name-sniffed, ui.go:724-738), no data labels, no CSV/JSON export button.
- Canvas DPR handling is correct in both chart renderers; the runtime overlay second-axis code duplicates `drawChart` internals twice (ui.go:2066-2100, 2106-2141) and would benefit from one multi-series/multi-axis renderer.

### 6.7 Sections & dividers
- Sections: Runtime Health, Histogram Time-Series, Hooks, My Charts, All Metrics, Endpoint Latency, Internals — fixed order, headers only, no visual dividers or collapse, and **section order is not persisted** (`DashboardOrder` has per-section card order but no section-level list; the "drag to reorder" hints apply inside sections only).
- **Late-appearing sections never render**: the Hooks section exists in the page only if hook metrics existed at page load (ui.go:993 `{{if .HookCards}}`); the first hook registered after load is invisible until a manual reload. Same class of issue for the histogram section's empty state (it at least shows an empty-state card, 987-990).

### 6.8 Not listed but needs attention
- **State is not shareable**: search text, filters, and per-section durations are not encoded in the URL — no permalinks to a filtered/zoomed view.
- **Accessibility**: modals lack focus traps, ARIA roles, and focus restore (ui.go:1242-1405, alert_html.go:175-310); canvas charts are invisible to screen readers (no aria-label/summary); the drag interaction has no keyboard alternative; tables are not sortable.
- **Mobile**: no touch DnD, and the alerts tables overflow horizontally on narrow screens (usable but unpolished).
- **Silent staleness on data-fetch failure**: HTMX/Alpine fetch failures are swallowed (`.catch(() => {})` throughout); the only failure signal is the cooldown ring turning red (1865-1869), which covers HTMX routes only — the Alpine-driven sections (runtime, endpoints, cardinality, histogram) show stale data forever with no indication.
- **The cooldown-ring timer keeps a rAF loop running permanently** (1841-1870) even when paused; harmless but wasteful.
- Toasts are good (dashboard) but the two pages implement them twice with different mechanisms (Alpine store vs DOM element).

## 7. Code smells & quality notes

- **Dead code**: `formatCardinalityValue` (labels.go:189-198, test-only — labels_test.go:238), `sumMetrics` (ui.go:674-680, test-only), `formatFloat` (ui.go:778-779, retained comment), `hooksSection.start/drop` (ui.go:1731-1749), `alertsApp().form` (alert_html.go:446-450), the SQLite branch of `newAlertStore` (alert_store.go:243-254) is production-unreachable — production wiring only calls it when the backend is *not* sqlite — but it is exercised directly by alert_test.go:1265, so dead-in-prod yet not removable, `MiddlewareFunc` is a pure alias (middleware.go:60-64, defensible as API sugar), the bare `"json"` media check in `wantsJSON` (ui.go:309).
- **`newAggregator(storage, interval)` ignores both parameters** (aggregator.go:27-29) — the flush cadence lives in `Registry.loop`; drop the parameters. `flushAll(r *Registry)` couples the aggregator to the whole Registry when it needs storage + debug flag.
- **Five files fail the project's own formatter**: `gofmt -l` flags alert.go, alert_store.go, alert_test.go, config.go, labels_test.go, while `.golangci.yml` enables gofmt/goimports formatters — the lint gate is configured but evidently not run in CI.
- **Inconsistent error surfaces**: some handlers use `writeJSONError`, some hand-concat JSON, some `http.Error` with a plaintext body behind JSON-shaped client parsing (ui.go:353-358 uses `http.Error` with JSON-looking strings — `http.Error` also appends `\n` and sets `text/plain`).
- `History` computes `avg` before the zero-count guard (registry.go:481-484) — harmless but backwards.
- `Query.Limit` is interpolated via `fmt.Sprintf` (storage_sqlite.go:87) — safe (int), but placeholders are the house style elsewhere.
- SQLite indexes don't match the query shape (`idx_metrics_window` on `(window_start, window_end)` vs queries filtering `name AND window_end`) (storage_sqlite.go:34-35, 71-88).
- `TruncateLabelValue` slices at byte boundaries and can split UTF-8 runes (labels.go:60-62).
- Docs drift to fix alongside: README:35 ("LRU eviction"), README:57 ("never impacted" hot path), README:108 ("two pure-Go libraries"), README:60/269-270 ("distribution evolution"), and the note that `alert_store`'s `log_persist` is a no-op with every backend — `LogPersist` is declared and defaulted (config.go:101, 172) but never read anywhere; `AppendLog` writes unconditionally, sqlite included.

## 8. Priority-ordered improvement options

**P0 — correctness (small, high-value)**
1. Gauge needle: `rotate(NeedlePct*1.8 - 90)` + guard `maxVal > minVal` (closes the open backlog div-by-zero).
2. HTTP status card: stop deriving per-status columns from last-seen labels — show totals + errors, or emit bounded per-class child counters in the middleware; fix `TestBuildHTTPCardStatusCodes` accordingly.
3. Histogram time-series: per-window delta pass in `Registry.History` (row-to-row diff with restart reset handling), after realigning each row's buckets to `HistogramBounds` order — stored arrays are map-ordered and misaligned with the bounds slice (`backlog/bugs/ui-histogram-history-bucket-order-random.md`).
4. `resolveAuth` + pattern compilation: log loudly / fail `New()` on silent-downgrade paths.
5. Alert handlers → `writeJSONError`; add condition/metric validation to `AddRule`.
6. `loop` nil-ctx guard ordering; stop the integrated alert ticker; `Start` should document/reject nil.

**P1 — packaging & ops**
7. Vendor htmx/alpine + a real Tailwind build (or plain CSS) — removes the public-CDN dependency entirely.
8. `//go:build unix` on runtime_metrics.go (or a portable `cpuTime`), so Windows/JS compile again.
9. Split `examples/` into its own module; drop gin from the library's go.mod.
10. SQLite retention: prune `metrics`/`alert_log` older than TTL on flush.
11. Run gofmt/goimports in CI (5 files currently fail); run golangci-lint in CI.

**P2 — dashboard UX (the "Grafana-like" core)**
12. Hover crosshair + tooltip on canvas charts; one shared multi-series renderer.
13. Global time-range picker + URL-encoded state (range, search, pause).
14. Panel resize (2-3 size presets stored per panel — this is when `DashboardPanel` grows its deferred fields back) + per-panel chart-type override.
15. Section collapse/reorder + persisted section order (extend `DashboardOrder`).
16. Drag handle per card + persist on `dragend`; render late-arriving sections (poll a section-presence flag instead of `{{if .HookCards}}`).
17. Alert rules: edit-in-place, delete confirmation, log pagination, test-notification button; rate/`for`-window semantics server-side; webhook notifier.

**P3 — differentiators**
18. Opt-in per-label series for named labels (bounded by the existing cardinality guard) — unlocks group-by, real status breakdowns, and multi-dimensional alerts; the single biggest parity gap (§3).
19. OpenAPI spec for `/metrics/*` (the "Swagger-like" story should describe itself).
20. OTLP hardening: retry/backoff, `service.name` from `ProjectName`, `startTimeUnixNano`.
21. CSV export endpoints + annotation webhooks as budget Grafana features.

## 9. Backlog cross-reference

Open backlog files reviewed against HEAD: `ui-gauge-needle-div-by-zero-equal-min-max` (still open, folded into §4.2), `ui-dashboard-config-stale-tmp-on-rename-failure` (still open, cosmetic), `ui-alerts-page-json-keys-not-migrated` (**already fixed at this HEAD** — alert_html.go:364 reads `s.name`/`s.type` correctly; the backlog file can be closed). All other backlog bugs (drag-reorder, wantsJSON, favicon, histogram N+1, gauge defaults, section-order selector, config error body, PUT-before-persist) are verifiably fixed in this tree.
