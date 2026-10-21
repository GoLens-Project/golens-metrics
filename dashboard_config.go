package golens

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

// Well-known fixed-block ids placeable in DashboardSection.Items. A block
// moves as a whole between sections; per-card rearrangement stays inside a
// block (existing reorder), and pinning remains the per-card escape hatch
// into user sections. Pinned panels are not blocks: they join sections by
// their panel id.
const (
	BlockRuntimeHealth = "runtime-health"
	BlockHistogram     = "histogram"
	BlockHooks         = "hooks"
	BlockAllMetrics    = "all-metrics"
	BlockEndpoints     = "endpoint-latency"
	BlockInternals     = "internals"
)

// wellKnownBlocks is the lookup set behind the PUT validator's item rules.
var wellKnownBlocks = map[string]bool{
	BlockRuntimeHealth: true,
	BlockHistogram:     true,
	BlockHooks:         true,
	BlockAllMetrics:    true,
	BlockEndpoints:     true,
	BlockInternals:     true,
}

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
	// Sections places dashboard content into named horizontal bands. Items
	// are well-known block ids or pinned-panel ids; order inside Items is
	// render order. Absent (or empty) Sections renders the built-in default
	// layout, so pre-sections documents are unchanged.
	Sections []DashboardSection `json:"sections,omitempty"`
}

// DashboardSection is one horizontal band of the dashboard. Items are panel
// ids or well-known fixed-block ids; order inside Items is render order.
// Name is free-text and optional — an unnamed section renders an untitled
// band. An empty Items list is allowed (a band holding only panels placed
// later, or an as-yet-empty drop target).
type DashboardSection struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Items []string `json:"items,omitempty"`
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

// defaultDashboardSections is the built-in layout — exactly the dashboard as
// it was before sections existed. The my-charts band holds the pinned panels
// (its Items stay empty until panels are pinned into it); every other band
// wraps one fixed block.
func defaultDashboardSections() []DashboardSection {
	return []DashboardSection{
		{ID: BlockRuntimeHealth, Name: "Runtime Health", Items: []string{BlockRuntimeHealth}},
		{ID: BlockHistogram, Name: "Histogram Time-Series", Items: []string{BlockHistogram}},
		{ID: BlockHooks, Name: "Hooks", Items: []string{BlockHooks}},
		{ID: "my-charts", Name: "My Charts"},
		{ID: BlockAllMetrics, Name: "All Metrics", Items: []string{BlockAllMetrics}},
		{ID: BlockEndpoints, Name: "Endpoint Latency", Items: []string{BlockEndpoints}},
		{ID: BlockInternals, Name: "Internals", Items: []string{BlockInternals}},
	}
}

// validateDashboardSections enforces the layout document's section contract:
// section ids non-empty and unique; items non-empty strings; each well-known
// block id and each panel id used at most once across all sections (a panel
// slot is a single DOM node, so the client cannot render it twice); any
// non-block item must reference an existing panel id (panels nil = presets,
// which cannot know runtime panel ids, so they may only place blocks); panel
// ids equal to a reserved block id are rejected — they would be ambiguous in
// Items — and so are duplicate ids or metrics within Panels (the client keys
// panels on the metric name).
func validateDashboardSections(sections []DashboardSection, panels []DashboardPanel) error {
	panelIDs := make(map[string]bool, len(panels))
	panelMetrics := make(map[string]bool, len(panels))
	for _, p := range panels {
		if panelIDs[p.ID] {
			return fmt.Errorf("duplicate panel id %q", p.ID)
		}
		panelIDs[p.ID] = true
		if panelMetrics[p.Metric] {
			return fmt.Errorf("duplicate panel metric %q", p.Metric)
		}
		panelMetrics[p.Metric] = true
		if wellKnownBlocks[p.ID] {
			return fmt.Errorf("panel id %q conflicts with a reserved block id", p.ID)
		}
	}
	seenSection := make(map[string]bool, len(sections))
	seenBlock := make(map[string]bool, len(sections))
	seenPanel := make(map[string]bool, len(panels))
	for _, s := range sections {
		if s.ID == "" {
			return fmt.Errorf("section id is required")
		}
		if seenSection[s.ID] {
			return fmt.Errorf("duplicate section id %q", s.ID)
		}
		seenSection[s.ID] = true
		for _, item := range s.Items {
			if item == "" {
				return fmt.Errorf("section %q has an empty item", s.ID)
			}
			if wellKnownBlocks[item] {
				if seenBlock[item] {
					return fmt.Errorf("block %q appears in more than one place", item)
				}
				seenBlock[item] = true
				continue
			}
			if !panelIDs[item] {
				return fmt.Errorf("section %q references unknown panel %q", s.ID, item)
			}
			if seenPanel[item] {
				return fmt.Errorf("panel %q appears in more than one place", item)
			}
			seenPanel[item] = true
		}
	}
	return nil
}

