package golens

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MountUI registers the HTMX dashboard routes on the given mux. The dashboard
// is server-side rendered; HTMX polls the data endpoint at the configured
// interval. Passing nil for mux uses http.DefaultServeMux.
//
// If declarative admin auth is configured (UI.Auth), every dashboard route is
// wrapped with HTTP Basic Auth before mounting. With auth disabled the raw
// handlers are mounted unprotected — advanced users can instead skip MountUI
// and wrap DashboardHandler / DataHandler in their own security middleware.
func (r *Registry) MountUI(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	if !r.cfg.UI.Enabled {
		return
	}

	// wrap applies the built-in basic-auth decorator only when configured.
	wrap := func(h http.HandlerFunc) http.Handler {
		if r.auth.active() {
			return r.basicAuth(h)
		}
		return h
	}

	mux.Handle("/metrics", wrap(r.DashboardHandler))
	mux.Handle("/metrics/data", wrap(r.DataHandler))
	mux.Handle("/metrics/data/", wrap(r.DataHandler))
	mux.Handle("/metrics/hooks", wrap(r.HooksHandler))
	mux.Handle("/metrics/endpoints", wrap(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.EndpointLatency())
	}))
	mux.Handle("/metrics/cardinality", wrap(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.CardinalitySnapshots())
	}))
	mux.Handle("/metrics/history", wrap(r.metricsHistoryHandler()))
	mux.Handle("/metrics/config", wrap(r.dashboardConfigHandler()))
	mux.Handle("/metrics/static/", wrap(staticHandler.ServeHTTP))
	mux.Handle("/favicon.ico", wrap(r.faviconHandler))

	// Alert subsystem routes.
	if r.alerter != nil {
		mux.Handle("/metrics/alerts", wrap(r.alertsPageHandler()))
		mux.Handle("/metrics/alerts/status", wrap(r.alertStatusHandler()))
		mux.Handle("/metrics/alerts/rules", wrap(r.alertRulesHandler()))
		mux.Handle("/metrics/alerts/rules/", wrap(r.alertRuleDeleteHandler()))
		mux.Handle("/metrics/alerts/log", wrap(r.alertLogHandler()))
		mux.Handle("/metrics/alerts/templates", wrap(r.alertTemplatesHandler()))
	}
}

// staticAssets holds the vendored dashboard assets (stylesheet, page scripts,
// pinned htmx and Alpine builds) so the dashboard renders with no CDN or
// network dependencies — safe to run air-gapped.
//
//go:embed ui/static
var staticAssets embed.FS

// staticHandler serves the embedded assets under /metrics/static/. Directory
// paths (including the bare prefix) are rejected so the FileServer never
// renders a listing of the asset names.
var staticHandler = func() http.Handler {
	sub, err := fs.Sub(staticAssets, "ui/static")
	if err != nil {
		panic("golens: embedded static assets missing: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	return http.StripPrefix("/metrics/static/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// StripPrefix reduces the bare prefix to an empty path; both it and
		// any trailing-slash path would render a directory listing.
		if req.URL.Path == "" || strings.HasSuffix(req.URL.Path, "/") {
			http.NotFound(w, req)
			return
		}
		files.ServeHTTP(w, req)
	}))
}()

// DashboardHandler is the raw, unprotected handler that renders the main
// dashboard HTML. Exposed so advanced users can mount it behind their own
// authentication middleware (JWT, sessions, OAuth, ...).
func (r *Registry) DashboardHandler(w http.ResponseWriter, req *http.Request) {
	r.metricsPageHandler().ServeHTTP(w, req)
}

// DataHandler is the raw, unprotected handler for /metrics/data (JSON snapshot
// or HTMX fragment). Exposed for custom-middleware wrapping.
func (r *Registry) DataHandler(w http.ResponseWriter, req *http.Request) {
	r.metricsDataHandler().ServeHTTP(w, req)
}

// HooksHandler is the raw, unprotected handler for /metrics/hooks (JSON snapshot
// or HTMX fragment). Exposed for custom-middleware wrapping.
func (r *Registry) HooksHandler(w http.ResponseWriter, req *http.Request) {
	r.hooksDataHandler().ServeHTTP(w, req)
}

// EndpointsHTTPHandler returns per-endpoint latency snapshots (p50/p95/p99) as
// JSON, for the endpoint-percentile chart. Returns nil if the UI is disabled.
func (r *Registry) EndpointsHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.EndpointLatency())
	})
}

// CardinalityHTTPHandler returns per-metric cardinality snapshots as JSON,
// showing unique label series counts and dropped samples. Returns nil if the
// UI is disabled.
func (r *Registry) CardinalityHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.CardinalitySnapshots())
	})
}

// MetricsHTTPHandler returns the dashboard page handler, for routers that need
// to mount routes individually (e.g. Gin). Returns nil if the UI is disabled.
func (r *Registry) MetricsHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return r.metricsPageHandler()
}

// MetricsDataHTTPHandler returns the metrics data handler (JSON or HTMX
// fragment) for individual route mounting. Returns nil if the UI is disabled.
func (r *Registry) MetricsDataHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return r.metricsDataHandler()
}

// HistoryHTTPHandler returns the metrics history handler for time-series data.
// Returns nil if the UI is disabled.
func (r *Registry) HistoryHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return r.metricsHistoryHandler()
}

// AlertsPageHTTPHandler returns the alerts dashboard page handler.
// Returns nil if alerts are disabled.
func (r *Registry) AlertsPageHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertsPageHandler()
}

// AlertStatusHTTPHandler returns the alert firing-status handler
// (GET /metrics/alerts/status, body {firing: n}). Returns nil if alerts are
// disabled.
func (r *Registry) AlertStatusHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertStatusHandler()
}

// AlertRulesHTTPHandler returns the alert rules CRUD handler (GET list, POST create/update).
// Returns nil if alerts are disabled.
func (r *Registry) AlertRulesHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertRulesHandler()
}

// AlertRulesDeleteHTTPHandler returns the alert rule delete handler (DELETE /rules/{id}).
// Returns nil if alerts are disabled.
func (r *Registry) AlertRulesDeleteHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertRuleDeleteHandler()
}

// AlertLogHTTPHandler returns the alert log handler (GET).
// Returns nil if alerts are disabled.
func (r *Registry) AlertLogHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertLogHandler()
}

// AlertTemplatesHTTPHandler returns the predefined email templates handler.
// Returns nil if alerts are disabled.
func (r *Registry) AlertTemplatesHTTPHandler() http.Handler {
	if r.alerter == nil {
		return nil
	}
	return r.alertTemplatesHandler()
}

// metricsPageHandler renders the full dashboard shell. The card grid is
// refreshed by HTMX polling /metrics/data.
func (r *Registry) metricsPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		intervalMs := r.cfg.UI.PollInterval.Milliseconds()
		if intervalMs <= 0 {
			intervalMs = 5000
		}
		data := map[string]interface{}{
			"PollMs":            intervalMs,
			"PollSec":           intervalMs / 1000,
			"Cards":             buildCardsOpts(r.Snapshots(), r.cardOptions()),
			"HookCards":         buildHookCards(r.Snapshots()),
			"Interval":          r.cfg.UI.PollInterval.String(),
			"RuntimeEnabled":    r.cfg.RuntimeMetrics.Enabled,
			"ProjectName":       r.cfg.ProjectName,
			"DashboardSubtitle": r.cfg.DashboardSubtitle,
			"MaxLabelSeries":    r.cfg.MaxLabelSeriesPerMetric,
			"AlertsEnabled":     r.alerter != nil,
			"HasNotifier":       r.alerter != nil && r.alerter.HasNotifier(),
		}
		// Render to a buffer first so a template error never produces a
		// half-written response (which would force a superfluous WriteHeader).
		var buf bytes.Buffer
		if err := dashboardTpl.Execute(&buf, data); err != nil {
			http.Error(w, "dashboard render error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
	})
}

