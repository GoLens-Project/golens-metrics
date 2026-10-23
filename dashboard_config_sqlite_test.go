package golens

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newDashConfigTestDB opens the dashboard-config sqlite store on a fresh file
// at path, closing it when the test ends.
func newDashConfigTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := newDashConfigDB(path)
	if err != nil {
		t.Fatalf("open dashboard config db %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The store's lifecycle mirrors the file backend's write/replace/remove
// semantics: a fresh store reads as "no document", persisting twice upserts
// the single row (latest document wins), delete removes it, and deleting a
// missing row is a no-op.
func TestDashConfigStoreLifecycle(t *testing.T) {
	db := newDashConfigTestDB(t, filepath.Join(t.TempDir(), "dash.db"))

	if _, exists, err := dashConfigLoad(db); err != nil || exists {
		t.Fatalf("fresh store: exists=%v err=%v, want no document", exists, err)
	}

	doc1 := []byte(`{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if err := dashConfigPersist(db, doc1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, exists, err := dashConfigLoad(db)
	if err != nil || !exists || string(got) != string(doc1) {
		t.Fatalf("load = %q exists=%v err=%v, want the saved document", got, exists, err)
	}

	doc2 := []byte(`{"version":1,"panels":[]}`)
	if err := dashConfigPersist(db, doc2); err != nil {
		t.Fatalf("re-persist: %v", err)
	}
	got, _, err = dashConfigLoad(db)
	if err != nil || string(got) != string(doc2) {
		t.Fatalf("load after re-persist = %q err=%v, want the replacement document", got, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dashboard_config`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("row count = %d err=%v, want a single row", n, err)
	}

	if err := dashConfigDelete(db); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, exists, err := dashConfigLoad(db); err != nil || exists {
		t.Fatalf("load after delete: exists=%v err=%v, want no document", exists, err)
	}
	if err := dashConfigDelete(db); err != nil {
		t.Fatalf("second delete: %v, want an idempotent no-op", err)
	}
}

// A dashboard_config_db path that is not a valid sqlite database fails
// startup: New errors rather than silently falling back to memory-only mode.
func TestDashboardConfigDBBadFileFailsStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.UI.DashboardConfigDB = path
	cfg.applyDefaults()
	if r, err := New(cfg); err == nil {
		r.Close()
		t.Fatal("New succeeded on a corrupt dashboard_config_db file, want an error")
	}
}

// A configured dashboard_config_db persists the layout document across
// Registry instances.
func TestDashboardConfigDBPersistsAcrossRegistries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")

	cfg := DefaultConfig()
	cfg.UI.DashboardConfigDB = path
	r := newConfigRegistry(t, cfg)
	doc := `{"version":1,"panels":[{"id":"p1","metric":"m","chart_type":"gauge"}],"order":{}}`
	if rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc); rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created: %v", err)
	}
	r.Close()

	r2 := newConfigRegistry(t, cfg)
	defer r2.Close()
	d := decodeConfig(t, configRequest(r2.dashboardConfigHandler(), http.MethodGet, ""))
	if len(d.Panels) != 1 || d.Panels[0].Metric != "m" || d.Panels[0].ChartType != "gauge" {
		t.Errorf("reloaded config = %+v, want the saved panel", d)
	}
}

