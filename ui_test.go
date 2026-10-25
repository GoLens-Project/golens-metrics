package golens

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMountUIRegistersRoutes(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "GoLens") || !strings.Contains(body, "discovery dashboard") {
		t.Error("dashboard shell not rendered")
	}
	// The dashboard ships vendored assets served from /metrics/static/ so it
	// renders with no CDN or network dependency (air-gapped safe).
	for _, asset := range []string{
		"/metrics/static/styles.css",
		"/metrics/static/htmx.min.js",
		"/metrics/static/common.js",
		"/metrics/static/dashboard.js",
		"/metrics/static/alpine.min.js",
	} {
		if !strings.Contains(body, asset) {
			t.Errorf("vendored asset %s not referenced", asset)
		}
	}
	for _, cdn := range []string{"cdn.tailwindcss.com", "unpkg.com", "fonts.googleapis.com"} {
		if strings.Contains(body, cdn) {
			t.Errorf("dashboard still references external CDN %s", cdn)
		}
	}
	if !strings.Contains(body, "hx-trigger") {
		t.Error("HTMX polling trigger missing")
	}
	if !strings.Contains(body, "grid-cards") {
		t.Error("responsive card grid missing")
	}
	// Accessibility floor of the shell: the toast announces changes and the
	// search input is labeled (it has no visible label element).
	if !strings.Contains(body, `aria-live="polite"`) {
		t.Error("toast missing aria-live")
	}
	if !strings.Contains(body, `aria-label="Search metrics"`) {
		t.Error("search input missing aria-label")
	}
	// Note: HTTP Request card only appears when there's actual HTTP traffic
	// This test doesn't make HTTP requests, so we just check the basic UI structure
}

// Every reorderable surface (All Metrics, Hooks, Endpoint Latency, Runtime
// Health, section blocks, pinned panels) must render keyboard/touch-reachable
// move buttons and move through the one shared moveCard helper in
// dashboard.js — not six divergent drag implementations. The cards' pin
// button is the other header affordance. Pinned-panel slots are client-
// rendered (makeSlot in dashboard.js), so their buttons are asserted there.
func TestDashboardSharedReorderPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RuntimeMetrics.Enabled = true // renders the Runtime Health cards
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// One plain metric so the All Metrics grid renders a real card.
	r.Register("app_queue_depth", GaugeType, "queue depth", nil, nil, 0, 0)
	r.Record("app_queue_depth", 3)
	waitForDrain(r)

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("dashboard status = %d", rec.Code)
	}
	body := rec.Body.String()

	// All Metrics and Hooks share the cards template: move buttons hooked to
	// the grid entry point on both directions.
	if !strings.Contains(body, "moveGridCard(this,-1)") || !strings.Contains(body, "moveGridCard(this,1)") {
		t.Error("All Metrics/Hooks cards missing move buttons")
	}
	// Section blocks carry edit-mode move buttons hooked to the section path.
	if !strings.Contains(body, "moveSectionItem(this,-1)") || !strings.Contains(body, "moveSectionItem(this,1)") {
		t.Error("section blocks missing move buttons")
	}
	// Runtime Health cards (Alpine component scope).
	if !strings.Contains(body, `"move($event,-1)"`) || !strings.Contains(body, `"move($event,1)"`) {
		t.Error("Runtime Health cards missing move buttons")
	}
	// Endpoint Latency cards render through the same markup family.
	if !strings.Contains(body, `"move(idx,-1)"`) || !strings.Contains(body, `"move(idx,1)"`) {
		t.Error("Endpoint Latency missing move buttons")
	}
	// Move button group class on every server-rendered surface.
	if !strings.Contains(body, "move-btns") {
		t.Error("move button group class missing")
	}
	// Pin affordance on unpinned cards (hooks cards are hook-scoped, never
	// pinnable).
	if !strings.Contains(body, "pinCard(this)") {
		t.Error("All Metrics cards missing pin button")
	}

	js := httptest.NewRecorder()
	mux.ServeHTTP(js, httptest.NewRequest("GET", "/metrics/static/dashboard.js", nil))
	if js.Code != 200 {
		t.Fatalf("dashboard.js status = %d", js.Code)
	}
	script := js.Body.String()
	if n := strings.Count(script, "function moveCard("); n != 1 {
		t.Errorf("dashboard.js defines moveCard %d times, want exactly one shared implementation", n)
	}
	// One definition plus the surface entry points calling it (All
	// Metrics/Hooks grid, section items, endpoints, runtime).
	if n := strings.Count(script, "moveCard("); n < 5 {
		t.Errorf("moveCard referenced %d times, want at least 5", n)
	}
	// Pinned-panel slots (My Charts) are client-rendered; their move buttons
	// share the section-item path.
	if !strings.Contains(script, "moveSectionItem(this,-1)") || !strings.Contains(script, "moveSectionItem(this,1)") {
		t.Error("pinned-panel slots missing move buttons in dashboard.js")
	}
}

func TestMountUIDisabledDoesNothing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	mux := http.NewServeMux()
	r.MountUI(mux)
	// No routes registered -> default 404
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("UI disabled should not register routes; got %d", rec.Code)
	}
}