// metricsDataHandler returns either a full JSON snapshot (for HTMX fragments
// or programmatic clients) or an HTML fragment depending on Accept header.
func (r *Registry) metricsDataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := req.URL.Query().Get("metric")
		if name != "" {
			if s, ok := r.Snapshot(name); ok {
				writeJSON(w, s)
				return
			}
			http.NotFound(w, req)
			return
		}
		if wantsJSON(req) {
			writeJSON(w, r.Snapshots())
			return
		}
		// HTMX HTML fragment (the card grid).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = chartsTpl.Execute(w, buildCardsOpts(r.Snapshots(), r.cardOptions()))
	})
}

var defaultDurations = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"12h": 12 * time.Hour,
	"24h": 24 * time.Hour,
	"1w":  7 * 24 * time.Hour,
}

func (r *Registry) metricsHistoryHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		durStr := q.Get("duration")
		if durStr == "" {
			durStr = "1h"
		}
		dur, ok := defaultDurations[durStr]
		if !ok {
			http.Error(w, "invalid duration; use 5m,30m,1h,4h,12h,24h,1w", http.StatusBadRequest)
			return
		}
		// Batch form: /metrics/history?names=a,b,c returns {name: HistorySeries}
		// so a panel grid costs one request per refresh.
		if namesParam := q.Get("names"); namesParam != "" {
			out := make(map[string]HistorySeries)
			for _, name := range strings.Split(namesParam, ",") {
				if name = strings.TrimSpace(name); name != "" {
					out[name] = r.History(name, dur)
				}
			}
			writeJSON(w, out)
			return
		}
		name := q.Get("name")
		if name == "" {
			http.Error(w, "missing ?name= parameter", http.StatusBadRequest)
			return
		}
		writeJSON(w, r.History(name, dur))
	}
}

// hooksDataHandler returns hook metrics as HTML fragment for HTMX
func (r *Registry) hooksDataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if wantsJSON(req) {
			writeJSON(w, buildHookCards(r.Snapshots()))
			return
		}
		// HTMX HTML fragment
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = chartsTpl.Execute(w, buildHookCards(r.Snapshots()))
	})
}

// faviconBytes is embedded so the favicon resolves regardless of the
// process working directory.
//
//go:embed favicon.ico
var faviconBytes []byte

// faviconHandler serves the embedded favicon.ico
func (r *Registry) faviconHandler(w http.ResponseWriter, req *http.Request) {
	http.ServeContent(w, req, "favicon.ico", time.Time{}, bytes.NewReader(faviconBytes))
}

// wantsJSON reports whether the request prefers a JSON payload over the HTMX
// HTML fragment. Composite Accept headers ("application/json, */*" — the
// axios/fetch default) are matched per media range; bare "*/*" does not select
// JSON so browser navigations keep rendering HTML.
func wantsJSON(req *http.Request) bool {
	if req.URL.Query().Get("format") == "json" {
		return true
	}
	for _, part := range strings.Split(req.Header.Get("Accept"), ",") {
		media := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if media == "json" || media == "application/json" {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --- alert handlers -------------------------------------------------------

// alertsPageHandler renders the alerts dashboard page.
func (r *Registry) alertsPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		hasNotifier := r.alerter != nil && r.alerter.HasNotifier()
		data := map[string]interface{}{
			"ProjectName":       r.cfg.ProjectName,
			"DashboardSubtitle": r.cfg.DashboardSubtitle,
			"HasNotifier":       hasNotifier,
		}
		var buf bytes.Buffer
		if err := alertsPageTpl.Execute(&buf, data); err != nil {
			http.Error(w, "alerts page render error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
	}
}

// alertStatusHandler returns the current alert firing count as JSON, polling
// the dashboard's status dot and firing-count pill.
func (r *Registry) alertStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, map[string]int{"firing": r.alerter.FiringCount()})
	}
}

// alertRulesHandler handles GET (list all rules) and POST (create/update a rule).
func (r *Registry) alertRulesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			writeJSON(w, r.alerter.ListRules())

		case http.MethodPost:
			var rule AlertRule
			if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
				writeJSONError(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if rule.Name == "" || rule.Metric == "" {
				writeJSONError(w, "name and metric are required", http.StatusBadRequest)
				return
			}
			saved, err := r.alerter.AddRule(rule)
			if err != nil {
				writeJSONError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(saved)

		default:
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// alertRuleDeleteHandler handles DELETE /metrics/alerts/rules/{id}.
func (r *Registry) alertRuleDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Extract ID from path: /metrics/alerts/rules/{id}
		id := strings.TrimPrefix(req.URL.Path, "/metrics/alerts/rules/")
		if id == "" || id == req.URL.Path {
			writeJSONError(w, "missing rule id", http.StatusBadRequest)
			return
		}
		if err := r.alerter.RemoveRule(id); err != nil {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// alertLogHandler returns the alert firing log as JSON.
func (r *Registry) alertLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.AlertLog(200))
	}
}

// alertTemplatesHandler returns the predefined email templates as JSON.
func (r *Registry) alertTemplatesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, r.AlertTemplates())
	}
}

// --- card rendering model --------------------------------------------------

// metricCard is the UI-facing projection of a MetricSnapshot. It carries
// pre-formatted strings, the effective body view, and semantic style tokens so
// the templates stay logic-free.
type metricCard struct {
	Snapshot     MetricSnapshot
	Badge        string // COUNTER / GAUGE / HISTOGRAM
	BadgeTone    string // badge tone token: counter / gauge / histogram / unknown
	View         string // body view: http / bars / gauge / number
	Size         string // grid span preset: "" single cell, "md" half width, "lg" full width
	DisplayValue string // formatted numeric value
	Unit         string // value unit label
	Hook         bool   // rendered in the Hooks grid (not pinnable — it has no All Metrics card)
	Bars         []histBar
	HTTPColumns  []httpColumn // for the HTTP status card
	HTTPView     string       // status card column set: breakdown / totals
	// Gauge-specific fields
	NeedlePct int     // 0..100 needle position for gauges
	NeedleDeg float64 // -90..+90 SVG rotation matching NeedlePct across the dial arc
	MinValue  float64 // minimum value for gauge scale
	MaxValue  float64 // maximum value for gauge scale
}

type histBar struct {
	HeightPct int    // 0..100 relative to the busiest bucket
	Label     string // tooltip text, e.g. "≤0.005s: 50"
	Axis      string // axis label, e.g. "0.005" or "+Inf"
	Zero      bool   // empty bucket: rendered as a 1px hollow tick, not a bar
}

type httpColumn struct {
	Label        string // e.g., "2xx", "3xx", "4xx", "5xx", "total"
	Value        float64
	DisplayValue string // formatted value
	HeightPct    int    // 0..100 relative to total
	Tone         string // column tone token: s2xx / s3xx / s4xx / s5xx / total / errors
}

// cardOptions carries the persisted dashboard customization into the card
// builders: the HTTP status card view and per-panel chart/size overrides for
// pinned metrics. Zero values mean server defaults.
type cardOptions struct {
	HTTPView string
	panels   map[string]DashboardPanel // keyed by metric name
}