// loadDashboardConfig reads the persisted layout document at construction
// time. A missing file or empty path yields defaults; a corrupt or
// wrong-version file logs a warning and falls back to defaults — a broken
// dashboard file must never crash startup. A successfully loaded document
// marks the registry as "saved" (dashSaved): presets seed only before the
// first save, never over an existing document.
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
	r.dashSaved = true
	return &d
}

// seedSections resolves the preset chain: the Go-API preset wins over the
// YAML fallback, which wins over the built-in default layout. An invalid
// preset is a programming/config error: it is logged and skipped rather than
// half-applied, falling through to the next surface.
func (r *Registry) seedSections() []DashboardSection {
	if len(r.dashPreset) > 0 {
		return r.dashPreset
	}
	if len(r.cfg.UI.DashboardSections) > 0 {
		// Presets run before any panels exist, so they may only place blocks —
		// the same rule the Go-API preset enforces. An invalid YAML preset is
		// logged and skipped so it cannot render a broken layout.
		if err := validateDashboardSections(r.cfg.UI.DashboardSections, nil); err != nil {
			log.Printf("[golens] warning: ignoring invalid dashboard_sections preset: %v", err)
		} else {
			return r.cfg.UI.DashboardSections
		}
	}
	return defaultDashboardSections()
}

// effectiveSections is the section list the dashboard renders: the saved
// document's list when present; the preset chain while no document has been
// saved yet (seed-once); the built-in default for a saved document that
// predates sections. GET projects this list so clients always see the
// layout that rendering uses.
func (r *Registry) effectiveSections(d *DashboardConfig) []DashboardSection {
	if len(d.Sections) > 0 {
		return d.Sections
	}
	if !r.dashSaved {
		return r.seedSections()
	}
	return defaultDashboardSections()
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
			// Project the effective section list onto a copy: the stored
			// document stays pristine until a PUT, but clients always see
			// the layout rendering uses (preset-seeded while unsaved).
			d := *r.dash
			d.Sections = r.effectiveSections(r.dash)
			r.dashMu.Unlock()
			writeJSON(w, &d)

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
			if err := validateDashboardSections(d.Sections, d.Panels); err != nil {
				writeJSONError(w, err.Error(), http.StatusBadRequest)
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
			if len(d.Sections) == 0 {
				d.Sections = nil
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
			r.dashSaved = true
			writeJSON(w, &d)

		case http.MethodDelete:
			r.dashMu.Lock()
			defer r.dashMu.Unlock()
			// Remove the persisted document instead of writing defaults: a
			// missing file is the "no saved document" state, so presets
			// re-seed after a reset (seed-once). The in-memory document
			// falls back to defaults either way.
			if r.dashPath != "" {
				if err := os.Remove(r.dashPath); err != nil && !os.IsNotExist(err) {
					writeJSONError(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
			r.dash = defaultDashboardConfig()
			r.dashSaved = false
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
