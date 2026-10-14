package golens

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Registry is the source of truth (Layer 2). It owns the metric store, the
// non-blocking ingestion queue, the background aggregation loop, optional
// persistence, and OTLP export.
//
// Concurrency: writes from the hot path never touch the map under a lock.
// Instead they enqueue onto a bounded channel; a single background goroutine
// drains it and updates the map under a mutex. Reads (snapshots) take a read
// lock. This keeps request latency predictable.
type Registry struct {
	cfg Config
	ctx context.Context

	mu      sync.RWMutex
	metrics map[string]*Metric
	order   []string // insertion order for stable UI listing

	queue chan *sample
	stop  chan struct{}
	done  chan struct{}

	storage  Storage
	exporter *otlpExporter
	agg      *aggregator

	endpoints   *endpointTracker
	cardinality *cardinalityTracker

	alertStore AlertStore
	alerter    *alerter

	auth authState

	// Dashboard layout document (pinned panels + section order) and its
	// optional on-disk persistence path. Guarded by dashMu.
	dashMu   sync.Mutex
	dash     *DashboardConfig
	dashPath string

	include []*regexp.Regexp
	exclude []*regexp.Regexp

	stopOnce sync.Once
	started  atomic.Bool
	debug    bool

	GoroutineBaseline int // Baseline goroutine count when runtime metrics start
}

type sample struct {
	name   string
	value  float64
	labels []Label
}

// New constructs a Registry from config. It does not start background work;
// call Start to begin ingestion/aggregation/export.
func New(cfg Config) (*Registry, error) {
	cfg.applyDefaults()

	// Resolve optional admin auth before building the Registry so the wiped
	// config (no plaintext password) is what gets stored on r.cfg. A supplied
	// credential that cannot be honored is a startup failure, not a silent
	// fallback to an unauthenticated dashboard.
	auth := authState{}
	if cfg.UI.Enabled {
		var err error
		if auth, err = resolveAuth(&cfg.UI.Auth); err != nil {
			return nil, err
		}
	}

	r := &Registry{
		cfg:     cfg,
		metrics: make(map[string]*Metric),
		queue:   make(chan *sample, cfg.IngestQueueSize),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		debug:   cfg.Debug,
		auth:    auth,
	}

	// Compile path filters once. An uncompilable pattern is a startup error,
	// not a silently dropped filter line.
	for _, p := range cfg.IncludePatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("golens: invalid include_patterns entry %q: %w", p, err)
		}
		r.include = append(r.include, re)
	}
	for _, p := range cfg.ExcludePatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("golens: invalid exclude_patterns entry %q: %w", p, err)
		}
		r.exclude = append(r.exclude, re)
	}

	// Persistence backend.
	switch cfg.Storage.Backend {
	case "sqlite":
		s, err := newSQLiteStorage(cfg.Storage.Path)
		if err != nil {
			return nil, err
		}
		r.storage = s
	default:
		r.storage = newMemoryStorage(cfg.Storage.TTL)
	}

	r.agg = newAggregator(r.storage, cfg.FlushInterval)

	r.endpoints = newEndpointTracker(cfg.MaxEndpoints)

	r.cardinality = newCardinalityTracker(cfg.MaxLabelSeriesPerMetric, cfg.Debug)

	// Alert subsystem: store + evaluator.
	if cfg.Alerts.Enabled {
		var alertDB *sql.DB
		// If the main storage is SQLite, reuse its connection for alerts.
		if s, ok := r.storage.(*sqliteStorage); ok {
			r.alertStore = newAlertStoreFromDB(s.db)
			alertDB = s.db
		} else {
			var err error
			r.alertStore, alertDB, err = newAlertStore(cfg.Storage)
			if err != nil {
				return nil, err
			}
		}
		if err := r.alertStore.InitRules(context.Background()); err != nil {
			if alertDB != nil {
				alertDB.Close()
			}
			return nil, fmt.Errorf("golens: init alert store: %w", err)
		}
		if len(cfg.Alerts.Rules) > 0 {
			_ = r.alertStore.SeedFromConfig(context.Background(), cfg.Alerts.Rules)
		}
		r.alerter = newAlerter(r, r.alertStore, cfg.Alerts)
		// Load persisted rules into the alerter.
		if stored, err := r.alertStore.LoadRules(context.Background()); err == nil {
			for _, rule := range stored {
				r.alerter.rules[rule.ID] = &rule
			}
		}
	}

	if cfg.OTLP.Enabled {
		r.exporter = newOTLPExporter(cfg.OTLP)
	}

	// Dashboard layout persistence: load the saved document, falling back to
	// defaults when unset or unreadable.
	r.dashPath = cfg.UI.DashboardConfig
	r.dash = r.loadDashboardConfig()

	// Pre-register the canonical RED metrics so they always exist.
	r.mustRegister("http_requests_total", CounterType, "Total HTTP requests", []string{"method", "path", "status"}, nil)
	r.mustRegister("http_request_errors_total", CounterType, "HTTP errors (status >= 400)", []string{"method", "path"}, nil)
	r.mustRegister("http_request_duration_seconds", HistogramType, "HTTP request latency", []string{"method", "path"}, DefaultHistogramBounds)
	// Per-class request counters for the dashboard status breakdown (fed by
	// the middleware; fixed names, no labels).
	for _, class := range httpStatusClasses {
		r.mustRegister("http_requests_total_"+class, CounterType, "HTTP requests with "+class+" status", nil, nil)
	}

	return r, nil
}