// The static handler serves the embedded assets but rejects directory paths
// (the bare prefix, trailing slashes) so the asset names are never listed.
func TestStaticHandlerRejectsDirectoryPaths(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	mux := http.NewServeMux()
	r.MountUI(mux)

	for _, path := range []string{"/metrics/static/", "/metrics/static/sub/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404 (no directory listing)", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/static/styles.css", nil))
	if rec.Code != 200 {
		t.Errorf("styles.css status = %d, want 200", rec.Code)
	}
}

func TestMetricsDataJSON(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	r.Record("http_requests_total", 7)
	waitForDrain(r)

	mux := http.NewServeMux()
	r.MountUI(mux)

	req := httptest.NewRequest("GET", "/metrics/data", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var snaps []MetricSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snaps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, s := range snaps {
		if s.Name == "http_requests_total" && s.Value == 7 {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot not in JSON output: %+v", snaps)
	}
}

func TestMetricsDataHTMLFragment(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	r.Record("http_requests_total", 7)
	waitForDrain(r)

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/data", nil))
	if !strings.Contains(rec.Body.String(), "http_requests_total") {
		t.Errorf("HTML fragment missing metric name: %q", rec.Body.String())
	}
}

func TestMetricsDataSingleMetric(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/data?metric=http_requests_total", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var s MetricSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Name != "http_requests_total" {
		t.Errorf("name = %q", s.Name)
	}
}

func TestMetricsDataUnknownMetric404(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/data?metric=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The overflow bucket serializes upper_bound 0 as its marker, so clients
// must be able to tell it apart from a legitimately registered 0 bound by
// the overflow flag alone. That flag is therefore serialized explicitly —
// present and false on finite buckets, never omitted.
func TestMetricsDataOverflowFlagSerialization(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	r.Register("hist_overflow", HistogramType, "hist", nil, []float64{0.5, 1}, 0, 0)
	r.Record("hist_overflow", 0.25)
	r.Record("hist_overflow", 10) // lands in the overflow bucket
	waitForDrain(r)

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics/data?metric=hist_overflow", nil)
	req.Header.Set("Accept", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}

	var snap struct {
		Buckets []map[string]interface{} `json:"buckets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(snap.Buckets))
	}

	overflowSeen := false
	for _, b := range snap.Buckets {
		flag, ok := b["overflow"]
		if !ok {
			t.Fatalf("bucket %v: overflow flag missing from JSON — clients could not filter on it", b)
		}
		isOverflow, _ := flag.(bool)
		if !isOverflow {
			continue
		}
		overflowSeen = true
		if b["upper_bound"] != float64(0) {
			t.Errorf("overflow bucket upper_bound = %v, want 0", b["upper_bound"])
		}
		if b["count"] != float64(1) {
			t.Errorf("overflow bucket count = %v, want 1", b["count"])
		}
	}
	if !overflowSeen {
		t.Error("no bucket carried overflow=true")
	}
}

func TestFormatFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.23456789, "1.23"},
		{50, "50"},
		{0.5, "0.50"},
		{42, "42"},
	}
	for _, c := range cases {
		if got := formatFloat(c.in); got != c.want {
			t.Errorf("formatFloat(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtBound(t *testing.T) {
	cases := map[float64]string{
		0.005: "0.005",
		0.01:  "0.01",
		0.025: "0.025",
		0.1:   "0.1",
		1:     "1",
		2.5:   "2.5",
		10:    "10",
	}
	for in, want := range cases {
		if got := fmtBound(in); got != want {
			t.Errorf("fmtBound(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWantsJSON(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "application/json")
	if !wantsJSON(req) {
		t.Error("application/json Accept should be JSON")
	}
	req2 := httptest.NewRequest("GET", "/?format=json", nil)
	if !wantsJSON(req2) {
		t.Error("?format=json should be JSON")
	}
	req3 := httptest.NewRequest("GET", "/", nil)
	if wantsJSON(req3) {
		t.Error("plain request should not be JSON")
	}
}

// --- Utility function tests ---

func TestFormatMetricValue(t *testing.T) {
	tests := []struct {
		name string
		val  float64
		want string
	}{
		{"go_memstats_alloc_bytes", 1048576, "1.0 MB"},
		{"go_memstats_sys_bytes", 512, "512 B"},
		{"go_memstats_heap_inuse_bytes", 2621440, "2.5 MB"},
		{"go_memstats_heap_objects", 10240, "10.0 KB"},
		{"http_requests_total", 500, "500"},
		{"http_requests_total", 1500, "1.5K"},
		{"http_requests_total", 2500000, "2.5M"},
		{"custom_gauge", 42.5, "42.50"},
	}
	for _, tc := range tests {
		got := formatMetricValue(tc.name, tc.val)
		if got != tc.want {
			t.Errorf("formatMetricValue(%q, %v) = %q, want %q", tc.name, tc.val, got, tc.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.00 GB"},
		{5368709120, "5.00 GB"},
	}
	for _, tc := range tests {
		got := formatBytes(tc.in)
		if got != tc.want {
			t.Errorf("formatBytes(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatCount(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{999999, "1000.0K"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, tc := range tests {
		got := formatCount(tc.in)
		if got != tc.want {
			t.Errorf("formatCount(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSumMetrics(t *testing.T) {
	snaps := []MetricSnapshot{
		{Value: 10},
		{Value: 20},
		{Value: 0.5},
	}
	if got := sumMetrics(snaps); got != 30.5 {
		t.Errorf("sumMetrics = %v, want 30.5", got)
	}
	if got := sumMetrics(nil); got != 0 {
		t.Errorf("sumMetrics(nil) = %v, want 0", got)
	}
}

func TestBuildBars(t *testing.T) {
	buckets := []BucketSnapshot{
		{UpperBound: 0.005, Count: 10},
		{UpperBound: 0.01, Count: 50},
		{UpperBound: 0.1, Count: 5},
		{Count: 2, Overflow: true},
	}
	bars := buildBars(buckets)
	if len(bars) != 4 {
		t.Fatalf("buildBars returned %d bars, want 4", len(bars))
	}
	// Busiest bucket (count=50) should be 100%
	if bars[1].HeightPct != 100 {
		t.Errorf("busiest bar HeightPct = %d, want 100", bars[1].HeightPct)
	}
	// Overflow bar should have +Inf axis
	if bars[3].Axis != "+Inf" {
		t.Errorf("overflow bar Axis = %q, want +Inf", bars[3].Axis)
	}
	// Empty-ish bucket should still have at least 2% height
	if bars[2].HeightPct < 2 {
		t.Errorf("small bar HeightPct = %d, want >= 2", bars[2].HeightPct)
	}
}

func TestBuildBarsEmpty(t *testing.T) {
	bars := buildBars(nil)
	if len(bars) != 0 {
		t.Errorf("buildBars(nil) returned %d bars, want 0", len(bars))
	}
}

// Zero-count buckets must not imply data: they are flagged Zero (rendered as
// a hollow baseline tick), never given a minimum bar height.
func TestBuildBarsAllZero(t *testing.T) {
	buckets := []BucketSnapshot{
		{UpperBound: 1, Count: 0},
		{UpperBound: 5, Count: 0},
	}
	bars := buildBars(buckets)
	for i, b := range bars {
		if !b.Zero {
			t.Errorf("zero-count bar[%d] not flagged Zero", i)
		}
		if b.HeightPct != 0 {
			t.Errorf("zero-count bar[%d] HeightPct = %d, want 0", i, b.HeightPct)
		}
	}
}

// In a mixed histogram only the empty buckets carry the Zero flag; populated
// bars keep their proportional height.
func TestBuildBarsZeroFlagOnlyOnEmptyBuckets(t *testing.T) {
	buckets := []BucketSnapshot{
		{UpperBound: 1, Count: 0},
		{UpperBound: 5, Count: 50},
		{Count: 2, Overflow: true},
	}
	bars := buildBars(buckets)
	if !bars[0].Zero {
		t.Error("empty bucket not flagged Zero")
	}
	if bars[1].Zero {
		t.Error("populated bucket flagged Zero")
	}
	if bars[1].HeightPct != 100 {
		t.Errorf("busiest bar HeightPct = %d, want 100", bars[1].HeightPct)
	}
	if bars[2].Zero {
		t.Error("populated overflow bucket flagged Zero")
	}
}

func TestBuildCardsExcludesRuntimeMetrics(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "go_memstats_alloc_bytes", Type: "gauge", Value: 1024},
		{Name: "go_goroutines", Type: "gauge", Value: 5},
		{Name: "cpu_usage_percent", Type: "gauge", Value: 50},
		{Name: "custom_counter", Type: "counter", Value: 42},
	}
	cards := buildCards(snaps)
	// Runtime metrics should be excluded, only custom_counter remains
	if len(cards) != 1 {
		t.Fatalf("buildCards returned %d cards, want 1", len(cards))
	}
	if cards[0].Snapshot.Name != "custom_counter" {
		t.Errorf("card name = %q, want custom_counter", cards[0].Snapshot.Name)
	}
}

func TestBuildHookCards(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "hook_metric", Type: "counter", Value: 10},
		{Name: "hook_gauge", Type: "gauge", Value: 75, GaugeMax: 100},
		{Name: "custom_counter", Type: "counter", Value: 5},
	}
	cards := buildHookCards(snaps)
	// Only metrics containing "hook" are hook metrics
	if len(cards) != 2 {
		t.Fatalf("buildHookCards returned %d cards, want 2", len(cards))
	}
}

func TestBuildHookCardsGaugeSmartDefaults(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "hook_percent_used", Type: "gauge", Value: 50},
		{Name: "hook_ratio_hits", Type: "gauge", Value: 0.75},
		{Name: "hook_bytes_cached", Type: "gauge", Value: 2048},
		{Name: "hook_depth", Type: "gauge", Value: 5},
	}
	cards := buildHookCards(snaps)
	if len(cards) != 4 {
		t.Fatalf("got %d cards, want 4", len(cards))
	}
	// Check that needle percentages are computed
	for _, c := range cards {
		if c.NeedlePct < 0 || c.NeedlePct > 100 {
			t.Errorf("card %q: NeedlePct = %d, want 0..100", c.Snapshot.Name, c.NeedlePct)
		}
	}
}

func TestBuildHookCardsHistogram(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "hook_latency", Type: "histogram", Count: 100, Buckets: []BucketSnapshot{
			{UpperBound: 0.1, Count: 80},
			{UpperBound: 1, Count: 15},
			{Count: 5, Overflow: true},
		}},
	}
	cards := buildHookCards(snaps)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].Badge != "HISTOGRAM" {
		t.Errorf("badge = %q, want HISTOGRAM", cards[0].Badge)
	}
	if len(cards[0].Bars) != 3 {
		t.Errorf("bars len = %d, want 3", len(cards[0].Bars))
	}
}

func TestBuildCardsCounterGaugeHistogram(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "my_counter", Type: "counter", Value: 100},
		{Name: "my_gauge", Type: "gauge", Value: 75, GaugeMax: 100},
		{Name: "my_histogram", Type: "histogram", Value: 0.5, Buckets: []BucketSnapshot{
			{UpperBound: 1, Count: 10},
			{UpperBound: 5, Count: 20},
		}},
	}
	cards := buildCards(snaps)
	if len(cards) != 3 {
		t.Fatalf("buildCards returned %d cards, want 3", len(cards))
	}
	// Verify badge types
	badges := map[string]bool{}
	for _, c := range cards {
		badges[c.Badge] = true
	}
	if !badges["COUNTER"] || !badges["GAUGE"] || !badges["HISTOGRAM"] {
		t.Errorf("missing badge types: %+v", badges)
	}
}

func TestBuildCardsGaugeAutoDetect(t *testing.T) {
	tests := []struct {
		name    string
		snap    MetricSnapshot
		wantMax float64
		wantPct int
	}{
		{
			name:    "percent metric auto-detects max=100",
			snap:    MetricSnapshot{Name: "cpu_percent", Type: "gauge", Value: 50},
			wantMax: 100,
			wantPct: 50,
		},
		{
			name:    "pct suffix auto-detects max=100",
			snap:    MetricSnapshot{Name: "disk_usage_pct", Type: "gauge", Value: 80},
			wantMax: 100,
			wantPct: 80,
		},
		{
			name:    "ratio metric auto-detects max=1",
			snap:    MetricSnapshot{Name: "cache_hit_ratio", Type: "gauge", Value: 0.8},
			wantMax: 1,
			wantPct: 80,
		},
		{
			name:    "rate metric auto-detects max=1",
			snap:    MetricSnapshot{Name: "error_rate", Type: "gauge", Value: 0.05},
			wantMax: 1,
			wantPct: 5,
		},
		{
			name:    "default gauge with 50% headroom",
			snap:    MetricSnapshot{Name: "queue_depth", Type: "gauge", Value: 20},
			wantMax: 30, // 20 * 1.5
			wantPct: 66,
		},
	}
	for _, tc := range tests {
		cards := buildCards([]MetricSnapshot{tc.snap})
		if len(cards) != 1 {
			t.Fatalf("%s: got %d cards, want 1", tc.name, len(cards))
		}
		if cards[0].MaxValue != tc.wantMax {
			t.Errorf("%s: MaxValue = %v, want %v", tc.name, cards[0].MaxValue, tc.wantMax)
		}
	}
}

func TestBuildCardsUnknownType(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "weird_metric", Type: "unknown_type", Value: 42},
	}
	cards := buildCards(snaps)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].Badge != "UNKNOWN" {
		t.Errorf("badge = %q, want UNKNOWN", cards[0].Badge)
	}
}

// The gauge needle maps the value onto the ±90° dial arc: scale minimum →
// -90°, maximum → +90°, values outside the scale clamp to the ends.
func TestGaugeNeedleDegreeMapping(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  float64
	}{
		{"at scale minimum", 0, -90},
		{"at scale middle", 50, 0},
		{"at scale maximum", 100, 90},
		{"above scale clamps", 150, 90},
		{"below scale clamps", -10, -90},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snaps := []MetricSnapshot{{Name: "g", Type: "gauge", Value: tc.value, GaugeMin: 0, GaugeMax: 100}}
			cards := buildCards(snaps)
			if len(cards) != 1 {
				t.Fatalf("got %d cards, want 1", len(cards))
			}
			if cards[0].NeedleDeg != tc.want {
				t.Errorf("value %v: NeedleDeg = %v, want %v", tc.value, cards[0].NeedleDeg, tc.want)
			}
		})
	}
}

// A degenerate scale (min == max) is widened, never NaN and never a pinned
// needle.
func TestGaugeDegenerateScaleWidens(t *testing.T) {
	snaps := []MetricSnapshot{{Name: "g", Type: "gauge", Value: 5, GaugeMin: 5, GaugeMax: 5}}
	cards := buildCards(snaps)
	if cards[0].MaxValue != 6 {
		t.Errorf("MaxValue = %v, want 6 (min+1)", cards[0].MaxValue)
	}
	if cards[0].NeedleDeg != -90 {
		t.Errorf("NeedleDeg = %v, want -90 (value sits at the widened minimum)", cards[0].NeedleDeg)
	}
}

// The gauge renders a neutral dial: needle and track, no hard-coded
// danger/safe zones (which side is dangerous depends on the metric).
func TestGaugeCardNeutralTrack(t *testing.T) {
	snaps := []MetricSnapshot{{Name: "g", Type: "gauge", Value: 50, GaugeMin: 0, GaugeMax: 100}}
	var buf strings.Builder
	if err := chartsTpl.Execute(&buf, buildCards(snaps)); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, "gauge-zone") {
		t.Error("gauge card renders hard-coded danger/safe zones")
	}
	if !strings.Contains(body, "gauge-needle") || !strings.Contains(body, "gauge-track") {
		t.Error("gauge card missing needle/track dial")
	}
	// The dial is an image with a text alternative summarizing the value and
	// scale.
	if !strings.Contains(body, `role="img"`) || !strings.Contains(body, "aria-label=\"g gauge:") {
		t.Error("gauge svg missing role/aria-label")
	}
}

// The status card reads the middleware's fixed per-class child counters
// (http_requests_total_<class>). The labeled parent carries only the last-seen
// label set, so it feeds the card only when no children exist yet; the errors
// counter (a subset of 4xx+5xx) is shown in the totals view, never added into
// the total.
func TestBuildHTTPCardStatusCodes(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "http_requests_total_2xx", Type: "counter", Value: 150},
		{Name: "http_requests_total_3xx", Type: "counter", Value: 10},
		{Name: "http_requests_total_4xx", Type: "counter", Value: 20},
		{Name: "http_requests_total_5xx", Type: "counter", Value: 5},
		// The labeled parent is stale once per-class children exist: counting
		// it too would double every request.
		{Name: "http_requests_total", Type: "counter", Value: 1000, LabelValues: []Label{{Name: "status", Value: "200"}}},
		{Name: "http_request_errors_total", Type: "counter", Value: 3},
	}
	cards := buildHTTPCard(snaps)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	card := cards[0]
	if card.Badge != "COUNTER" {
		t.Errorf("badge = %q, want COUNTER", card.Badge)
	}
	if card.HTTPView != httpViewBreakdown {
		t.Errorf("default view = %q, want breakdown", card.HTTPView)
	}
	// Breakdown columns: 2xx=150, 3xx=10, 4xx=20, 5xx=5, total=185
	colMap := map[string]float64{}
	for _, col := range card.HTTPColumns {
		colMap[col.Label] = col.Value
	}
	for label, want := range map[string]float64{"2xx": 150, "3xx": 10, "4xx": 20, "5xx": 5, "total": 185} {
		if colMap[label] != want {
			t.Errorf("%s = %v, want %v (class sum; parent excluded)", label, colMap[label], want)
		}
	}
	if _, ok := colMap["errors"]; ok {
		t.Error("breakdown view must not carry an errors column (totals view only)")
	}

	// Totals view: total + errors side by side, errors still excluded from
	// the total.
	totals := buildHTTPCardView(snaps, httpViewTotals)
	if len(totals) != 1 {
		t.Fatalf("totals view: got %d cards, want 1", len(totals))
	}
	totCols := map[string]float64{}
	for _, col := range totals[0].HTTPColumns {
		totCols[col.Label] = col.Value
	}
	if totCols["total"] != 185 {
		t.Errorf("totals view: total = %v, want 185", totCols["total"])
	}
	if totCols["errors"] != 3 {
		t.Errorf("totals view: errors = %v, want 3", totCols["errors"])
	}
}

func TestBuildHTTPCardNoHTTPMetrics(t *testing.T) {
	snaps := []MetricSnapshot{
		{Name: "custom_counter", Type: "counter", Value: 10},
	}
	cards := buildHTTPCard(snaps)
	if cards != nil {
		t.Errorf("expected nil for non-HTTP metrics, got %d cards", len(cards))
	}
}

func TestBuildHTTPCardUncategorized(t *testing.T) {
	// HTTP metric with no status label → goes to "other"
	snaps := []MetricSnapshot{
		{Name: "http_requests_total", Type: "counter", Value: 25},
	}
	cards := buildHTTPCard(snaps)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	colMap := map[string]float64{}
	for _, col := range cards[0].HTTPColumns {
		colMap[col.Label] = col.Value
	}
	if colMap["total"] != 25 {
		t.Errorf("total = %v, want 25", colMap["total"])
	}
}

// --- Handler tests ---

func TestHistoryHTTPHandler(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Record some data and wait for flush
	r.Record("test_metric", 10)
	r.Record("test_metric", 20)
	waitForDrain(r)

	handler := r.HistoryHTTPHandler()
	if handler == nil {
		t.Fatal("HistoryHTTPHandler returned nil")
	}

	// Test with valid duration
	req := httptest.NewRequest("GET", "/history?name=test_metric&duration=1h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	// Test missing name parameter
	req2 := httptest.NewRequest("GET", "/history?duration=1h", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != 400 {
		t.Errorf("missing name: status = %d, want 400", rec2.Code)
	}

	// Test invalid duration
	req3 := httptest.NewRequest("GET", "/history?name=test_metric&duration=bad", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != 400 {
		t.Errorf("invalid duration: status = %d, want 400", rec3.Code)
	}

	// Test default duration (no duration param)
	req4 := httptest.NewRequest("GET", "/history?name=test_metric", nil)
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != 200 {
		t.Errorf("default duration: status = %d, want 200", rec4.Code)
	}

	// Batch form: ?names=a,b returns a {name: HistorySeries} map so a panel
	// grid refresh costs one request instead of one per panel.
	r.Record("second_metric", 5)
	waitForDrain(r)

	req5 := httptest.NewRequest("GET", "/history?names=test_metric,second_metric&duration=1h", nil)
	rec5 := httptest.NewRecorder()
	handler.ServeHTTP(rec5, req5)
	if rec5.Code != 200 {
		t.Fatalf("batch: status = %d, body %s", rec5.Code, rec5.Body.String())
	}
	var batch map[string]HistorySeries
	if err := json.Unmarshal(rec5.Body.Bytes(), &batch); err != nil {
		t.Fatalf("batch: body is not a JSON object: %v", err)
	}
	if len(batch) != 2 {
		t.Errorf("batch: got %d series, want 2", len(batch))
	}
	for _, name := range []string{"test_metric", "second_metric"} {
		s, ok := batch[name]
		if !ok {
			t.Fatalf("batch: missing series for %q", name)
		}
		if s.Name != name {
			t.Errorf("batch: series name = %q, want %q", s.Name, name)
		}
		if len(s.Points) == 0 {
			t.Errorf("batch: series %q has no points", name)
		}
	}

	// Mixed known/unknown names: an unknown metric still gets its own (empty)
	// entry so clients can tell "no data" from "not requested".
	req6 := httptest.NewRequest("GET", "/history?names=test_metric,ghost_metric&duration=1h", nil)
	rec6 := httptest.NewRecorder()
	handler.ServeHTTP(rec6, req6)
	if rec6.Code != 200 {
		t.Fatalf("mixed: status = %d, body %s", rec6.Code, rec6.Body.String())
	}
	batch = nil
	if err := json.Unmarshal(rec6.Body.Bytes(), &batch); err != nil {
		t.Fatalf("mixed: body is not a JSON object: %v", err)
	}
	ghost, ok := batch["ghost_metric"]
	if !ok {
		t.Fatal("mixed: missing entry for unknown name")
	}
	if ghost.Name != "ghost_metric" || len(ghost.Points) != 0 {
		t.Errorf("mixed: ghost series = %+v, want named empty series", ghost)
	}
}

func TestHistoryHTTPHandlerDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if r.HistoryHTTPHandler() != nil {
		t.Error("HistoryHTTPHandler should return nil when UI disabled")
	}
}

// The dashboard's custom timeframe choice fetches with an absolute ?start=
// (unix seconds) instead of a ?duration= preset. The window it produces must
// match the equivalent preset's window (a roll-up 90 minutes old falls inside
// the 4h window and outside the 1h one), and malformed start values are
// rejected.
func TestHistoryHTTPHandlerCustomStart(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Seeded directly so its age is known; nothing is ever recorded through
	// the registry, so the in-flight partial window stays empty.
	now := time.Now()
	if err := r.storage.Store(context.Background(), AggregatedMetric{
		Name:        "tf_metric",
		Count:       2,
		Sum:         7,
		WindowStart: now.Add(-2 * time.Hour),
		WindowEnd:   now.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("seed storage: %v", err)
	}

	fetch := func(query string) (*httptest.ResponseRecorder, HistorySeries) {
		t.Helper()
		req := httptest.NewRequest("GET", "/history?"+query, nil)
		rec := httptest.NewRecorder()
		r.HistoryHTTPHandler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			return rec, HistorySeries{}
		}
		var s HistorySeries
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return rec, s
	}
	startAt := func(d time.Duration) string {
		return strconv.FormatInt(now.Add(d).Unix(), 10)
	}

	rec, wide := fetch("name=tf_metric&start=" + startAt(-4*time.Hour))
	if rec.Code != 200 || wide.Name != "tf_metric" || len(wide.Points) != 1 {
		t.Fatalf("start 4h: status = %d, series = %+v", rec.Code, wide)
	}
	rec, narrow := fetch("name=tf_metric&start=" + startAt(-time.Hour))
	if rec.Code != 200 {
		t.Fatalf("start 1h: status = %d, want 200", rec.Code)
	}
	if len(narrow.Points) != 0 {
		t.Errorf("start 1h: got %d points, want the 90-minute-old roll-up excluded", len(narrow.Points))
	}
	rec, preset := fetch("name=tf_metric&duration=4h")
	if rec.Code != 200 || len(preset.Points) != 1 {
		t.Fatalf("duration 4h: status = %d, points = %d, want 1", rec.Code, len(preset.Points))
	}
	if preset.Points[0].V != wide.Points[0].V {
		t.Errorf("start and duration windows diverge: %+v vs %+v", wide.Points, preset.Points)
	}

	for _, bad := range []string{"abc", "-5", "0", "1.5"} {
		req := httptest.NewRequest("GET", "/history?name=tf_metric&start="+bad, nil)
		rec := httptest.NewRecorder()
		r.HistoryHTTPHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("start=%q: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestEndpointsHTTPHandlerDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if r.EndpointsHTTPHandler() != nil {
		t.Error("EndpointsHTTPHandler should return nil when UI disabled")
	}
}

func TestMetricsHTTPHandler(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	handler := r.MetricsHTTPHandler()
	if handler == nil {
		t.Fatal("MetricsHTTPHandler returned nil")
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestMetricsHTTPHandlerDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if r.MetricsHTTPHandler() != nil {
		t.Error("MetricsHTTPHandler should return nil when UI disabled")
	}
}

func TestMetricsDataHTTPHandler(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	r.Record("test_metric", 42)
	waitForDrain(r)

	handler := r.MetricsDataHTTPHandler()
	if handler == nil {
		t.Fatal("MetricsDataHTTPHandler returned nil")
	}

	// JSON mode
	req := httptest.NewRequest("GET", "/metrics/data", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("JSON: status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("JSON: content-type = %q", ct)
	}
}

func TestMetricsDataHTTPHandlerDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if r.MetricsDataHTTPHandler() != nil {
		t.Error("MetricsDataHTTPHandler should return nil when UI disabled")
	}
}

func TestHooksHandlerJSON(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Record a hook metric
	r.Record("hook_test_metric", 10)
	waitForDrain(r)

	req := httptest.NewRequest("GET", "/hooks", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	r.HooksHandler(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHooksHandlerHTML(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	req := httptest.NewRequest("GET", "/hooks", nil)
	rec := httptest.NewRecorder()
	r.HooksHandler(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

// --- Cardinality endpoint tests ---

func TestCardinalityEndpointJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxLabelSeriesPerMetric = 10
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Generate some cardinality data
	r.Record("test_metric", 1, Label{Name: "k", Value: "a"})
	r.Record("test_metric", 2, Label{Name: "k", Value: "b"})
	waitForDrain(r)

	mux := http.NewServeMux()
	r.MountUI(mux)

	req := httptest.NewRequest("GET", "/metrics/cardinality", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var snaps []CardinalitySnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snaps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, s := range snaps {
		if s.MetricName == "test_metric" {
			found = true
			if s.Series != 2 {
				t.Errorf("series = %d, want 2", s.Series)
			}
			if s.MaxSeries != 10 {
				t.Errorf("max = %d, want 10", s.MaxSeries)
			}
		}
	}
	if !found {
		t.Errorf("test_metric not in cardinality snapshots: %+v", snaps)
	}
}

func TestCardinalityEndpointDisabledUI(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)

	h := r.CardinalityHTTPHandler()
	if h != nil {
		t.Error("expected nil handler when UI disabled")
	}
}

func TestDashboardContainsInternalsSection(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "GoLens") || !strings.Contains(body, "Internals") {
		t.Error("dashboard missing GoLens Internals section")
	}
	if !strings.Contains(body, "cardinalityPanel(") {
		t.Error("dashboard missing cardinalityPanel Alpine wiring")
	}
	// The endpoint URL lives in the client script served from the embedded
	// assets, not inline in the page — fetch it and check the wiring there.
	js := httptest.NewRecorder()
	mux.ServeHTTP(js, httptest.NewRequest("GET", "/metrics/static/dashboard.js", nil))
	if js.Code != 200 {
		t.Fatalf("dashboard.js status = %d", js.Code)
	}
	if !strings.Contains(js.Body.String(), "/metrics/cardinality") {
		t.Error("dashboard.js missing cardinality endpoint reference")
	}
	if !strings.Contains(body, "Per-Metric Cardinality") {
		t.Error("dashboard missing per-metric cardinality table")
	}
}

func TestCardinalityEndpointNoData(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mux := http.NewServeMux()
	r.MountUI(mux)

	req := httptest.NewRequest("GET", "/metrics/cardinality", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	// Should return empty array, not null
	if rec.Body.String() != "[]\n" && rec.Body.String() != "null\n" {
		// Either empty array or null is acceptable
		var snaps []CardinalitySnapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &snaps); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(snaps) != 0 {
			t.Errorf("expected empty, got %d entries", len(snaps))
		}
	}
}

// --- dashboard sections ---------------------------------------------------------

// renderDashboard starts the registry, mounts the UI, and returns the
// rendered /metrics page.
func renderDashboard(t *testing.T, r *Registry) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	t.Cleanup(func() { cancel(); r.Close() })
	rec := httptest.NewRecorder()
	newServer(r).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("dashboard status = %d, body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// markupWindow returns the body slice from the first marker occurrence to the
// first end marker after it — the local window one rendered control occupies,
// so option assertions stay scoped to that control.
func markupWindow(body, marker, end string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		return ""
	}
	return body[i : i+j]
}

// staticAsset fetches an embedded dashboard asset from /metrics/static.
func staticAsset(t *testing.T, r *Registry, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	newServer(r).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/static/"+name, nil))
	if rec.Code != 200 {
		t.Fatalf("%s status = %d", name, rec.Code)
	}
	return rec.Body.String()
}

// With no saved document and no preset, the dashboard renders the built-in
// default layout: one band per fixed block in the historical order, with the
// empty my-charts band for pinned panels. Bands whose content is unavailable
// (runtime metrics off, no hook metrics, no panels) start hidden.
func TestDashboardDefaultSectionsRender(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	body := renderDashboard(t, r)

	ids := []string{"runtime-health", "histogram", "hooks", "my-charts", "all-metrics", "endpoint-latency", "internals"}
	last := -1
	for _, id := range ids {
		marker := `data-section="` + id + `"`
		i := strings.Index(body, marker)
		if i < 0 {
			t.Errorf("default band %s missing", id)
			continue
		}
		if i < last {
			t.Errorf("band %s out of default order", id)
		}
		last = i
	}
	// Blocks render inside their band: the histogram block sits after the
	// histogram band opens and before the next band (my-charts) starts.
	hist := strings.Index(body, `data-section="histogram"`)
	if hist < 0 || hist > strings.Index(body, `data-block="histogram"`) ||
		strings.Index(body, `data-block="histogram"`) > strings.Index(body, `data-section="my-charts"`) {
		t.Error("histogram block not nested inside its band")
	}
	// No runtime or hook metrics by default: those blocks are dropped and
	// their panel-less bands start hidden, as does the empty my-charts band.
	for _, band := range []struct{ id, name string }{
		{"runtime-health", "Runtime Health"},
		{"hooks", "Hooks"},
		{"my-charts", "My Charts"},
	} {
		if !strings.Contains(body, `data-section="`+band.id+`" data-name="`+band.name+`" hidden`) {
			t.Errorf("band %s should render hidden with no content", band.id)
		}
	}
	// Bands with renderable content are not hidden.
	for _, band := range []struct{ id, name string }{
		{"histogram", "Histogram Time-Series"},
		{"all-metrics", "All Metrics"},
		{"endpoint-latency", "Endpoint Latency"},
		{"internals", "Internals"},
	} {
		if strings.Contains(body, `data-name="`+band.name+`" hidden`) {
			t.Errorf("band %s renders content but is hidden", band.id)
		}
	}
	// The client bootstrap carries the same effective list, so the page and
	// the layout API can never diverge.
	if !strings.Contains(body, `[{"id":"runtime-health","name":"Runtime Health","items":["runtime-health"]},{"id":"histogram","name":"Histogram Time-Series","items":["histogram"]}`) {
		t.Error("client bootstrap missing the default section list")
	}
}

// Chart-grid spans reach the rendered markup: the cards container renders as
// the 12-column chart grid, the HTTP status card spans the full row (12x4),
// and a histogram card carries the widened default span (8x4) its bar rows
// need.
func TestDashboardCardsRenderChartGridSpans(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	r.Register("job_duration_seconds", HistogramType, "job duration", nil, DefaultHistogramBounds, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()
	r.Record("job_duration_seconds", 0.5)
	r.Record("http_requests_total_2xx", 3)
	waitForDrain(r)

	// Fetch the page directly rather than through renderDashboard: the
	// registry is already running, and renderDashboard's Start would write
	// r.ctx while the loop goroutine reads it on flush ticks (go test -race).
	rec := httptest.NewRecorder()
	newServer(r).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("dashboard status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="grid grid-charts"`) {
		t.Error("cards container missing the grid-charts class")
	}
	for _, tc := range []struct{ metric, span string }{
		{"job_duration_seconds", "card-8x4"},
		{"http_requests_total", "card-12x4"},
	} {
		w := markupWindow(body, `data-metric="`+tc.metric+`"`, "card-head")
		if w == "" {
			t.Errorf("card %s missing from the rendered dashboard", tc.metric)
			continue
		}
		if !strings.Contains(w, tc.span) {
			t.Errorf("card %s renders %q, want span class %s", tc.metric, w, tc.span)
		}
	}
}

// The add-chart modal's size options are the client half of the size
// vocabulary: every offered value must be a span the server's PUT validator
// accepts, or saving a chart with that size would 400 every layout PUT and
// retry forever.
func TestAddChartSizeOptionsMatchServerVocabulary(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	body := renderDashboard(t, r)

	selectHTML := markupWindow(body, `id="add-chart-size"`, "</select>")
	if selectHTML == "" {
		t.Fatal("add-chart size select missing from the dashboard")
	}
	values := regexp.MustCompile(`value="([^"]*)"`).FindAllStringSubmatch(selectHTML, -1)
	spans := map[string]bool{}
	for _, v := range values {
		if v[1] == "" {
			continue // the "default" option: empty size, not a span
		}
		spans[v[1]] = true
		if _, ok := gridSize(v[1]); !ok {
			t.Errorf("modal offers size %q, which the server rejects", v[1])
		}
	}
	// The whole stepped 5-width × 3-height vocabulary must be offered, so a
	// size the server accepts is never unreachable from the UI.
	if len(spans) != 15 {
		t.Errorf("modal offers %d spans (%v), want all 15 width×height sizes", len(spans), spans)
	}
}

// The pin-slot size select is built client-side from dashboard.js's
// PANEL_SIZES array — the primary size-editing surface. If it drifts from
// the server's vocabulary, setPanelSize writes a size the layout PUT rejects
// and persistDash retries forever. The served JS must carry exactly the
// spans gridSize accepts.
func TestClientPanelSizesMatchServerVocabulary(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	js := staticAsset(t, r, "dashboard.js")

	m := regexp.MustCompile(`PANEL_SIZES = \[([^\]]*)\]`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("PANEL_SIZES array missing from dashboard.js")
	}
	client := map[string]bool{}
	for _, s := range strings.Split(m[1], ",") {
		if s = strings.Trim(strings.TrimSpace(s), "'"); s != "" {
			client[s] = true
		}
	}
	for s := range client {
		if !validPanelSizes[s] {
			t.Errorf("dashboard.js offers size %q, which the server rejects", s)
		}
	}
	for s := range validPanelSizes {
		if !client[s] {
			t.Errorf("server size %q missing from dashboard.js PANEL_SIZES", s)
		}
	}
}

// A Go preset replaces the whole section list: exactly the preset bands, in
// order, with their blocks nested inside, server-rendered and bootstrapped to
// the client.
func TestDashboardPresetSectionsRender(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RuntimeMetrics.Enabled = true // the runtime block renders content
	r := newConfigRegistry(t, cfg)
	r.PresetDashboardSections(
		DashboardSection{ID: "ops", Name: "Ops", Items: []string{BlockInternals, BlockRuntimeHealth}},
		DashboardSection{ID: "tail", Items: []string{BlockAllMetrics}},
	)
	body := renderDashboard(t, r)

	ops := strings.Index(body, `data-section="ops"`)
	tail := strings.Index(body, `data-section="tail"`)
	internals := strings.Index(body, `data-block="internals"`)
	runtime := strings.Index(body, `data-block="runtime-health"`)
	allMetrics := strings.Index(body, `data-block="all-metrics"`)
	if ops < 0 || tail < 0 || internals < ops || runtime < internals || tail < runtime || allMetrics < tail {
		t.Errorf("preset bands/blocks out of order: ops=%d internals=%d runtime=%d tail=%d all-metrics=%d",
			ops, internals, runtime, tail, allMetrics)
	}
	if !strings.Contains(body, `data-name="Ops"`) || !strings.Contains(body, `class="sec-name">Ops</span>`) {
		t.Error("named preset band missing its name")
	}
	if !strings.Contains(body, `data-section="tail" data-name=""`) {
		t.Error("unnamed preset band missing the empty name slot")
	}
	// The preset replaces the default list wholesale — no default-only band.
	if strings.Contains(body, `data-section="histogram"`) {
		t.Error("default band survived a full-list preset")
	}
	if !strings.Contains(body, `[{"id":"ops","name":"Ops","items":["internals","runtime-health"]},{"id":"tail","items":["all-metrics"]}]`) {
		t.Error("client bootstrap missing the preset section list")
	}
}

// The timeframe selectors are server-rendered: one dashboard-global select
// (preset choices with custom last; the saved choice preselected, defaulting
// to 1h), the datetime-local picker for the custom start, and a per-band
// select with the band's override — or "global" — preselected. The client
// bootstrap carries the band overrides so the page and the layout API cannot
// diverge.
func TestDashboardTimeframeSelectorsRender(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		body := renderDashboard(t, r)

		global := markupWindow(body, `id="tf-global"`, "</select>")
		if global == "" {
			t.Fatal("global timeframe select missing")
		}
		if !strings.Contains(global, `<option value="1h" selected>`) {
			t.Error("global select does not default to 1h")
		}
		if !strings.Contains(global, `<option value="5m">`) {
			t.Error("global select missing the preset choices")
		}
		if custom, lastPreset := strings.Index(global, `value="custom"`), strings.Index(global, `value="1w"`); custom < 0 || custom < lastPreset {
			t.Errorf("custom choice at %d, want present after the presets (1w at %d)", custom, lastPreset)
		}
		if !strings.Contains(body, `id="tf-start"`) {
			t.Error("custom start picker missing")
		}
		if !strings.Contains(body, `<option value="" selected>global</option>`) {
			t.Error("band selects do not default to global")
		}
	})
	t.Run("saved choices preselect", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		doc := `{"version":1,"timeframe":"4h","order":{},"sections":[` +
			`{"id":"main","items":["all-metrics"],"timeframe":"5m"},` +
			`{"id":"spare","items":[]}]}`
		if rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc); rec.Code != 200 {
			t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
		}
		body := renderDashboard(t, r)

		if global := markupWindow(body, `id="tf-global"`, "</select>"); !strings.Contains(global, `<option value="4h" selected>`) {
			t.Error("saved global timeframe not preselected")
		}
		main := markupWindow(body, `data-section="main"`, "</select>")
		if !strings.Contains(main, `<option value="5m" selected>`) {
			t.Error("band override not preselected in its own select")
		}
		spare := markupWindow(body, `data-section="spare"`, "</select>")
		if !strings.Contains(spare, `<option value="" selected>`) {
			t.Error("untouched band should keep global selected")
		}
		if !strings.Contains(body, `"timeframe":"5m"`) {
			t.Error("client bootstrap missing the band override")
		}
	})
}

