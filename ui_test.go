package golens

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// Every reorderable surface (All Metrics, Hooks, My Charts, Endpoint Latency,
// Runtime Health) must render keyboard/touch-reachable move buttons and move
// through the one shared moveCard helper in dashboard.js — not five divergent
// drag implementations. The cards' pin button is the other header affordance.
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
	// My Charts pinned slots.
	if !strings.Contains(body, `"move(idx,-1)"`) || !strings.Contains(body, `"move(idx,1)"`) {
		t.Error("My Charts missing move buttons")
	}
	// Runtime Health cards (Alpine component scope).
	if !strings.Contains(body, `"move($event,-1)"`) || !strings.Contains(body, `"move($event,1)"`) {
		t.Error("Runtime Health cards missing move buttons")
	}
	// Endpoint Latency cards render through the same markup family.
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
	// One definition plus the four surface entry points calling it.
	if n := strings.Count(script, "moveCard("); n < 5 {
		t.Errorf("moveCard referenced %d times, want at least 5 (definition + all-metrics/hooks, my-charts, endpoints, runtime)", n)
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
}

func TestHistoryHTTPHandlerDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if r.HistoryHTTPHandler() != nil {
		t.Error("HistoryHTTPHandler should return nil when UI disabled")
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