func (r *Registry) mustRegister(name string, t MetricType, desc string, labels []string, bounds []float64) {
	m := newMetric(name, t, desc, labels, bounds, 0, 0)
	r.mu.Lock()
	r.metrics[name] = m
	r.order = append(r.order, name)
	r.mu.Unlock()
}

// Register declares a custom metric. Safe to call before Start.
func (r *Registry) Register(name string, t MetricType, desc string, labels []string, bounds []float64, gaugeMin, gaugeMax float64) *Metric {
	r.mu.RLock()
	if existing, ok := r.metrics[name]; ok {
		r.mu.RUnlock()
		return existing
	}
	r.mu.RUnlock()

	m := newMetric(name, t, desc, labels, bounds, gaugeMin, gaugeMax)
	r.mu.Lock()
	if len(r.metrics) >= r.cfg.MaxMetrics {
		// Evict least-recently-used to bound cardinality.
		r.evictLocked()
	}
	r.metrics[name] = m
	r.order = append(r.order, name)
	r.mu.Unlock()
	return m
}

// evictLocked removes the oldest idle metric. Caller holds the write lock.
func (r *Registry) evictLocked() {
	if len(r.order) == 0 {
		return
	}
	now := time.Now().UnixNano()
	ttl := r.cfg.MetricTTL.Nanoseconds()
	for _, name := range r.order {
		m := r.metrics[name]
		if m == nil {
			continue
		}
		if m.value.last.Load() != 0 && now-m.value.last.Load() > ttl {
			delete(r.metrics, name)
			r.removeFromOrder(name)
			if r.debug {
				log.Printf("[golens] evicted idle metric %s", name)
			}
			return
		}
	}
	// Nothing idle: drop the head.
	name := r.order[0]
	delete(r.metrics, name)
	r.order = r.order[1:]
}