// View mode is frozen: every edit affordance carries .edit-only and the CSS
// hides it unless body.edit-mode is on, and every drag/move entry point
// refuses to run outside edit mode. The edit flag itself persists across
// reloads in localStorage, and the toggle button announces its state.
func TestDashboardEditModeGatesEditAffordances(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	body := renderDashboard(t, r)

	for _, affordance := range []string{
		`onclick="document.body.dataset.add='1'" class="btn edit-only"`, // + Add Chart
		`onclick="resetLayout()" class="btn edit-only"`,                 // Reset layout
		`class="sec-rename edit-only"`,                                  // section rename input
		`class="block-actions edit-only"`,                               // section block moves
		`class="move-btns edit-only"`,                                   // card move/pin buttons
	} {
		if !strings.Contains(body, affordance) {
			t.Errorf("edit affordance not gated with edit-only: %s", affordance)
		}
	}
	if !strings.Contains(body, `id="edit-toggle"`) || !strings.Contains(body, `aria-pressed="false"`) {
		t.Error("edit toggle missing or not announcing view mode")
	}

	js := staticAsset(t, r, "dashboard.js")
	css := staticAsset(t, r, "styles.css")

	if !strings.Contains(css, "body:not(.edit-mode) .edit-only") {
		t.Error("styles.css missing the edit-only visibility gate")
	}
	// Drag/move handlers refuse to run outside edit mode: All Metrics, Hooks,
	// Endpoints (drag start + move buttons), Runtime Health, and section
	// blocks/panels.
	if n := strings.Count(js, "if (!editMode()) return;"); n < 6 {
		t.Errorf("drag/move edit-mode gating found %d times, want at least 6", n)
	}
	// The flag survives reloads (D5) and drives the body class.
	if !strings.Contains(js, `localStorage.getItem('golens.editmode') === '1'`) ||
		!strings.Contains(js, `localStorage.setItem('golens.editmode'`) {
		t.Error("edit mode not persisted in localStorage")
	}
	if !strings.Contains(js, `classList.toggle('edit-mode', !!on)`) {
		t.Error("edit mode does not drive the body.edit-mode class")
	}
}

