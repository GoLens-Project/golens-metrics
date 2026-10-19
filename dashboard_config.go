package golens

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

// DashboardConfig is the versioned dashboard layout document: pinned panels,
// section ordering, and the HTTP status card view. It is the source of truth
// for the dashboard's client-side layout; localStorage acts only as a
// write-through cache/fallback.
//
// A single versioned JSON document is persisted atomically (temp file +
// rename) to cfg.UI.DashboardConfig. An empty path keeps the document in
// registry memory only.
type DashboardConfig struct {
	Version int              `json:"version"`
	Panels  []DashboardPanel `json:"panels"`
	Order   DashboardOrder   `json:"order"`
	// HTTPCardView selects the HTTP status card's column set:
	// "breakdown" (2xx-5xx + total, default) or "totals" (total + errors).
	HTTPCardView string `json:"http_card_view,omitempty"`
}

// DashboardPanel is one pinned dashboard panel. ChartType and Size are the
// minimal customization set; both default to server-side values derived from
// the metric kind when empty, so documents saved before they existed render
// unchanged.
type DashboardPanel struct {
	ID     string `json:"id"`
	Metric string `json:"metric"`
	// ChartType overrides the rendered chart: line | bars | gauge |
	// big-number. Empty = default derived from metric kind.
	ChartType string `json:"chart_type,omitempty"`
	// Size selects the grid span preset: sm | md | lg. Empty = the chart's
	// default span. Full free-form resize stays out of scope.
	Size string `json:"size,omitempty"`
}

// Panel field vocabularies, shared by the PUT validator and the renderer.
var (
	validPanelChartTypes = map[string]bool{"": true, "line": true, "bars": true, "gauge": true, "big-number": true}
	validPanelSizes      = map[string]bool{"": true, "sm": true, "md": true, "lg": true}
	validHTTPCardViews   = map[string]bool{"": true, httpViewBreakdown: true, httpViewTotals: true}
)

// DashboardOrder holds the user's drag-reorder arrangement per section.
type DashboardOrder struct {
	AllMetrics []string `json:"all_metrics"`
	Hooks      []string `json:"hooks"`
	Endpoints  []string `json:"endpoints"`
}

func defaultDashboardConfig() *DashboardConfig {
	return &DashboardConfig{
		Version: 1,
		Panels:  []DashboardPanel{},
		Order:   DashboardOrder{AllMetrics: []string{}, Hooks: []string{}, Endpoints: []string{}},
	}
}

// loadDashboardConfig reads the persisted layout document at construction
// time. A missing file or empty path yields defaults; a corrupt or
// wrong-version file logs a warning and falls back to defaults — a broken
// dashboard file must never crash startup.
func (r *Registry) loadDashboardConfig() *DashboardConfig {
	def := defaultDashboardConfig()
	if r.dashPath == "" {
		return def
	}
	data, err := os.ReadFile(r.dashPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[golens] warning: cannot read dashboard config %s: %v; using defaults", r.dashPath, err)
		}
		return def
	}
	var d DashboardConfig
	if err := json.Unmarshal(data, &d); err != nil {
		log.Printf("[golens] warning: corrupt dashboard config %s: %v; using defaults", r.dashPath, err)
		return def
	}
	if d.Version != 1 {
		log.Printf("[golens] warning: dashboard config %s has unsupported version %d (need 1); using defaults", r.dashPath, d.Version)
		return def
	}
	return &d
}

// persistDashboardConfig atomically writes the layout document. Caller holds
// dashMu. A no-op when no path is configured (memory-only mode).
func (r *Registry) persistDashboardConfig(d *DashboardConfig) error {
	if r.dashPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.dashPath + ".tmp"
	// Best-effort cleanup: a failed write (partial file) or a failed rename
	// must not leave the temp file behind. After a successful rename there is
	// nothing left to remove and the error is ignored.
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.dashPath)
}

// writeJSONError writes a JSON-encoded error body so clients parsing
// r.json() never hit invalid JSON on failure paths (error text may contain
// quotes or control characters).
func writeJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// dashboardConfigHandler serves GET/PUT/DELETE /metrics/config: read the
// current layout document, validate + persist a new one, or reset to defaults.
func (r *Registry) dashboardConfigHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			r.dashMu.Lock()
			d := r.dash
			r.dashMu.Unlock()
			writeJSON(w, d)

		case http.MethodPut:
			var d DashboardConfig
			if err := json.NewDecoder(req.Body).Decode(&d); err != nil {
				writeJSONError(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if d.Version != 1 {
				writeJSONError(w, "version must be 1", http.StatusBadRequest)
				return
			}
			if len(d.Panels) > r.cfg.MaxMetrics {
				writeJSONError(w, "too many panels", http.StatusBadRequest)
				return
			}
			for _, p := range d.Panels {
				if p.ID == "" {
					writeJSONError(w, "panel id is required", http.StatusBadRequest)
					return
				}
				if p.Metric == "" {
					writeJSONError(w, "panel metric is required", http.StatusBadRequest)
					return
				}
				if !validPanelChartTypes[p.ChartType] {
					writeJSONError(w, "panel chart_type must be one of: line, bars, gauge, big-number", http.StatusBadRequest)
					return
				}
				if !validPanelSizes[p.Size] {
					writeJSONError(w, "panel size must be one of: sm, md, lg", http.StatusBadRequest)
					return
				}
			}
			if !validHTTPCardViews[d.HTTPCardView] {
				writeJSONError(w, "http_card_view must be breakdown or totals", http.StatusBadRequest)
				return
			}
			if d.Order.AllMetrics == nil {
				d.Order.AllMetrics = []string{}
			}
			if d.Order.Hooks == nil {
				d.Order.Hooks = []string{}
			}
			if d.Order.Endpoints == nil {
				d.Order.Endpoints = []string{}
			}
			if d.Panels == nil {
				d.Panels = []DashboardPanel{}
			}

			// Persist first, mutate only on success: a failed persist must
			// leave memory and disk consistent (GET keeps serving the last
			// saved document).
			r.dashMu.Lock()
			defer r.dashMu.Unlock()
			if err := r.persistDashboardConfig(&d); err != nil {
				writeJSONError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			r.dash = &d
			writeJSON(w, &d)

		case http.MethodDelete:
			def := defaultDashboardConfig()
			r.dashMu.Lock()
			defer r.dashMu.Unlock()
			if err := r.persistDashboardConfig(def); err != nil {
				writeJSONError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			r.dash = def
			w.WriteHeader(http.StatusNoContent)

		default:
			writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// DashboardConfigHTTPHandler returns the dashboard layout config handler
// (GET/PUT/DELETE /metrics/config) for individual route mounting. Returns nil
// if the UI is disabled.
func (r *Registry) DashboardConfigHTTPHandler() http.Handler {
	if !r.cfg.UI.Enabled {
		return nil
	}
	return r.dashboardConfigHandler()
}