func (r *Registry) removeFromOrder(name string) {
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// Record enqueues a sample. This is the hot path: it never blocks. If the
// ingestion queue is saturated the sample is dropped (and optionally logged).
func (r *Registry) Record(name string, value float64, labels ...Label) {
	select {
	case r.queue <- &sample{name: name, value: value, labels: labels}:
	default:
		if r.debug {
			log.Printf("[golens] ingestion queue full; dropped sample for %s", name)
		}
	}
}

// Start launches background ingestion + aggregation + export. The provided
// context is stored on the Registry and governs graceful shutdown: cancelling
// it triggers a final flush and stops all goroutines. Start is idempotent.
func (r *Registry) Start(ctx context.Context) error {
	r.ctx = ctx
	if r.started.CompareAndSwap(false, true) {
		// Capture baseline goroutine count before starting any background goroutines
		r.GoroutineBaseline = runtime.NumGoroutine()
		go r.loop()
		if r.cfg.RuntimeMetrics.Enabled {
			startRuntimeMetrics(ctx, r, r.cfg.RuntimeMetrics.Interval)
		}
		// Standalone alert mode: run the alerter in its own goroutine.
		if r.alerter != nil && r.cfg.Alerts.Mode == "standalone" {
			go r.alerter.run(ctx)
		}
	}
	return nil
}

// loop is the single background goroutine. It drains the ingestion queue,
// flushes aggregated buckets to storage on a ticker, and pushes OTLP batches.
func (r *Registry) loop() {
	defer close(r.done)

	flush := time.NewTicker(r.cfg.FlushInterval)
	defer flush.Stop()

	var export <-chan time.Time
	if r.exporter != nil {
		t := time.NewTicker(r.cfg.OTLP.Interval)
		export = t.C
		defer t.Stop()
	}

	// Integrated alert mode: ticker channel consumed here in the main loop.
	var alertTick <-chan time.Time
	if r.alerter != nil && r.cfg.Alerts.Mode == "integrated" {
		alertTick = r.alerter.C()
	}

	// Resolve the shutdown channel once; the loop body must not re-evaluate
	// it every iteration (that would allocate a channel per spin).
	done := r.ctx.Done()
	if r.ctx == nil {
		done = nil // never fires; relies on r.stop instead
	}

	for {
		select {
		case <-done:
			r.finalFlush()
			return
		case <-r.stop:
			r.finalFlush()
			return
		case s := <-r.queue:
			r.process(s)
		case <-flush.C:
			r.agg.flushAll(r)
		case <-alertTick:
			r.alerter.evaluate(r.ctx)
		case <-export:
			r.exportBatch()
		}
	}
}

// process applies an ingested sample to the metric store.
func (r *Registry) process(s *sample) {
	// Sanitize labels before processing.
	s.labels = SanitizeLabels(s.labels, r.cfg.Labels)

	// Check cardinality guard — drop if this label combo exceeds the cap.
	if len(s.labels) > 0 && !r.cardinality.Allow(s.name, s.labels) {
		if r.debug {
			log.Printf("[golens] cardinality cap reached for %s; dropped sample", s.name)
		}
		return
	}

	r.mu.Lock()
	m, ok := r.metrics[s.name]
	if !ok {
		if len(r.metrics) >= r.cfg.MaxMetrics {
			r.evictLocked()
		}
		m = newMetric(s.name, CounterType, "", nil, nil, 0, 0)
		r.metrics[s.name] = m
		r.order = append(r.order, s.name)
	}
	r.mu.Unlock()

	m.Record(s.value)
	m.setLabels(s.labels) // last-seen label values for display (lock-free)
	r.agg.add(s.name, s.value, m)

	if r.debug {
		log.Printf("[golens] recorded %s = %f", s.name, s.value)
	}
}

// exportBatch collects current snapshots and pushes them via OTLP/HTTP.
func (r *Registry) exportBatch() {
	if r.exporter == nil {
		return
	}
	snapshots := r.Snapshots()
	if len(snapshots) == 0 {
		return
	}
	if err := r.exporter.export(r.ctx, snapshots); err != nil && r.debug {
		log.Printf("[golens] otlp export failed: %v", err)
	}
}

// finalFlush persists aggregated buckets and pushes a final OTLP batch before
// shutdown. It closes the storage backend exactly once.
func (r *Registry) finalFlush() {
	r.agg.flushAll(r)
	if r.exporter != nil {
		r.exportBatch()
	}
	if r.alertStore != nil {
		_ = r.alertStore.Close()
	}
	_ = r.storage.Close()
}

// Close triggers graceful shutdown and blocks until the loop exits. It is safe
// to call concurrently and from multiple sites (idempotent). If Start was
// never called, it performs a one-shot final flush and returns immediately.
func (r *Registry) Close() error {
	if !r.started.Load() {
		// No loop running; do a synchronous final flush and tear down storage.
		if r.ctx == nil {
			r.ctx = context.Background()
		}
		r.finalFlush()
		return nil
	}
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		// Defensive: never hang the caller if the loop is stuck.
	}
	return nil
}

// shouldTrack reports whether a request path should be instrumented.
func (r *Registry) shouldTrack(path string) bool {
	for _, re := range r.exclude {
		if re.MatchString(path) {
			return false
		}
	}
	if len(r.include) == 0 {
		return true
	}
	for _, re := range r.include {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// SnapshotAll returns current snapshots for the UI/exporter, in registration
// order.
func (r *Registry) Snapshots() []MetricSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MetricSnapshot, 0, len(r.order))
	for _, name := range r.order {
		if m, ok := r.metrics[name]; ok {
			out = append(out, m.Snapshot())
		}
	}
	return out
}

// Snapshot returns a single metric by name.
func (r *Registry) Snapshot(name string) (MetricSnapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.metrics[name]; ok {
		return m.Snapshot(), true
	}
	return MetricSnapshot{}, false
}

// Storage exposes the configured persistence backend for tests/extension.
func (r *Registry) Storage() Storage { return r.storage }

// HistoryPoint is a single point in a metric's time series.
type HistoryPoint struct {
	T                int64             `json:"t"`                 // unix timestamp (seconds)
	V                float64           `json:"v"`                 // average value in this window
	Min              float64           `json:"min"`               // minimum value in this window
	Max              float64           `json:"max"`               // maximum value in this window
	HistogramBuckets []HistogramBucket `json:"histogram_buckets"` // histogram bucket counts (for histogram metrics only)
}

// HistorySeries is a time series for one metric.
type HistorySeries struct {
	Name            string         `json:"name"`
	Points          []HistoryPoint `json:"points"`
	HistogramBounds []float64      `json:"histogram_bounds"` // histogram bucket boundaries (for histogram metrics only)
}

