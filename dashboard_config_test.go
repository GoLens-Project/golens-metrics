package golens

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newConfigRegistry(t *testing.T, cfg Config) *Registry {
	t.Helper()
	cfg.UI.Enabled = true
	return newTestRegistry(t, cfg)
}

func configRequest(handler http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, "/metrics/config", reader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeConfig(t *testing.T, rec *httptest.ResponseRecorder) DashboardConfig {
	t.Helper()
	var d DashboardConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return d
}

// GET with no saved document serves the defaults: version 1 with empty —
// not null — panels and order arrays.
func TestDashboardConfigDefaults(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	rec := configRequest(r.dashboardConfigHandler(), http.MethodGet, "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	d := decodeConfig(t, rec)
	if d.Version != 1 {
		t.Errorf("version = %d, want 1", d.Version)
	}
	if d.Panels == nil || len(d.Panels) != 0 {
		t.Errorf("panels = %+v, want empty non-nil", d.Panels)
	}
	if d.Order.AllMetrics == nil || d.Order.Hooks == nil || d.Order.Endpoints == nil {
		t.Errorf("order arrays must be non-nil, got %+v", d.Order)
	}
}

// PUT saves the document and GET serves it back.
func TestDashboardConfigPutGetRoundTrip(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	h := r.dashboardConfigHandler()

	doc := `{"version":1,"panels":[{"id":"p1","metric":"queue_depth","chart_type":"line","size":"lg"}],
	         "order":{"all_metrics":["queue_depth"],"hooks":[],"endpoints":[]},
	         "http_card_view":"totals"}`
	rec := configRequest(h, http.MethodPut, doc)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	saved := decodeConfig(t, rec)
	if len(saved.Panels) != 1 || saved.Panels[0].Metric != "queue_depth" ||
		saved.Panels[0].ChartType != "line" || saved.Panels[0].Size != "lg" {
		t.Errorf("echoed panels = %+v", saved.Panels)
	}
	if saved.Order.AllMetrics[0] != "queue_depth" {
		t.Errorf("echoed order = %+v", saved.Order)
	}
	if saved.HTTPCardView != "totals" {
		t.Errorf("http_card_view = %q, want totals", saved.HTTPCardView)
	}

	rec = configRequest(h, http.MethodGet, "")
	if got := decodeConfig(t, rec); len(got.Panels) != 1 || got.Panels[0].Metric != "queue_depth" {
		t.Errorf("GET after PUT = %+v", got)
	}
}

// Invalid documents are rejected with a 400 whose body is itself valid JSON
// (clients parsing r.json() must never hit invalid JSON on failure paths).
func TestDashboardConfigPutValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{"version":1,`},
		{"wrong version", `{"version":2}`},
		{"missing panel id", `{"version":1,"panels":[{"metric":"m"}]}`},
		{"missing panel metric", `{"version":1,"panels":[{"id":"p1"}]}`},
		{"bad chart type", `{"version":1,"panels":[{"id":"p1","metric":"m","chart_type":"pyramid"}]}`},
		{"bad size", `{"version":1,"panels":[{"id":"p1","metric":"m","size":"xxl"}]}`},
		{"bad http card view", `{"version":1,"http_card_view":"mega"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newConfigRegistry(t, DefaultConfig())
			rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var e map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("error body is not valid JSON: %q", rec.Body.String())
			}
			if e["error"] == "" {
				t.Error("error body missing error message")
			}
		})
	}
}

// The panel count is capped by MaxMetrics.
func TestDashboardConfigPutPanelCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxMetrics = 2
	r := newConfigRegistry(t, cfg)
	doc := `{"version":1,"panels":[
		{"id":"p1","metric":"m1"},{"id":"p2","metric":"m2"},{"id":"p3","metric":"m3"}]}`
	rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Unknown methods are rejected; DELETE resets to defaults.
func TestDashboardConfigDeleteResets(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	h := r.dashboardConfigHandler()

	rec := configRequest(h, http.MethodPatch, `{}`)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH status = %d, want 405", rec.Code)
	}

	rec = configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d", rec.Code)
	}

	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	if d := decodeConfig(t, configRequest(h, http.MethodGet, "")); len(d.Panels) != 0 {
		t.Errorf("panels after reset = %+v, want none", d.Panels)
	}
}