// The section header (rule + name + chevron) is one collapse click target;
// the rename input and action buttons opt out. Collapse state lives in
// localStorage per section id. Renaming goes through the inline input — an
// empty value keeps the band untitled — and persists the section list.
func TestDashboardSectionCollapseAndRenameWiring(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	body := renderDashboard(t, r)

	if !strings.Contains(body, `class="sec-head" role="button" tabindex="0" aria-label="Toggle section"`) {
		t.Error("section header not rendered as one focusable click target")
	}
	if !strings.Contains(body, `placeholder="section name (optional)"`) ||
		!strings.Contains(body, `onchange="renameSection(this)"`) {
		t.Error("section rename input missing")
	}

	js := staticAsset(t, r, "dashboard.js")
	css := staticAsset(t, r, "styles.css")

	// Whole-header click target; interactive children opt out.
	if !strings.Contains(js, `e.target.closest('.sec-head')`) ||
		!strings.Contains(js, `e.target.closest('.sec-rename-input, .block-actions, .pin-actions')`) {
		t.Error("header click delegation missing or missing the interactive opt-out")
	}
	// Collapse is keyboard-reachable and persists per section id.
	if !strings.Contains(js, `e.key !== 'Enter' && e.key !== ' '`) {
		t.Error("collapse not keyboard-reachable (Enter/Space)")
	}
	if !strings.Contains(js, `'golens.collapsed'`) {
		t.Error("collapse state not persisted in localStorage")
	}
	// Rename trims the input and persists the whole section list (an empty
	// value renames to the untitled band).
	if !strings.Contains(js, "function renameSection(input)") ||
		!strings.Contains(js, "input.value.trim()") ||
		!strings.Contains(js, "function persistSectionsFromDOM()") {
		t.Error("rename wiring missing trim/persist")
	}
	// Collapsed state flips the chevron; hidden bands/bodies beat display rules.
	if !strings.Contains(css, ".sec-head.sec-collapsed .sec-chev") {
		t.Error("styles.css missing the collapsed chevron state")
	}
	if !strings.Contains(css, "[hidden] { display: none !important; }") {
		t.Error("styles.css does not enforce the hidden attribute")
	}
}