// When both stores are configured the sqlite database wins: its document is
// served, the file is ignored — neither read nor rewritten — and the startup
// log names the ignored dashboard_config path.
func TestDashboardConfigDBWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "dash.json")
	dbPath := filepath.Join(dir, "dash.db")
	fileDoc := `{"version":1,"panels":[{"id":"pf","metric":"file_metric"}],"order":{}}`
	dbDoc := `{"version":1,"panels":[{"id":"pd","metric":"db_metric"}],"order":{}}`

	if err := os.WriteFile(filePath, []byte(fileDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	db := newDashConfigTestDB(t, dbPath)
	if err := dashConfigPersist(db, []byte(dbDoc)); err != nil {
		t.Fatalf("seed db document: %v", err)
	}
	db.Close()

	cfg := DefaultConfig()
	cfg.UI.DashboardConfig = filePath
	cfg.UI.DashboardConfigDB = dbPath
	var logs bytes.Buffer
	redirectLog(t, &logs)
	r := newConfigRegistry(t, cfg)
	defer r.Close()

	if d := getDashboardConfig(t, r); len(d.Panels) != 1 || d.Panels[0].Metric != "db_metric" {
		t.Errorf("panels = %+v, want the db document", d.Panels)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != fileDoc {
		t.Errorf("ignored file changed: %q err=%v", data, err)
	}
	if !strings.Contains(logs.String(), filePath) {
		t.Errorf("startup log must name the ignored dashboard_config path, got: %s", logs.String())
	}
}

// A corrupt or wrong-version document in the database must never crash
// startup: the registry falls back to defaults, like the file backend.
func TestDashboardConfigDBCorruptDocumentFallsBack(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{"garbage", "not json at all"},
		{"wrong version", `{"version":99,"panels":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "dash.db")
			db := newDashConfigTestDB(t, dbPath)
			if err := dashConfigPersist(db, []byte(tc.doc)); err != nil {
				t.Fatalf("seed document: %v", err)
			}
			db.Close()

			cfg := DefaultConfig()
			cfg.UI.DashboardConfigDB = dbPath
			r := newConfigRegistry(t, cfg)
			defer r.Close()
			d := decodeConfig(t, configRequest(r.dashboardConfigHandler(), http.MethodGet, ""))
			if d.Version != 1 || len(d.Panels) != 0 {
				t.Errorf("loaded = %+v, want defaults", d)
			}
		})
	}
}

// DELETE on the sqlite backend removes the saved row — the "no saved
// document" state — so presets re-seed after a reset (seed-once), and a
// second DELETE stays idempotent.
func TestDashboardConfigDBDeleteRemovesRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dash.db")
	cfg := DefaultConfig()
	cfg.UI.DashboardConfigDB = dbPath
	r := newConfigRegistry(t, cfg)
	h := r.dashboardConfigHandler()

	r.PresetDashboardSections(DashboardSection{ID: "preset", Items: []string{BlockAllMetrics}})
	rec := configRequest(h, http.MethodPut, `{"version":1,"order":{},"sections":[{"id":"saved","items":["internals"]}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"saved"}) {
		t.Fatalf("saved document did not win over the preset: %v", ids)
	}

	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	// The row must really be gone, not just shadowed in memory: a dropped row
	// is what lets the preset re-seed and keeps a restart from resurrecting
	// the deleted layout.
	if _, exists, err := dashConfigLoad(r.dashDB); err != nil || exists {
		t.Errorf("DELETE left the row in %s: exists=%v err=%v", dbPath, exists, err)
	}
	if ids := sectionIDs(getDashboardConfig(t, r)); !sameStrings(ids, []string{"preset"}) {
		t.Errorf("preset must re-seed after DELETE: %v", ids)
	}

	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204 (idempotent)", rec.Code)
	}
}

// Persist-first holds on the sqlite backend too: a failed store write fails
// the PUT (and the DELETE) with 500, and the last saved document keeps
// serving from memory.
func TestDashboardConfigDBPutPersistFailureKeepsLastDoc(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.DashboardConfigDB = filepath.Join(t.TempDir(), "dash.db")
	r := newConfigRegistry(t, cfg)
	h := r.dashboardConfigHandler()

	rec := configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p1","metric":"m"}]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}

	// Drop the table under the registry so the next store write fails.
	if _, err := r.dashDB.Exec(`DROP TABLE dashboard_config`); err != nil {
		t.Fatal(err)
	}

	rec = configRequest(h, http.MethodPut, `{"version":1,"panels":[{"id":"p2","metric":"other"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT after store failure = %d, want 500", rec.Code)
	}
	rec = configRequest(h, http.MethodDelete, "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("DELETE after store failure = %d, want 500", rec.Code)
	}
	if d := decodeConfig(t, configRequest(h, http.MethodGet, "")); len(d.Panels) != 1 || d.Panels[0].ID != "p1" {
		t.Errorf("memory mutated despite failed persist: %+v", d)
	}
}

// The documented coexistence promise: ui.dashboard_config_db may name the
// same database as storage.path. Two pooled connections share one file, the
// layout document and the metric history stay independent, and both survive a
// restart.
func TestDashboardConfigDBSharedWithStorage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "golens.db")
	cfg := DefaultConfig()
	cfg.Storage.Backend = "sqlite"
	cfg.Storage.Path = dbPath
	cfg.UI.DashboardConfigDB = dbPath
	r := newConfigRegistry(t, cfg)

	doc := `{"version":1,"panels":[{"id":"p1","metric":"m"}],"order":{}}`
	if rec := configRequest(r.dashboardConfigHandler(), http.MethodPut, doc); rec.Code != 200 {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	now := time.Now()
	m := AggregatedMetric{Name: "http_requests_total", Type: "counter", Count: 3, Sum: 3,
		WindowStart: now, WindowEnd: now.Add(time.Minute)}
	if err := r.storage.Store(ctx, m); err != nil {
		t.Fatalf("storage store: %v", err)
	}
	r.Close()

	r2 := newConfigRegistry(t, cfg)
	defer r2.Close()
	if d := decodeConfig(t, configRequest(r2.dashboardConfigHandler(), http.MethodGet, "")); len(d.Panels) != 1 || d.Panels[0].Metric != "m" {
		t.Errorf("layout after restart = %+v, want the saved panel", d)
	}
	res, err := r2.storage.Query(ctx, Query{Name: "http_requests_total"})
	if err != nil {
		t.Fatalf("storage query: %v", err)
	}
	if len(res) != 1 || res[0].Count != 3 {
		t.Errorf("metric after restart = %+v, want the stored sample", res)
	}
}
