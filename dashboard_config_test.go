package golens

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// getDashboardConfig fetches the layout document via GET /metrics/config.
func getDashboardConfig(t *testing.T, mux http.Handler) DashboardConfig {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics/config status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var d DashboardConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("GET /metrics/config body is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	return d
}

// putDashboardConfig PUTs the document and returns the recorder for status
// and body assertions.
func putDashboardConfig(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/metrics/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDashboardConfigGetDefaults(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	mux := newServer(r)

	d := getDashboardConfig(t, mux)
	if d.Version != 1 {
		t.Errorf("version = %d, want 1", d.Version)
	}
	if d.Panels == nil || len(d.Panels) != 0 {
		t.Errorf("panels = %#v, want empty non-nil slice", d.Panels)
	}
	if d.Order.AllMetrics == nil || d.Order.Hooks == nil || d.Order.Endpoints == nil {
		t.Errorf("order slices must be non-nil, got %#v", d.Order)
	}
}

func TestDashboardConfigPutRoundTripPersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.json")
	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = path
	r := newTestRegistry(t, cfg)
	mux := newServer(r)

	rec := putDashboardConfig(t, mux, `{
		"version": 1,
		"panels": [{"id": "p1", "metric": "http_requests_total"}],
		"order": {"all_metrics": ["http_requests_total", "b_second"], "hooks": [], "endpoints": []}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var echoed DashboardConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("PUT response not valid JSON: %v (%s)", err, rec.Body.String())
	}
	if len(echoed.Panels) != 1 || echoed.Panels[0].Metric != "http_requests_total" {
		t.Errorf("PUT response panels = %#v, want one pinned http_requests_total", echoed.Panels)
	}

	// The document is written to the configured file without a leftover
	// temp file from the atomic rename.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not persisted: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temp file survived the atomic rename")
	}
	if !strings.Contains(string(data), `"http_requests_total"`) {
		t.Errorf("persisted file missing pinned metric: %s", data)
	}

	// GET serves the updated document, and a fresh registry loads it from
	// disk at construction time.
	if d := getDashboardConfig(t, mux); len(d.Panels) != 1 {
		t.Errorf("GET after PUT panels = %#v, want the pinned panel", d.Panels)
	}
	r2 := newTestRegistry(t, cfg)
	if d := getDashboardConfig(t, newServer(r2)); len(d.Panels) != 1 || d.Panels[0].ID != "p1" {
		t.Errorf("reloaded panels = %#v, want persisted panel p1", d.Panels)
	}
}

func TestDashboardConfigPutValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxMetrics = 1
	r := newTestRegistry(t, cfg)
	mux := newServer(r)

	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"invalid JSON", `{"version": 1,`, "invalid JSON"},
		{"wrong version", `{"version": 2}`, "version must be 1"},
		{"too many panels", `{"version": 1, "panels": [{"id":"a","metric":"m"}, {"id":"b","metric":"n"}]}`, "too many panels"},
		{"panel without id", `{"version": 1, "panels": [{"metric": "m"}]}`, "panel id is required"},
		{"panel without metric", `{"version": 1, "panels": [{"id": "a"}]}`, "panel metric is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := putDashboardConfig(t, mux, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body: %s", rec.Code, rec.Body.String())
			}
			// Error bodies must themselves be valid JSON so client-side
			// r.json() never chokes on failure paths.
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("error body not valid JSON: %v (%s)", err, rec.Body.String())
			}
			if !strings.Contains(e.Error, tc.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", e.Error, tc.wantMsg)
			}
			// Rejected documents must not be stored.
			if d := getDashboardConfig(t, mux); len(d.Panels) != 0 {
				t.Errorf("rejected document was stored: %#v", d)
			}
		})
	}
}

func TestDashboardConfigPutNormalizesMissingFields(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	mux := newServer(r)

	rec := putDashboardConfig(t, mux, `{"version": 1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var d DashboardConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if d.Panels == nil || d.Order.AllMetrics == nil || d.Order.Hooks == nil || d.Order.Endpoints == nil {
		t.Errorf("missing fields must normalize to empty slices, got %#v", d)
	}
}

func TestDashboardConfigDeleteResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.json")
	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = path
	r := newTestRegistry(t, cfg)
	mux := newServer(r)

	if rec := putDashboardConfig(t, mux, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rec.Code)
	}

	req := httptest.NewRequest("DELETE", "/metrics/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}

	d := getDashboardConfig(t, mux)
	if len(d.Panels) != 0 {
		t.Errorf("panels after DELETE = %#v, want defaults", d.Panels)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("defaults not persisted after DELETE: %v", err)
	}
	if strings.Contains(string(data), `"p1"`) {
		t.Errorf("persisted defaults still contain the deleted panel: %s", data)
	}
}

func TestDashboardConfigMemoryOnlyMode(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig()) // no DashboardConfig path
	mux := newServer(r)

	if rec := putDashboardConfig(t, mux, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if d := getDashboardConfig(t, mux); len(d.Panels) != 1 {
		t.Errorf("panels = %#v, want the in-memory document", d.Panels)
	}
}

func TestDashboardConfigLoadFallsBackOnBadFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string
	}{
		{"corrupt JSON", `{"version": 1, "panels": [`},
		{"wrong version", `{"version": 99, "panels": [{"id":"p1","metric":"m"}]}`},
		{"valid JSON, not an object", `"just a string"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			redirectLog(t, &strings.Builder{})
			cfg := DefaultConfig()
			cfg.UI.DashboardConfig = filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(cfg.UI.DashboardConfig, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			r := newTestRegistry(t, cfg)
			d := getDashboardConfig(t, newServer(r))
			if d.Version != 1 || len(d.Panels) != 0 {
				t.Errorf("bad file must fall back to defaults, got version %d panels %#v", d.Version, d.Panels)
			}
		})
	}

	t.Run("unreadable path", func(t *testing.T) {
		var logs strings.Builder
		redirectLog(t, &logs)
		cfg := DefaultConfig()
		// A directory cannot be read as a config file, but startup must
		// still survive with defaults.
		cfg.UI.DashboardConfig = dir
		r := newTestRegistry(t, cfg)
		d := getDashboardConfig(t, newServer(r))
		if d.Version != 1 || len(d.Panels) != 0 {
			t.Errorf("unreadable path must fall back to defaults, got version %d panels %#v", d.Version, d.Panels)
		}
		if !strings.Contains(logs.String(), "cannot read dashboard config") {
			t.Errorf("expected a warning log, got: %s", logs.String())
		}
	})
}

// TestDashboardConfigPutPersistFailureIsNoOp: when the on-disk persist fails,
// the client gets a 500 with a JSON error body and the in-memory document is
// left untouched — GET keeps serving the last saved document.
func TestDashboardConfigPutPersistFailureIsNoOp(t *testing.T) {
	cfg := DefaultConfig()
	// Parent directory does not exist, so writing the temp file fails.
	cfg.UI.DashboardConfig = filepath.Join(t.TempDir(), "missing-subdir", "dashboard.json")
	r := newTestRegistry(t, cfg)
	mux := newServer(r)

	rec := putDashboardConfig(t, mux, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500, body: %s", rec.Code, rec.Body.String())
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("500 body not valid JSON: %v (%s)", err, rec.Body.String())
	}
	if e["error"] == "" {
		t.Error("500 body missing `error` field")
	}

	if d := getDashboardConfig(t, mux); len(d.Panels) != 0 {
		t.Errorf("failed PUT mutated the served document: panels = %#v, want defaults", d.Panels)
	}
}

// TestDashboardConfigDeletePersistFailureIsNoOp: a failing persist on DELETE
// must return 500 and keep the pre-DELETE document in memory.
func TestDashboardConfigDeletePersistFailureIsNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = filepath.Join(dir, "dashboard.json")
	r := newTestRegistry(t, cfg)
	mux := newServer(r)

	if rec := putDashboardConfig(t, mux, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}

	// Make the persist fail by replacing the config file with a directory:
	// the temp write succeeds, but os.Rename onto a directory fails with
	// EISDIR regardless of uid (permission bits would not stop root).
	if err := os.Remove(cfg.UI.DashboardConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.UI.DashboardConfig, 0o700); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/metrics/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE status = %d, want 500, body: %s", rec.Code, rec.Body.String())
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("500 body not valid JSON: %v (%s)", err, rec.Body.String())
	}

	if d := getDashboardConfig(t, mux); len(d.Panels) != 1 || d.Panels[0].ID != "p1" {
		t.Errorf("failed DELETE mutated the served document: panels = %#v, want p1", d.Panels)
	}
}

func TestDashboardConfigMethodNotAllowed(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	mux := newServer(r)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/metrics/config", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH status = %d, want 405", rec.Code)
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Errorf("error body not valid JSON: %v (%s)", err, rec.Body.String())
	}
}

func TestDashboardConfigHTTPHandlerNilWhenDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = false
	r := newTestRegistry(t, cfg)
	if h := r.DashboardConfigHTTPHandler(); h != nil {
		t.Error("handler must be nil when the UI is disabled")
	}

	if h := newTestRegistry(t, DefaultConfig()).DashboardConfigHTTPHandler(); h == nil {
		t.Error("handler must be non-nil when the UI is enabled")
	}
}

// TestMetricsDataJSONKeysAreLowercase pins the /metrics/data wire contract:
// the JSON keys are lowercase (GoLens 1.0 consumers, including the alerts
// page picker, read s.name / s.type).
func TestMetricsDataJSONKeysAreLowercase(t *testing.T) {
	r := newTestRegistry(t, DefaultConfig())
	mux := newServer(r)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics/data", nil)
	req.Header.Set("Accept", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected at least the pre-registered RED metrics")
	}
	for _, m := range raw {
		for key := range m {
			if key != strings.ToLower(key) {
				t.Errorf("snapshot JSON key %q is not lowercase", key)
			}
		}
		if _, ok := m["name"]; !ok {
			t.Error("snapshot JSON missing lowercase `name` key")
		}
		if _, ok := m["type"]; !ok {
			t.Error("snapshot JSON missing lowercase `type` key")
		}
	}
}