// sectionTree links the flat view list into the render tree: each band nests
// under the band its Parent names in list order, a band whose parent is
// nonetheless missing renders as a root (the server render and GET can never
// diverge on the layout), and settleHidden keeps an empty ancestor visible
// when a nested band below it carries content while an empty leaf stays
// hidden.
func TestSectionTreeLinksAndSettlesHidden(t *testing.T) {
	views := buildSectionViews([]DashboardSection{
		{ID: "orphan", Parent: "missing"}, // dangling parent: defensive root fallback
		{ID: "holder"},                    // no own content, content nested below
		{ID: "nested", Parent: "holder", Items: []string{BlockAllMetrics}},
		{ID: "tip", Parent: "nested"}, // empty leaf under a content band
	}, false, nil)
	roots := sectionTree(views, nil)

	if len(roots) != 2 || roots[0].ID != "orphan" || roots[1].ID != "holder" {
		t.Fatalf("roots = [%s %s], want [orphan holder]", roots[0].ID, roots[1].ID)
	}
	if len(roots[1].Children) != 1 || roots[1].Children[0].ID != "nested" ||
		len(roots[1].Children[0].Children) != 1 || roots[1].Children[0].Children[0].ID != "tip" {
		t.Fatal("nested not linked under holder, or tip not linked under nested")
	}
	for _, n := range []*secRender{roots[0], roots[1].Children[0].Children[0]} {
		if !n.Hidden {
			t.Errorf("band %s has no content anywhere and must render hidden", n.ID)
		}
	}
	for _, n := range []*secRender{roots[1], roots[1].Children[0]} {
		if n.Hidden {
			t.Errorf("band %s anchors nested content and must render visible", n.ID)
		}
	}
}

