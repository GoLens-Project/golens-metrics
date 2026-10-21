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

// The persist temp file never outlives the PUT: a failed rename (target path
// is a directory) and the happy path both leave no <path>.tmp behind.
func TestDashboardConfigPersistLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	// A directory as the file path makes the temp write succeed but the
	// rename onto the target fail.
	cfg.UI.DashboardConfig = filepath.Join(dir, "dash.json")
	if err := os.MkdirAll(cfg.UI.DashboardConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newConfigRegistry(t, cfg).dashboardConfigHandler()
	tmp := cfg.UI.DashboardConfig + ".tmp"

	rec := configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500 (rename onto a directory fails)", rec.Code)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("failed rename left %s behind", tmp)
	}

	// Happy path: the rename consumes the temp file; nothing lingers.
	if err := os.Remove(cfg.UI.DashboardConfig); err != nil {
		t.Fatal(err)
	}
	rec = configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("successful persist left %s behind", tmp)
	}
}

// A PUT persist failure whose error text contains double quotes (the
// configured path itself does) must still yield a valid JSON error body
// through writeJSONError — string interpolation would emit unparseable JSON.
// DELETE is covered separately: it removes the file instead of persisting,
// so its failure modes differ (see TestDashboardConfigDeleteFailureQuotedErrorBody).
func TestDashboardConfigPersistFailureQuotedErrorBody(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	// A missing parent directory fails the temp write, and the quote in the
	// file name surfaces in the os error text.
	cfg.UI.DashboardConfig = filepath.Join(dir, "missing", `da"sh.json`)
	h := newConfigRegistry(t, cfg).dashboardConfigHandler()

	rec := configRequest(h, http.MethodPut, `{"version":1,"panels":[]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not valid JSON: %q", rec.Body.String())
	}
	if !strings.Contains(e["error"], `da"sh.json`) {
		t.Errorf("error = %q, want the quoted path to survive intact", e["error"])
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

// --- sections -------------------------------------------------------------------

func getDashboardConfig(t *testing.T, r *Registry) DashboardConfig {
	t.Helper()
	return decodeConfig(t, configRequest(r.dashboardConfigHandler(), http.MethodGet, ""))
}

func sectionIDs(d DashboardConfig) []string {
	ids := make([]string, 0, len(d.Sections))
	for _, s := range d.Sections {
		ids = append(ids, s.ID)
	}
	return ids
}

func defaultSectionIDs() []string {
	def := defaultDashboardSections()
	ids := make([]string, 0, len(def))
	for _, s := range def {
		ids = append(ids, s.ID)
	}
	return ids
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The PUT validator enforces the section contract: section ids required and
// unique; items non-empty strings; each well-known block id placed at most
// once across all sections; non-block items must reference an existing
// panel; panel ids may never collide with a reserved block id (they would be
// ambiguous in Items).
func TestDashboardConfigPutSectionValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty section id", `{"version":1,"sections":[{"id":"","items":[]}]}`},
		{"duplicate section id", `{"version":1,"sections":[{"id":"a"},{"id":"a"}]}`},
		{"block in two sections", `{"version":1,"sections":[{"id":"a","items":["hooks"]},{"id":"b","items":["hooks"]}]}`},
		{"unknown panel ref", `{"version":1,"sections":[{"id":"a","items":["no_such_panel"]}]}`},
		{"empty item", `{"version":1,"sections":[{"id":"a","items":[""]}]}`},
		{"panel id reserved block id", `{"version":1,"panels":[{"id":"hooks","metric":"webhook_calls"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newConfigRegistry(t, DefaultConfig())
			rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
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

// Valid section documents save and read back: names are optional (empty =
// untitled band), items may be empty, and items may mix well-known blocks
// with pinned panel ids.
func TestDashboardConfigPutSectionsRoundTrip(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	h := r.dashboardConfigHandler()

	doc := `{"version":1,"panels":[{"id":"p1","metric":"my_metric"}],"order":{},
	        "sections":[{"id":"main","items":["all-metrics","p1"]},{"id":"spare","items":[]}]}`
	rec := configRequest(h, http.MethodPut, doc)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	saved := decodeConfig(t, rec)
	if len(saved.Sections) != 2 {
		t.Fatalf("echoed sections = %+v, want 2", saved.Sections)
	}
	if saved.Sections[0].ID != "main" || saved.Sections[0].Name != "" ||
		saved.Sections[0].Items[0] != "all-metrics" || saved.Sections[0].Items[1] != "p1" {
		t.Errorf("section main = %+v", saved.Sections[0])
	}
	if saved.Sections[1].ID != "spare" || len(saved.Sections[1].Items) != 0 {
		t.Errorf("section spare = %+v, want empty items", saved.Sections[1])
	}

	// A saved document's sections win over any seeding on every later GET.
	got := getDashboardConfig(t, r)
	if !sameStrings(sectionIDs(got), []string{"main", "spare"}) {
		t.Errorf("GET sections = %v, want [main spare]", sectionIDs(got))
	}
}

// Seed priority while no document has been saved: Go preset > YAML fallback >
// built-in default. An invalid preset is skipped whole (never half-applied)
// and falls through to the next surface.
func TestDashboardSectionsSeedPriority(t *testing.T) {
	goPreset := []DashboardSection{{ID: "from-go", Name: "Go", Items: []string{BlockAllMetrics}}}
	yamlPreset := []DashboardSection{{ID: "from-yaml", Name: "YAML", Items: []string{BlockInternals}}}
	dupPreset := []DashboardSection{{ID: "x"}, {ID: "x"}}

	t.Run("go preset", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		r.PresetDashboardSections(goPreset...)
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"from-go"}) {
			t.Errorf("sections = %v, want [from-go]", ids)
		}
	})
	t.Run("yaml fallback", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.UI.DashboardSections = yamlPreset
		r := newConfigRegistry(t, cfg)
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"from-yaml"}) {
			t.Errorf("sections = %v, want [from-yaml]", ids)
		}
	})
	t.Run("go wins over yaml", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.UI.DashboardSections = yamlPreset
		r := newConfigRegistry(t, cfg)
		r.PresetDashboardSections(goPreset...)
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"from-go"}) {
			t.Errorf("sections = %v, want [from-go]", ids)
		}
	})
	t.Run("default layout", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, defaultSectionIDs()) {
			t.Errorf("sections = %v, want default %v", ids, defaultSectionIDs())
		}
	})
	t.Run("invalid go preset skipped", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		r.PresetDashboardSections(dupPreset...)
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, defaultSectionIDs()) {
			t.Errorf("sections = %v, want default %v after an invalid preset", ids, defaultSectionIDs())
		}
	})
	t.Run("invalid yaml skipped", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.UI.DashboardSections = dupPreset
		r := newConfigRegistry(t, cfg)
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, defaultSectionIDs()) {
			t.Errorf("sections = %v, want default %v after an invalid yaml preset", ids, defaultSectionIDs())
		}
	})
}

// Seed-once: presets shape the layout only while no document has been saved.
// A PUT's document then wins on every GET, and DELETE drops it so the preset
// seeds again.
func TestDashboardSectionsSeedOnce(t *testing.T) {
	r := newConfigRegistry(t, DefaultConfig())
	r.PresetDashboardSections(DashboardSection{ID: "preset", Items: []string{BlockAllMetrics}})
	h := r.dashboardConfigHandler()

	if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"preset"}) {
		t.Fatalf("preset did not seed the unsaved layout: %v", ids)
	}

	rec := configRequest(h, http.MethodPut, `{"version":1,"order":{},"sections":[{"id":"saved","items":["internals"]}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"saved"}) {
		t.Errorf("saved document must win over the preset: %v", ids)
	}

	if rec := configRequest(h, http.MethodDelete, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"preset"}) {
		t.Errorf("preset must re-seed after DELETE: %v", ids)
	}
}

// Pre-sections v1 documents keep working unchanged: they load, keep their
// panels, and render the built-in default layout — both loaded from disk and
// saved via a PUT that omits sections.
func TestDashboardSectionsBackCompat(t *testing.T) {
	t.Run("saved document without sections", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dash.json")
		if err := os.WriteFile(path, []byte(`{"version":1,"panels":[{"id":"p1","metric":"m","chart_type":"gauge"}],"order":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		cfg.UI.DashboardConfig = path
		r := newConfigRegistry(t, cfg)
		d := getDashboardConfig(t, r)
		if len(d.Panels) != 1 || d.Panels[0].Metric != "m" {
			t.Errorf("panels = %+v, want the saved panel", d.Panels)
		}
		if ids := sectionIDs(d); !sameStrings(ids, defaultSectionIDs()) {
			t.Errorf("sections = %v, want default %v", ids, defaultSectionIDs())
		}
	})
	t.Run("put without sections", func(t *testing.T) {
		r := newConfigRegistry(t, DefaultConfig())
		rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, `{"version":1,"order":{}}`)
		if rec.Code != 200 {
			t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
		}
		if saved := decodeConfig(t, rec); saved.Sections != nil {
			t.Errorf("sections = %+v, want nil (omitted sections normalize off)", saved.Sections)
		}
		if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, defaultSectionIDs()) {
			t.Errorf("sections = %v, want default %v", ids, defaultSectionIDs())
		}
	})
}

// DELETE's contract is "no saved document": the file is removed, not
// overwritten with defaults, so presets re-seed after a reset (seed-once).
// Resetting with nothing on disk is still a successful, idempotent reset.
func TestDashboardConfigDeleteRemovesSavedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.json")
	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = path
	h := newConfigRegistry(t, cfg).dashboardConfigHandler()

	rec := configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("document not persisted: %v", err)
	}

	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("DELETE left %s on disk", path)
	}

	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204 (idempotent)", rec.Code)
	}
}

// A DELETE that cannot remove the document (the path is a non-empty
// directory) fails with a valid JSON error body through writeJSONError —
// string interpolation would emit unparseable JSON. A missing file is not a
// failure: it is the "no saved document" state DELETE is meant to reach.
func TestDashboardConfigDeleteFailureQuotedErrorBody(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	// A non-empty directory as the path makes os.Remove fail with an error
	// that is not ENOENT, and the quote in the name surfaces in its text.
	cfg.UI.DashboardConfig = filepath.Join(dir, `da"shdir`)
	if err := os.MkdirAll(cfg.UI.DashboardConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.UI.DashboardConfig, "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newConfigRegistry(t, cfg).dashboardConfigHandler()

	rec := configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not valid JSON: %q", rec.Body.String())
	}
	if !strings.Contains(e["error"], `da"shdir`) {
		t.Errorf("error = %q, want the quoted path to survive intact", e["error"])
	}
}
