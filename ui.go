package golens

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html/template"
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
	mux.Handle("/favicon.ico", wrap(r.faviconHandler))

	// Alert subsystem routes.
	if r.alerter != nil {
		mux.Handle("/metrics/alerts", wrap(r.alertsPageHandler()))
		mux.Handle("/metrics/alerts/rules", wrap(r.alertRulesHandler()))
		mux.Handle("/metrics/alerts/rules/", wrap(r.alertRuleDeleteHandler()))
		mux.Handle("/metrics/alerts/log", wrap(r.alertLogHandler()))
		mux.Handle("/metrics/alerts/templates", wrap(r.alertTemplatesHandler()))
	}
}

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
			"Cards":             buildCards(r.Snapshots()),
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
		_ = chartsTpl.Execute(w, map[string]interface{}{"Cards": buildCards(r.Snapshots())})
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
		_ = chartsTpl.Execute(w, map[string]interface{}{"Cards": buildHookCards(r.Snapshots())})
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
			"ProjectName": r.cfg.ProjectName,
			"HasNotifier": hasNotifier,
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

// alertRulesHandler handles GET (list all rules) and POST (create/update a rule).
func (r *Registry) alertRulesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			writeJSON(w, r.alerter.ListRules())

		case http.MethodPost:
			var rule AlertRule
			if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
				http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
				return
			}
			if rule.Name == "" || rule.Metric == "" {
				http.Error(w, `{"error":"name and metric are required"}`, http.StatusBadRequest)
				return
			}
			saved, err := r.alerter.AddRule(rule)
			if err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(saved)

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// alertRuleDeleteHandler handles DELETE /metrics/alerts/rules/{id}.
func (r *Registry) alertRuleDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		// Extract ID from path: /metrics/alerts/rules/{id}
		id := strings.TrimPrefix(req.URL.Path, "/metrics/alerts/rules/")
		if id == "" || id == req.URL.Path {
			http.Error(w, `{"error":"missing rule id"}`, http.StatusBadRequest)
			return
		}
		if err := r.alerter.RemoveRule(id); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
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
// pre-formatted strings and per-type styling so the templates stay logic-free.
type metricCard struct {
	Snapshot     MetricSnapshot
	Badge        string // COUNTER / GAUGE / HISTOGRAM
	BadgeClass   string // tailwind classes for the badge
	BarClass     string // tailwind classes for histogram bars
	DisplayValue string // formatted numeric value
	Unit         string // value unit label
	Bars         []histBar
	HTTPColumns  []httpColumn // for HTTP request cards
	// Gauge-specific fields
	NeedlePct int     // 0..100 needle position for gauges
	MinValue  float64 // minimum value for gauge scale
	MaxValue  float64 // maximum value for gauge scale
}

type histBar struct {
	HeightPct int    // 0..100 relative to the busiest bucket
	Label     string // tooltip text, e.g. "≤0.005s: 50"
	Axis      string // axis label, e.g. "0.005" or "+Inf"
}

type httpColumn struct {
	Label        string // e.g., "2xx", "3xx", "4xx", "5xx", "total"
	Value        float64
	DisplayValue string // formatted value
	HeightPct    int    // 0..100 relative to total
	ColorClass   string // tailwind color class
}