// A nested layout renders the band tree server-side: a parent band opens
// before its children and its own items render before them, empty ancestors
// of a content band stay visible while empty leaves and empty top-level bands
// hide, and the client bootstrap carries the same nesting so the page and the
// layout API can never diverge.
func TestDashboardNestedSectionsRender(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	doc := `{"version":1,"order":{},"sections":[
	        {"id":"holder"},
	        {"id":"nested","parent":"holder","items":["all-metrics"]},
	        {"id":"tip","parent":"nested"},
	        {"id":"spare"}]}`
	if rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc); rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := renderDashboard(t, r)

	holder := strings.Index(body, `data-section="holder"`)
	nested := strings.Index(body, `data-section="nested"`)
	tip := strings.Index(body, `data-section="tip"`)
	spare := strings.Index(body, `data-section="spare"`)
	allMetrics := strings.Index(body, `data-block="all-metrics"`)
	if holder < 0 || nested < 0 || tip < 0 || spare < 0 || allMetrics < 0 {
		t.Fatalf("nested bands missing from render: holder=%d nested=%d tip=%d spare=%d block=%d",
			holder, nested, tip, spare, allMetrics)
	}
	// The child band follows its parent, and a band's own items precede the
	// nested bands inside it (nested carries both the all-metrics item and
	// the child tip, so this pins items-before-children and the depth-2
	// recursion in one ordering).
	if !(holder < nested && nested < allMetrics && allMetrics < tip) {
		t.Errorf("band tree out of order: holder=%d nested=%d all-metrics=%d tip=%d",
			holder, nested, allMetrics, tip)
	}
	// settleHidden: a band above nested content stays visible; empty leaves
	// and empty top-level bands hide.
	if strings.Contains(body, `data-section="holder" data-name="" hidden`) ||
		strings.Contains(body, `data-section="nested" data-name="" hidden`) {
		t.Error("band anchoring nested content renders hidden")
	}
	for _, hidden := range []string{"tip", "spare"} {
		if !strings.Contains(body, `data-section="`+hidden+`" data-name="" hidden`) {
			t.Errorf("band %s has no content anywhere and should render hidden", hidden)
		}
	}
	if !strings.Contains(body, `{"id":"nested","items":["all-metrics"],"parent":"holder"}`) ||
		!strings.Contains(body, `{"id":"tip","parent":"nested"}`) {
		t.Error("client bootstrap missing the nested section list")
	}
}

