# GoLens

[![CI](https://github.com/GoLens-Project/golens/actions/workflows/ci.yml/badge.svg)](https://github.com/GoLens-Project/golens/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/GoLens-Project/golens.svg)](https://pkg.go.dev/github.com/GoLens-Project/golens)
[![Coverage](https://img.shields.io/badge/coverage-94%25-brightgreen)](https://github.com/GoLens-Project/golens/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.22+-007d9c)](https://go.dev/)

## 1. Overview

**GoLens** is a lightweight, declarative observability middleware for Go applications. It provides a "Swagger-like" developer experience by enabling automatic metric collection through middleware, while decoupling the **Collection (Middleware)**, **Registry (Source of Truth)**, and **Exposition (UI/OTEL)** layers.

GoLens is designed to be "zero-config" for small services while remaining production-ready for high-throughput environments via standard OpenTelemetry (OTLP) integration.

---

## 2. Architectural Design: The 3-Layered Approach

### Layer 1: The Collector (Middleware)

The Collector acts as the entry point. It intercepts incoming HTTP requests, extracts performance data (RED metrics: Rate, Errors, Duration), and forwards them to the Registry.

* **Auto-Instrumentation:** Automatically records standard request metrics without developer intervention via RED middleware.
* **Custom Hooks:** Provides both fluent API and middleware chain for domain-specific business metrics (e.g., `inventory_count`, `user_signup_event`).
* **Flexible Registration:** Manual metric registration supported alongside automatic collection.

### Layer 2: The Registry (Source of Truth)

The Registry is a thread-safe container that maintains the current state of the application's telemetry using non-blocking concurrency patterns.

* **Concurrency Model:** Uses bounded channels with `sync.RWMutex` for safe concurrent access without blocking request lifecycle.
* **Context Management:** Context stored as Registry field for graceful shutdown.
* **Storage Strategy:**
* **Hot Ingestion:** RAM-based with non-blocking writes via bounded channel (drops if full).
* **Memory Pool:** Fixed-size with LRU eviction and TTL-based expiration.
* **Optional Persistence:** Background aggregation of raw metrics into summarized SQLite buckets for historical analysis.
* **Roll-up Windows:** 1-minute, 5-minute, 1-hour, 1-day aggregated views.

### Layer 3: The Exposition Layer (Presentation & Export)

This layer handles how the world sees your telemetry. It is designed to be "Swagger-like," where the documentation *is* the interface.

* **HTMX UI:** An embedded, server-side rendered dashboard (mounted at `/metrics`) with:
  - Pre-configured charts with add chart button + metric search
  - Chart type defaults per metric type (Counter→Line, Gauge→Sparkline, Histogram→Bar)
  - Customizable chart name, time interval, and alert thresholds
  - Non-aggressive HTMX polling (configurable, default 5s)
* **OTLP Exporter:** HTTP-based export (gRPC deferred to separate sub-package) complying with OpenTelemetry standard for integration with Prometheus, Grafana Mimir, or VictoriaMetrics.

---

## 3. Core Features

* **"Swagger-Inspired" Discovery:** View all registered metrics and their definitions via a clean, auto-generated browser interface with pre-configured charts.
* **Flexible Configuration:** Struct-based config with YAML support. Blank fields use sensible defaults.
* **Dual Hook API:** Both fluent registration and middleware chain patterns for metric collection.
* **Zero-Blocking:** Non-blocking metric ingestion via bounded channels ensures request lifecycle is never impacted.
* **High-Cardinality Safety:** Path normalization, label truncation, and a configurable cardinality guard prevent unbounded series growth.
* **Context-Based Lifecycle:** Graceful shutdown with context stored in Registry struct.
* **Histogram Time-Series:** Visualize histogram bucket distribution evolution over time with stacked area charts, enabling analysis of distribution trends and patterns.

---

## 4. Implementation Guidelines

### Developer Workflow

1. **Initialize:** Create the `GoLens` registry with optional persistence settings (struct-based or YAML).
2. **Mount:** Add the `GoLensMiddleware` to your router (compatible with all Go routers).
3. **Configure:** Set include/exclude path patterns, polling interval, OTLP endpoint.
4. **Consume:**
  * Access `/metrics` for the interactive HTMX dashboard.
  * Configure OTEL backend (Prometheus/Grafana) to scrape OTLP HTTP endpoints.



### Persistence Strategy

* **In-Memory:** Primary storage with fixed-size pool, LRU eviction, and TTL expiration.
* **SQLite (Optional):** Used exclusively for **summarized data (roll-ups)** across multiple time windows (1m, 5m, 1h, 1d) without impacting runtime performance.
* **OTLP:** The primary mechanism for **Production-Grade long-term storage**, shifting the burden of retention and query-heavy processing to purpose-built TSDBs.
* **Migration:** No migration needed—toggle between in-memory and SQLite via config update.

---

## 5. Summary of Choices

| Component | Responsibility | Recommended Implementation |
| --- | --- | --- |
| **Ingestion** | Collect metrics | `sync.RWMutex` + bounded channels (non-blocking, drops when full) |
| **Hooks** | Custom metrics | Fluent API + middleware chain (both supported) |
| **Storage** | Durability | In-memory (LRU+TTL) + SQLite roll-ups (optional) + OTLP (production) |
| **UI** | Visualization | HTMX + Alpine.js + `html/template` with pre-configured charts |
| **Protocol** | Standard | OpenTelemetry (OTLP HTTP, gRPC deferred) |
| **Config** | Settings | Struct-based + YAML, defaults for blank fields |
| **Lifecycle** | Shutdown | Context stored in Registry struct |

---

## 6. Instructions

### Install

```bash
go get github.com/GoLens-Project/golens
```

GoLens depends only on two pure-Go libraries at runtime (`gopkg.in/yaml.v3` for config and `modernc.org/sqlite` for optional persistence — no CGo).

### Minimal usage (zero-config)

```go
package main

import (
    "context"
    "net/http"

    golens "golens"
)

func main() {
    registry, _ := golens.New(golens.DefaultConfig())
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    registry.Start(ctx)
    defer registry.Close()

    mux := http.NewServeMux()
    registry.MountUI(mux) // dashboard at /metrics
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })

    http.ListenAndServe(":8080", registry.Middleware(mux))
}
```

That's it — RED metrics (`http_requests_total`, `http_request_errors_total`, `http_request_duration_seconds`) are collected automatically, and the dashboard is live at `http://localhost:8080/metrics`.

### Configuration

Every field is optional; blank fields fall back to sensible defaults. Configure either in code or via YAML.

**In code:**

```go
cfg := golens.DefaultConfig()
cfg.OTLP.Enabled = true
cfg.OTLP.Endpoint = "http://otel-collector:4318/v1/metrics"
cfg.Storage.Backend = "sqlite"
cfg.Storage.Path = "./golens.db"
cfg.UI.PollInterval = 3 * time.Second
cfg.ExcludePatterns = []string{"^/health$"}
registry, _ := golens.New(cfg)
```

**Via YAML:**

```go
cfg, _ := golens.LoadConfig("golens.yaml")
registry, _ := golens.New(cfg)
```

```yaml
# golens.yaml
storage:
  backend: sqlite        # "memory" (default) or "sqlite"
  path: ./golens.db
  ttl: 24h

otlp:
  enabled: true
  endpoint: http://otel-collector:4318/v1/metrics
  protocol: http         # gRPC deferred
  batch_size: 100
  interval: 10s
  timeout: 5s

ui:
  enabled: true
  poll_interval: 5s

ingest_queue_size: 4096  # saturation drops samples (never blocks requests)
max_metrics: 10000       # in-memory series cap (LRU eviction)
max_endpoints: 128       # per-endpoint latency cap
metric_ttl: 1h
flush_interval: 30s

include_patterns: []     # empty = track everything
exclude_patterns:
  - ^/health$
  - ^/metrics

labels:
  normalize_paths: true  # collapse /users/123 → /users/:id (default: true)
  max_length: 64         # truncate long label values (0 = no limit)

max_label_series_per_metric: 256  # cardinality guard per metric (0 = unlimited)

runtime_metrics:
  enabled: true          # collect Go runtime stats (memory, goroutines)
  interval: 15s

debug: false             # set true / GOLENS_DEBUG=true for verbose logs
```

### Custom metrics

Both styles are supported and feed the same Registry.

**Fluent hook API** — extract values from the request:

```go
signupHook := registry.On("user_signup_event").
    Type(golens.CounterType).
    Description("user signups").
    Labels("plan").
    Extract(func(r *http.Request) (float64, []golens.Label) {
        return 1, []golens.Label{{Name: "plan", Value: r.URL.Query().Get("plan")}}
    })

mux.Handle("/signup", signupHook(yourSignupHandler))
```

**Middleware chain** — apply only the RED pieces you want:

```go
router.Use(registry.RequestCountMiddleware)
router.Use(registry.LatencyMiddleware)
router.Use(registry.ErrorRateMiddleware)
```

**Manual registration** — record values anywhere in your code:

```go
orders := registry.Register("orders_created", golens.CounterType, "orders", []string{"sku"}, nil)
orders.Record(1)
// or via the hot path (non-blocking):
registry.Record("orders_created", 1, golens.Label{Name: "sku", Value: "ABC"})
```

### Router integration

The middleware is a standard `func(http.Handler) http.Handler`, so it works with any router (gorilla/mux, chi, stdlib) and with Gin by wrapping the engine:

```go
// chi
r.Use(golensRegistry.Middleware)

// gorilla/mux
router.Use(golensRegistry.Middleware)

// stdlib
http.Handle("/", golensRegistry.Middleware(handler))

// gin — wrap the whole engine; it satisfies http.Handler
srv := &http.Server{Handler: golensRegistry.Middleware(engine)}
```

See `examples/gin` and `examples/stdlibmux` for complete, runnable servers. For
routers that need per-route mounting (e.g. Gin), `MetricsHTTPHandler()` and
`MetricsDataHTTPHandler()` expose the dashboard handlers individually.

### Dashboard

Open `/metrics`. The dashboard is server-side rendered with HTMX polling (default 5s) and Alpine.js for the search/add-chart/pause UX:

- **Pre-configured charts** render automatically per metric type (Counter→line, Gauge→sparkline, Histogram→bar).
- **Histogram Time-Series** section displays histogram bucket distribution evolution with stacked area charts, allowing analysis of how distributions change over time.
- **Search** filters metrics by name.
- **+ Add Chart** opens a modal with metric autocomplete.
- **Pause** halts polling.
- **GoLens Internals** section shows per-metric cardinality stats (series counts, cap, dropped samples).
- Programmatic JSON is available at `/metrics/data` (`Accept: application/json` or `?format=json`); a single metric at `/metrics/data?metric=<name>`.

The JSON schema is the stable contract for external consumers. Each metric object carries `name`, `type`, `chart`, and `value`, plus — depending on type — `count`, `sum`, `avg`, and `buckets` (histograms), `gauge_min`/`gauge_max` (gauges), `description`, `labels` (declared label names) with `label_values` as `[{name, value}]` pairs, and `updated_at` (unix nanoseconds). Optional fields are omitted when empty. Histogram buckets are `[{upper_bound, count, overflow}]`; the catch-all bucket above the last bound is the one with `overflow: true`.

### Production export (OTLP)

Point any OTLP/HTTP receiver (OTel Collector, Grafana Alloy, etc.) at GoLens. GoLens **pushes** batches on a configurable interval (default 10s) and on graceful shutdown:

```yaml
otlp:
  enabled: true
  endpoint: http://otel-collector:4318/v1/metrics
```

### Go runtime metrics

Optionally collect Go runtime stats as gauge metrics alongside your HTTP metrics:

```yaml
runtime_metrics:
  enabled: true
  interval: 15s
```

Or in code:

```go
cfg := golens.DefaultConfig()
cfg.RuntimeMetrics.Enabled = true
```

Collected metrics:

| Metric | Type | Description |
|---|---|---|
| `go_memstats_alloc_bytes` | Gauge | Currently allocated heap bytes |
| `go_memstats_sys_bytes` | Gauge | Bytes obtained from the OS |
| `go_memstats_heap_inuse_bytes` | Gauge | Bytes in active heap spans |
| `go_memstats_heap_objects` | Gauge | Total number of allocated objects |
| `go_goroutines` | Gauge | Current number of goroutines |

### Cardinality management

GoLens protects against unbounded label cardinality at three levels:

**1. Path normalization** — the `path` label on RED metrics automatically collapses dynamic segments (`/users/123` → `/users/:id`, UUIDs, hex hashes). Controlled by `labels.normalize_paths` (default: `true`).

**2. Label value truncation** — long label values are capped at `labels.max_length` characters (default: `0` = unlimited).

**3. Cardinality guard** — `max_label_series_per_metric` (default: `256`) caps unique label combinations per metric name. When exceeded, new combinations are silently dropped. Set to `0` to disable.

The **GoLens Internals** section on the dashboard shows per-metric series counts, the configured cap, and how many samples were dropped. This gives you visibility into cardinality growth without requiring external tooling.

```yaml
labels:
  normalize_paths: true   # collapse /users/123 → /users/:id
  max_length: 64          # truncate long values (0 = no limit)

max_label_series_per_metric: 256  # 0 = unlimited
```

Or in code:

```go
cfg := golens.DefaultConfig()
cfg.Labels.NormalizePaths = true
cfg.Labels.MaxLength = 64
cfg.MaxLabelSeriesPerMetric = 256
```

### Graceful shutdown

GoLens stores the context on the Registry. Cancelling it triggers a final flush of roll-ups to storage and a final OTLP batch, then stops all goroutines:

```go
ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer cancel()
registry.Start(ctx)
// ... serve ...
<-ctx.Done()
registry.Close() // final flush, never hangs
```

### Running the examples

Two example servers live under `examples/` — a stdlib `net/http` mux and a
Gin server. Both expose the dashboard at `/metrics` on `:8080`.

```bash
make run-stdlib     # builds and runs examples/stdlibmux on :8080
# or
make run-gin        # builds and runs examples/gin on :8080

# then visit http://localhost:8080/metrics
curl http://localhost:8080/?fail=1     # generate an error
curl http://localhost:8080/order?sku=ABC
curl http://localhost:8080/tenant?tenant=acme  # cardinality-bounded label demo
```

### Development

#### Build

```bash
make build              # compile all packages (binaries go to /dev/null)
make example            # compile example servers to bin/
```

#### Test

```bash
make test               # run unit tests
make test-race          # run tests with the race detector
make cover              # coverage summary (library target: 95%)
make cover-ci           # coverage matching CI (race + atomic mode)
```

Run a specific test or set of tests:

```bash
go test -run TestNormalizePath ./...                    # single test
go test -run "TestCardinality|TestSanitize" ./...       # pattern match
go test -run TestRegistry -v ./...                      # verbose output
go test -race -count=3 ./...                            # repeat to catch flakes
```

#### Lint & format

```bash
make vet                # go vet
make lint               # golangci-lint (if installed)
make fmt                # gofmt + goimports
make tidy               # go mod tidy
make tidy-check         # verify go.mod/go.sum are clean (CI gate)
```

#### Full CI pipeline locally

```bash
make ci                 # build → vet → tidy-check → lint → race tests
```

#### Run examples

```bash
make run-stdlib         # examples/stdlibmux on :8080
make run-gin            # examples/gin on :8080
make run-basic-authed-gin    # examples/basic-authed-gin on :8080
make run-token-authed-gin    # examples/token-authed-gin on :8080
make clean              # remove build artifacts
```

CI runs the build/vet/race-test/coverage matrix across Go 1.22–1.24 on every push and pull request (`.github/workflows/ci.yml`).

## 8. Attribution

### Favicon
The GoLens dashboard favicon is sourced from [Flaticon](https://www.flaticon.com/free-icon/statistics_2920323) and is used under their [Free License](https://www.flaticon.com/license). Original icon by [Freepik](https://www.flaticon.com/authors/freepik).