// applyCardDefaults fills the per-type badge, styling, and display fields of a
// metric card. It is the single source of truth shared by the all-metrics
// (buildCards) and hooks (buildHookCards) builders so their defaults can never
// diverge per name-pattern.
func applyCardDefaults(c *metricCard, s MetricSnapshot) {
	switch s.Type {
	case "counter":
		c.Badge = "COUNTER"
		c.BadgeClass = "bg-emerald-500/10 text-emerald-400 border-emerald-500/20"
		c.BarClass = "bg-emerald-500 hover:bg-emerald-400"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
	case "gauge":
		c.Badge = "GAUGE"
		c.BadgeClass = "bg-sky-500/10 text-sky-400 border-sky-500/20"
		c.BarClass = "bg-sky-500 hover:bg-sky-400"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
		applyGaugeScale(c, s)
	case "histogram":
		c.Badge = "HISTOGRAM"
		c.BadgeClass = "bg-purple-500/10 text-purple-400 border-purple-500/20"
		c.BarClass = "bg-purple-500 hover:bg-purple-400"
		c.DisplayValue = fmtInt(float64(s.Count))
		c.Unit = "samples"
		c.Bars = buildBars(s.Buckets)
	default:
		c.Badge = "UNKNOWN"
		c.BadgeClass = "bg-slate-500/10 text-slate-400 border-slate-500/20"
		c.DisplayValue = formatMetricValue(s.Name, s.Value)
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

	c.MaxValue = maxVal
	c.MinValue = minVal
	if maxVal > 0 {
		c.NeedlePct = int((s.Value - minVal) / (maxVal - minVal) * 100)
		if c.NeedlePct > 100 {
			c.NeedlePct = 100
		}
		if c.NeedlePct < 0 {
			c.NeedlePct = 0
		}
	}
}

func buildCards(snaps []MetricSnapshot) []metricCard {
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
		applyCardDefaults(&c, s)
		cards = append(cards, c)
	}

	// Add HTTP request card first if any HTTP metrics exist
	if httpCards := buildHTTPCard(snaps); httpCards != nil {
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
		c := metricCard{Snapshot: s}
		applyCardDefaults(&c, s)
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

// buildHTTPCard creates a single card grouping all HTTP request metrics by status
func buildHTTPCard(snaps []MetricSnapshot) []metricCard {
	// Categorize HTTP metrics by examining their labels for status codes
	var sum2xx, sum3xx, sum4xx, sum5xx, sumErrors, sumOther float64
	sum2xx, sum3xx, sum4xx, sum5xx, sumErrors, sumOther = 0, 0, 0, 0, 0, 0

	for _, s := range snaps {
		if !isHTTPMetric(s.Name) {
			continue
		}
		lowerName := strings.ToLower(s.Name)

		// Skip error-specific metrics, we'll count them separately
		if strings.Contains(lowerName, "error") {
			sumErrors += s.Value
			continue
		}

		// Check labels for status codes
		categorized := false
		for _, lv := range s.LabelValues {
			if lv.Name == "status" {
				statusCode := lv.Value
				// Categorize by status code range
				if strings.HasPrefix(statusCode, "2") {
					sum2xx += s.Value
					categorized = true
					break
				} else if strings.HasPrefix(statusCode, "3") {
					sum3xx += s.Value
					categorized = true
					break
				} else if strings.HasPrefix(statusCode, "4") {
					sum4xx += s.Value
					categorized = true
					break
				} else if strings.HasPrefix(statusCode, "5") {
					sum5xx += s.Value
					categorized = true
					break
				}
			}
		}

		// If not categorized by status code, add to other
		if !categorized {
			sumOther += s.Value
		}
	}

	// Only show the card if we have HTTP metrics
	totalHTTP := sum2xx + sum3xx + sum4xx + sum5xx + sumErrors + sumOther
	if totalHTTP == 0 {
		return nil
	}

	// Create columns for the chart
	columns := []httpColumn{
		{Label: "2xx", Value: sum2xx, ColorClass: "bg-emerald-500"},
		{Label: "3xx", Value: sum3xx, ColorClass: "bg-blue-500"},
		{Label: "4xx", Value: sum4xx, ColorClass: "bg-amber-500"},
		{Label: "5xx", Value: sum5xx, ColorClass: "bg-rose-500"},
		{Label: "errors", Value: sumErrors, ColorClass: "bg-red-500"},
		{Label: "total", Value: totalHTTP, ColorClass: "bg-slate-400"},
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
		BadgeClass:   "bg-emerald-500/10 text-emerald-400 border-emerald-500/20",
		DisplayValue: fmtInt(totalHTTP),
		Unit:         "requests",
		HTTPColumns:  columns,
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
// is 100% height; empty buckets get a 2% sliver so the axis stays legible.
func buildBars(buckets []BucketSnapshot) []histBar {
	var max int64
	for _, b := range buckets {
		if b.Count > max {
			max = b.Count
		}
	}
	bars := make([]histBar, 0, len(buckets))
	for _, b := range buckets {
		h := 2
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
		bars = append(bars, histBar{HeightPct: h, Label: label, Axis: axis})
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

const dashboardSrc = `<!DOCTYPE html>
<html lang="en" x-data="{}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" type="image/x-icon" href="/favicon.ico">
<title>{{.ProjectName}} Metrics</title>
<script src="https://cdn.tailwindcss.com"></script>
<script>tailwind.config={theme:{extend:{fontFamily:{sans:['Inter','system-ui','sans-serif'],mono:['JetBrains Mono','ui-monospace','SFMono-Regular','Menlo','monospace']}}}}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<script defer src="https://unpkg.com/alpinejs@3.14.1/dist/cdn.min.js"></script>
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
<style>
  [x-cloak]{display:none}
  ::-webkit-scrollbar{height:8px;width:8px}
  ::-webkit-scrollbar-thumb{background:#334155;border-radius:4px}
  /* Light theme: remap the dark slate palette to light surfaces/text.
     Accent colors (emerald/amber/rose/sky/purple) read fine on both. */
  html.light{color-scheme:light}
  html.light body{background:#ffffff;color:#0f172a}
  html.light .bg-slate-950{background-color:#ffffff !important}
  html.light .bg-slate-950\/80{background-color:rgba(255,255,255,.8) !important}
  html.light .bg-slate-900{background-color:#f8fafc !important}
  html.light .bg-slate-800{background-color:#e2e8f0 !important}
  html.light .bg-slate-800\/80{background-color:rgba(226,232,240,.9) !important}
  html.light .hover\:bg-slate-800:hover{background-color:#e2e8f0 !important}
  html.light .hover\:bg-rose-500\/80:hover{background-color:rgba(244,63,94,.85) !important}
  html.light .hover\:bg-slate-700{border-color:#cbd5e1 !important}
  html.light .border-slate-800{border-color:#e2e8f0 !important}
  html.light .border-slate-800\/80{border-color:rgba(226,232,240,.8) !important}
  html.light .border-slate-700{border-color:#cbd5e1 !important}
  html.light .text-white{color:#0f172a !important}
  html.light .text-slate-100{color:#0f172a !important}
  html.light .text-slate-200{color:#1e293b !important}
  html.light .text-slate-300{color:#334155 !important}
  html.light .text-slate-400{color:#475569 !important}
  html.light .text-slate-500{color:#64748b !important}
  html.light .text-slate-600{color:#94a3b8 !important}
  html.light ::-webkit-scrollbar-thumb{background:#cbd5e1}
</style>
</head>
<body class="bg-slate-950 min-h-screen text-slate-100 font-sans antialiased">

<!-- Header -->
<header class="border-b border-slate-800/80 bg-slate-950/80 backdrop-blur sticky top-0 z-20">
  <div class="max-w-7xl mx-auto px-6 py-4 flex items-center gap-4 flex-wrap">
    <div class="flex items-center gap-2 mr-auto">
      <span class="inline-block w-2.5 h-2.5 rounded-full bg-emerald-400 animate-pulse"></span>
      <h1 class="text-lg font-semibold tracking-tight">{{.ProjectName}}</h1>
      <span class="text-xs text-slate-500 font-mono">{{.DashboardSubtitle}}</span>
    </div>
    <input id="search" type="text" placeholder="Search metrics..."
           class="text-sm font-mono px-3 py-1.5 rounded-lg bg-slate-900 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600 w-56"
           oninput="applyFilter()">
    <button onclick="document.body.dataset.add='1'"
            class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 transition">+ Add Chart</button>
    {{if .AlertsEnabled}}
    <a href="/metrics/alerts"
       class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 transition">Alerts</a>
    <button onclick="document.body.dataset.alertAdd='1'"
            class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 transition">+ Add Alert</button>
    {{end}}
    <label class="text-xs text-slate-500 font-mono flex items-center gap-1.5 cursor-pointer select-none">
      <input type="checkbox" class="accent-slate-500"
             onchange="document.body.dataset.paused=this.checked?'1':''"> pause
    </label>
    <!-- Cooldown ring: depletes over the poll interval, resets on each HTMX refresh -->
    <div class="relative w-9 h-9" title="next refresh">
      <svg class="w-9 h-9 -rotate-90" viewBox="0 0 36 36">
        <circle cx="18" cy="18" r="14" fill="none" stroke="#1e293b" stroke-width="3"></circle>
        <circle id="cooldown-ring" cx="18" cy="18" r="14" fill="none" stroke="#34d399"
                stroke-width="3" stroke-linecap="round"></circle>
      </svg>
      <span id="cooldown-label" class="absolute inset-0 flex items-center justify-center text-[10px] font-mono text-slate-400">{{.PollSec}}</span>
    </div>
    <!-- Theme toggle -->
    <button id="theme-toggle" title="toggle theme"
            onclick="toggleTheme()"
            class="w-9 h-9 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 text-slate-300 text-base flex items-center justify-center transition">🌙</button>
  </div>
</header>

<main class="max-w-7xl mx-auto px-6 py-6">
  {{if .RuntimeEnabled}}
  <!-- Runtime Health: time-series charts for memory, CPU, goroutines -->
  <section x-data="runtimeHealth({{.PollMs}})" class="mb-8" x-cloak>
    <div class="flex items-center gap-2 mb-3 flex-wrap">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">Runtime Health</h2>
      <span class="text-[10px] font-mono text-slate-600">· drag to reorder</span>
      <div class="flex gap-1 ml-2">
        <template x-for="d in ['5m','30m','1h','4h','12h','24h','1w']" :key="d">
          <button @click="duration=d; refresh()"
                  :class="duration===d ? 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30' : 'bg-slate-800 text-slate-500 border-slate-700 hover:bg-slate-700'"
                  class="text-[10px] font-mono px-2 py-0.5 rounded border transition" x-text="d"></button>
        </template>
      </div>
    </div>

    <!-- Runtime charts container for drag-to-reorder -->
    <div id="runtime-charts" class="space-y-5">
      <!-- Cards carry the rt-card class so the drop handler selects exactly
           the card elements (never their inner header/legend divs). -->
      <!-- Memory Overview (full width) -->
      <div draggable="true"
           @dragstart="start(0)" @dragover.prevent @drop="drop(0)" @dragend="drag=null"
           :class="drag===0 ? 'opacity-40 ring-2 ring-emerald-500/60' : ''"
           class="rt-card bg-slate-900 border border-slate-800 rounded-xl p-5 cursor-grab active:cursor-grabbing">
        <div class="flex items-center justify-between mb-1">
          <span class="text-xs font-mono text-slate-400">Memory Overview</span>
          <div class="flex gap-4">
            <span class="text-xs font-mono text-slate-500">sys: <span x-text="fmtBytes(currentSysBytes)"></span></span>
            <span class="text-xs font-mono text-slate-500">heap: <span x-text="fmtBytes(currentHeapInuse)"></span></span>
            <span class="text-xs font-mono text-slate-500">alloc: <span x-text="fmtBytes(currentMem)"></span></span>
            <span class="text-xs font-mono text-slate-500">obj: <span x-text="fmtCount(currentHeapObj)"></span></span>
          </div>
        </div>
        <canvas id="rt-mem" class="w-full" height="100"></canvas>
        <div class="flex justify-between text-[10px] font-mono text-slate-600 mt-1">
          <span class="text-emerald-400">sys bytes</span>
          <span class="text-teal-400">heap inuse</span>
          <span class="text-blue-400">alloc bytes</span>
          <span class="text-amber-400">heap objects (right)</span>
        </div>
      </div>

      <!-- CPU and Goroutines in grid -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-5">
        <!-- CPU -->
        <div draggable="true"
             @dragstart="start(1)" @dragover.prevent @drop="drop(1)" @dragend="drag=null"
             :class="drag===1 ? 'opacity-40 ring-2 ring-emerald-500/60' : ''"
             class="rt-card bg-slate-900 border border-slate-800 rounded-xl p-5 cursor-grab active:cursor-grabbing">
          <div class="flex items-center justify-between mb-1">
            <span class="text-xs font-mono text-slate-400">CPU Usage</span>
            <span class="text-xs font-mono text-slate-500" x-text="cpuPct.toFixed(1) + '%'"></span>
          </div>
          <canvas id="rt-cpu" class="w-full" height="120"></canvas>
          <div class="flex justify-between text-[10px] font-mono text-slate-600 mt-1">
            <span>cpu %</span><span>goroutines (right)</span>
          </div>
        </div>
        <!-- Goroutines -->
        <div draggable="true"
             @dragstart="start(2)" @dragover.prevent @drop="drop(2)" @dragend="drag=null"
             :class="drag===2 ? 'opacity-40 ring-2 ring-emerald-500/60' : ''"
             class="rt-card bg-slate-900 border border-slate-800 rounded-xl p-5 cursor-grab active:cursor-grabbing">
          <div class="flex items-center justify-between mb-1">
            <span class="text-xs font-mono text-slate-400">App Goroutines</span>
            <span class="text-xs font-mono text-slate-500" x-text="currentGoro"></span>
          </div>
          <canvas id="rt-goro" class="w-full" height="120"></canvas>
        </div>
      </div>
    </div>
  </section>
  {{end}}

  <!-- Histogram Time-Series: bucket distribution evolution over time -->
  <section x-data="histogramTimeSeries({{.PollMs}})" class="mb-8" x-cloak>
    <div class="flex items-center gap-2 mb-3 flex-wrap">
      <h2 class="text-xs font-mono uppercase tracking-wider text-purple-400">Histogram Time-Series</h2>
      <span class="text-[10px] font-mono text-slate-600">· bucket distribution evolution</span>
      <div class="flex gap-1 ml-2">
        <template x-for="d in ['5m','30m','1h','4h','12h','24h','1w']" :key="d">
          <button @click="duration=d; refresh()"
                  :class="duration===d ? 'bg-purple-500/20 text-purple-400 border-purple-500/30' : 'bg-slate-800 text-slate-500 border-slate-700 hover:bg-slate-700'"
                  class="text-[10px] font-mono px-2 py-0.5 rounded border transition" x-text="d"></button>
        </template>
      </div>
    </div>

    <!-- Histogram charts grid -->
    <div id="histogram-timeseries" class="grid grid-cols-1 lg:grid-cols-2 gap-5">
      <template x-for="metric in histogramMetrics" :key="metric.name">
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5">
          <div class="flex items-center justify-between mb-1">
            <span class="text-xs font-mono text-purple-400" x-text="metric.name"></span>
            <span class="text-xs font-mono text-slate-500" x-text="metric.description"></span>
          </div>
          <canvas :id="'hist-ts-' + metric.name" class="w-full" height="150"></canvas>
          <div class="flex flex-wrap gap-2 text-[10px] font-mono text-slate-600 mt-1">
            <template x-for="(bound, index) in metric.bounds" :key="index">
              <span :class="getBucketColor(index)" x-text="'≤' + formatBound(bound)"></span>
            </template>
          </div>
        </div>
      </template>
    </div>

    <!-- Empty state -->
    <div x-show="histogramMetrics.length === 0" class="bg-slate-900 border border-slate-800 rounded-xl p-8 text-center">
      <p class="text-sm font-mono text-slate-500">No histogram metrics with time-series data found.</p>
      <p class="text-xs font-mono text-slate-600 mt-2">Histogram metrics will appear here after data collection begins.</p>
    </div>
  </section>

  {{if .HookCards}}
  <!-- Hooks Section: webhook and lifecycle hook metrics -->
  <section x-data="hooksSection({{.PollMs}})" class="mb-8" x-cloak>
    <div class="flex items-center gap-2 mb-3">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">Hooks</h2>
      <span class="text-[10px] font-mono text-slate-600">· drag to reorder</span>
    </div>
    <div id="hooks"
         hx-get="/metrics/hooks"
         hx-trigger="every {{.PollMs}}ms [!document.body.dataset.paused && !document.body.dataset.hidden]"
         hx-swap="innerHTML">
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
      {{range .HookCards}}
      {{$c := .}}
      {{$hist := eq .Snapshot.Type "histogram"}}
      <div data-metric="{{.Snapshot.Name}}" draggable="true"
           class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm hover:border-slate-700 transition flex flex-col justify-between cursor-grab active:cursor-grabbing {{if $hist}}md:col-span-2{{end}}">
        <div>
          <div class="flex items-center justify-between mb-2 gap-2">
            <span class="text-[10px] font-mono font-semibold px-2 py-0.5 rounded-full border {{.BadgeClass}}">{{.Badge}}</span>
            <span class="text-xs text-slate-500 font-mono truncate">{{.Snapshot.Name}}</span>
          </div>
          <p class="text-sm text-slate-400">{{or .Snapshot.Description "—"}}</p>
        </div>

        {{if $hist}}
          <!-- histogram: CSS flexbox bar chart -->
          <div class="flex items-end justify-between h-28 gap-1 border-b border-slate-800 mt-4 pb-1">
            {{range .Bars}}
            <div class="w-full rounded-t group relative cursor-pointer transition-all {{$c.BarClass}}" style="height:{{.HeightPct}}%">
              <div class="opacity-0 group-hover:opacity-100 absolute -top-7 left-1/2 -translate-x-1/2 bg-slate-800 text-[10px] px-1.5 py-0.5 rounded font-mono whitespace-nowrap z-10 transition-opacity">{{.Label}}</div>
            </div>
            {{end}}
          </div>
          <div class="flex justify-between text-[10px] font-mono text-slate-600 mt-1">
            {{range .Bars}}<span class="truncate">{{.Axis}}</span>{{end}}
          </div>
          <div class="flex justify-between text-[11px] font-mono text-slate-400 mt-2">
            <span>count <span class="text-slate-200">{{.Snapshot.Count}}</span></span>
            <span>sum <span class="text-slate-200">{{fmt .Snapshot.Sum}}s</span></span>
            <span>avg <span class="text-slate-200">{{fmt .Snapshot.Avg}}s</span></span>
          </div>
        {{else if eq .Snapshot.Type "gauge"}}
          <!-- gauge: semi-circle with red/green zones -->
          <div class="mt-4">
            <div class="relative w-40 h-20 mx-auto overflow-hidden">
              <!-- Semi-circle gauge -->
              <div class="absolute inset-0">
                <!-- Red zone (danger) - left side -->
                <svg viewBox="0 0 100 50" class="w-full h-full">
                  <!-- Background arc -->
                  <path d="M 10 50 A 40 40 0 0 1 90 50" fill="none" stroke="#1e293b" stroke-width="8"/>
                  <!-- Red zone (0-50%) -->
                  <path d="M 10 50 A 40 40 0 0 1 50 10" fill="none" stroke="#ef4444" stroke-width="8" stroke-dasharray="62.83" stroke-dashoffset="0"/>
                  <!-- Green zone (50-100%) -->
                  <path d="M 50 10 A 40 40 0 0 1 90 50" fill="none" stroke="#22c55e" stroke-width="8" stroke-dasharray="62.83" stroke-dashoffset="0"/>
                  <!-- Needle -->
                  <line x1="50" y1="50" x2="50" y2="10" stroke="white" stroke-width="2" transform="rotate({{if .NeedlePct}}{{.NeedlePct}}{{else}}0{{end}} 50 50)" style="transform-origin: 50px 50px;"/>
                  <!-- Center dot -->
                  <circle cx="50" cy="50" r="3" fill="white"/>
                </svg>
              </div>
            </div>
            <div class="flex items-baseline mt-2 justify-center">
              <span class="text-4xl font-semibold tracking-tight text-white font-mono">{{.DisplayValue}}</span>
              {{if .Unit}}<span class="text-xs text-slate-500 ml-1.5 font-mono">{{.Unit}}</span>{{end}}
            </div>
          </div>
          {{if .Snapshot.LabelValues}}
          <div class="mt-3 flex flex-wrap gap-1">
            {{range .Snapshot.LabelValues}}<span class="text-[10px] font-mono px-1.5 py-0.5 bg-slate-800 text-slate-400 rounded">{{.Name}}={{.Value}}</span>{{end}}
          </div>
          {{end}}
        {{else}}
          <!-- counter: simple number display -->
          <div class="mt-4 flex items-baseline">
            <span class="text-4xl font-semibold tracking-tight text-white font-mono">{{.DisplayValue}}</span>
            {{if .Unit}}<span class="text-xs text-slate-500 ml-1.5 font-mono">{{.Unit}}</span>{{end}}
          </div>
          {{if .Snapshot.LabelValues}}
          <div class="mt-3 flex flex-wrap gap-1">
            {{range .Snapshot.LabelValues}}<span class="text-[10px] font-mono px-1.5 py-0.5 bg-slate-800 text-slate-400 rounded">{{.Name}}={{.Value}}</span>{{end}}
          </div>
          {{end}}
        {{end}}
      </div>
      {{end}}
      </div>
    </div>
  </section>
  {{end}}

  <!-- My Charts: pinned metrics from the server-persisted dashboard config
       (localStorage is a fallback cache), drag-to-reorder, kept live by
       cloning the matching server-rendered card from #charts. -->
  <section x-show="$store.ui.charts.length" x-cloak
           x-data="{ drag: null,
                     start(i){ this.drag = i; },
                     drop(i){ if(this.drag===null || this.drag===i){ this.drag=null; return; }
                              const c = Alpine.store('ui').charts;
                              const m = c.splice(this.drag, 1)[0];
                              c.splice(i, 0, m);
                              dashCfg.panels = chartsToPanels(c);
                              persistDash();
                              this.drag = null;
                              this.$nextTick(() => renderPinned()); } }"
           x-effect="$store.ui.charts.length; renderPinned()"
           class="mb-8">
    <div class="flex items-center gap-2 mb-3">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">My Charts</h2>
      <span class="text-[10px] font-mono text-slate-600" x-text="'(' + $store.ui.charts.length + ')'"></span>
      <span class="text-[10px] font-mono text-slate-600">· drag to reorder</span>
    </div>
    <div id="pinned" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
      <template x-for="(name, idx) in $store.ui.charts" :key="name">
        <div draggable="true"
             @dragstart="start(idx)"
             @dragover.prevent
             @drop="drop(idx)"
             @dragend="drag=null"
             :class="drag===idx ? 'opacity-40 ring-2 ring-emerald-500/60' : ''"
             class="relative bg-slate-900 border border-slate-800 rounded-xl overflow-hidden cursor-grab active:cursor-grabbing">
          <button title="remove" @click="$store.ui.remove(name)"
                  class="absolute top-2 right-2 z-10 w-6 h-6 rounded-full bg-slate-800/80 hover:bg-rose-500/80 text-slate-300 text-xs flex items-center justify-center">×</button>
          <div class="pin-body" :data-pin="name"></div>
        </div>
      </template>
    </div>
  </section>

  <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500 mb-3">All Metrics</h2>
  <div id="charts"
       hx-get="/metrics/data"
       hx-trigger="every {{.PollMs}}ms [!document.body.dataset.paused && !document.body.dataset.hidden]"
       hx-swap="innerHTML">
    {{template "cards" .}}
  </div>

  <!-- Endpoint latency: p50 / p95 / p99 per endpoint, polled as JSON,
       drag-to-reorder with persisted order. -->
  <section x-data="endpointLatency({{.PollMs}})" class="mt-8">
    <div class="flex items-center gap-2 mb-3">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">Endpoint Latency</h2>
      <span class="text-[10px] font-mono text-slate-600">· drag to reorder</span>
    </div>
    <div x-show="endpoints.length === 0" class="text-xs text-slate-600 font-mono">no endpoint traffic yet.</div>
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
      <template x-for="(e, idx) in endpoints" :key="e.method + ' ' + e.path">
        <div draggable="true"
             @dragstart="start(idx)" @dragover.prevent @drop="drop(idx)" @dragend="drag=null"
             :class="drag===idx ? 'opacity-40 ring-2 ring-emerald-500/60' : ''"
             class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm flex flex-col cursor-grab active:cursor-grabbing">
          <div class="flex items-center justify-between mb-2 gap-2">
            <span class="text-[10px] font-mono px-2 py-0.5 rounded-full border bg-sky-500/10 text-sky-400 border-sky-500/20" x-text="e.method"></span>
            <span class="text-xs text-slate-400 font-mono truncate" x-text="e.path"></span>
          </div>
          <div class="text-[11px] text-slate-500 font-mono mb-3"><span x-text="e.count"></span> requests · avg <span x-text="fmt(e.avg)"></span>s</div>
          <div class="flex items-end justify-between h-24 gap-4 border-b border-slate-800 pb-1">
            <template x-for="p in [{k:'p50',v:e.p50,c:'bg-emerald-500'},{k:'p95',v:e.p95,c:'bg-amber-500'},{k:'p99',v:e.p99,c:'bg-rose-500'}]" :key="p.k">
              <div class="flex-1 flex flex-col items-center justify-end h-full group relative">
                <div :class="p.c" class="w-full rounded-t transition-all" :style="'height:'+h(e,p.v)+'%'"></div>
                <span class="absolute -top-5 text-[10px] font-mono text-slate-300 opacity-0 group-hover:opacity-100" x-text="fmt(p.v)+'s'"></span>
              </div>
            </template>
          </div>
          <div class="flex justify-between text-[10px] font-mono text-slate-500 mt-1">
            <span class="text-emerald-400">p50</span><span class="text-amber-400">p95</span><span class="text-rose-400">p99</span>
          </div>
        </div>
      </template>
    </div>
  </section>

  <!-- GoLens Internals: cardinality tracking and system stats -->
  <section x-data="cardinalityPanel({{.PollMs}}, {{.MaxLabelSeries}})" class="mt-8" x-cloak>
    <button @click="open = !open"
            class="flex items-center gap-2 mb-3 group cursor-pointer">
      <svg class="w-3 h-3 text-slate-500 transition-transform" :class="open ? 'rotate-90' : ''" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M7.21 14.77a.75.75 0 01.02-1.06L11.168 10 7.23 6.29a.75.75 0 111.04-1.08l4.5 4.25a.75.75 0 010 1.08l-4.5 4.25a.75.75 0 01-1.06-.02z" clip-rule="evenodd"/>
      </svg>
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500 group-hover:text-slate-400 transition">{{.ProjectName}} Internals</h2>
      <span class="text-[10px] font-mono text-slate-600">· cardinality & system</span>
    </button>
    <div x-show="open" x-transition.duration.200ms>
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5 mb-5">
        <!-- Summary cards -->
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5">
          <div class="text-[10px] font-mono text-slate-500 uppercase mb-1">Total Series</div>
          <div class="text-2xl font-mono font-semibold text-white" x-text="totalSeries"></div>
          <div class="text-[10px] font-mono text-slate-600 mt-1">across <span x-text="metricCount"></span> metrics</div>
        </div>
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5">
          <div class="text-[10px] font-mono text-slate-500 uppercase mb-1">Total Dropped</div>
          <div class="text-2xl font-mono font-semibold" :class="totalDropped > 0 ? 'text-amber-400' : 'text-emerald-400'" x-text="totalDropped"></div>
          <div class="text-[10px] font-mono text-slate-600 mt-1">samples rejected by guard</div>
        </div>
        <div class="bg-slate-900 border border-slate-800 rounded-xl p-5">
          <div class="text-[10px] font-mono text-slate-500 uppercase mb-1">Series Cap</div>
          <div class="text-2xl font-mono font-semibold text-white" x-text="maxSeries === 0 ? '∞' : maxSeries"></div>
          <div class="text-[10px] font-mono text-slate-600 mt-1">per metric (<span x-text="maxSeries === 0 ? 'unlimited' : 'configured'"></span>)</div>
        </div>
      </div>

      <!-- Per-metric detail table -->
      <div class="bg-slate-900 border border-slate-800 rounded-xl overflow-hidden">
        <div class="px-5 py-3 border-b border-slate-800 flex items-center justify-between">
          <span class="text-xs font-mono text-slate-400">Per-Metric Cardinality</span>
          <span class="text-[10px] font-mono text-slate-600" x-text="metrics.length + ' metrics'"></span>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-xs font-mono">
            <thead>
              <tr class="text-slate-500 text-left">
                <th class="px-5 py-2 font-medium">Metric</th>
                <th class="px-5 py-2 font-medium text-right">Series</th>
                <th class="px-5 py-2 font-medium text-right">Cap</th>
                <th class="px-5 py-2 font-medium text-right">Dropped</th>
                <th class="px-5 py-2 font-medium">Usage</th>
              </tr>
            </thead>
            <tbody>
              <template x-for="m in metrics" :key="m.metric_name">
                <tr class="border-t border-slate-800/50 hover:bg-slate-800/30 transition">
                  <td class="px-5 py-2 text-slate-300" x-text="m.metric_name"></td>
                  <td class="px-5 py-2 text-right text-slate-200" x-text="m.series"></td>
                  <td class="px-5 py-2 text-right text-slate-500" x-text="m.max_series === 0 ? '∞' : m.max_series"></td>
                  <td class="px-5 py-2 text-right" :class="m.dropped > 0 ? 'text-amber-400' : 'text-slate-500'" x-text="m.dropped"></td>
                  <td class="px-5 py-2">
                    <div class="w-full bg-slate-800 rounded-full h-1.5 max-w-[120px]">
                      <div class="h-1.5 rounded-full transition-all"
                           :class="usagePct(m) >= 90 ? 'bg-rose-500' : usagePct(m) >= 70 ? 'bg-amber-500' : 'bg-emerald-500'"
                           :style="'width:' + Math.min(100, usagePct(m)) + '%'"></div>
                    </div>
                  </td>
                </tr>
              </template>
              <tr x-show="metrics.length === 0">
                <td colspan="5" class="px-5 py-4 text-center text-slate-600">No cardinality data yet.</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </section>
</main>

<!-- Add-chart modal (Alpine): lists all currently-available metrics by default,
     filtered live by the search box; refreshes whenever the modal opens. -->
<div x-data="{
        get list(){
          this.$store.ui.add;                       // refresh on open
          var q = (this.$store.ui.name||'').toLowerCase();
          return metricNames().filter(function(n){ return n.toLowerCase().indexOf(q) >= 0; });
        }
     }"
     x-show="$store.ui.add" x-cloak
     class="fixed inset-0 z-30 flex items-center justify-center bg-black/60" @keydown.escape.window="$store.ui.add=false">
  <div class="bg-slate-900 border border-slate-800 rounded-xl p-5 w-full max-w-md shadow-xl">
    <h3 class="text-sm font-semibold mb-3">Add chart from metric</h3>
    <input type="text" placeholder="search metrics..." x-model="$store.ui.name" autofocus
           @keydown.enter="addChart()"
           class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
    <div class="mt-2 max-h-64 overflow-y-auto rounded-lg border border-slate-800 bg-slate-950">
      <template x-for="m in list" :key="m">
        <button type="button" @click="$store.ui.name=m; addChart()"
                class="block w-full text-left text-sm font-mono px-3 py-2 text-slate-300 hover:bg-slate-800 transition" x-text="m"></button>
      </template>
      <div x-show="list.length === 0" class="px-3 py-4 text-center text-xs text-slate-600 font-mono">no matching metrics</div>
    </div>
    <div class="flex justify-end gap-2 mt-4">
      <button @click="$store.ui.add=false" class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 hover:bg-slate-800">Cancel</button>
      <button @click="addChart()" class="text-sm px-3 py-1.5 rounded-lg bg-emerald-500/90 hover:bg-emerald-500 text-slate-950 font-medium">Add</button>
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
     class="fixed inset-0 z-30 flex items-center justify-center bg-black/60"
     @keydown.escape.window="$store.ui.closeAlert()">
  <div class="bg-slate-900 border border-slate-800 rounded-xl p-6 w-full max-w-lg shadow-xl"
       @click.outside="$store.ui.closeAlert()">

    <!-- Step 1: Pick metric -->
    <template x-if="$store.ui.alertStep === 1">
      <div>
        <h3 class="text-sm font-semibold mb-3">New Alert — select a metric</h3>
        <input type="text" placeholder="search metrics..." x-model="$store.ui.alertSearch" autofocus
               class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
        <div class="mt-2 max-h-64 overflow-y-auto rounded-lg border border-slate-800 bg-slate-950">
          <template x-for="m in filteredMetrics" :key="m.name">
            <button type="button" @click="$store.ui.selectMetric(m.name)"
                    class="block w-full text-left text-sm font-mono px-3 py-2 hover:bg-slate-800 transition flex items-center justify-between">
              <span class="text-slate-300" x-text="m.name"></span>
              <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-500" x-text="m.type"></span>
            </button>
          </template>
          <div x-show="filteredMetrics.length === 0" class="px-3 py-4 text-center text-xs text-slate-600 font-mono">no matching metrics</div>
        </div>
        <div class="flex justify-end gap-2 mt-4">
          <button @click="$store.ui.closeAlert()" class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 hover:bg-slate-800">Cancel</button>
        </div>
      </div>
    </template>

    <!-- Step 2: Configure rule -->
    <template x-if="$store.ui.alertStep === 2">
      <div>
        <div class="flex items-center gap-2 mb-4">
          <button @click="$store.ui.alertStep=1" class="text-xs text-slate-500 hover:text-slate-300 transition">&larr; Back</button>
          <h3 class="text-sm font-semibold">Configure Alert</h3>
          <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-500" x-text="$store.ui.alertMetric"></span>
        </div>
        <form @submit.prevent="$store.ui.submitAlert()">
          <div class="mb-3">
            <label class="block text-xs text-slate-500 font-mono mb-1">Name</label>
            <input type="text" x-model="$store.ui.alertForm.name" required placeholder="e.g. High Error Rate"
                   class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
          </div>
          <div class="flex gap-3 mb-3">
            <div class="flex-1">
              <label class="block text-xs text-slate-500 font-mono mb-1">Condition</label>
              <select x-model="$store.ui.alertForm.condition"
                      class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
                <option value="gt">&gt; Greater than</option>
                <option value="gte">&ge; Greater or equal</option>
                <option value="lt">&lt; Less than</option>
                <option value="lte">&le; Less or equal</option>
                <option value="eq">= Equal</option>
              </select>
            </div>
            <div class="w-32">
              <label class="block text-xs text-slate-500 font-mono mb-1">Threshold</label>
              <input type="number" step="any" x-model.number="$store.ui.alertForm.threshold" required placeholder="0"
                     class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
            </div>
          </div>
          <div class="mb-3">
            <label class="block text-xs text-slate-500 font-mono mb-1">Cooldown (e.g. 5m, 1h)</label>
            <input type="text" x-model="$store.ui.alertForm.cooldown" placeholder="5m"
                   class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
          </div>
          {{if .HasNotifier}}
          <div class="border-t border-slate-800 my-4"></div>
          <p class="text-xs text-slate-500 font-mono mb-3">Email Notification (optional)</p>
          <div class="mb-3">
            <label class="block text-xs text-slate-500 font-mono mb-1">To (comma-separated)</label>
            <input type="text" x-model="$store.ui.alertForm.email_to" placeholder="team@example.com"
                   class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
          </div>
          <div class="mb-3" x-show="$store.ui.alertTemplates.length > 0">
            <label class="block text-xs text-slate-500 font-mono mb-1">Format</label>
            <select x-model="$store.ui.alertForm.email_html"
                    class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
              <option value="false">Text</option>
              <option value="true">HTML</option>
            </select>
          </div>
          <div class="mb-3" x-show="$store.ui.alertForm.email_html === 'true' && $store.ui.alertTemplates.length > 0">
            <label class="block text-xs text-slate-500 font-mono mb-1">Template</label>
            <select @change="$store.ui.applyTemplate($event.target.value)"
                    class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
              <option value="">Custom (edit below)</option>
              <template x-for="t in $store.ui.alertTemplates" :key="t.id">
                <option :value="t.id" x-text="t.name"></option>
              </template>
            </select>
          </div>
          <div class="mb-3">
            <label class="block text-xs text-slate-500 font-mono mb-1">Subject</label>
            <input type="text" x-model="$store.ui.alertForm.email_subject" placeholder="Alert: {{"{{.RuleName}}"}}"
                   class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
          </div>
          <div class="mb-4">
            <label class="block text-xs text-slate-500 font-mono mb-1">Body</label>
            <textarea x-model="$store.ui.alertForm.email_body" rows="4" placeholder="Custom alert message..."
                      class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600 resize-none"></textarea>
          </div>
          <details class="mb-4" x-show="$store.ui.alertForm.email_html === 'true'">
            <summary class="text-xs text-slate-500 font-mono cursor-pointer hover:text-slate-400 transition">Available template variables</summary>
            <div class="mt-2 bg-slate-950 border border-slate-800 rounded-lg p-3 text-[11px] font-mono text-slate-400 space-y-0.5">
              <div><span class="text-emerald-400">{{"{{.RuleName}}"}}</span> — rule name</div>
              <div><span class="text-emerald-400">{{"{{.Metric}}"}}</span> — metric name</div>
              <div><span class="text-emerald-400">{{"{{.Value}}"}}</span> — current value</div>
              <div><span class="text-emerald-400">{{"{{.Threshold}}"}}</span> — threshold</div>
              <div><span class="text-emerald-400">{{"{{.ConditionLabel}}"}}</span> — condition (&gt;, &lt;, &gt;=, &lt;=, ==)</div>
              <div><span class="text-emerald-400">{{"{{.FiredAt}}"}}</span> — timestamp</div>
              <div><span class="text-emerald-400">{{"{{.ProjectName}}"}}</span> — project name</div>
            </div>
          </details>
          {{end}}
          <div class="flex justify-end gap-2">
            <button type="button" @click="$store.ui.closeAlert()"
                    class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 hover:bg-slate-800">Cancel</button>
            <button type="submit"
                    class="text-sm px-3 py-1.5 rounded-lg bg-emerald-500/90 hover:bg-emerald-500 text-slate-950 font-medium">Create Alert</button>
          </div>
        </form>
      </div>
    </template>

  </div>
</div>
{{end}}

<script>
  function applyTheme(t){
    var c = document.documentElement.classList;
    c.toggle('light', t === 'light');
    c.toggle('dark', t !== 'light');
    try { localStorage.setItem('golens.theme', t); } catch(e){}
    var btn = document.getElementById('theme-toggle');
    if (btn) btn.textContent = (t === 'light') ? '☀️' : '🌙';
  }
  function toggleTheme(){
    var light = document.documentElement.classList.contains('light');
    applyTheme(light ? 'dark' : 'light');
  }
  // Sync the toggle button's icon once the DOM is ready.
  document.addEventListener('DOMContentLoaded', function(){
    var t = 'dark'; try { t = localStorage.getItem('golens.theme') || 'dark'; } catch(e){}
    applyTheme(t);
  });

  // --- Dashboard layout persistence ---
  // The server-side document (/metrics/config) is the source of truth for
  // pinned charts and section ordering; localStorage is only a write-through
  // cache/fallback for when the server copy is unavailable (UI config disabled,
  // read-only mount, failed PUT). golens.theme stays purely client-side.
  function lsJSON(key){ try { return JSON.parse(localStorage.getItem(key) || '[]'); } catch(e){ return []; } }

  // Boot from the localStorage cache; when the server document loads it
  // replaces this state (loadDash).
  var dashCfg = { version:1,
    panels: lsJSON('golens.charts').map(function(n){ return { id:n, metric:n }; }),
    order: { all_metrics: lsJSON('golens.order'), hooks: lsJSON('golens.hooksorder'), endpoints: lsJSON('golens.eporder') } };
  var dashRemote = false;

  // dashHasOrder reports whether a config document carries any section order.
  function dashHasOrder(d){ var o = d.order || {}; return !!((o.all_metrics||[]).length || (o.hooks||[]).length || (o.endpoints||[]).length); }

  // chartsToPanels re-derives the config's panel list from a chart-name list,
  // preserving any per-panel config already saved for a metric.
  function chartsToPanels(charts){
    var byMetric = {};
    dashCfg.panels.forEach(function(p){ byMetric[p.metric] = p; });
    return charts.map(function(n){ return byMetric[n] || { id:n, metric:n }; });
  }

  function persistDash(){
    // localStorage write-through cache first, so the layout survives even if
    // the server PUT fails.
    try {
      localStorage.setItem('golens.charts', JSON.stringify(dashCfg.panels.map(function(p){ return p.metric; })));
      localStorage.setItem('golens.order', JSON.stringify(dashCfg.order.all_metrics || []));
      localStorage.setItem('golens.hooksorder', JSON.stringify(dashCfg.order.hooks || []));
      localStorage.setItem('golens.eporder', JSON.stringify(dashCfg.order.endpoints || []));
    } catch(e){}
    if (!dashRemote) return;
    fetch('/metrics/config', {
      method: 'PUT',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify(dashCfg)
    }).then(function(r){
      if (!r.ok) { dashRemote = false; showToast('changes are local only'); }
    }).catch(function(){ dashRemote = false; showToast('changes are local only'); });
  }

  // applyDash re-hydrates the UI from the current config document.
  function applyDash(){
    if (window.Alpine && Alpine.store('ui')) {
      Alpine.store('ui').charts = dashCfg.panels.map(function(p){ return p.metric; });
    }
    sortCharts(); applyFilter(); renderPinned(); applyHookOrder();
  }

  function loadDash(){
    fetch('/metrics/config', {headers:{'Accept':'application/json'}})
      .then(function(r){ if (!r.ok) throw 0; return r.json(); })
      .then(function(d){
        if (!d || d.version !== 1) return;
        if (!d.panels) d.panels = [];
        if (!d.order) d.order = { all_metrics:[], hooks:[], endpoints:[] };
        // First connect against an empty server doc: promote the localStorage
        // layout (still in dashCfg) so existing pins AND section orderings are
        // migrated, even when only orderings exist.
        var promoted = !(d.panels.length || dashHasOrder(d)) && (dashCfg.panels.length || dashHasOrder(dashCfg));
        if (promoted) { d.panels = dashCfg.panels; d.order = dashCfg.order; }
        dashCfg = d;
        dashRemote = true;
        if (promoted || d.panels.length) persistDash();
        applyDash();
      })
      .catch(function(){}); // server copy unavailable: keep the localStorage boot state
  }

  document.addEventListener('alpine:init', () => {
    Alpine.store('ui', {
      add: false,
      name: '',
      charts: dashCfg.panels.map(function(p){ return p.metric; }),
      has(n){ return this.charts.indexOf(n) >= 0; },
      add(n){ if (n && !this.has(n)) { this.charts.push(n); dashCfg.panels = chartsToPanels(this.charts); persistDash(); } },
      remove(n){ this.charts = this.charts.filter(x => x !== n); dashCfg.panels = chartsToPanels(this.charts); persistDash(); },

      // Alert modal state
      alertModal: false,
      alertStep: 1,
      alertMetric: '',
      alertSearch: '',
      alertMetrics: [],
      alertTemplates: [],
      alertForm: { name:'', condition:'gt', threshold:0, cooldown:'5m', email_to:'', email_subject:'', email_body:'', email_html:'false' },

      openAlert(){
        this.alertStep = 1;
        this.alertMetric = '';
        this.alertSearch = '';
        this.alertForm = { name:'', condition:'gt', threshold:0, cooldown:'5m', email_to:'', email_subject:'', email_body:'', email_html:'false' };
        var self = this;
        fetch('/metrics/data', {headers:{'Accept':'application/json'}})
          .then(function(r){ return r.json(); })
          .then(function(d){ self.alertMetrics = (d||[]).map(function(s){ return {name:s.name, type:s.type}; }); })
          .catch(function(){});
        fetch('/metrics/alerts/templates', {headers:{'Accept':'application/json'}})
          .then(function(r){ return r.json(); })
          .then(function(d){ self.alertTemplates = d || []; })
          .catch(function(){ self.alertTemplates = []; });
        this.alertModal = true;
      },

      applyTemplate(id){
        if (!id) return;
        var t = this.alertTemplates.find(function(x){ return x.id === id; });
        if (t) {
          this.alertForm.email_subject = t.subject;
          this.alertForm.email_body = t.body;
        }
      },

      selectMetric(name){
        this.alertMetric = name;
        this.alertForm.name = name + ' alert';
        this.alertStep = 2;
      },

      closeAlert(){
        this.alertModal = false;
        this.alertStep = 1;
      },

      submitAlert(){
        var body = {
          name: this.alertForm.name,
          metric: this.alertMetric,
          condition: this.alertForm.condition,
          threshold: this.alertForm.threshold,
          cooldown: this.alertForm.cooldown,
          enabled: true
        };
        var hasNotifier = {{if .HasNotifier}}true{{else}}false{{end}};
        if (hasNotifier) {
          body.email_to = this.alertForm.email_to ? this.alertForm.email_to.split(',').map(function(s){ return s.trim(); }).filter(Boolean) : [];
          body.email_subject = this.alertForm.email_subject;
          body.email_body = this.alertForm.email_body;
          body.email_html = this.alertForm.email_html === 'true';
        }
        var self = this;
        fetch('/metrics/alerts/rules', {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify(body)
        }).then(function(r){
          if (!r.ok) return r.json().then(function(e){ throw new Error(e.error || 'Server error'); });
          return r.json();
        }).then(function(){
          self.closeAlert();
          showToast('Alert created for ' + self.alertMetric, 'success');
        }).catch(function(e){ showToast(e.message || 'Failed to create alert'); });
      }
    });
  });
  // open modal from the header button — always start from a clean state
  const mo = new MutationObserver(() => {
    if (document.body.dataset.add === '1') {
      const ui = Alpine.store('ui');
      ui.name = '';        // clear any previous search/selection
      ui.add = true;       // open
      delete document.body.dataset.add;
    }
  });
  mo.observe(document.body, { attributes: true, attributeFilter: ['data-add'] });

  // open alert modal from the header button
  const moAlert = new MutationObserver(() => {
    if (document.body.dataset.alertAdd === '1') {
      Alpine.store('ui').openAlert();
      delete document.body.dataset.alertAdd;
    }
  });
  moAlert.observe(document.body, { attributes: true, attributeFilter: ['data-alert-add'] });

  // Add the searched/selected metric as a pinned chart (no navigation).
  function addChart(){
    const n = Alpine.store('ui').name;
    Alpine.store('ui').add = false;
    if (n) Alpine.store('ui').add(n);
    Alpine.store('ui').name = '';
  }
  function saveOrder(o){ dashCfg.order.all_metrics = o; persistDash(); }

  function metricNames(){
    return Array.from(document.querySelectorAll('#charts [data-metric]')).map(el => el.dataset.metric);
  }
  function cssEsc(s){ return (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&'); }
  // Clone each pinned metric's server-rendered card from #charts into its slot.
  function renderPinned(){
    document.querySelectorAll('#pinned [data-pin]').forEach(body => {
      const name = body.getAttribute('data-pin');
      const card = document.querySelector('#charts [data-metric="' + cssEsc(name) + '"]');
      if (card) { body.innerHTML = card.innerHTML; }
      else { body.innerHTML = '<div class="p-5 text-xs text-slate-600 font-mono">metric unavailable</div>'; }
    });
  }
  function applyFilter(){
    const q = (document.getElementById('search').value || '').toLowerCase();
    document.querySelectorAll('#charts [data-metric]').forEach(el => {
      el.style.display = (!q || el.dataset.metric.toLowerCase().indexOf(q) >= 0) ? '' : 'none';
    });
  }
  // sortCharts reorders the All Metrics cards to match the persisted order
  // (unknown/new metrics appended at the end). Called after every HTMX swap so
  // the server's order doesn't clobber the user's custom arrangement.
  function sortCharts(){
    const grid = document.getElementById('charts');
    if (!grid) return;
    const order = dashCfg.order.all_metrics || [];
    const cards = Array.from(grid.querySelectorAll('[data-metric]'));
    if (cards.length <= 1) return;
    const idx = name => { const i = order.indexOf(name); return i < 0 ? Infinity : i; };
    cards.sort((a, b) => idx(a.dataset.metric) - idx(b.dataset.metric));
    // Cards live inside the section's grid wrapper div, not directly under
    // #charts — re-append to their real parent to keep the grid intact.
    const parent = cards[0].parentNode;
    cards.forEach(c => parent.appendChild(c));
  }

  // Drag-and-drop for the All Metrics grid. Uses event delegation on the
  // persistent #charts container so listeners survive HTMX innerHTML swaps.
  let dragCard = null;
  function initMetricsDnD(){
    const grid = document.getElementById('charts');
    if (!grid) return;
    grid.addEventListener('dragstart', e => {
      const card = e.target.closest('[data-metric]');
      if (!card) return;
      dragCard = card;
      card.classList.add('opacity-40', 'ring-2', 'ring-emerald-500/60');
      e.dataTransfer.effectAllowed = 'move';
    });
    grid.addEventListener('dragend', e => {
      const card = e.target.closest('[data-metric]');
      if (card) { card.classList.remove('opacity-40', 'ring-2', 'ring-emerald-500/60'); }
      dragCard = null;
    });
    grid.addEventListener('dragover', e => {
      if (!dragCard) return;
      const target = e.target.closest('[data-metric]');
      if (!target || target === dragCard) return;
      e.preventDefault();
      const r = target.getBoundingClientRect();
      const after = (e.clientY - r.top) > r.height / 2;
      if (after) target.after(dragCard); else target.before(dragCard);
    });
    grid.addEventListener('drop', e => {
      if (!dragCard) return;
      e.preventDefault();
      saveOrder(metricNames());   // persist the new arrangement
    });
  }

  // After each HTMX swap: restore custom order, reapply search filter, refresh pins.
  document.body.addEventListener('htmx:afterSwap', () => { sortCharts(); applyFilter(); renderPinned(); applyHookOrder(); });

  function saveEpOrder(o){ dashCfg.order.endpoints = o; persistDash(); }

  // Per-endpoint latency chart: polls /metrics/endpoints (JSON), renders
  // p50/p95/p99 bars per endpoint, drag-to-reorder with persisted order.
  function endpointLatency(pollMs){
    return {
      raw: [],
      bump: 0,
      drag: null,
      init(){ this.load(); setInterval(() => { if (!document.hidden) this.load(); }, pollMs); },
      get endpoints(){
        this.bump; // reactive trigger for manual reorders
        const ord = dashCfg.order.endpoints || [];
        const key = e => e.method + ' ' + e.path;
        const idx = e => { const i = ord.indexOf(key(e)); return i < 0 ? Infinity : i; };
        return this.raw.slice().sort((a, b) => idx(a) - idx(b));
      },
      load(){
        fetch('/metrics/endpoints').then(r => r.json()).then(d => { this.raw = d || []; }).catch(() => {});
      },
      start(i){ this.drag = i; },
      drop(i){
        if (this.drag === null || this.drag === i) { this.drag = null; return; }
        const keys = this.endpoints.map(e => e.method + ' ' + e.path);
        const moved = keys.splice(this.drag, 1)[0];
        keys.splice(i, 0, moved);
        saveEpOrder(keys);
        this.drag = null;
        this.bump++;
      },
      // Calculate height relative to each endpoint's p99 (the highest bar)
      h(endpoint, value){
        const max = endpoint.p99 || 0.001;
        return Math.max(2, Math.round((value / max) * 100));
      },
      fmt(v){ if (v >= 1) return v.toFixed(2); if (v >= 0.01) return v.toFixed(3); return v.toFixed(4); }
    };
  }

  // Hooks section: provides drag-and-drop for hook metrics
  function hooksSection(pollMs){
    return {
      drag: null,
      init(){ this.setupDnD(); },
      start(i){ this.drag = i; },
      drop(i){
        if (this.drag === null || this.drag === i) { this.drag = null; return; }
        const grid = document.getElementById('hooks');
        const cards = Array.from(grid.querySelectorAll('[data-metric]'));
        if (this.drag < cards.length && i < cards.length) {
          const dragged = cards[this.drag];
          const target = cards[i];
          const parent = dragged.parentNode;
          const draggedNext = dragged.nextSibling;
          const targetNext = target.nextSibling;
          parent.insertBefore(dragged, targetNext);
          parent.insertBefore(target, draggedNext);
          // Save new order
          const names = Array.from(grid.querySelectorAll('[data-metric]')).map(el => el.dataset.metric);
          saveHookOrder(names);
        }
        this.drag = null;
      },
      setupDnD(){
        const grid = document.getElementById('hooks');
        if (!grid) return;
        // Apply persisted order on load
        applyHookOrder();
        // Setup drag-and-drop
        let dragCard = null;
        grid.addEventListener('dragstart', e => {
          const card = e.target.closest('[data-metric]');
          if (!card) return;
          dragCard = card;
          card.classList.add('opacity-40', 'ring-2', 'ring-emerald-500/60');
          e.dataTransfer.effectAllowed = 'move';
        });
        grid.addEventListener('dragend', e => {
          const card = e.target.closest('[data-metric]');
          if (card) { card.classList.remove('opacity-40', 'ring-2', 'ring-emerald-500/60'); }
          dragCard = null;
        });
        grid.addEventListener('dragover', e => {
          if (!dragCard) return;
          const target = e.target.closest('[data-metric]');
          if (!target || target === dragCard) return;
          e.preventDefault();
          const r = target.getBoundingClientRect();
          const after = (e.clientY - r.top) > r.height / 2;
          if (after) target.after(dragCard); else target.before(dragCard);
        });
        grid.addEventListener('drop', e => {
          if (!dragCard) return;
          e.preventDefault();
          const names = Array.from(grid.querySelectorAll('[data-metric]')).map(el => el.dataset.metric);
          saveHookOrder(names);
        });
      }
    };
  }

  // Hooks section ordering helpers, backed by the dashboard config document.
  function saveHookOrder(names){ dashCfg.order.hooks = names; persistDash(); }
  // Re-apply the persisted hook order to the (server-rendered) hooks grid.
  // Called after HTMX swaps so the server's order doesn't clobber the user's.
  function applyHookOrder(){
    const grid = document.getElementById('hooks');
    if (!grid) return;
    const saved = dashCfg.order.hooks || [];
    if (saved.length === 0) return;
    const cards = Array.from(grid.querySelectorAll('[data-metric]'));
    if (!cards.length) return;
    const idx = name => { const i = saved.indexOf(name); return i < 0 ? Infinity : i; };
    cards.sort((a, b) => idx(a.dataset.metric) - idx(b.dataset.metric));
    // Cards live inside the section's grid wrapper div, not directly under
    // #hooks — re-append to their real parent to keep the grid intact.
    const parent = cards[0].parentNode;
    cards.forEach(c => parent.appendChild(c));
  }

  // Cardinality panel: polls /metrics/cardinality and shows per-metric series stats.
  function cardinalityPanel(pollMs, maxSeries){
    return {
      open: false,
      metrics: [],
      maxSeries: maxSeries,
      init(){ this.load(); setInterval(() => { if (this.open && !document.hidden) this.load(); }, pollMs); },
      load(){
        fetch('/metrics/cardinality').then(r => r.json()).then(d => { this.metrics = d || []; }).catch(() => {});
      },
      get totalSeries(){ return this.metrics.reduce((s, m) => s + m.series, 0); },
      get totalDropped(){ return this.metrics.reduce((s, m) => s + m.dropped, 0); },
      get metricCount(){ return this.metrics.length; },
      usagePct(m){
        if (!m.max_series) return 0;
        return Math.round((m.series / m.max_series) * 100);
      }
    };
  }

  // Boot: apply persisted All Metrics order + wire up its drag-and-drop, then
  // load the server-side dashboard layout document (falls back to the
  // localStorage boot state when unavailable).
  document.addEventListener('DOMContentLoaded', () => { sortCharts(); applyFilter(); initMetricsDnD(); loadDash(); });

  // Pause all polling while the tab is hidden; resume on return. HTMX polling
  // checks body.dataset.hidden; the Alpine components check document.hidden.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) { document.body.dataset.hidden = '1'; } else { delete document.body.dataset.hidden; }
  });

  // Cooldown ring: depletes over the poll interval, shows remaining seconds,
  // resets on each HTMX refresh, and freezes while paused. Turns red when the
  // previous refresh failed, green when successful.
  (function(){
    const pollMs = {{.PollMs}};
    const ring = document.getElementById('cooldown-ring');
    const label = document.getElementById('cooldown-label');
    if (!ring || !label || !pollMs) return;
    const r = 14;
    const circ = 2 * Math.PI * r;
    ring.style.strokeDasharray = circ;
    ring.style.strokeDashoffset = 0;
    let start = performance.now();
    let pausedAt = 0;
    function frame(now){
      if (document.body.dataset.paused) {
        if (!pausedAt) { pausedAt = now; label.textContent = 'II'; }
      } else {
        if (pausedAt) { start += now - pausedAt; pausedAt = 0; }
        const remaining = pollMs - (now - start);
        const frac = Math.max(0, Math.min(1, remaining / pollMs)); // 1 → 0
        ring.style.strokeDashoffset = circ * (1 - frac);
        label.textContent = Math.max(0, Math.ceil(remaining / 1000));
      }
      requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
    document.body.addEventListener('htmx:afterRequest', (evt) => {
      start = performance.now();
      const ok = evt.detail.successful;
      ring.setAttribute('stroke', ok ? '#34d399' : '#ef4444');
    });
  })();

  // --- Runtime Health: time-series charts ---
  function fmtBytes(b) {
    if (b < 1024) return b.toFixed(0) + ' B';
    if (b < 1024*1024) return (b/1024).toFixed(1) + ' KB';
    if (b < 1024*1024*1024) return (b/(1024*1024)).toFixed(1) + ' MB';
    return (b/(1024*1024*1024)).toFixed(2) + ' GB';
  }
  function fmtCount(n) {
    if (n < 1000) return n.toString();
    if (n < 1e6) return (n/1e3).toFixed(1) + 'K';
    return (n/1e6).toFixed(1) + 'M';
  }

  function drawChart(canvasId, series, opts) {
    const canvas = document.getElementById(canvasId);
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    canvas.width = rect.width * dpr;
    canvas.height = rect.height * dpr;
    ctx.scale(dpr, dpr);
    const W = rect.width, H = rect.height;
    const pad = {top:8, right:opts.padRight||8, bottom:20, left:50};
    const cw = W - pad.left - pad.right;
    const ch = H - pad.top - pad.bottom;

    ctx.clearRect(0, 0, W, H);
    if (!series.length || !series[0].points.length) {
      ctx.fillStyle = '#475569';
      ctx.font = '11px JetBrains Mono, monospace';
      ctx.textAlign = 'center';
      ctx.fillText('no data yet', W/2, H/2);
      return;
    }

    // compute global min/max across all series
    let gMin = Infinity, gMax = -Infinity;
    series.forEach(s => s.points.forEach(p => {
      if (p.min !== undefined) gMin = Math.min(gMin, p.min);
      if (p.max !== undefined) gMax = Math.max(gMax, p.max);
      gMin = Math.min(gMin, p.v);
      gMax = Math.max(gMax, p.v);
    }));
    if (gMin === gMax) { gMin *= 0.9; gMax *= 1.1; }
    if (gMin === 0 && gMax === 0) { gMax = 1; }
    const range = gMax - gMin;

    // time range
    let tMin = Infinity, tMax = -Infinity;
    series[0].points.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
    const tRange = tMax - tMin || 1;

    function xPos(t) { return pad.left + ((t - tMin) / tRange) * cw; }
    function yPos(v) { return pad.top + ch - ((v - gMin) / range) * ch; }

    // grid lines
    ctx.strokeStyle = '#1e293b';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = pad.top + (ch * i / 4);
      ctx.beginPath(); ctx.moveTo(pad.left, y); ctx.lineTo(pad.left + cw, y); ctx.stroke();
    }

    // Y-axis labels
    ctx.fillStyle = '#475569';
    ctx.font = '10px JetBrains Mono, monospace';
    ctx.textAlign = 'right';
    for (let i = 0; i <= 4; i++) {
      const v = gMax - (range * i / 4);
      const y = pad.top + (ch * i / 4);
      ctx.fillText((opts.fmtY || fmtCount)(v), pad.left - 6, y + 3);
    }

    // X-axis labels
    ctx.textAlign = 'center';
    const steps = Math.min(series[0].points.length, 6);
    for (let i = 0; i <= steps; i++) {
      const t = tMin + (tRange * i / steps);
      const x = xPos(t);
      const d = new Date(t * 1000);
      const label = d.getHours().toString().padStart(2,'0') + ':' + d.getMinutes().toString().padStart(2,'0');
      ctx.fillText(label, x, pad.top + ch + 14);
    }

    // draw each series
    const colors = ['#34d399', '#38bdf8', '#f59e0b', '#f472b6'];
    series.forEach((s, si) => {
      const color = colors[si % colors.length];
      const pts = s.points;
      if (!pts.length) return;

      // min/max band
      if (pts[0].min !== undefined) {
        ctx.fillStyle = color + '15';
        ctx.beginPath();
        ctx.moveTo(xPos(pts[0].t), yPos(pts[0].max));
        pts.forEach(p => ctx.lineTo(xPos(p.t), yPos(p.max)));
        for (let i = pts.length - 1; i >= 0; i--) ctx.lineTo(xPos(pts[i].t), yPos(pts[i].min));
        ctx.closePath();
        ctx.fill();
      }

      // line
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      pts.forEach((p, i) => {
        const x = xPos(p.t), y = yPos(p.v);
        if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      });
      ctx.stroke();

      // end dot
      const last = pts[pts.length - 1];
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.arc(xPos(last.t), yPos(last.v), 3, 0, Math.PI * 2);
      ctx.fill();
    });
  }

  function runtimeHealth(pollMs) {
    const metrics = [
      {key:'go_memstats_sys_bytes'},
      {key:'go_memstats_heap_inuse_bytes'},
      {key:'go_memstats_alloc_bytes'},
      {key:'go_memstats_heap_objects'},
      {key:'cpu_usage_percent'},
      {key:'go_goroutines'}
    ];
    return {
      duration: '1h',
      series: {},
      currentSysBytes: 0, currentHeapInuse: 0, currentMem: 0, currentHeapObj: 0, cpuPct: 0, currentGoro: 0,
      drag: null,
      init() { this.refresh(); setInterval(() => { if (!document.hidden) this.refresh(); }, pollMs); },
      start(i){ this.drag = i; },
      drop(i){
        if(this.drag === null || this.drag === i){ this.drag = null; return; }
        // Reorder the runtime charts by moving the dragged card relative to
        // the target card. Selecting .rt-card (not every nested div) keeps the
        // wired 0..2 indexes aligned with the actual cards, and after/before
        // move nodes cleanly even when the two cards live in different
        // containers (the memory card is a direct child of #runtime-charts,
        // the CPU/goroutine cards sit inside the inner grid).
        const cards = document.getElementById('runtime-charts').querySelectorAll('.rt-card');
        if(this.drag < cards.length && i < cards.length){
          const dragged = cards[this.drag];
          const target = cards[i];
          if (this.drag < i) { target.after(dragged); } else { target.before(dragged); }
        }
        this.drag = null;
      },
      refresh() {
        metrics.forEach(m => {
          fetch('/metrics/history?name=' + m.key + '&duration=' + this.duration)
            .then(r => r.json())
            .then(d => {
              this.series[m.key] = d.points || [];
              // update current values from last point
              if (d.points && d.points.length) {
                const last = d.points[d.points.length - 1].v;
                if (m.key === 'go_memstats_sys_bytes') this.currentSysBytes = last;
                if (m.key === 'go_memstats_heap_inuse_bytes') this.currentHeapInuse = last;
                if (m.key === 'go_memstats_alloc_bytes') this.currentMem = last;
                if (m.key === 'go_memstats_heap_objects') this.currentHeapObj = last;
                if (m.key === 'cpu_usage_percent') this.cpuPct = last;
                if (m.key === 'go_goroutines') this.currentGoro = Math.round(last);
              }
              this.draw();
            }).catch(() => {});
        });
        // also fetch current snapshot for live values
        fetch('/metrics/data?format=json').then(r => r.json()).then(snaps => {
          (snaps||[]).forEach(s => {
            if (s.name === 'go_memstats_sys_bytes') this.currentSysBytes = s.value;
            if (s.name === 'go_memstats_heap_inuse_bytes') this.currentHeapInuse = s.value;
            if (s.name === 'go_memstats_alloc_bytes') this.currentMem = s.value;
            if (s.name === 'go_memstats_heap_objects') this.currentHeapObj = s.value;
            if (s.name === 'cpu_usage_percent') this.cpuPct = s.value;
            if (s.name === 'go_goroutines') this.currentGoro = Math.round(s.value);
          });
        }).catch(() => {});
      },
      draw() {
        // Memory: all 4 metrics (3 byte metrics on left axis, objects on right axis)
        const memByteSeries = [
          {points: this.series['go_memstats_sys_bytes']||[]},
          {points: this.series['go_memstats_heap_inuse_bytes']||[]},
          {points: this.series['go_memstats_alloc_bytes']||[]}
        ];
        drawChart('rt-mem', memByteSeries, {fmtY: fmtBytes, padRight: 50});
        // overlay heap objects on same canvas with right axis
        const heapObjPts = this.series['go_memstats_heap_objects']||[];
        if (heapObjPts.length) {
          const canvas = document.getElementById('rt-mem');
          if (canvas) {
            const ctx = canvas.getContext('2d');
            const rect = canvas.getBoundingClientRect();
            const W = rect.width, H = rect.height;
            const pad = {top:8, right:50, bottom:20, left:50};
            const cw = W - pad.left - pad.right;
            const ch = H - pad.top - pad.bottom;
            let tMin = Infinity, tMax = -Infinity;
            memByteSeries.forEach(s => s.points.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); }));
            heapObjPts.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
            const tRange = tMax - tMin || 1;
            let gMin = Infinity, gMax = -Infinity;
            heapObjPts.forEach(p => { gMin = Math.min(gMin, p.v); gMax = Math.max(gMax, p.v); });
            if (gMin === gMax) { gMin *= 0.9; gMax *= 1.1; }
            const range = gMax - gMin;
            function xP(t) { return pad.left + ((t - tMin) / tRange) * cw; }
            function yP(v) { return pad.top + ch - ((v - gMin) / range) * ch; }
            ctx.strokeStyle = '#f59e0b';
            ctx.lineWidth = 1.5;
            ctx.setLineDash([4, 3]);
            ctx.beginPath();
            heapObjPts.forEach((p, i) => { i===0 ? ctx.moveTo(xP(p.t), yP(p.v)) : ctx.lineTo(xP(p.t), yP(p.v)); });
            ctx.stroke();
            ctx.setLineDash([]);
            ctx.fillStyle = '#f59e0b';
            ctx.font = '10px JetBrains Mono, monospace';
            ctx.textAlign = 'left';
            for (let i = 0; i <= 4; i++) {
              const v = gMax - (range * i / 4);
              ctx.fillText(fmtCount(v), pad.left + cw + 4, pad.top + (ch * i / 4) + 3);
            }
          }
        }
        // CPU + goroutines (dual axis)
        const cpuPts = this.series['cpu_usage_percent']||[];
        const goroPts = this.series['go_goroutines']||[];
        drawChart('rt-cpu', [{points:cpuPts}], {fmtY: v => v.toFixed(0)+'%', padRight: 40});
        // overlay goroutines on same canvas
        if (goroPts.length) {
          const canvas = document.getElementById('rt-cpu');
          if (canvas) {
            const ctx = canvas.getContext('2d');
            const rect = canvas.getBoundingClientRect();
            const W = rect.width, H = rect.height;
            const pad = {top:8, right:40, bottom:20, left:50};
            const cw = W - pad.left - pad.right;
            const ch = H - pad.top - pad.bottom;
            let tMin = Infinity, tMax = -Infinity;
            cpuPts.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
            goroPts.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
            const tRange = tMax - tMin || 1;
            let gMin = Infinity, gMax = -Infinity;
            goroPts.forEach(p => { gMin = Math.min(gMin, p.v); gMax = Math.max(gMax, p.v); });
            if (gMin === gMax) { gMin *= 0.9; gMax *= 1.1; }
            const range = gMax - gMin;
            function xP(t) { return pad.left + ((t - tMin) / tRange) * cw; }
            function yP(v) { return pad.top + ch - ((v - gMin) / range) * ch; }
            ctx.strokeStyle = '#38bdf8';
            ctx.lineWidth = 1.5;
            ctx.setLineDash([4, 3]);
            ctx.beginPath();
            goroPts.forEach((p, i) => { i===0 ? ctx.moveTo(xP(p.t), yP(p.v)) : ctx.lineTo(xP(p.t), yP(p.v)); });
            ctx.stroke();
            ctx.setLineDash([]);
            ctx.fillStyle = '#38bdf8';
            ctx.font = '10px JetBrains Mono, monospace';
            ctx.textAlign = 'left';
            for (let i = 0; i <= 4; i++) {
              const v = gMax - (range * i / 4);
              ctx.fillText(Math.round(v).toString(), pad.left + cw + 4, pad.top + (ch * i / 4) + 3);
            }
          }
        }
        // Goroutines
        drawChart('rt-goro', [{points: this.series['go_goroutines']||[]}], {fmtY: v => Math.round(v).toString()});
      }
    };
  }

  // Histogram Time-Series component
  function histogramTimeSeries(pollMs) {
    return {
      pollMs: pollMs,
      duration: '1h',
      histogramMetrics: [],
      timer: null,

      init() {
        this.refresh();
        this.timer = setInterval(() => { if (!document.hidden) this.refresh(); }, this.pollMs);
      },

      async refresh() {
        try {
          // Fetch all histogram metrics from current data
          const dataResp = await fetch('/metrics/data?format=json');
          const metrics = await dataResp.json();
          const histogramCards = (metrics || []).filter(c => c.type === 'histogram');

          // Build histogram metrics list
          this.histogramMetrics = histogramCards.map(card => ({
            name: card.name,
            description: card.description || '',
            bounds: (card.buckets || []).map(b => b.upper_bound).filter(b => b > 0) // Extract bounds from buckets
          }));
          if (!this.histogramMetrics.length) return;

          // One batch request for every histogram series — the N+1 sequential
          // fetches this loop used to make are served by /metrics/history?names=.
          const names = this.histogramMetrics.map(m => m.name).join(',');
          const histResp = await fetch('/metrics/history?names=' + encodeURIComponent(names) + '&duration=' + this.duration);
          const histories = await histResp.json();

          for (const metric of this.histogramMetrics) {
            const history = (histories || {})[metric.name] || {};
            metric.points = history.points || [];
            metric.histogramBounds = history.histogram_bounds || [];

            // Render stacked area chart
            this.renderHistogramChart(metric);
          }
        } catch (e) {
          console.error('Failed to fetch histogram time-series:', e);
        }
      },

      renderHistogramChart(metric) {
        const canvasId = 'hist-ts-' + metric.name;
        const canvas = document.getElementById(canvasId);
        if (!canvas || !metric.points.length) return;

        const ctx = canvas.getContext('2d');
        const rect = canvas.getBoundingClientRect();
        const W = rect.width, H = rect.height;

        // Handle high-DPI displays
        const dpr = window.devicePixelRatio || 1;
        canvas.width = W * dpr;
        canvas.height = H * dpr;
        ctx.scale(dpr, dpr);

        const pad = {top: 15, right: 20, bottom: 30, left: 45};
        const cw = W - pad.left - pad.right;
        const ch = H - pad.top - pad.bottom;

        // Clear canvas
        ctx.clearRect(0, 0, W, H);

        // Find time range
        let tMin = Infinity, tMax = -Infinity;
        metric.points.forEach(p => {
          tMin = Math.min(tMin, p.t);
          tMax = Math.max(tMax, p.t);
        });
        const tRange = tMax - tMin || 1;

        // Find max bucket count across all buckets for Y-axis scaling
        let maxCount = 0;
        metric.points.forEach(p => {
          if (p.histogram_buckets) {
            p.histogram_buckets.forEach(b => {
              maxCount = Math.max(maxCount, b.Count);
            });
          }
        });
        if (maxCount === 0) maxCount = 1;

        // Purple color palette for buckets
        const bucketColors = [
          'rgba(168, 85, 247, 0.7)',   // purple-500
          'rgba(139, 92, 246, 0.7)',   // violet-500
          'rgba(124, 58, 237, 0.7)',   // violet-600
          'rgba(109, 40, 217, 0.7)',   // violet-700
          'rgba(88, 28, 135, 0.7)',    // violet-900
          'rgba(192, 132, 252, 0.7)',  // purple-400
          'rgba(216, 180, 254, 0.7)',  // purple-300
        ];

        // Helper functions
        function xP(t) { return pad.left + ((t - tMin) / tRange) * cw; }
        function yP(v) { return pad.top + ch - (v / maxCount) * ch; }

        // Draw stacked area chart for each bucket
        const numBuckets = metric.histogramBounds.length;
        for (let bucketIdx = 0; bucketIdx < numBuckets; bucketIdx++) {
          const bound = metric.histogramBounds[bucketIdx];
          const color = bucketColors[bucketIdx % bucketColors.length];

          ctx.beginPath();
          ctx.moveTo(xP(tMin), yP(0));

          // Build the stacked area
          for (const pt of metric.points) {
            if (!pt.histogram_buckets || !pt.histogram_buckets[bucketIdx]) continue;
            const bucketData = pt.histogram_buckets[bucketIdx];
            let cumulativeCount = 0;

            // Sum counts for this bucket and all below it
            for (let i = 0; i <= bucketIdx; i++) {
              if (pt.histogram_buckets[i]) {
                cumulativeCount += pt.histogram_buckets[i].Count;
              }
            }
            ctx.lineTo(xP(pt.t), yP(cumulativeCount));
          }

          // Close the path
          ctx.lineTo(xP(tMax), yP(0));
          ctx.closePath();

          ctx.fillStyle = color;
          ctx.fill();
        }

        // Draw axes
        ctx.strokeStyle = '#475569';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(pad.left, pad.top);
        ctx.lineTo(pad.left, pad.top + ch);
        ctx.lineTo(pad.left + cw, pad.top + ch);
        ctx.stroke();

        // Draw Y-axis labels
        ctx.fillStyle = '#64748b';
        ctx.font = '10px JetBrains Mono, monospace';
        ctx.textAlign = 'right';
        ctx.textBaseline = 'middle';
        for (let i = 0; i <= 4; i++) {
          const v = Math.round(maxCount * (1 - i / 4));
          ctx.fillText(v.toString(), pad.left - 8, pad.top + (ch * i / 4));
        }

        // Draw X-axis labels (time)
        ctx.textAlign = 'center';
        ctx.textBaseline = 'top';
        const timeLabels = 5;
        for (let i = 0; i < timeLabels; i++) {
          const t = tMin + (tRange * i / (timeLabels - 1));
          const x = pad.left + (cw * i / (timeLabels - 1));
          const date = new Date(t * 1000);
          const label = date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false });
          ctx.fillText(label, x, pad.top + ch + 8);
        }
      },

      getBucketColor(index) {
        const colors = [
          'text-purple-400', 'text-violet-400', 'text-violet-500',
          'text-violet-600', 'text-violet-700', 'text-purple-300'
        ];
        return colors[index % colors.length];
      },

      formatBound(bound) {
        if (bound >= 1) return bound.toFixed(0);
        if (bound >= 0.01) return bound.toFixed(2);
        return bound.toFixed(3);
      }
    };
  }

  // Dashboard toast notification
  var _toastTimer = null;
  function showToast(msg, type){
    var el = document.getElementById('dash-toast');
    if (!el) return;
    el.textContent = msg;
    el.className = 'fixed bottom-6 right-6 z-40 max-w-sm px-4 py-2.5 rounded-lg text-sm font-mono shadow-lg border ' +
      (type === 'success' ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-400' : 'bg-rose-500/10 border-rose-500/30 text-rose-400');
    el.style.display = 'block';
    if (_toastTimer) clearTimeout(_toastTimer);
    _toastTimer = setTimeout(function(){ el.style.display = 'none'; }, 4000);
  }
</script>

<!-- Toast -->
<div id="dash-toast" style="display:none"
     class="fixed bottom-6 right-6 z-40 max-w-sm px-4 py-2.5 rounded-lg text-sm font-mono shadow-lg border"></div>

</body>
</html>`

const cardsSrc = `{{define "cards"}}
{{if not .Cards}}
<div class="text-sm text-slate-500 font-mono py-12 text-center">No metrics recorded yet.</div>
{{end}}
<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
{{range .Cards}}
{{$c := .}}
{{$hist := eq .Snapshot.Type "histogram"}}
{{$http := .HTTPColumns}}
<div data-metric="{{.Snapshot.Name}}" draggable="true"
     class="bg-slate-900 border border-slate-800 rounded-xl p-5 shadow-sm hover:border-slate-700 transition flex flex-col justify-between cursor-grab active:cursor-grabbing {{if $hist}}md:col-span-2{{end}} {{if $http}}md:col-span-2 lg:col-span-3{{end}}">
  <div>
    <div class="flex items-center justify-between mb-2 gap-2">
      <span class="text-[10px] font-mono font-semibold px-2 py-0.5 rounded-full border {{.BadgeClass}}">{{.Badge}}</span>
      <span class="text-xs text-slate-500 font-mono truncate">{{.Snapshot.Name}}</span>
    </div>
    <p class="text-sm text-slate-400">{{or .Snapshot.Description "—"}}</p>
  </div>

  {{if $http}}
    <!-- HTTP request status column chart -->
    <div class="flex items-end justify-between h-32 gap-3 mt-4 pb-1">
      {{range .HTTPColumns}}
      <div class="flex-1 flex flex-col items-center justify-end h-full">
        <div class="w-full rounded-t {{.ColorClass}}" style="height:{{.HeightPct}}%"></div>
        <span class="text-[10px] font-mono text-slate-600 mt-1 truncate">{{.Label}}</span>
        <span class="text-[10px] font-mono text-slate-400">{{.DisplayValue}}</span>
      </div>
      {{end}}
    </div>
  {{else if $hist}}
    <!-- histogram: CSS flexbox bar chart -->
    <div class="flex items-end justify-between h-28 gap-1 border-b border-slate-800 mt-4 pb-1">
      {{range .Bars}}
      <div class="w-full rounded-t group relative cursor-pointer transition-all {{$c.BarClass}}" style="height:{{.HeightPct}}%">
        <div class="opacity-0 group-hover:opacity-100 absolute -top-7 left-1/2 -translate-x-1/2 bg-slate-800 text-[10px] px-1.5 py-0.5 rounded font-mono whitespace-nowrap z-10 transition-opacity">{{.Label}}</div>
      </div>
      {{end}}
    </div>
    <div class="flex justify-between text-[10px] font-mono text-slate-600 mt-1">
      {{range .Bars}}<span class="truncate">{{.Axis}}</span>{{end}}
    </div>
    <div class="flex justify-between text-[11px] font-mono text-slate-400 mt-2">
      <span>count <span class="text-slate-200">{{.Snapshot.Count}}</span></span>
      <span>sum <span class="text-slate-200">{{fmt .Snapshot.Sum}}s</span></span>
      <span>avg <span class="text-slate-200">{{fmt .Snapshot.Avg}}s</span></span>
    </div>
  {{else if eq .Snapshot.Type "gauge"}}
    <!-- gauge: semi-circle with red/green zones -->
    <div class="mt-4">
      <div class="relative w-40 h-20 mx-auto overflow-hidden">
        <!-- Semi-circle gauge -->
        <div class="absolute inset-0">
          <svg viewBox="0 0 100 50" class="w-full h-full">
            <!-- Background arc -->
            <path d="M 10 50 A 40 40 0 0 1 90 50" fill="none" stroke="#1e293b" stroke-width="8"/>
            <!-- Red zone (0-50%) -->
            <path d="M 10 50 A 40 40 0 0 1 50 10" fill="none" stroke="#ef4444" stroke-width="8" stroke-dasharray="62.83" stroke-dashoffset="0"/>
            <!-- Green zone (50-100%) -->
            <path d="M 50 10 A 40 40 0 0 1 90 50" fill="none" stroke="#22c55e" stroke-width="8" stroke-dasharray="62.83" stroke-dashoffset="0"/>
            <!-- Needle -->
            <line x1="50" y1="50" x2="50" y2="10" stroke="white" stroke-width="2" transform="rotate({{if .NeedlePct}}{{.NeedlePct}}{{else}}0{{end}} 50 50)" style="transform-origin: 50px 50px;"/>
            <!-- Center dot -->
            <circle cx="50" cy="50" r="3" fill="white"/>
          </svg>
        </div>
      </div>
      <div class="flex items-baseline mt-2 justify-center">
        <span class="text-4xl font-semibold tracking-tight text-white font-mono">{{.DisplayValue}}</span>
        {{if .Unit}}<span class="text-xs text-slate-500 ml-1.5 font-mono">{{.Unit}}</span>{{end}}
      </div>
    </div>
    {{if .Snapshot.LabelValues}}
    <div class="mt-3 flex flex-wrap gap-1">
      {{range .Snapshot.LabelValues}}<span class="text-[10px] font-mono px-1.5 py-0.5 bg-slate-800 text-slate-400 rounded">{{.Name}}={{.Value}}</span>{{end}}
    </div>
  {{end}}
  {{else}}
    <!-- counter: simple number display -->
    <div class="mt-4 flex items-baseline">
      <span class="text-4xl font-semibold tracking-tight text-white font-mono">{{.DisplayValue}}</span>
      {{if .Unit}}<span class="text-xs text-slate-500 ml-1.5 font-mono">{{.Unit}}</span>{{end}}
    </div>
    {{if .Snapshot.LabelValues}}
    <div class="mt-3 flex flex-wrap gap-1">
      {{range .Snapshot.LabelValues}}<span class="text-[10px] font-mono px-1.5 py-0.5 bg-slate-800 text-slate-400 rounded">{{.Name}}={{.Value}}</span>{{end}}
    </div>
    {{end}}
  {{end}}
</div>
{{end}}
</div>
{{end}}`

var (
	dashboardTpl = template.Must(template.New("dashboard").Funcs(tmplFuncs).Parse(dashboardSrc + cardsSrc))
	chartsTpl    = template.Must(template.New("cards").Funcs(tmplFuncs).Parse(cardsSrc))
)