// cardOptions snapshots the persisted dashboard document into builder options.
func (r *Registry) cardOptions() cardOptions {
	r.dashMu.Lock()
	d := r.dash
	r.dashMu.Unlock()
	o := cardOptions{HTTPView: d.HTTPCardView}
	if len(d.Panels) > 0 {
		o.panels = make(map[string]DashboardPanel, len(d.Panels))
		for _, p := range d.Panels {
			o.panels[p.Metric] = p
		}
	}
	return o
}

// applyCardDefaults fills the per-type badge, display, and view fields of a
// metric card. It is the single source of truth shared by the all-metrics
// (buildCards) and hooks (buildHookCards) builders so their defaults can never
// diverge per name-pattern. p carries the pinned panel's chart/size overrides;
// its zero value keeps the defaults derived from the metric kind, so persisted
// configs saved before these fields existed render unchanged.
func applyCardDefaults(c *metricCard, s MetricSnapshot, p DashboardPanel) {
	switch s.Type {
	case "counter":
		c.Badge = "COUNTER"
		c.BadgeTone = "counter"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
	case "gauge":
		c.Badge = "GAUGE"
		c.BadgeTone = "gauge"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
	case "histogram":
		c.Badge = "HISTOGRAM"
		c.BadgeTone = "histogram"
		c.DisplayValue = fmtInt(float64(s.Count))
		c.Unit = "samples"
	default:
		c.Badge = "UNKNOWN"
		c.BadgeTone = "unknown"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
	}

	// Effective body view: the panel's saved chart type when it can be
	// honored, else the default derived from the metric kind. A saved bars
	// type on a bucket-less non-histogram metric falls back to a number
	// instead of an empty chart; a bucket-less histogram always keeps the
	// (empty) bars chart, whether the bars view was saved or derived.
	switch {
	case p.ChartType == "gauge":
		c.View = "gauge"
	case p.ChartType == "bars" && len(s.Buckets) > 0:
		c.View = "bars"
	case p.ChartType == "line":
		c.View = "line"
	case p.ChartType == "big-number":
		c.View = "number"
	default:
		c.View = defaultCardView(s)
	}
	switch c.View {
	case "gauge":
		applyGaugeScale(c, s)
	case "bars":
		c.Bars = buildBars(s.Buckets)
	}

	// Grid span preset: the panel's saved size wins, otherwise histograms get
	// the half-width span their bar rows need. "" is a single cell.
	switch {
	case p.Size == "md" || p.Size == "lg":
		c.Size = p.Size
	case p.Size == "sm":
		c.Size = ""
	case c.View == "bars":
		c.Size = "md"
	}
}

// defaultCardView maps a metric kind to its default dashboard body view.
func defaultCardView(s MetricSnapshot) string {
	switch s.Type {
	case "gauge":
		return "gauge"
	case "histogram":
		return "bars"
	default:
		return "number"
	}
}

// applyGaugeScale resolves the gauge scale (configured GaugeMin/GaugeMax win,
// otherwise smart defaults derived from the metric name) and computes the
// needle position.
func applyGaugeScale(c *metricCard, s MetricSnapshot) {
	minVal := s.GaugeMin
	maxVal := s.GaugeMax

	// Smart defaults based on metric name patterns
	if maxVal == 0 {
		// Auto-detect reasonable max based on metric name
		lowerName := strings.ToLower(s.Name)
		switch {
		case strings.Contains(lowerName, "percent") || strings.Contains(lowerName, "pct"):
			maxVal = 100 // Percentage metrics
		case strings.Contains(lowerName, "ratio") || strings.Contains(lowerName, "rate"):
			maxVal = 1 // Ratio/rate metrics
		case strings.Contains(lowerName, "bytes") || strings.Contains(lowerName, "mem"):
			maxVal = s.Value * 2 // Memory metrics: 2x current value
			if maxVal < 1024*1024 {
				maxVal = 1024 * 1024
			} // Min 1MB
		default:
			maxVal = s.Value * 1.5 // Default: 50% headroom
			if maxVal < 10 {
				maxVal = 10
			} // Minimum scale
		}
	}

	// Degenerate range (min == max, or inverted): widen it so the needle
	// percentage can never divide by zero — a flat scale is widened, never
	// NaN, never a pinned needle (same approach as Grafana).
	if maxVal <= minVal {
		maxVal = minVal + 1
	}

	c.MaxValue = maxVal
	c.MinValue = minVal
	pct := (s.Value - minVal) / (maxVal - minVal) * 100
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	c.NeedlePct = int(pct)
	// The dial arc spans ±90° around vertical; map 0..100% onto -90..+90° so
	// the needle sweeps the whole arc. Rounded to shed float noise (pct*1.8
	// can land on 27.000000000000004) before the templates print it.
	c.NeedleDeg = math.Round((pct*1.8-90)*100) / 100
}

func buildCards(snaps []MetricSnapshot) []metricCard {
	return buildCardsOpts(snaps, cardOptions{})
}

// buildCardsOpts builds the All Metrics grid, honoring the persisted
// dashboard customization (HTTP card view, per-panel chart/size overrides).
func buildCardsOpts(snaps []MetricSnapshot, o cardOptions) []metricCard {
	// Filter out individual metrics that are shown in Runtime Health charts and hooks
	excludedMetrics := map[string]bool{
		"go_memstats_alloc_bytes":      true,
		"go_memstats_sys_bytes":        true,
		"go_memstats_heap_inuse_bytes": true,
		"go_memstats_heap_objects":     true,
		"cpu_usage_percent":            true,
		"go_goroutines":                true,
	}

	cards := make([]metricCard, 0, len(snaps))
	for _, s := range snaps {
		// Skip HTTP metrics (they'll be grouped into a single card),
		// excluded runtime metrics, and hook metrics (hooks section).
		if isHTTPMetric(s.Name) || excludedMetrics[s.Name] || isHookMetric(s.Name) {
			continue
		}
		c := metricCard{Snapshot: s}
		applyCardDefaults(&c, s, o.panels[s.Name])
		cards = append(cards, c)
	}

	// Add HTTP request card first if any HTTP metrics exist
	if httpCards := buildHTTPCardView(snaps, o.HTTPView); httpCards != nil {
		cards = append(httpCards, cards...)
	}

	return cards
}

// buildHookCards creates cards for hook-related metrics
func buildHookCards(snaps []MetricSnapshot) []metricCard {
	cards := make([]metricCard, 0, len(snaps))
	for _, s := range snaps {
		if !isHookMetric(s.Name) {
			continue
		}
		c := metricCard{Snapshot: s, Hook: true}
		applyCardDefaults(&c, s, DashboardPanel{})
		cards = append(cards, c)
	}
	return cards
}

// isHookMetric returns true if the metric name suggests it's hook-related
func isHookMetric(name string) bool {
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "hook") || strings.Contains(lowerName, "webhook")
}

// isHTTPMetric returns true if the metric name suggests it's HTTP request related
func isHTTPMetric(name string) bool {
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "http") || strings.Contains(lowerName, "request") || strings.Contains(lowerName, "response")
}

// httpStatusClasses are the fixed per-class request counters the RED
// middleware emits (zero label cardinality). The HTTP status card reads them
// by exact name because the labeled parent metric only carries the last-seen
// label set — one value series per name is a core design decision.
var httpStatusClasses = [...]string{"2xx", "3xx", "4xx", "5xx"}

// HTTP status card views.
const (
	httpViewBreakdown = "breakdown" // 2xx-5xx columns + total (default)
	httpViewTotals    = "totals"    // total + errors
)

func buildHTTPCard(snaps []MetricSnapshot) []metricCard {
	return buildHTTPCardView(snaps, httpViewBreakdown)
}