// History queries storage for a metric's roll-up history over the given duration.
func (r *Registry) History(name string, dur time.Duration) HistorySeries {
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	from := time.Now().Add(-dur)
	q := Query{Name: name, From: from, To: time.Now()}
	aggs, err := r.storage.Query(ctx, q)
	if err != nil {
		return HistorySeries{Name: name}
	}
	// Append the aggregator's in-flight window so a freshly started process
	// or sparse traffic still yields a current point instead of "no data".
	if pw, ok := r.agg.partial(name); ok {
		aggs = append(aggs, pw)
	}
	if len(aggs) == 0 {
		return HistorySeries{Name: name}
	}

	// Get histogram bounds if this is a histogram metric
	var histogramBounds []float64
	r.mu.RLock()
	if m, ok := r.metrics[name]; ok && m.Type == HistogramType {
		histogramBounds = m.value.bounds
	}
	r.mu.RUnlock()

	pts := make([]HistoryPoint, 0, len(aggs))
	for _, a := range aggs {
		avg := a.Sum / float64(a.Count)
		if a.Count == 0 {
			avg = 0
		}
		pts = append(pts, HistoryPoint{
			T:                a.WindowEnd.Unix(),
			V:                avg,
			Min:              a.Min,
			Max:              a.Max,
			HistogramBuckets: a.HistogramBuckets,
		})
	}
	// Storage may hold bucket slices in arbitrary order (map-iteration order
	// is scrambled at flush time); normalize each point to the metric's
	// bounds order with the overflow bucket last. This also repairs data
	// already at rest, and matches the client's index-against-bounds layout.
	if len(histogramBounds) > 0 {
		for i := range pts {
			pts[i].HistogramBuckets = orderedBuckets(pts[i].HistogramBuckets, histogramBounds)
		}
	}
	return HistorySeries{
		Name:            name,
		Points:          pts,
		HistogramBounds: histogramBounds,
	}
}

// orderedBuckets rebuilds a stored bucket slice into bounds order, with the
// overflow bucket (UpperBound 0) trailing. Buckets missing from the slice
// count as zero; unknown boundaries are dropped.
func orderedBuckets(buckets []HistogramBucket, bounds []float64) []HistogramBucket {
	byBound := make(map[float64]int64, len(buckets))
	for _, b := range buckets {
		byBound[b.UpperBound] = b.Count
	}
	out := make([]HistogramBucket, 0, len(bounds)+1)
	for _, bound := range bounds {
		out = append(out, HistogramBucket{UpperBound: bound, Count: byBound[bound]})
	}
	if c, ok := byBound[0]; ok {
		out = append(out, HistogramBucket{UpperBound: 0, Count: c})
	}
	return out
}

// EndpointLatency returns per-endpoint latency snapshots (p50/p95/p99) for the
// dashboard chart.
func (r *Registry) EndpointLatency() []EndpointLatencySnapshot {
	if r.endpoints == nil {
		return nil
	}
	return r.endpoints.Snapshots()
}

// CardinalitySnapshots returns per-metric cardinality state for the dashboard
// internals panel. Each entry shows how many unique label series exist for a
// metric, the configured cap, and how many samples were dropped.
func (r *Registry) CardinalitySnapshots() []CardinalitySnapshot {
	series := r.cardinality.AllSeriesCounts()
	dropped := r.cardinality.AllDroppedCounts()
	out := make([]CardinalitySnapshot, 0, len(series))
	for name, count := range series {
		out = append(out, CardinalitySnapshot{
			MetricName: name,
			Series:     count,
			MaxSeries:  r.cfg.MaxLabelSeriesPerMetric,
			Dropped:    dropped[name],
		})
	}
	return out
}

// SetEmailNotifier injects a custom email notifier for alert delivery.
// Call this after New and before Start. When nil (the default), alerts fire
// silently (log + store only).
func (r *Registry) SetEmailNotifier(n EmailNotifier) {
	if r.alerter != nil {
		r.alerter.SetEmailNotifier(n)
	}
}

// Alerter returns the alert evaluator, or nil if alerts are disabled.
func (r *Registry) Alerter() *alerter { return r.alerter }

// AlertRules returns the current alert rules as JSON-serializable data.
func (r *Registry) AlertRules() []AlertRule {
	if r.alerter == nil {
		return nil
	}
	return r.alerter.ListRules()
}

// AlertLog returns the most recent alert firing entries.
func (r *Registry) AlertLog(limit int) []AlertLogEntry {
	if r.alertStore == nil {
		return nil
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := r.alertStore.ListLog(ctx, limit)
	if err != nil {
		return nil
	}
	return entries
}

// AlertTemplates returns the available predefined email templates for alerts.
func (r *Registry) AlertTemplates() []AlertTemplate {
	if r.alerter == nil {
		return nil
	}
	return r.alerter.Templates()
}
