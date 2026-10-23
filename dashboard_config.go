package golens

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
// A single versioned JSON document is persisted atomically to the configured
// store: the ui.dashboard_config_db sqlite database (single-row upsert) when
// set, else the ui.dashboard_config file (temp file + rename). Neither set
// keeps the document in registry memory only.
type DashboardConfig struct {
	Version int              `json:"version"`
	Panels  []DashboardPanel `json:"panels"`
	Order   DashboardOrder   `json:"order"`
	// HTTPCardView selects the HTTP status card's column set:
	// "breakdown" (2xx-5xx + total, default) or "totals" (total + errors).
	HTTPCardView string `json:"http_card_view,omitempty"`
	// Timeframe is the dashboard-global chart window: a relative preset
	// (5m,30m,1h,4h,12h,24h,1w) or "custom" (start-moment driven, the moment
	// itself stays client-side). Empty = the client default (1h).
	Timeframe string `json:"timeframe,omitempty"`
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
// later, or an as-yet-empty drop target). Parent optionally nests the band
// under the section with that id, at unlimited depth; empty = a top-level
// band. The list itself stays flat: every section is still addressed,
// validated, and persisted by its single id.
type DashboardSection struct {
	ID     string   `json:"id"`
	Name   string   `json:"name,omitempty"`
	Items  []string `json:"items,omitempty"`
	Parent string   `json:"parent,omitempty"`
	// Timeframe overrides the global chart window for the charts in this
	// band; nested bands inherit the nearest ancestor's override. Empty =
	// follow the global value.
	Timeframe string `json:"timeframe,omitempty"`
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

// validTimeframes is the accepted timeframe vocabulary: empty (= follow the
// global value), the relative presets (the defaultDurations keys), and
// "custom". PUT rejects anything else; load heals anything else to empty.
var validTimeframes = func() map[string]bool {
	m := map[string]bool{"": true}
	for _, tf := range timeframeChoices {
		m[tf] = true
	}
	return m
}()

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
// slot is a single DOM node, so the client cannot render it twice) — the flat
// list makes those once-only checks tree-wide regardless of nesting; any
// non-block item must reference an existing panel id (panels nil = presets,
// which cannot know runtime panel ids, so they may only place blocks); panel
// ids equal to a reserved block id are rejected — they would be ambiguous in
// Items — and so are duplicate ids or metrics within Panels (the client keys
// panels on the metric name). Parent pointers share the section-id namespace:
// a parent must name another existing section, and the parent graph must stay
// acyclic — the server's recursive band render halts on exactly that.
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
	// Parent-pointer integrity, after all ids are collected (a parent may be
	// defined later in the list): no self-parent, no dangling id, no cycle.
	parentOf := make(map[string]string, len(sections))
	for _, s := range sections {
		if s.Parent == "" {
			continue
		}
		if s.Parent == s.ID {
			return fmt.Errorf("section %q cannot be its own parent", s.ID)
		}
		if !seenSection[s.Parent] {
			return fmt.Errorf("section %q references unknown parent %q", s.ID, s.Parent)
		}
		parentOf[s.ID] = s.Parent
	}
	// Cycle walk over the parent graph, memoized: 1 = on the current chain,
	// 2 = the chain resolves to a top-level section. Every section gets one
	// walk regardless of depth, so unbounded nesting costs nothing extra.
	state := make(map[string]int, len(sections))
	for _, s := range sections {
		chain := []string{}
		cur := s.ID
		for state[cur] == 0 {
			state[cur] = 1
			chain = append(chain, cur)
			p := parentOf[cur]
			if p == "" {
				cur = "" // resolved at a top-level section
				break
			}
			cur = p
		}
		// cur was marked 1 by this walk and is not the chain's root: it is
		// the cycle's entry point.
		if cur != "" && state[cur] == 1 {
			return fmt.Errorf("section %q is part of a parent cycle", cur)
		}
		for _, id := range chain {
			state[id] = 2
		}
	}
	return nil
}

// healDashboardSections repairs a hand-edited document in memory: a section
// whose parent chain is broken — a dangling parent id, a self-parent, or a
// cycle — is promoted to top level (parent cleared), keeping its items and
// its own children. It returns the healed section ids. Orphans cannot
// originate from the UI or a validated PUT, so this path exists for
// hand-edited or legacy files; the file on disk stays as authored and a later
// PUT re-persists the healed layout.
func healDashboardSections(d *DashboardConfig) []string {
	byID := make(map[string]int, len(d.Sections))
	for i, s := range d.Sections {
		byID[s.ID] = i
	}
	var healed []string
	for i := range d.Sections {
		if d.Sections[i].Parent == "" {
			continue
		}
		broken := false
		seen := map[string]bool{d.Sections[i].ID: true}
		for cur := d.Sections[i].Parent; cur != ""; {
			if seen[cur] {
				broken = true // cycle (includes the self-parent case)
				break
			}
			seen[cur] = true
			j, ok := byID[cur]
			if !ok {
				broken = true // dangling parent id
				break
			}
			cur = d.Sections[j].Parent
		}
		if broken {
			d.Sections[i].Parent = ""
			healed = append(healed, d.Sections[i].ID)
		}
	}
	return healed
}

// healTimeframes copies secs with unsupported timeframe values cleared.
// loadDashboardConfig applies it to saved documents; seedSections applies it
// to presets, which reach the client through neither the loader heal nor the
// PUT validator — the two ingestion paths an invalid value must not survive,
// or it 400s every history fetch in the band and bricks layout persistence
// (persistDash retries forever) once the band is PUT back. The input may
// alias shared config/preset state, so healing happens on a copy.
func healTimeframes(secs []DashboardSection) []DashboardSection {
	out := make([]DashboardSection, len(secs))
	for i, s := range secs {
		if !validTimeframes[s.Timeframe] {
			s.Timeframe = ""
		}
		out[i] = s
	}
	return out
}

// dashStoreName is the persistence location named in dashboard-config log
// lines: the sqlite database path when that backend is active, else the file
// path (empty in memory-only mode).
func (r *Registry) dashStoreName() string {
	if r.dashDB != nil {
		return r.cfg.UI.DashboardConfigDB
	}
	return r.dashPath
}

// loadDashboardConfig reads the persisted layout document from the active
// store at construction time. A missing document or empty store yields
// defaults; a corrupt or wrong-version document logs a warning and falls back
// to defaults — a broken dashboard document must never crash startup. A
// successfully loaded document marks the registry as "saved" (dashSaved):
// presets seed only before the first save, never over an existing document. A
// loaded document with broken parent pointers is healed in memory (one
// warning line), so GET always serves the layout rendering uses.
func (r *Registry) loadDashboardConfig() *DashboardConfig {
	def := defaultDashboardConfig()
	var data []byte
	switch {
	case r.dashDB != nil:
		d, exists, err := dashConfigLoad(r.dashDB)
		if err != nil {
			log.Printf("[golens] warning: cannot read dashboard config from %s: %v; using defaults", r.dashStoreName(), err)
			return def
		}
		if !exists {
			return def
		}
		data = d
	case r.dashPath != "":
		d, err := os.ReadFile(r.dashPath)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[golens] warning: cannot read dashboard config %s: %v; using defaults", r.dashStoreName(), err)
			}
			return def
		}
		data = d
	default:
		return def
	}
	var d DashboardConfig
	if err := json.Unmarshal(data, &d); err != nil {
		log.Printf("[golens] warning: corrupt dashboard config %s: %v; using defaults", r.dashStoreName(), err)
		return def
	}
	if d.Version != 1 {
		log.Printf("[golens] warning: dashboard config %s has unsupported version %d (need 1); using defaults", r.dashStoreName(), d.Version)
		return def
	}
	if healed := healDashboardSections(&d); len(healed) > 0 {
		log.Printf("[golens] warning: dashboard config %s: sections with broken parents promoted to top level: %s", r.dashStoreName(), strings.Join(healed, ", "))
	}
	// Heal unsupported timeframes (hand-edited files, or presets — which
	// bypass both this loader and the PUT validator; see healTimeframes): an
	// invalid value would otherwise poison every history fetch with a
	// 400-ing duration and brick layout persistence on the next PUT.
	if !validTimeframes[d.Timeframe] {
		d.Timeframe = ""
	}
	d.Sections = healTimeframes(d.Sections)
	r.dashSaved = true
	return &d
}