// buildHTTPCardView builds the single HTTP status card from the middleware's
// fixed per-class counters. view selects the column set: breakdown (2xx-5xx +
// total) or totals (total + errors). The errors counter is a subset of 4xx+5xx
// (the middleware counts the same requests), so it is shown alongside the
// total but never added into it.
func buildHTTPCardView(snaps []MetricSnapshot, view string) []metricCard {
	byName := make(map[string]float64, len(snaps))
	for _, s := range snaps {
		byName[s.Name] = s.Value
	}

	var classes [len(httpStatusClasses)]float64
	classSum := 0.0
	for i, class := range httpStatusClasses {
		classes[i] = byName["http_requests_total_"+class]
		classSum += classes[i]
	}

	var sumErrors, sumOther float64
	for _, s := range snaps {
		if !isHTTPMetric(s.Name) {
			continue
		}
		lowerName := strings.ToLower(s.Name)
		// The per-class children are read directly above; the labeled parent
		// counts the same requests, so it only feeds the card when it is the
		// only total available (no children recorded yet).
		if strings.HasPrefix(lowerName, "http_requests_total") &&
			!(lowerName == "http_requests_total" && classSum == 0) {
			continue
		}
		// Error-specific metrics are counted separately.
		if strings.Contains(lowerName, "error") {
			sumErrors += s.Value
			continue
		}
		sumOther += s.Value
	}

	totalHTTP := classSum + sumOther
	if totalHTTP == 0 {
		return nil
	}

	var columns []httpColumn
	if view == httpViewTotals {
		columns = []httpColumn{
			{Label: "total", Value: totalHTTP, Tone: "total"},
			{Label: "errors", Value: sumErrors, Tone: "errors"},
		}
	} else {
		columns = []httpColumn{
			{Label: "2xx", Value: classes[0], Tone: "s2xx"},
			{Label: "3xx", Value: classes[1], Tone: "s3xx"},
			{Label: "4xx", Value: classes[2], Tone: "s4xx"},
			{Label: "5xx", Value: classes[3], Tone: "s5xx"},
			{Label: "total", Value: totalHTTP, Tone: "total"},
		}
	}

	// Format display values for columns
	for i := range columns {
		columns[i].DisplayValue = formatCount(columns[i].Value)
	}

	// Calculate heights relative to the maximum value
	maxVal := 0.0
	for _, col := range columns {
		if col.Value > maxVal {
			maxVal = col.Value
		}
	}

	for i := range columns {
		if maxVal > 0 {
			columns[i].HeightPct = int((columns[i].Value / maxVal) * 100)
			if columns[i].HeightPct < 2 {
				columns[i].HeightPct = 2 // Minimum visible height
			}
		}
	}

	// Create a single card with all HTTP metrics
	card := metricCard{
		Snapshot:     MetricSnapshot{Name: "http_requests_total", Description: "HTTP requests by status"},
		Badge:        "COUNTER",
		BadgeTone:    "counter",
		View:         "http",
		Size:         "lg",
		DisplayValue: fmtInt(totalHTTP),
		Unit:         "requests",
		HTTPColumns:  columns,
		HTTPView:     view,
	}

	return []metricCard{card}
}

func sumMetrics(snaps []MetricSnapshot) float64 {
	sum := 0.0
	for _, s := range snaps {
		sum += s.Value
	}
	return sum
}

// buildBars turns histogram buckets into render-ready bars. The busiest bucket
// is 100% height; empty buckets render as a 1px hollow baseline tick (Zero
// sets the style hook) so the axis stays legible without implying data.
func buildBars(buckets []BucketSnapshot) []histBar {
	var max int64
	for _, b := range buckets {
		if b.Count > max {
			max = b.Count
		}
	}
	bars := make([]histBar, 0, len(buckets))
	for _, b := range buckets {
		h := 0
		if max > 0 && b.Count > 0 {
			h = int(b.Count * 100 / max)
			if h < 2 {
				h = 2
			}
		}
		var label, axis string
		if b.Overflow {
			label = "+Inf: " + strconv.FormatInt(b.Count, 10)
			axis = "+Inf"
		} else {
			s := fmtBound(b.UpperBound)
			label = "≤" + s + "s: " + strconv.FormatInt(b.Count, 10)
			axis = s
		}
		bars = append(bars, histBar{HeightPct: h, Label: label, Axis: axis, Zero: b.Count == 0})
	}
	return bars
}

func fmtInt(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }
func fmtFloat(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// formatMetricValue applies intelligent formatting based on metric name patterns.
// Memory-related metrics get byte formatting (KB/MB/GB), large counts get K/M suffixes.
func formatMetricValue(name string, v float64) string {
	// Memory-related metrics: use byte formatting
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "bytes") || strings.Contains(lowerName, "alloc") ||
		strings.Contains(lowerName, "heap") || strings.Contains(lowerName, "memstats") ||
		strings.Contains(lowerName, "sys") && strings.Contains(lowerName, "mem") {
		return formatBytes(v)
	}
	// Large counts: use K/M formatting
	if v >= 1000 {
		return formatCount(v)
	}
	// Default: regular float formatting
	return fmtFloat(v)
}

// formatBytes formats a byte value as KB/MB/GB
func formatBytes(b float64) string {
	if b < 1024 {
		return strconv.FormatFloat(b, 'f', 0, 64) + " B"
	}
	if b < 1024*1024 {
		return strconv.FormatFloat(b/1024, 'f', 1, 64) + " KB"
	}
	if b < 1024*1024*1024 {
		return strconv.FormatFloat(b/(1024*1024), 'f', 1, 64) + " MB"
	}
	return strconv.FormatFloat(b/(1024*1024*1024), 'f', 2, 64) + " GB"
}