// The client honors nesting: the persist walk is depth-first from the
// top-level bands (parent = nearest band ancestor), the drift check and the
// carried-forward bands keep their parent, pinned rebuilds insert ahead of
// nested bands instead of reordering them, band re-parenting drags are gated
// on edit mode and refuse to drop a band into its own subtree, the outdent
// zone returns a band to top level, and nested bands indent by cascade alone.
func TestDashboardNestedSectionsClientWiring(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	js := staticAsset(t, r, "dashboard.js")
	css := staticAsset(t, r, "styles.css")

	for _, frag := range []string{
		"querySelectorAll('main > [data-section]')",                    // persist walks top-level roots
		"if (parentID) out.parent = parentID;",                         // emit records the band ancestor
		"parent: s.parent || ''",                                       // drift check + carried bands keep parent
		"body.insertBefore(n, nested[0] || null)",                      // pinned rebuild never reorders nested bands
		"sec.setAttribute('draggable', editMode() ? 'true' : 'false')", // drag handle is edit-mode only
		"if (host && dragSection.contains(host)) return;",              // no drop into the band's own subtree
		"main.appendChild(dragSection)",                                // outdent zone: drop outside any body = top level
	} {
		if !strings.Contains(js, frag) {
			t.Errorf("dashboard.js missing nested-section wiring: %s", frag)
		}
	}
	if !strings.Contains(css, ".dash-section .dash-section {") {
		t.Error("styles.css missing the nested band cascade rule")
	}
	if !strings.Contains(css, "body.edit-mode .dash-section > .sec-head") {
		t.Error("styles.css missing the band drag affordance")
	}
}