// A configured dashboard_config path persists the document across Registry
// instances.
func TestDashboardConfigPersistsAcrossRegistries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.json")

	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = path
	r := newConfigRegistry(t, cfg)
	doc := `{"version":1,"panels":[{"id":"p1","metric":"m","chart_type":"gauge"}],"order":{}}`
	if rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc); rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("document not persisted: %v", err)
	}
	r.Close()

	r2 := newConfigRegistry(t, cfg)
	defer r2.Close()
	d := decodeConfig(t, configRequest(r2.dashboardConfigHandler(), http.MethodGet, ""))
	if len(d.Panels) != 1 || d.Panels[0].Metric != "m" || d.Panels[0].ChartType != "gauge" {
		t.Errorf("reloaded config = %+v, want saved panel", d)
	}
}

// A corrupt or wrong-version file on disk must never crash startup: the
// registry falls back to defaults.
func TestDashboardConfigCorruptFileFallsBack(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"garbage", "not json at all"},
		{"wrong version", `{"version":99,"panels":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dash.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig()
			cfg.UI.DashboardConfig = path
			r := newConfigRegistry(t, cfg)
			defer r.Close()
			d := decodeConfig(t, configRequest(r.dashboardConfigHandler(), http.MethodGet, ""))
			if d.Version != 1 || len(d.Panels) != 0 {
				t.Errorf("loaded = %+v, want defaults", d)
			}
		})
	}
}

// Persist-first: when the document cannot be written, the PUT fails and GET
// keeps serving the last successfully saved document.
func TestDashboardConfigPutPersistFailureKeepsLastDoc(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	// A directory as the file path makes the persist write fail.
	cfg.UI.DashboardConfig = filepath.Join(dir, "dash.json")
	if err := os.MkdirAll(cfg.UI.DashboardConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	r := newConfigRegistry(t, cfg)
	h := r.dashboardConfigHandler()

	rec := configRequest(h, http.MethodPut, `{"version":1,"panels":[]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	rec = configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("second PUT status = %d, want 500", rec.Code)
	}
	if d := decodeConfig(t, configRequest(h, http.MethodGet, "")); len(d.Panels) != 0 {
		t.Errorf("memory mutated despite failed persist: %+v", d)
	}
}

// The saved document drives the card builders: a panel's chart_type and size
// override the per-kind default, an unhonorable saved chart falls back to the
// default view, and the HTTP card view selects the column set.
func TestCardOptionsHonorDashboardConfig(t *testing.T) {
	o := cardOptions{
		HTTPView: "totals",
		panels: map[string]DashboardPanel{
			"m_line":     {ID: "1", Metric: "m_line", ChartType: "line", Size: "lg"},
			"m_nobars":   {ID: "2", Metric: "m_nobars", ChartType: "bars"},
			"queue_hist": {ID: "3", Metric: "queue_hist", Size: "sm"},
		},
	}
	snaps := []MetricSnapshot{
		{Name: "m_line", Type: "counter", Value: 1},
		{Name: "m_nobars", Type: "counter", Value: 2},
		{Name: "queue_hist", Type: "histogram", Count: 5, Buckets: []BucketSnapshot{{UpperBound: 1, Count: 5}}},
		{Name: "http_requests_total_2xx", Type: "counter", Value: 9},
	}
	cards := buildCardsOpts(snaps, o)

	byName := map[string]metricCard{}
	for _, c := range cards {
		byName[c.Snapshot.Name] = c
	}
	if c := byName["m_line"]; c.View != "line" || c.Size != "lg" {
		t.Errorf("m_line view/size = %q/%q, want line/lg", c.View, c.Size)
	}
	// A saved bars chart on a bucket-less counter cannot be honored: number
	// fallback instead of an empty chart.
	if c := byName["m_nobars"]; c.View != "number" {
		t.Errorf("m_nobars view = %q, want number fallback", c.View)
	}
	// Size sm maps to the single-cell span ("" preset).
	if c := byName["queue_hist"]; c.View != "bars" || c.Size != "" {
		t.Errorf("queue_hist view/size = %q/%q, want bars/\"\"", c.View, c.Size)
	}
	// The HTTP card leads the grid with the configured view.
	if cards[0].Snapshot.Name != "http_requests_total" || cards[0].HTTPView != "totals" {
		t.Errorf("http card = %+v, want totals view", cards[0].Snapshot)
	}
}