// formatCount formats a large number with K/M suffixes
func formatCount(n float64) string {
	if n < 1000 {
		return strconv.FormatFloat(n, 'f', 0, 64)
	}
	if n < 1e6 {
		return strconv.FormatFloat(n/1e3, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(n/1e6, 'f', 1, 64) + "M"
}

// fmtBound formats a histogram bucket boundary with up to 3 decimals and no
// trailing zeros, so 0.005 and 0.01 don't collide on the axis.
func fmtBound(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// formatFloat is retained for template/test reuse.
func formatFloat(f float64) string { return fmtFloat(f) }

var tmplFuncs = template.FuncMap{
	"fmt": fmtFloat,
}

// The dashboard shell. Markup uses semantic classes defined in
// ui/static/styles.css; behavior lives in ui/static/{common,dashboard}.js and
// the pinned Alpine build, all served from /metrics/static/. The inline
// bootstrap publishes server-rendered values (poll interval, notifier flag)
// for the scripts; the theme guard stays inline because it must run pre-paint.
const dashboardSrc = `<!DOCTYPE html>
<html lang="en" x-data="{}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" type="image/x-icon" href="/favicon.ico">
<title>{{.ProjectName}} Metrics</title>
<link rel="stylesheet" href="/metrics/static/styles.css">
<script src="/metrics/static/htmx.min.js" defer></script>
<script>
  window.GoLensConfig = { pollMs: {{.PollMs}}, hasNotifier: {{if .HasNotifier}}true{{else}}false{{end}}, alertsEnabled: {{if .AlertsEnabled}}true{{else}}false{{end}} };
</script>
<script>
  // Apply the saved theme before paint to avoid a flash of the wrong theme.
  (function(){
    var t = 'dark';
    try { t = localStorage.getItem('golens.theme') || 'dark'; } catch(e){}
    var c = document.documentElement.classList;
    c.add(t === 'light' ? 'light' : 'dark');
    c.remove(t === 'light' ? 'dark' : 'light');
  })();
</script>
<script src="/metrics/static/common.js" defer></script>
<script src="/metrics/static/dashboard.js" defer></script>
<script defer src="/metrics/static/alpine.min.js"></script>
</head>
<body>

<!-- Header -->
<header class="topbar">
  <div class="wrap topbar-inner row">
    <div class="brand grow">
      <span class="brand-dot" id="brand-dot"></span>
      <h1 class="brand-title">{{.ProjectName}}</h1>
      <span class="brand-sub">{{.DashboardSubtitle}}</span>
    </div>
    <input id="search" type="text" placeholder="Search metrics..." aria-label="Search metrics" class="search" oninput="applyFilter()">
    <button onclick="document.body.dataset.add='1'" class="btn">+ Add Chart</button>
    {{if .AlertsEnabled}}
    <a href="/metrics/alerts" class="btn">Alerts</a>
    <span id="alert-count" class="status-pill status-pill-alert" hidden></span>
    <button onclick="document.body.dataset.alertAdd='1'" class="btn">+ Add Alert</button>
    {{end}}
    <label class="pause-toggle">
      <input type="checkbox" class="check" id="pause-check" onchange="setPaused(this.checked)"> pause
    </label>
    <button onclick="resetLayout()" class="btn" title="Reset layout: clears pinned charts, ordering, and saved layout">Reset layout</button>
    <!-- Cooldown ring: depletes over the poll interval, resets on each HTMX refresh -->
    <div class="cooldown" title="time until next refresh — polling pauses while paused">
      <svg viewBox="0 0 36 36">
        <circle cx="18" cy="18" r="14" fill="none" stroke="#1e293b" stroke-width="3"></circle>
        <circle id="cooldown-ring" cx="18" cy="18" r="14" fill="none" stroke="#34d399" stroke-width="3" stroke-linecap="round"></circle>
      </svg>
      <span id="cooldown-label" class="cooldown-label">{{.PollSec}}</span>
    </div>
    <!-- Theme toggle -->
    <button id="theme-toggle" title="toggle theme" onclick="toggleTheme()" class="btn btn-icon">🌙</button>
  </div>
</header>

<main class="wrap stack">
  {{if .RuntimeEnabled}}
  <!-- Runtime Health: time-series charts for memory, CPU, goroutines -->
  <section x-data="runtimeHealth()" x-cloak>
    <div class="row">
      <h2 class="section-title">Runtime Health</h2>
      <span class="meta-faint">· drag to reorder</span>
      <div class="row-tight">
        <template x-for="d in ['5m','30m','1h','4h','12h','24h','1w']" :key="d">
          <button @click="duration=d; refresh()" class="pill" :class="duration===d ? 'active' : ''" x-text="d"></button>
        </template>
      </div>
    </div>

    <!-- Runtime charts container for drag-to-reorder. Cards carry the
         rt-card class so the drop handler selects exactly the card elements
         (never their inner header/legend divs). Drag and button moves derive
         indexes from the element's current position, so they stay correct
         after a reorder. -->
    <div id="runtime-charts" class="stack">
      <!-- Memory Overview (full width) -->
      <div draggable="true"
           @dragstart="start($event)" @dragover.prevent @drop="drop($event)" @dragend="dragEnd($event)"
           class="card card-grab rt-card">
        <div class="card-sub">
          <span class="card-name">Memory Overview</span>
          <span class="move-btns">
            <button type="button" class="move-btn" title="move earlier" aria-label="Move Memory Overview earlier" @click="move($event,-1)">&larr;</button>
            <button type="button" class="move-btn" title="move later" aria-label="Move Memory Overview later" @click="move($event,1)">&rarr;</button>
          </span>
          <div class="row-tight meta">
            <span>sys: <span x-text="fmtBytes(currentSysBytes)"></span></span>
            <span>heap: <span x-text="fmtBytes(currentHeapInuse)"></span></span>
            <span>alloc: <span x-text="fmtBytes(currentMem)"></span></span>
            <span>obj: <span x-text="fmtCount(currentHeapObj)"></span></span>
          </div>
        </div>
        <canvas id="rt-mem" class="chart-canvas" height="100" role="img"
                aria-label="Memory overview time-series chart: sys, heap in use, and alloc bytes, with heap objects on the right axis"></canvas>
        <div class="chart-legend">
          <span class="legend-accent">sys bytes</span>
          <span class="legend-teal">heap inuse</span>
          <span class="legend-blue">alloc bytes</span>
          <span class="legend-amber">heap objects (right)</span>
        </div>
      </div>

      <!-- CPU and Goroutines in grid -->
      <div class="grid grid-half">
        <!-- CPU -->
        <div draggable="true"
             @dragstart="start($event)" @dragover.prevent @drop="drop($event)" @dragend="dragEnd($event)"
             class="card card-grab rt-card">
          <div class="card-sub">
            <span class="card-name">CPU Usage</span>
            <span class="move-btns">
              <button type="button" class="move-btn" title="move earlier" aria-label="Move CPU Usage earlier" @click="move($event,-1)">&larr;</button>
              <button type="button" class="move-btn" title="move later" aria-label="Move CPU Usage later" @click="move($event,1)">&rarr;</button>
            </span>
            <span class="card-name" x-text="cpuPct.toFixed(1) + '%'"></span>
          </div>
          <canvas id="rt-cpu" class="chart-canvas" height="120" role="img"
                  aria-label="CPU usage time-series chart with goroutines on the right axis"></canvas>
          <div class="chart-legend">
            <span>cpu %</span><span>goroutines (right)</span>
          </div>
        </div>
        <!-- Goroutines -->
        <div draggable="true"
             @dragstart="start($event)" @dragover.prevent @drop="drop($event)" @dragend="dragEnd($event)"
             class="card card-grab rt-card">
          <div class="card-sub">
            <span class="card-name">App Goroutines</span>
            <span class="move-btns">
              <button type="button" class="move-btn" title="move earlier" aria-label="Move App Goroutines earlier" @click="move($event,-1)">&larr;</button>
              <button type="button" class="move-btn" title="move later" aria-label="Move App Goroutines later" @click="move($event,1)">&rarr;</button>
            </span>
            <span class="card-name" x-text="currentGoro"></span>
          </div>
          <canvas id="rt-goro" class="chart-canvas" height="120" role="img"
                  aria-label="Goroutines time-series chart"></canvas>
        </div>
      </div>
    </div>
  </section>
  {{end}}

  <!-- Histogram Time-Series: bucket distribution evolution over time -->
  <section x-data="histogramTimeSeries()" class="section-histogram" x-cloak>
    <div class="row">
      <h2 class="section-title section-title-accent">Histogram Time-Series</h2>
      <span class="meta-faint">· bucket distribution evolution</span>
      <div class="row-tight">
        <template x-for="d in ['5m','30m','1h','4h','12h','24h','1w']" :key="d">
          <button @click="duration=d; refresh()" class="pill" :class="duration===d ? 'active' : ''" x-text="d"></button>
        </template>
      </div>
    </div>

    <!-- Histogram charts grid -->
    <div id="histogram-timeseries" class="grid grid-half-lg">
      <template x-for="metric in histogramMetrics" :key="metric.name">
        <div class="card card-static">
          <div class="card-sub">
            <span class="card-name card-name-accent" x-text="metric.name"></span>
            <span class="card-name" x-text="metric.description"></span>
          </div>
          <canvas :id="'hist-ts-' + metric.name" class="chart-canvas" height="150" role="img"
                  :aria-label="metric.name + ' bucket distribution over time'"></canvas>
          <div class="chart-legend-bucket">
            <template x-for="(bound, index) in metric.bounds" :key="index">
              <span :style="'color:' + getBucketColor(index)" x-text="'≤' + formatBound(bound)"></span>
            </template>
          </div>
        </div>
      </template>
    </div>

    <!-- Empty state -->
    <div x-show="histogramMetrics.length === 0" class="empty-state">
      <p class="empty-title">No histogram metrics with time-series data found.</p>
      <p class="empty-hint">Histogram metrics will appear here after data collection begins.</p>
    </div>
  </section>

  {{if .HookCards}}
  <!-- Hooks Section: webhook and lifecycle hook metrics -->
  <section x-data="hooksSection()" x-cloak>
    <div class="row">
      <h2 class="section-title">Hooks</h2>
      <span class="meta-faint">· drag to reorder</span>
    </div>
    <div id="hooks"
         hx-get="/metrics/hooks"
         hx-trigger="every {{.PollMs}}ms [!document.body.dataset.paused && !document.body.dataset.hidden]"
         hx-swap="innerHTML">
      {{template "cards" .HookCards}}
    </div>
  </section>
  {{end}}

  <!-- My Charts: pinned metrics from the server-persisted dashboard config
       (localStorage is a fallback cache), drag-to-reorder, kept live by
       cloning the matching server-rendered card from #charts. -->
  <section x-show="$store.ui.charts.length" x-cloak
           x-data="{ drag: null,
                     start(i){ this.drag = i; },
                     drop(i){ if(this.drag===null){ return; }
                              const from = this.drag; this.drag = null;
                              moveCharts(from, i); },
                     move(i, d){ moveCharts(i, i + d); } }"
           x-effect="$store.ui.charts.length; renderPinned()">
    <div class="row">
      <h2 class="section-title">My Charts</h2>
      <span class="meta-faint" x-text="'(' + $store.ui.charts.length + ')'"></span>
      <span class="meta-faint">· drag to reorder</span>
    </div>
    <div id="pinned" class="grid grid-cards">
      <template x-for="(name, idx) in $store.ui.charts" :key="name">
        <div draggable="true"
             @dragstart="start(idx)"
             @dragover.prevent
             @drop="drop(idx)"
             @dragend="drag=null"
             :class="drag===idx ? 'dragging' : ''"
             class="card card-grab pin-slot">
          <div class="pin-actions">
            <button type="button" class="pin-btn" title="move earlier" :aria-label="'Move ' + name + ' earlier'" @click="move(idx,-1)">&larr;</button>
            <button type="button" class="pin-btn" title="move later" :aria-label="'Move ' + name + ' later'" @click="move(idx,1)">&rarr;</button>
            <button type="button" class="pin-remove" title="remove" :aria-label="'Remove ' + name + ' pin'" @click="$store.ui.remove(name)">×</button>
          </div>
          <div class="pin-body" :data-pin="name"></div>
        </div>
      </template>
    </div>
  </section>

  <h2 class="section-title">All Metrics</h2>
  <div id="charts"
       hx-get="/metrics/data"
       hx-trigger="every {{.PollMs}}ms [!document.body.dataset.paused && !document.body.dataset.hidden]"
       hx-swap="innerHTML">
    {{template "cards" .Cards}}
  </div>

  <!-- Endpoint latency: p50 / p95 / p99 per endpoint, polled as JSON,
       drag-to-reorder with persisted order. -->
  <section x-data="endpointLatency()">
    <div class="row">
      <h2 class="section-title">Endpoint Latency</h2>
      <span class="meta-faint">· drag to reorder</span>
    </div>
    <div x-show="endpoints.length === 0" class="meta">no endpoint traffic yet.</div>
    <div class="grid grid-cards">
      <template x-for="(e, idx) in endpoints" :key="e.method + ' ' + e.path">
        <div draggable="true"
             @dragstart="start(idx)" @dragover.prevent @drop="drop(idx)" @dragend="drag=null"
             :class="drag===idx ? 'dragging' : ''"
             class="card card-grab">
          <div class="card-head">
            <span class="badge badge-method" x-text="e.method"></span>
            <span class="card-name nowrap" x-text="e.path"></span>
            <span class="move-btns">
              <button type="button" class="move-btn" title="move earlier" :aria-label="'Move ' + e.path + ' earlier'" @click="move(idx,-1)">&larr;</button>
              <button type="button" class="move-btn" title="move later" :aria-label="'Move ' + e.path + ' later'" @click="move(idx,1)">&rarr;</button>
            </span>
          </div>
          <div class="ep-meta"><span x-text="e.count"></span> requests · avg <span x-text="fmt(e.avg)"></span>s</div>
          <div class="ep-chart">
            <template x-for="p in [{k:'p50',v:e.p50,c:'ep-bar-p50'},{k:'p95',v:e.p95,c:'ep-bar-p95'},{k:'p99',v:e.p99,c:'ep-bar-p99'}]" :key="p.k">
              <div class="ep-col">
                <div class="ep-col-bar" :class="p.c" :style="'height:'+h(e,p.v)+'%'"></div>
                <span class="ep-col-tip" x-text="fmt(p.v)+'s'"></span>
              </div>
            </template>
          </div>
          <div class="ep-legend">
            <span class="legend-accent">p50</span><span class="legend-amber">p95</span><span class="legend-rose">p99</span>
          </div>
        </div>
      </template>
    </div>
  </section>

  <!-- GoLens Internals: cardinality tracking and system stats -->
  <section x-data="cardinalityPanel({{.MaxLabelSeries}})" x-cloak>
    <button @click="open = !open" class="disclose">
      <svg class="disclose-caret" :class="open ? 'disclose-caret-open' : ''" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M7.21 14.77a.75.75 0 01.02-1.06L11.168 10 7.23 6.29a.75.75 0 111.04-1.08l4.5 4.25a.75.75 0 010 1.08l-4.5 4.25a.75.75 0 01-1.06-.02z" clip-rule="evenodd"/>
      </svg>
      <h2 class="section-title">{{.ProjectName}} Internals</h2>
      <span class="meta-faint">· cardinality &amp; system</span>
    </button>
    <div x-show="open" x-transition.duration.200ms class="stack">
      <!-- Summary cards -->
      <div class="grid grid-cards">
        <div class="stat-tile">
          <div class="stat-label">Total Series</div>
          <div class="stat-value" x-text="totalSeries"></div>
          <div class="stat-sub">across <span x-text="metricCount"></span> metrics</div>
        </div>
        <div class="stat-tile">
          <div class="stat-label">Total Dropped</div>
          <div class="stat-value" :class="totalDropped > 0 ? 'stat-value-warn' : 'stat-value-ok'" x-text="totalDropped"></div>
          <div class="stat-sub">samples rejected by guard</div>
        </div>
        <div class="stat-tile">
          <div class="stat-label">Series Cap</div>
          <div class="stat-value" x-text="maxSeries === 0 ? '∞' : maxSeries"></div>
          <div class="stat-sub">per metric (<span x-text="maxSeries === 0 ? 'unlimited' : 'configured'"></span>)</div>
        </div>
      </div>

      <!-- Per-metric detail table -->
      <div class="data-card">
        <div class="data-card-head">
          <span class="meta">Per-Metric Cardinality</span>
          <span class="meta-faint" x-text="metrics.length + ' metrics'"></span>
        </div>
        <div class="table-scroll">
          <table class="data-table data-table-dashboard">
            <thead>
              <tr>
                <th>Metric</th>
                <th class="cell-num">Series</th>
                <th class="cell-num">Cap</th>
                <th class="cell-num">Dropped</th>
                <th>Usage</th>
              </tr>
            </thead>
            <tbody>
              <template x-for="m in metrics" :key="m.metric_name">
                <tr>
                  <td class="cell-mono" x-text="m.metric_name"></td>
                  <td class="cell-num" x-text="m.series"></td>
                  <td class="cell-dim" x-text="m.max_series === 0 ? '∞' : m.max_series"></td>
                  <td class="cell-dim" :class="m.dropped > 0 ? 'cell-danger' : ''" x-text="m.dropped"></td>
                  <td>
                    <div class="meter">
                      <div class="meter-fill"
                           :class="usagePct(m) >= 90 ? 'meter-full' : usagePct(m) >= 70 ? 'meter-warn' : 'meter-ok'"
                           :style="'width:' + Math.min(100, usagePct(m)) + '%'"></div>
                    </div>
                  </td>
                </tr>
              </template>
              <tr x-show="metrics.length === 0">
                <td colspan="5" class="cell-empty">No cardinality data yet.</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </section>
</main>

<!-- Add-chart modal (Alpine): lists all currently-available metrics by default,
     filtered live by the search box; refreshes whenever the modal opens.
     Chart type and size apply to the chart being added (empty = the server's
     per-kind default). -->
<div x-data="{
        get list(){
          this.$store.ui.add;                       // refresh on open
          var q = (this.$store.ui.name||'').toLowerCase();
          return metricNames().filter(function(n){ return n.toLowerCase().indexOf(q) >= 0; });
        }
     }"
     x-show="$store.ui.add" x-cloak
     class="modal-overlay" @keydown.escape.window="$store.ui.add=false">
  <div class="modal modal-sm">
    <h3 class="modal-title">Add chart from metric</h3>
    <input type="text" placeholder="search metrics..." x-model="$store.ui.name" autofocus
           @keydown.enter="addChart()" class="input">
    <div class="form-cols">
      <div class="field">
        <label class="field-label" for="add-chart-type">Chart type</label>
        <select id="add-chart-type" class="input">
          <option value="">default</option>
          <option value="line">line</option>
          <option value="bars">bars</option>
          <option value="gauge">gauge</option>
          <option value="big-number">big number</option>
        </select>
      </div>
      <div class="field field-narrow">
        <label class="field-label" for="add-chart-size">Size</label>
        <select id="add-chart-size" class="input">
          <option value="">default</option>
          <option value="sm">small</option>
          <option value="md">medium</option>
          <option value="lg">large</option>
        </select>
      </div>
    </div>
    <div class="modal-list">
      <template x-for="m in list" :key="m">
        <button type="button" @click="$store.ui.name=m; addChart()" class="modal-item" x-text="m"></button>
      </template>
      <div x-show="list.length === 0" class="modal-empty">no matching metrics</div>
    </div>
    <div class="modal-footer">
      <button @click="$store.ui.add=false" class="btn">Cancel</button>
      <button @click="addChart()" class="btn-primary" :disabled="list.indexOf($store.ui.name || '') < 0">Add</button>
    </div>
  </div>
</div>

<!-- Add-alert modal (Alpine): step 1 = pick metric, step 2 = configure rule -->
{{if .AlertsEnabled}}
<div x-data="{
        get filteredMetrics(){
          var q = (this.$store.ui.alertSearch||'').toLowerCase();
          return (this.$store.ui.alertMetrics||[]).filter(function(m){
            return m.name.toLowerCase().indexOf(q) >= 0;
          });
        }
     }"
     x-show="$store.ui.alertModal" x-cloak
     class="modal-overlay"
     @keydown.escape.window="$store.ui.closeAlert()">
  <div class="modal modal-lg" @click.outside="$store.ui.closeAlert()">

    <!-- Step 1: Pick metric -->
    <template x-if="$store.ui.alertStep === 1">
      <div>
        <h3 class="modal-title">New Alert — select a metric</h3>
        <input type="text" placeholder="search metrics..." x-model="$store.ui.alertSearch" autofocus class="input">
        <div class="modal-list">
          <template x-for="m in filteredMetrics" :key="m.name">
            <button type="button" @click="$store.ui.selectMetric(m.name)" class="modal-item">
              <span x-text="m.name"></span>
              <span class="badge" x-text="m.type"></span>
            </button>
          </template>
          <div x-show="filteredMetrics.length === 0" class="modal-empty">no matching metrics</div>
        </div>
        <div class="modal-footer">
          <button @click="$store.ui.closeAlert()" class="btn">Cancel</button>
        </div>
      </div>
    </template>

    <!-- Step 2: Configure rule -->
    <template x-if="$store.ui.alertStep === 2">
      <div>
        <div class="row-tight form-row">
          <button @click="$store.ui.alertStep=1" class="modal-back">&larr; Back</button>
          <h3 class="modal-title">Configure Alert</h3>
          <span class="badge" x-text="$store.ui.alertMetric"></span>
        </div>
        <form @submit.prevent="$store.ui.submitAlert()">
          <div class="form-row">
            <label class="field-label">Name</label>
            <input type="text" x-model="$store.ui.alertForm.name" required placeholder="e.g. High Error Rate" class="input">
          </div>
          <div class="form-cols">
            <div class="field">
              <label class="field-label">Condition</label>
              <select x-model="$store.ui.alertForm.condition" class="input">
                <option value="gt">&gt; Greater than</option>
                <option value="gte">&ge; Greater or equal</option>
                <option value="lt">&lt; Less than</option>
                <option value="lte">&le; Less or equal</option>
                <option value="eq">= Equal</option>
              </select>
            </div>
            <div class="field field-narrow">
              <label class="field-label">Threshold</label>
              <input type="number" step="any" x-model.number="$store.ui.alertForm.threshold" required placeholder="0" class="input">
            </div>
          </div>
          <div class="form-row">
            <label class="field-label">Cooldown (number + ms|s|m|h, e.g. 5m)</label>
            <input type="text" x-model="$store.ui.alertForm.cooldown" pattern="[0-9]+(ms|s|m|h)" title="Duration: a number followed by ms, s, m, or h" placeholder="5m" class="input">
          </div>
          {{if .HasNotifier}}
          <hr class="form-sep">
          <p class="form-note">Email Notification (optional)</p>
          <div class="form-row">
            <label class="field-label">To (comma-separated)</label>
            <input type="text" x-model="$store.ui.alertForm.email_to" placeholder="team@example.com" class="input">
          </div>
          <div class="form-row" x-show="$store.ui.alertTemplates.length > 0">
            <label class="field-label">Format</label>
            <select x-model="$store.ui.alertForm.email_html" class="input">
              <option value="false">Text</option>
              <option value="true">HTML</option>
            </select>
          </div>
          <div class="form-row" x-show="$store.ui.alertForm.email_html === 'true' && $store.ui.alertTemplates.length > 0">
            <label class="field-label">Template</label>
            <select @change="$store.ui.applyTemplate($event.target.value)" class="input">
              <option value="">Custom (edit below)</option>
              <template x-for="t in $store.ui.alertTemplates" :key="t.id">
                <option :value="t.id" x-text="t.name"></option>
              </template>
            </select>
          </div>
          <div class="form-row">
            <label class="field-label">Subject</label>
            <input type="text" x-model="$store.ui.alertForm.email_subject" placeholder="Alert: {{"{{.RuleName}}"}}" class="input">
          </div>
          <div class="form-row">
            <label class="field-label">Body</label>
            <textarea x-model="$store.ui.alertForm.email_body" rows="4" placeholder="Custom alert message..." class="input"></textarea>
          </div>
          <details class="tpl-help" x-show="$store.ui.alertForm.email_html === 'true'">
            <summary>Available template variables</summary>
            <div class="tpl-vars">
              <div><span class="tpl-var-name">{{"{{.RuleName}}"}}</span> — rule name</div>
              <div><span class="tpl-var-name">{{"{{.Metric}}"}}</span> — metric name</div>
              <div><span class="tpl-var-name">{{"{{.Value}}"}}</span> — current value</div>
              <div><span class="tpl-var-name">{{"{{.Threshold}}"}}</span> — threshold</div>
              <div><span class="tpl-var-name">{{"{{.ConditionLabel}}"}}</span> — condition (&gt;, &lt;, &gt;=, &lt;=, ==)</div>
              <div><span class="tpl-var-name">{{"{{.FiredAt}}"}}</span> — timestamp</div>
              <div><span class="tpl-var-name">{{"{{.ProjectName}}"}}</span> — project name</div>
            </div>
          </details>
          {{end}}
          <div class="modal-footer">
            <button type="button" @click="$store.ui.closeAlert()" class="btn">Cancel</button>
            <button type="submit" class="btn-primary">Create Alert</button>
          </div>
        </form>
      </div>
    </template>

  </div>
</div>
{{end}}

<!-- Toast -->
<div id="app-toast" class="toast" aria-live="polite"></div>

</body>
</html>`

// The card grid, shared by the All Metrics section, the Hooks section, and
// the HTMX fragment endpoints. Ranges over a []metricCard slice. Each card's
// body branches on its effective View; Size adds a grid-span preset class.
const cardsSrc = `{{define "cards"}}
{{if not .}}
<div class="empty-inline">No metrics recorded yet.</div>
{{end}}
<div class="grid grid-cards">
{{range .}}
<div data-metric="{{.Snapshot.Name}}" draggable="true"
     class="card card-grab{{if .Size}} card-{{.Size}}{{end}}">
  <div>
    <div class="card-head">
      <span class="badge badge-{{.BadgeTone}}">{{.Badge}}</span>
      <span class="card-name nowrap">{{.Snapshot.Name}}</span>
      <span class="move-btns">
        {{if not .Hook}}
        <button type="button" class="pin-add" title="pin to My Charts" aria-label="Pin {{.Snapshot.Name}} to My Charts" onclick="pinCard(this)">+</button>
        {{end}}
        <button type="button" class="move-btn" title="move earlier" aria-label="Move {{.Snapshot.Name}} earlier" onclick="moveGridCard(this,-1)">&larr;</button>
        <button type="button" class="move-btn" title="move later" aria-label="Move {{.Snapshot.Name}} later" onclick="moveGridCard(this,1)">&rarr;</button>
      </span>
    </div>
    <p class="card-desc">{{or .Snapshot.Description "—"}}</p>
  </div>

  {{if eq .View "http"}}
  <div>
    <!-- HTTP request status column chart -->
    <div class="http-chart">
      {{range .HTTPColumns}}
      <div class="http-col">
        <div class="http-col-bar tone-{{.Tone}}" style="height:{{.HeightPct}}%"></div>
        <span class="http-col-label">{{.Label}}</span>
        <span class="http-col-value">{{.DisplayValue}}</span>
      </div>
      {{end}}
    </div>
    <div class="row-tight http-toggle">
      <button type="button" class="pill{{if ne .HTTPView "totals"}} active{{end}}" onclick="setHTTPView('breakdown')">breakdown</button>
      <button type="button" class="pill{{if eq .HTTPView "totals"}} active{{end}}" onclick="setHTTPView('totals')">totals</button>
    </div>
  </div>
  {{else if eq .View "bars"}}
  <!-- histogram: CSS flexbox bar chart -->
  <div>
    <div class="bar-chart">
      {{range .Bars}}
      {{if .Zero}}
      <!-- empty bucket: hollow baseline tick, no implied data -->
      <div class="bar bar-zero">
        <div class="bar-tip">{{.Label}}</div>
      </div>
      {{else}}
      <div class="bar bar-histogram" style="height:{{.HeightPct}}%">
        <div class="bar-tip">{{.Label}}</div>
      </div>
      {{end}}
      {{end}}
    </div>
    <div class="bar-axis">
      {{range .Bars}}<span>{{.Axis}}</span>{{end}}
    </div>
    <div class="bar-stats">
      <span>count <span>{{.Snapshot.Count}}</span></span>
      <span>sum <span>{{fmt .Snapshot.Sum}}s</span></span>
      <span>avg <span>{{fmt .Snapshot.Avg}}s</span></span>
    </div>
  </div>
  {{else if eq .View "gauge"}}
  <!-- gauge: neutral semi-circle dial (no danger/safe zones — which side is
       "dangerous" depends on the metric, and a hard-coded split lies) -->
  <div>
    <div class="gauge">
      <div class="gauge-dial-box">
        <svg viewBox="0 0 100 50" class="gauge-dial" role="img" aria-label="{{.Snapshot.Name}} gauge: {{.DisplayValue}} on a scale from {{fmt .MinValue}} to {{fmt .MaxValue}}">
          <path class="gauge-track" d="M 10 50 A 40 40 0 0 1 90 50" fill="none" stroke-width="8"/>
          <line class="gauge-needle" x1="50" y1="50" x2="50" y2="10" stroke-width="2" transform="rotate({{.NeedleDeg}} 50 50)" style="transform-origin: 50px 50px;"/>
          <circle class="gauge-hub" cx="50" cy="50" r="3"/>
        </svg>
      </div>
      <div class="gauge-value-row">
        <span class="metric-value">{{.DisplayValue}}</span>
        {{if .Unit}}<span class="metric-unit">{{.Unit}}</span>{{end}}
      </div>
    </div>
    {{if .Snapshot.LabelValues}}
    <div class="labels-row">
      {{range .Snapshot.LabelValues}}<span class="chip">{{.Name}}={{.Value}}</span>{{end}}
    </div>
    {{end}}
  </div>
  {{else if eq .View "line"}}
  <!-- line: current value plus a time-series canvas drawn client-side from
       /metrics/history (see drawLineCharts); the value keeps the card legible
       before the first draw. -->
  <div>
    <div class="metric-value-row">
      <span class="metric-value">{{.DisplayValue}}</span>
      {{if .Unit}}<span class="metric-unit">{{.Unit}}</span>{{end}}
    </div>
    <canvas class="chart-canvas" data-history="{{.Snapshot.Name}}" height="120" role="img"
            aria-label="{{.Snapshot.Name}} time-series, last hour"></canvas>
    {{if .Snapshot.LabelValues}}
    <div class="labels-row">
      {{range .Snapshot.LabelValues}}<span class="chip">{{.Name}}={{.Value}}</span>{{end}}
    </div>
    {{end}}
  </div>
  {{else}}
  <!-- counter/gauge: number display -->
  <div>
    <div class="metric-value-row">
      <span class="metric-value">{{.DisplayValue}}</span>
      {{if .Unit}}<span class="metric-unit">{{.Unit}}</span>{{end}}
    </div>
    {{if .Snapshot.LabelValues}}
    <div class="labels-row">
      {{range .Snapshot.LabelValues}}<span class="chip">{{.Name}}={{.Value}}</span>{{end}}
    </div>
    {{end}}
  </div>
  {{end}}
</div>
{{end}}
</div>
{{end}}`

var (
	dashboardTpl = template.Must(template.New("dashboard").Funcs(tmplFuncs).Parse(dashboardSrc + cardsSrc))
	chartsTpl    = template.Must(template.New("cards").Funcs(tmplFuncs).Parse(cardsSrc))
)