// seedSections resolves the preset chain: the Go-API preset wins over the
// YAML fallback, which wins over the built-in default layout. An invalid
// preset is a programming/config error: it is logged and skipped rather than
// half-applied, falling through to the next surface.
func (r *Registry) seedSections() []DashboardSection {
	if len(r.dashPreset) > 0 {
		return healTimeframes(r.dashPreset)
	}
	if len(r.cfg.UI.DashboardSections) > 0 {
		// Presets run before any panels exist, so they may only place blocks —
		// the same rule the Go-API preset enforces. An invalid YAML preset is
		// logged and skipped so it cannot render a broken layout.
		if err := validateDashboardSections(r.cfg.UI.DashboardSections, nil); err != nil {
			log.Printf("[golens] warning: ignoring invalid dashboard_sections preset: %v", err)
		} else {
			return healTimeframes(r.cfg.UI.DashboardSections)
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

// persistDashboardConfig atomically writes the layout document to the active
// store: a single-row upsert when the sqlite backend is configured, else a
// temp-file write + rename. Caller holds dashMu. A no-op when no store is
// configured (memory-only mode).
func (r *Registry) persistDashboardConfig(d *DashboardConfig) error {
	if r.dashPath == "" && r.dashDB == nil {
		return nil
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if r.dashDB != nil {
		return dashConfigPersist(r.dashDB, data)
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
			if !validTimeframes[d.Timeframe] {
				writeJSONError(w, "timeframe must be a preset (5m,30m,1h,4h,12h,24h,1w), custom, or empty", http.StatusBadRequest)
				return
			}
			for _, s := range d.Sections {
				if !validTimeframes[s.Timeframe] {
					writeJSONError(w, "section timeframe must be a preset (5m,30m,1h,4h,12h,24h,1w), custom, or empty", http.StatusBadRequest)
					return
				}
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
			// missing document is the "no saved document" state, so presets
			// re-seed after a reset (seed-once). The in-memory document
			// falls back to defaults either way.
			switch {
			case r.dashDB != nil:
				if err := dashConfigDelete(r.dashDB); err != nil {
					writeJSONError(w, err.Error(), http.StatusInternalServerError)
					return
				}
			case r.dashPath != "":
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
