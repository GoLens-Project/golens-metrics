// GoLens dashboard page script. Server-rendered values (poll interval,
// notifier flag) arrive via window.GoLensConfig, set by an inline bootstrap
// before this file loads. Shared helpers live in common.js.
'use strict';

var GoLensConfig = window.GoLensConfig || {};
var POLL_MS = GoLensConfig.pollMs || 5000;
var HAS_NOTIFIER = !!GoLensConfig.hasNotifier;

// --- Dashboard layout persistence -------------------------------------------
// The server-side document (/metrics/config) is the source of truth for
// pinned charts (with per-panel chart type / size), the HTTP card view, and
// section ordering; localStorage is only a write-through cache/fallback for
// when the server copy is unavailable (UI config disabled, read-only mount,
// failed PUT). golens.theme stays purely client-side (common.js).

function lsJSON(key) { try { return JSON.parse(localStorage.getItem(key) || '[]'); } catch (e) { return []; } }

// bootSections clones the server-rendered effective section list (same list
// that rendered the page's bands), so the client can boot and render even
// before /metrics/config answers.
function bootSections() {
  var s = GoLensConfig.sections;
  return (s && s.length) ? JSON.parse(JSON.stringify(s)) : [];
}

// Boot from the localStorage cache; when the server document loads it
// replaces this state (loadDash).
var dashCfg = {
  version: 1,
  panels: lsJSON('golens.charts').map(function (n) { return { id: n, metric: n }; }),
  order: { all_metrics: lsJSON('golens.order'), hooks: lsJSON('golens.hooksorder'), endpoints: lsJSON('golens.eporder') },
  http_card_view: localStorage.getItem('golens.httpview') || '',
  timeframe: localStorage.getItem('golens.timeframe') || '',
  sections: bootSections()
};
var dashRemote = false;

// Custom-start moment (unix seconds) behind the "custom" timeframe choice.
// Deliberately browser state, not part of the layout document: an absolute
// timestamp goes stale, so only the choice itself persists server-side.
var tfStartSec = parseInt(localStorage.getItem('golens.tfstart'), 10) || 0;

// dashHasOrder reports whether a config document carries any section order.
function dashHasOrder(d) { var o = d.order || {}; return !!((o.all_metrics || []).length || (o.hooks || []).length || (o.endpoints || []).length); }

// chartsToPanels re-derives the config's panel list from a chart-name list,
// preserving any per-panel config already saved for a metric.
function chartsToPanels(charts) {
  var byMetric = {};
  dashCfg.panels.forEach(function (p) { byMetric[p.metric] = p; });
  return charts.map(function (n) { return byMetric[n] || { id: n, metric: n }; });
}

// setHTTPView flips the HTTP status card's column set, persists it, and
// refreshes the card grid so the change is visible immediately. The column
// set is rendered server-side, so with the server copy unavailable the reload
// would re-render the old view and the toggle would never move — skip it and
// keep the pending view for the first-connect promotion instead.
function setHTTPView(view) {
  dashCfg.http_card_view = view;
  persistDash();
  if (dashRemote && window.htmx) htmx.ajax('GET', '/metrics/data', '#charts');
}

// Backoff state for the server-document PUT: dashRemote stays true on a
// failure (the server copy remains the source of truth) and the change is
// retried with doubling delay up to a 60s ceiling.
var dashRetryTimer = null;
var dashRetryMs = 0;

function persistDash() {
  // localStorage write-through cache first, so the layout survives even if
  // the server PUT fails.
  try {
    localStorage.setItem('golens.charts', JSON.stringify(dashCfg.panels.map(function (p) { return p.metric; })));
    localStorage.setItem('golens.order', JSON.stringify(dashCfg.order.all_metrics || []));
    localStorage.setItem('golens.hooksorder', JSON.stringify(dashCfg.order.hooks || []));
    localStorage.setItem('golens.eporder', JSON.stringify(dashCfg.order.endpoints || []));
    localStorage.setItem('golens.httpview', dashCfg.http_card_view || '');
    localStorage.setItem('golens.timeframe', dashCfg.timeframe || '');
  } catch (e) {}
  if (!dashRemote) return;
  fetchJSON('/metrics/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(dashCfg)
  }).then(function () {
    dashRetryMs = 0;
  }).catch(function () {
    if (!dashRetryMs) showToast('changes are local only — retrying');
    dashRetryMs = dashRetryMs ? Math.min(dashRetryMs * 2, 60000) : 2000;
    clearTimeout(dashRetryTimer);
    dashRetryTimer = setTimeout(persistDash, dashRetryMs);
  });
}

// --- timeframes -----------------------------------------------------------------
// One dashboard-global timeframe plus a per-band override. The effective
// timeframe for a chart is the nearest enclosing band with its own selection
// (so a nested child without one inherits its parent's override), else the
// global value; resolution happens client-side at fetch time, where every
// history consumer asks effectiveDuration() just before fetching. "custom"
// trades the relative window for a start moment (tfStartSec).

var TF_DEFAULT = '1h';

function tfForSection(id) {
  var secs = dashCfg.sections || [];
  for (var i = 0; i < secs.length; i++) if (secs[i].id === id) return secs[i].timeframe || '';
  return '';
}

function effectiveDuration(el) {
  var sec = el ? el.closest('[data-section]') : null;
  while (sec) {
    var tf = tfForSection(sec.getAttribute('data-section'));
    if (tf) return tf;
    sec = sec.parentElement ? sec.parentElement.closest('[data-section]') : null;
  }
  return dashCfg.timeframe || TF_DEFAULT;
}

// historyQS maps a timeframe choice onto /metrics/history query params:
// presets fetch ?duration=, custom fetches ?start= (unix seconds). A custom
// choice with no stored moment (doc persisted elsewhere, fresh browser)
// keeps the default window rather than seeding "now" — an empty window tells
// the user less than the default; picking a moment in the picker activates it.
function historyQS(tf) {
  if (tf === 'custom' && tfStartSec) return 'start=' + tfStartSec;
  return 'duration=' + (tf && tf !== 'custom' ? tf : TF_DEFAULT);
}

// applyTimeframeUI syncs the selector controls with the config document.
function applyTimeframeUI() {
  var sel = document.getElementById('tf-global');
  if (sel) sel.value = dashCfg.timeframe || TF_DEFAULT;
  document.querySelectorAll('[data-section]').forEach(function (sec) {
    var s = sec.querySelector('.sec-tf');
    if (s) s.value = tfForSection(sec.getAttribute('data-section'));
  });
  var picker = document.getElementById('tf-start');
  if (picker) {
    picker.hidden = !(dashCfg.timeframe === 'custom' || (dashCfg.sections || []).some(function (s) { return s.timeframe === 'custom'; }));
    if (tfStartSec && !picker.value) picker.value = tfLocalInput(tfStartSec);
  }
}

// tfLocalInput formats unix seconds for the datetime-local picker (local time).
function tfLocalInput(sec) {
  var d = new Date(sec * 1000), p = function (x) { return (x < 10 ? '0' : '') + x; };
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + 'T' + p(d.getHours()) + ':' + p(d.getMinutes());
}

// refreshTimeframes re-fetches every history consumer under the new
// resolution: the runtime/histogram components listen for golens-tf, the
// line-chart canvases redraw here.
function refreshTimeframes() {
  window.dispatchEvent(new Event('golens-tf'));
  drawLineCharts();
}

function setGlobalTimeframe(v) {
  dashCfg.timeframe = v;
  applyTimeframeSettle(v);
}

function setSectionTimeframe(sel) {
  var sec = sel.closest('[data-section]');
  if (!sec) return;
  var id = sec.getAttribute('data-section');
  var secs = dashCfg.sections || [];
  for (var i = 0; i < secs.length; i++) if (secs[i].id === id) { secs[i].timeframe = sel.value; break; }
  applyTimeframeSettle(sel.value);
}

function applyTimeframeSettle(v) {
  if (v === 'custom') ensureCustomStart();
  applyTimeframeUI();
  persistDash();
  refreshTimeframes();
}

// ensureCustomStart seeds the picker with "now" the first time a selector
// lands on custom, so ?start= always carries a value.
function ensureCustomStart() {
  if (!tfStartSec) tfStartSec = Math.floor(Date.now() / 1000);
  try { localStorage.setItem('golens.tfstart', String(tfStartSec)); } catch (e) {}
  var picker = document.getElementById('tf-start');
  if (picker && !picker.value) picker.value = tfLocalInput(tfStartSec);
}

// setCustomStart applies the picked moment; datetime-local values parse as
// local time.
function setCustomStart(v) {
  var t = v ? new Date(v).getTime() : NaN;
  if (isNaN(t)) return;
  tfStartSec = Math.floor(t / 1000);
  try { localStorage.setItem('golens.tfstart', String(tfStartSec)); } catch (e) {}
  refreshTimeframes();
}

// applyDash re-hydrates the UI from the current config document.
function applyDash() {
  if (window.Alpine && Alpine.store('ui')) {
    Alpine.store('ui').charts = dashCfg.panels.map(function (p) { return p.metric; });
  }
  applyTimeframeUI();
  sortCharts(); applyFilter(); renderPinned(); applyHookOrder();
}

// normalizeSections repairs a loaded document for the client: without
// sections (shouldn't happen — GET projects the effective list) fall back to
// the page's boot list; duplicate panel identities collapse (the client is
// metric-keyed end to end — the store, every chartsToPanels rebuild, and
// remove — and the PUT validator rejects duplicate metrics and ids, so a
// legacy document must be healed before it is ever re-PUT); and any panel
// not placed in a section lands in the my-charts band (or the last one), so
// a pin is never invisible.
function normalizeSections(d) {
  if (!d.sections || !d.sections.length) d.sections = bootSections();
  // Keep the last panel per metric (what the by-metric rebuild renders),
  // then collapse duplicate ids to the first. A dropped panel's id is
  // pruned from section items unless a kept panel still holds it, so the
  // healed document never references an unknown panel.
  var seenMetric = {}, kept = [], droppedIDs = {};
  for (var i = d.panels.length - 1; i >= 0; i--) {
    var p = d.panels[i];
    if (seenMetric[p.metric]) { droppedIDs[p.id] = true; continue; }
    seenMetric[p.metric] = true;
    kept.unshift(p);
  }
  var seenID = {};
  d.panels = kept.filter(function (p) {
    if (seenID[p.id]) { droppedIDs[p.id] = true; return false; }
    seenID[p.id] = true;
    return true;
  });
  d.panels.forEach(function (p) { delete droppedIDs[p.id]; });
  (d.sections || []).forEach(function (s) {
    if (s.items && s.items.length) s.items = s.items.filter(function (id) { return !droppedIDs[id]; });
  });
  // A panel id may repeat across sections (multi-band copies) but never
  // twice within one band: the PUT validator rejects that and a rejected
  // PUT retries forever, so a hand-edited document is healed before it can
  // ever be re-PUT.
  (d.sections || []).forEach(function (s) {
    if (!s.items || !s.items.length) return;
    var seenItem = {};
    s.items = s.items.filter(function (id) {
      if (seenItem[id]) return false;
      seenItem[id] = true;
      return true;
    });
  });
  var placed = {};
  d.sections.forEach(function (s) { (s.items || []).forEach(function (id) { placed[id] = true; }); });
  var host = null;
  d.sections.forEach(function (s) { if (s.id === 'my-charts') host = s; });
  if (!host) host = d.sections[d.sections.length - 1];
  d.panels.forEach(function (p) {
    if (!placed[p.id] && host) {
      if (!host.items) host.items = [];
      host.items.push(p.id);
      placed[p.id] = true;
    }
  });
}

// sanitizeLegacyPanels drops panels whose id is a reserved block id. Pre-
// section documents could legally pin a metric named e.g. "hooks", but the
// current PUT validator rejects such panel ids, so keeping them would brick
// persistence (every layout PUT 400s and retries forever). Section items with
// the same id need no cleanup: they resolve to the well-known block, not to
// the dropped panel, on both the server and in renderPinned.
function sanitizeLegacyPanels(d) {
  var bad = d.panels.filter(function (p) { return RESERVED_BLOCK_IDS.indexOf(p.id) >= 0; });
  if (!bad.length) return;
  d.panels = d.panels.filter(function (p) { return RESERVED_BLOCK_IDS.indexOf(p.id) < 0; });
  showToast('dropped pinned chart ' + bad.map(function (p) { return '"' + p.metric + '"'; }).join(', ') +
    ': reserved section block ids cannot be pinned', 'error');
}

function loadDash() {
  fetchJSON('/metrics/config', { headers: { 'Accept': 'application/json' } })
    .then(function (d) {
      if (!d || d.version !== 1) return;
      if (!d.panels) d.panels = [];
      if (!d.order) d.order = { all_metrics: [], hooks: [], endpoints: [] };
      if (!d.http_card_view) d.http_card_view = '';
      if (!d.timeframe) d.timeframe = '';
      // First connect against an empty server doc: promote the localStorage
      // layout (still in dashCfg) so existing pins, section orderings, and the
      // HTTP card view are migrated, even when only orderings exist.
      var promoted = !(d.panels.length || dashHasOrder(d)) && (dashCfg.panels.length || dashHasOrder(dashCfg));
      if (promoted) { d.panels = dashCfg.panels; d.order = dashCfg.order; d.http_card_view = dashCfg.http_card_view; d.timeframe = dashCfg.timeframe; }
      sanitizeLegacyPanels(d);
      normalizeSections(d);
      dashCfg = d;
      dashRemote = true;
      if (promoted || d.panels.length) persistDash();
      applyDash();
    })
    .catch(function () {}); // server copy unavailable: keep the localStorage boot state
}

// The add-chart modal's current chart-type/size selection, applied to the
// panel being added (empty = server defaults).
function addChartSelection() {
  var t = '', s = '';
  var ts = document.getElementById('add-chart-type');
  var ss = document.getElementById('add-chart-size');
  if (ts) t = ts.value;
  if (ss) s = ss.value;
  return { chartType: t, size: s };
}

document.addEventListener('alpine:init', function () {
  Alpine.store('ui', {
    // `add` is the add-chart modal's open flag; `pin` adds a panel. Separate
    // names on purpose: one object key cannot be both, and the old single-key
    // version left the modal open on load and made addChart() throw.
    add: false,
    name: '',
    charts: dashCfg.panels.map(function (p) { return p.metric; }),
    has: function (n) { return this.charts.indexOf(n) >= 0; },
    pin: function (n) {
      if (!n) return;
      // A panel's id is its metric name, and a metric sharing a reserved
      // block id would make every layout PUT fail validation — reject the
      // pin instead of bricking persistence.
      if (RESERVED_BLOCK_IDS.indexOf(n) >= 0 && !this.has(n)) {
        showToast('metric "' + n + '" is a reserved section block id and cannot be pinned', 'error');
        return;
      }
      if (!this.has(n)) {
        this.charts.push(n);
        dashCfg.panels = chartsToPanels(this.charts);
        var sel = addChartSelection();
        for (var i = 0; i < dashCfg.panels.length; i++) {
          var p = dashCfg.panels[i];
          if (p.metric === n) {
            // Explicit reset: re-adding a metric takes the current modal
            // selection, dropping any config from a previous pin.
            p.chart_type = sel.chartType || undefined;
            p.size = sel.size || undefined;
            placePanel(p.id);
          }
        }
        persistDash();
        renderPinned();
        return;
      }
      // Already pinned: another placement of the same panel. Copies render
      // from one metric pipeline and share the panel's config — only the
      // document gains an item.
      var id = null;
      dashCfg.panels.forEach(function (p) { if (p.metric === n) id = p.id; });
      if (id && placePanel(id)) {
        persistDash();
        renderPinned();
        return;
      }
      showToast('chart "' + n + '" is already placed in every band', 'error');
    },
    remove: function (n) {
      var id = null;
      dashCfg.panels.forEach(function (p) { if (p.metric === n) id = p.id; });
      this.charts = this.charts.filter(function (x) { return x !== n; });
      dashCfg.panels = chartsToPanels(this.charts);
      if (id) unplacePanel(id);
      persistDash();
      renderPinned();
    },

    // Alert modal state
    alertModal: false,
    alertStep: 1,
    alertMetric: '',
    alertSearch: '',
    alertMetrics: [],
    alertTemplates: [],
    alertForm: { name: '', condition: 'gt', threshold: 0, cooldown: '5m', email_to: '', email_subject: '', email_body: '', email_html: 'false' },

    openAlert: function () {
      this.alertStep = 1;
      this.alertMetric = '';
      this.alertSearch = '';
      this.alertForm = { name: '', condition: 'gt', threshold: 0, cooldown: '5m', email_to: '', email_subject: '', email_body: '', email_html: 'false' };
      var self = this;
      fetchJSON('/metrics/data', { headers: { 'Accept': 'application/json' } })
        .then(function (d) { self.alertMetrics = (d || []).map(function (s) { return { name: s.name, type: s.type }; }); })
        .catch(function () {});
      fetchJSON('/metrics/alerts/templates', { headers: { 'Accept': 'application/json' } })
        .then(function (d) { self.alertTemplates = d || []; })
        .catch(function () { self.alertTemplates = []; });
      this.alertModal = true;
    },

    applyTemplate: function (id) {
      if (!id) return;
      var t = this.alertTemplates.find(function (x) { return x.id === id; });
      if (t) {
        this.alertForm.email_subject = t.subject;
        this.alertForm.email_body = t.body;
      }
    },

    selectMetric: function (name) {
      this.alertMetric = name;
      this.alertForm.name = name + ' alert';
      this.alertStep = 2;
    },

    closeAlert: function () {
      this.alertModal = false;
      this.alertStep = 1;
    },

    submitAlert: function () {
      var body = {
        name: this.alertForm.name,
        metric: this.alertMetric,
        condition: this.alertForm.condition,
        threshold: this.alertForm.threshold,
        cooldown: this.alertForm.cooldown,
        enabled: true
      };
      if (HAS_NOTIFIER) {
        body.email_to = this.alertForm.email_to ? this.alertForm.email_to.split(',').map(function (s) { return s.trim(); }).filter(Boolean) : [];
        body.email_subject = this.alertForm.email_subject;
        body.email_body = this.alertForm.email_body;
        body.email_html = this.alertForm.email_html === 'true';
      }
      var self = this;
      postJSON('/metrics/alerts/rules', body)
        .then(function () {
          self.closeAlert();
          showToast('Alert created for ' + self.alertMetric, 'success');
        })
        .catch(function (e) { showToast(e.message || 'Failed to create alert'); });
    }
  });
});

// open modal from the header button — always start from a clean state
var mo = new MutationObserver(function () {
  if (document.body.dataset.add === '1') {
    var ui = Alpine.store('ui');
    ui.name = '';        // clear any previous search/selection
    ui.add = true;       // open
    delete document.body.dataset.add;
  }
});
mo.observe(document.body, { attributes: true, attributeFilter: ['data-add'] });

// open alert modal from the header button
var moAlert = new MutationObserver(function () {
  if (document.body.dataset.alertAdd === '1') {
    Alpine.store('ui').openAlert();
    delete document.body.dataset.alertAdd;
  }
});
moAlert.observe(document.body, { attributes: true, attributeFilter: ['data-alert-add'] });

// Add the searched/selected metric as a pinned chart (no navigation). A
// partial or unknown name can never pin: an exact match with the rendered
// list is required, otherwise Enter falls back to the first filtered match.
function addChart() {
  var st = Alpine.store('ui');
  var q = (st.name || '').toLowerCase();
  var list = metricNames().filter(function (n) { return n.toLowerCase().indexOf(q) >= 0; });
  var n = list.indexOf(st.name) >= 0 ? st.name : list[0];
  st.add = false;
  if (n) st.pin(n);
  st.name = '';
}

function saveOrder(o) { dashCfg.order.all_metrics = o; persistDash(); }

// --- shared reorder path -------------------------------------------------------
// Every reorderable surface (All Metrics, Hooks, My Charts, Endpoint Latency,
// Runtime Health) moves through moveCard, so drag and the move buttons share
// one code path.

// moveCard moves items[from] to items[to]'s position and persists. Works on
// both live DOM node lists (moves the node after/before the target, the same
// placement the drag handlers use) and plain arrays (splices), then calls
// persist. Returns true when a move happened.
function moveCard(items, from, to, persist) {
  if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length) return false;
  var item = items[from], target = items[to];
  if (typeof target.after === 'function') {
    if (from < to) target.after(item); else target.before(item);
  } else {
    items.splice(to, 0, items.splice(from, 1)[0]);
  }
  persist();
  return true;
}

// neighborIndex returns the index of the nearest visible card in direction
// dir (-1 | 1) from cards[i], or -1 at the visible edge. Search-filtered
// cards are skipped so a move is always visible in the grid.
function neighborIndex(cards, i, dir) {
  for (var j = i + dir; j >= 0 && j < cards.length; j += dir) {
    if (cards[j].style.display !== 'none') return j;
  }
  return -1;
}

// moveGridCard is the move-button entry point for the server-rendered metric
// grids. #charts (All Metrics) and #hooks share the cards template, so the
// handler dispatches the persist call on which grid the card sits in.
function moveGridCard(btn, dir) {
  var card = btn.closest('[data-metric]');
  if (!card) return;
  var hooksGrid = card.closest('#hooks');
  var grid = hooksGrid || card.closest('#charts');
  if (!grid) return;
  var cards = Array.from(grid.querySelectorAll('[data-metric]'));
  var from = cards.indexOf(card);
  moveCard(cards, from, neighborIndex(cards, from, dir), function () {
    var names = Array.from(grid.querySelectorAll('[data-metric]')).map(function (el) { return el.dataset.metric; });
    if (hooksGrid) saveHookOrder(names); else saveOrder(names);
  });
}

// pinCard pins a card's metric to My Charts (the + button on each card).
function pinCard(btn) {
  var card = btn.closest('[data-metric]');
  if (card) Alpine.store('ui').pin(card.dataset.metric);
}

function metricNames() {
  return Array.from(document.querySelectorAll('#charts [data-metric]')).map(function (el) { return el.dataset.metric; });
}

function cssEsc(s) { return (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&'); }

// --- sections: pinned panels, collapse, edit mode ------------------------------
// Sections place fixed blocks and pinned panels into horizontal bands. The
// server renders the bands and their blocks; pinned panels are client-rendered
// slots reconciled into the bands from dashCfg.sections. Edit mode (localStorage,
// so it survives reloads) gates every drag/move/pin/rename/size affordance.
// Collapse state per section id lives in localStorage only, never in the doc.

// RESERVED_BLOCK_IDS mirrors the server's well-known block ids (see
// validateDashboardSections); pin() refuses metrics with these names.
var RESERVED_BLOCK_IDS = ['runtime-health', 'histogram', 'hooks', 'all-metrics', 'endpoint-latency', 'internals'];

// PANEL_SIZES is the stepped width×height chart-grid vocabulary (width of 12
// columns, height in 4rem row units) — the same set the server's size
// validation and the add-chart modal's options accept. A size renders as the
// card-<cols>x<rows> span class; anything else (legacy sm/md/lg, unknown)
// falls back to the default 4×4 cell.
var PANEL_SIZES = ['4x2', '6x2', '8x2', '10x2', '12x2', '4x4', '6x4', '8x4', '10x4', '12x4', '4x6', '6x6', '8x6', '10x6', '12x6'];

// placementCount counts a panel id's placements across all bands.
function placementCount(id) {
  var n = 0;
  (dashCfg.sections || []).forEach(function (s) { (s.items || []).forEach(function (x) { if (x === id) n++; }); });
  return n;
}

function panelByID(id) {
  for (var i = 0; i < dashCfg.panels.length; i++) {
    if (dashCfg.panels[i].id === id) return dashCfg.panels[i];
  }
  return null;
}

// sectionForPanels returns the band new pins land in: the my-charts band when
// the layout has one (the default layout does), else the last band.
function sectionForPanels() {
  var secs = dashCfg.sections;
  for (var i = 0; i < secs.length; i++) if (secs[i].id === 'my-charts') return secs[i];
  if (secs.length) return secs[secs.length - 1];
  var s = { id: 'my-charts', items: [] };
  secs.push(s);
  return s;
}

// placePanel adds one more placement of a panel: the my-charts band when it
// does not already show it, else the first band that does not — a panel may
// repeat across bands, never twice in one (the PUT validator rejects that,
// and a rejected PUT retries forever). Returns false when every band already
// shows the panel and no placement is possible.
function placePanel(id) {
  var s = sectionForPanels();
  if (!s) return false;
  if (!s.items) s.items = [];
  if (s.items.indexOf(id) >= 0) {
    s = null;
    for (var i = 0; i < dashCfg.sections.length && !s; i++) {
      if (!dashCfg.sections[i].items || dashCfg.sections[i].items.indexOf(id) < 0) s = dashCfg.sections[i];
    }
    if (!s) return false;
    if (!s.items) s.items = [];
  }
  s.items.push(id);
  return true;
}

function unplacePanel(id) {
  dashCfg.sections.forEach(function (s) {
    if (s.items) s.items = s.items.filter(function (x) { return x !== id; });
  });
}

// attrEsc makes a metric name safe inside a double-quoted HTML attribute.
function attrEsc(s) {
  return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
}

// makeSlot builds a pinned-panel slot: actions row (edit mode only) and the
// body renderPinned fills by cloning the server-rendered card.
function makeSlot(p) {
  var slot = document.createElement('div');
  slot.className = 'pin-slot card card-grab';
  slot.setAttribute('data-pin-id', p.id);
  slot.setAttribute('data-pin', p.metric);
  slot.setAttribute('draggable', 'true');
  var label = attrEsc(p.metric);
  slot.innerHTML =
    '<div class="pin-actions edit-only">' +
    '<button type="button" class="pin-btn" title="move earlier" aria-label="Move ' + label + ' earlier" onclick="moveSectionItem(this,-1)">&larr;</button>' +
    '<button type="button" class="pin-btn" title="move later" aria-label="Move ' + label + ' later" onclick="moveSectionItem(this,1)">&rarr;</button>' +
    '<select class="pin-size" title="panel size" aria-label="Panel size" onchange="setPanelSize(this)">' +
    '<option value="">default</option>' +
    PANEL_SIZES.map(function (s) { return '<option value="' + s + '">' + s.replace('x', '×') + '</option>'; }).join('') +
    '</select>' +
    '<button type="button" class="pin-remove" title="remove" aria-label="Remove ' + label + '" onclick="unpinSlot(this)">&times;</button>' +
    '</div>' +
    '<div class="pin-body"></div>';
  var sel = slot.querySelector('.pin-size');
  if (sel) sel.value = p.size || '';
  return slot;
}

// itemKey is a section body child's config item id (block id or panel id).
function itemKey(el) {
  return el.hasAttribute('data-block') ? el.getAttribute('data-block') : el.getAttribute('data-pin-id');
}

// sectionItemsIn lists a section body child's config item ids (block ids or
// panel ids). The body is the section's own — the first .sec-body in document
// order, since nested bands live inside it — so items of nested child bands
// stay theirs, never the ancestors'.
function sectionItemsIn(sec) {
  var body = sec.querySelector('.sec-body');
  if (!body) return [];
  return Array.from(body.children)
    .filter(function (el) { return el.matches('[data-block], .pin-slot'); })
    .map(itemKey);
}

function sectionItems() {
  return Array.from(document.querySelectorAll('.sec-body > [data-block], .sec-body > .pin-slot'));
}

// renderPinned reconciles the section bodies with dashCfg.sections (creating
// and placing panel slots, restoring block/panel order), then fills each
// slot by cloning its server-rendered card from #charts. Identical HTML is
// skipped so a poll that changed nothing doesn't destroy hover tooltips or
// an in-progress text selection.
function renderPinned() {
  var bodies = {};
  document.querySelectorAll('[data-section]').forEach(function (sec) {
    bodies[sec.getAttribute('data-section')] = sec.querySelector('.sec-body');
  });
  var blocks = {}, slots = {}, seen = {};
  document.querySelectorAll('.sec-body > [data-block]').forEach(function (b) { blocks[b.getAttribute('data-block')] = b; });
  // Slot identity is per placement: a panel id may occur once per band (the
  // same chart in several sections), so each id owns the list of its slot
  // nodes — the k-th occurrence reuses the k-th node, fresh occurrences
  // create fresh slots, and surplus nodes are removed below.
  document.querySelectorAll('.sec-body > .pin-slot').forEach(function (s) {
    var id = s.getAttribute('data-pin-id');
    (slots[id] = slots[id] || []).push(s);
  });
  (dashCfg.sections || []).forEach(function (s) {
    var body = bodies[s.id];
    if (!body) return;
    var nodes = [];
    (s.items || []).forEach(function (id) {
      var p = panelByID(id);
      if (p) {
        var list = slots[id] || (slots[id] = []);
        var k = seen[id] || 0;
        seen[id] = k + 1;
        if (!list[k]) list[k] = makeSlot(p);
        nodes.push(list[k]);
        return;
      }
      if (blocks[id]) nodes.push(blocks[id]);
    });
    // Nested child bands share the body grid and render after the band's own
    // items. Compare and rebuild against the item nodes only, inserting them
    // ahead of the first child band so a rebuild never reorders the bands.
    var nested = Array.from(body.children).filter(function (el) { return el.hasAttribute('data-section'); });
    var cur = Array.from(body.children).filter(function (el) { return el.matches('[data-block], .pin-slot'); });
    var same = cur.length === nodes.length && cur.every(function (el, i) { return el === nodes[i]; });
    if (!same) nodes.forEach(function (n) { body.insertBefore(n, nested[0] || null); });
  });
  Object.keys(slots).forEach(function (id) {
    var want = seen[id] || 0;
    slots[id].forEach(function (slot, i) {
      if (i >= want && slot.parentNode) slot.remove();
    });
  });
  document.querySelectorAll('.pin-slot').forEach(function (slot) {
    var name = slot.getAttribute('data-pin');
    slot.className = 'pin-slot card card-grab ' + pinSizeClass(name);
    var body = slot.querySelector('.pin-body');
    var card = document.querySelector('#charts [data-metric="' + cssEsc(name) + '"]');
    var html = card ? card.innerHTML : '<div class="pin-missing">metric unavailable</div>';
    if (body.innerHTML !== html) body.innerHTML = html;
  });
  refreshSections();
  drawLineCharts();
}

// --- edit mode -----------------------------------------------------------------

function editMode() { return document.body.classList.contains('edit-mode'); }

// setEditMode flips the layout-editing gate. The flag persists in
// localStorage, so a reload while editing stays in edit mode (view mode is
// the default when the flag is absent). The body.edit-mode class drives the
// CSS visibility of every .edit-only affordance.
function setEditMode(on) {
  document.body.classList.toggle('edit-mode', !!on);
  try { localStorage.setItem('golens.editmode', on ? '1' : ''); } catch (e) {}
  var b = document.getElementById('edit-toggle');
  if (b) b.setAttribute('aria-pressed', on ? 'true' : 'false');
  refreshSections();
}

function toggleEditMode() { setEditMode(!editMode()); }

// --- collapse ------------------------------------------------------------------

function collapsedMap() {
  try { return JSON.parse(localStorage.getItem('golens.collapsed') || '{}') || {}; } catch (e) { return {}; }
}
var secCollapsed = collapsedMap();

function toggleCollapse(sec) {
  if (!sec) return;
  var id = sec.getAttribute('data-section');
  if (secCollapsed[id]) delete secCollapsed[id]; else secCollapsed[id] = true;
  try { localStorage.setItem('golens.collapsed', JSON.stringify(secCollapsed)); } catch (e) {}
  refreshSections();
}

// refreshSections applies the three client-owned visibility/affordance rules:
// an empty band stays hidden in view mode (it is an edit-mode drop target),
// a collapsed band hides its body behind the header, and bands carry the
// draggable attribute in edit mode only (the re-parent drag handle; in view
// mode the attribute stays off so normal text selection works).
function refreshSections() {
  document.querySelectorAll('[data-section]').forEach(function (sec) {
    var body = sec.querySelector('.sec-body');
    if (!body) return;
    var empty = !body.querySelector('[data-block], .pin-slot');
    sec.hidden = !editMode() && empty;
    body.hidden = !!secCollapsed[sec.getAttribute('data-section')];
    var head = sec.querySelector('.sec-head');
    if (head) head.classList.toggle('sec-collapsed', body.hidden);
    sec.setAttribute('draggable', editMode() ? 'true' : 'false');
  });
}

// --- section persistence ---------------------------------------------------------

// normSections renders a section list in one comparable shape: server
// documents omit empty name/items/parent, DOM-built bands always carry them.
function normSections(list) {
  return JSON.stringify((list || []).map(function (s) {
    return { id: s.id, name: s.name || '', items: s.items || [], parent: s.parent || '', timeframe: s.timeframe || '' };
  }));
}

// persistSectionsFromDOM serializes the section bands back into
// dashCfg.sections (the DOM is authoritative for the items and the nesting it
// shows) and persists. No-op when nothing moved. The walk is depth-first in
// document order: parent = nearest band ancestor, child bands in DOM order.
// Items the server could not render — an unavailable block has no DOM node,
// and a wholly unavailable band has no DOM section — are carried forward from
// dashCfg.sections, so an edit-mode persist can never delete them from the
// saved layout; a carried block re-renders (at the end of its band) once its
// content becomes available, and a wholly dropped band is re-appended with
// its saved subtree intact.
function persistSectionsFromDOM() {
  var prevById = {};
  (dashCfg.sections || []).forEach(function (s) { prevById[s.id] = s; });
  var domSecs = Array.from(document.querySelectorAll('[data-section]'));
  var domIds = {}, inDom = {};
  domSecs.forEach(function (sec) {
    domIds[sec.getAttribute('data-section')] = true;
    sectionItemsIn(sec).forEach(function (id) { inDom[id] = true; });
  });
  var secs = [];
  function emit(sec, parentID) {
    var inp = sec.querySelector('.sec-rename-input');
    var name = inp ? inp.value.trim() : (sec.getAttribute('data-name') || '');
    var items = sectionItemsIn(sec);
    var prev = prevById[sec.getAttribute('data-section')];
    if (prev && prev.items) {
      prev.items.forEach(function (id) { if (!inDom[id]) items.push(id); });
    }
    var tfs = sec.querySelector('.sec-tf');
    var out = { id: sec.getAttribute('data-section'), name: name, items: items, timeframe: tfs ? tfs.value : '' };
    if (parentID) out.parent = parentID;
    secs.push(out);
    var body = sec.querySelector('.sec-body');
    if (!body) return;
    Array.from(body.children).forEach(function (child) {
      if (child.hasAttribute('data-section')) emit(child, out.id);
    });
  }
  Array.from(document.querySelectorAll('main > [data-section]')).forEach(function (sec) {
    emit(sec, null);
  });
  // Bands the server dropped entirely keep their saved subtree untouched.
  (dashCfg.sections || []).forEach(function (s) {
    if (!domIds[s.id]) secs.push({ id: s.id, name: s.name || '', items: (s.items || []).slice(), parent: s.parent || '', timeframe: s.timeframe || '' });
  });
  if (normSections(secs) === normSections(dashCfg.sections)) return;
  dashCfg.sections = secs;
  persistDash();
  refreshSections();
}

// moveSectionItem is the move-button entry point for blocks and pinned
// panels: one flat list across all bands, so ←/→ cross section boundaries.
function moveSectionItem(btn, dir) {
  var el = btn.closest('[data-block], .pin-slot');
  if (!el) return;
  var items = sectionItems();
  var from = items.indexOf(el);
  var to = neighborIndex(items, from, dir);
  // A panel copy cannot move into a band that already shows the same panel
  // (the PUT validator rejects the duplicate and a rejected PUT retries
  // forever): the ←/→ buttons refuse the move the same way the drag does.
  if (to >= 0 && el.classList.contains('pin-slot') &&
      items[to].closest('[data-section]') !== el.closest('[data-section]')) {
    var dup = items[to].closest('.sec-body').querySelector('.pin-slot[data-pin-id="' + cssEsc(el.getAttribute('data-pin-id')) + '"]');
    if (dup) return;
  }
  moveCard(items, from, to, persistSectionsFromDOM);
}

function renameSection(input) {
  var sec = input.closest('[data-section]');
  if (!sec) return;
  var name = input.value.trim();
  sec.setAttribute('data-name', name);
  var label = sec.querySelector('.sec-name');
  if (label) label.textContent = name;
  persistSectionsFromDOM();
}

function setPanelSize(sel) {
  var slot = sel.closest('.pin-slot');
  if (!slot) return;
  var metric = slot.getAttribute('data-pin');
  for (var i = 0; i < dashCfg.panels.length; i++) {
    if (dashCfg.panels[i].metric === metric) dashCfg.panels[i].size = sel.value || undefined;
  }
  // Copies share the panel's config: restyle every placement of this metric
  // now instead of waiting for the next render pass.
  document.querySelectorAll('.pin-slot').forEach(function (s) {
    if (s.getAttribute('data-pin') === metric) s.className = 'pin-slot card card-grab ' + pinSizeClass(metric);
  });
  persistDash();
}

// unpinSlot removes the one placement the × sits on; only the last
// placement's removal deletes the panel itself (the chart dies with its
// last copy).
function unpinSlot(btn) {
  var slot = btn.closest('.pin-slot');
  if (!slot) return;
  var id = slot.getAttribute('data-pin-id');
  var sec = slot.closest('[data-section]');
  var s = null;
  (dashCfg.sections || []).forEach(function (x) {
    if (sec && x.id === sec.getAttribute('data-section')) s = x;
  });
  if (s && s.items) {
    var i = s.items.indexOf(id);
    if (i >= 0) s.items.splice(i, 1);
  }
  if (placementCount(id)) {
    persistDash();
    renderPinned();
    return;
  }
  var p = panelByID(id);
  if (p) Alpine.store('ui').remove(p.metric);
}

// --- section drag & drop (edit mode only) -----------------------------------------

// Cross-band and within-band moves of blocks and pinned panels, plus band
// re-parenting: one live-move delegated handler on main. Drags that begin on
// a nested draggable (metric cards, runtime cards) belong to their own grids
// and are ignored here.
var dragSectionItem = null;
var dragSection = null;
function initSectionsDnD() {
  var main = document.querySelector('main');
  if (!main) return;
  main.addEventListener('dragstart', function (e) {
    if (!editMode()) return;
    if (e.target.closest('[data-metric]')) return;
    var el = e.target.closest('.sec-body > [data-block], .sec-body > .pin-slot');
    if (el) {
      if (e.target.closest('[draggable="true"]') !== el) return;
      dragSectionItem = el;
      el.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      return;
    }
    // Band re-parenting: grab the header. Inputs/buttons keep their own
    // interaction (rename, move buttons).
    if (e.target.closest('input, select, button')) return;
    var sec = e.target.closest('[data-section]');
    if (!sec || sec.getAttribute('draggable') !== 'true') return;
    dragSection = sec;
    sec.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
  });
  main.addEventListener('dragover', function (e) {
    if (!dragSectionItem && !dragSection) return;
    if (dragSection && dragSection.contains(e.target)) return; // no drop on the dragged band's own chrome
    var body = e.target.closest('.sec-body');
    if (!body) {
      // Outdent zone: main's gaps and top-level band chrome sit outside
      // every .sec-body, so a band dragged there returns to top level —
      // the inverse of nesting, and the only way out of a nest. Item drags
      // have no top-level form and stay put.
      if (!dragSection) return;
      e.preventDefault();
      main.appendChild(dragSection);
      return;
    }
    if (dragSection) {
      // Structural guard: a band cannot drop into itself or one of its own
      // descendants. No preventDefault = no drop indication.
      var host = body.closest('[data-section]');
      if (host && dragSection.contains(host)) return;
      e.preventDefault();
      // Bands render after a band's own items: a drop lands at the end.
      body.appendChild(dragSection);
      return;
    }
    // Structural guard, mirroring the band one: a panel copy cannot land in
    // a band that already shows the same panel — two identical cards in one
    // band carry no information, and persisting one would 400 every layout
    // PUT and retry forever. No preventDefault = no drop indication, no move.
    if (dragSectionItem.classList.contains('pin-slot')) {
      var dup = body.querySelector('.pin-slot[data-pin-id="' + cssEsc(dragSectionItem.getAttribute('data-pin-id')) + '"]');
      if (dup && dup !== dragSectionItem) return;
    }
    e.preventDefault();
    var next = null;
    var kids = Array.from(body.children);
    for (var i = 0; i < kids.length; i++) {
      var k = kids[i];
      if (k === dragSectionItem) continue;
      var r = k.getBoundingClientRect();
      if (e.clientY < r.top + r.height / 2) { next = k; break; }
    }
    if (next) body.insertBefore(dragSectionItem, next); else body.appendChild(dragSectionItem);
  });
  main.addEventListener('drop', function (e) {
    if (dragSectionItem || dragSection) e.preventDefault();
  });
  main.addEventListener('dragend', function () {
    if (dragSection) {
      dragSection.classList.remove('dragging');
      dragSection = null;
    }
    if (dragSectionItem) {
      dragSectionItem.classList.remove('dragging');
      dragSectionItem = null;
    }
    persistSectionsFromDOM();
  });
}

function initSectionsUI() {
  var main = document.querySelector('main');
  if (!main) return;
  // Whole header is the collapse click target; interactive children opt out.
  main.addEventListener('click', function (e) {
    if (e.target.closest('.sec-tf')) return;
    if (e.target.closest('.sec-rename-input, .block-actions, .pin-actions')) return;
    var head = e.target.closest('.sec-head');
    if (head) toggleCollapse(head.closest('[data-section]'));
  });
  main.addEventListener('keydown', function (e) {
    if ((e.key !== 'Enter' && e.key !== ' ') || e.target !== e.target.closest('.sec-head')) return;
    e.preventDefault();
    toggleCollapse(e.target.closest('[data-section]'));
  });
  initSectionsDnD();
}

// pinSizeClass returns the grid span class configured for a pinned panel:
// the panel's width×height size ("8x4" → card-8x4). Legacy or unknown sizes
// (the server heals legacy names on read) render as the default cell.
function pinSizeClass(name) {
  for (var i = 0; i < dashCfg.panels.length; i++) {
    var p = dashCfg.panels[i];
    if (p.metric === name && PANEL_SIZES.indexOf(p.size) >= 0) return 'card-' + p.size;
  }
  return '';
}

// --- line-chart panels (chart_type "line") -------------------------------------

// Draw every canvas[data-history] on the page: one history fetch per
// (metric, timeframe) pair, rendered into each matching canvas (a pinned clone
// duplicates the #charts canvas, so both must draw). The window is each
// canvas's effective timeframe (band override, else global). htmx swaps
// recreate the canvases, so this re-runs after every swap via renderPinned.
var lineInflight = {};
function drawLineCharts() {
  var groups = {};
  document.querySelectorAll('canvas[data-history]').forEach(function (c) {
    var n = c.getAttribute('data-history');
    var qs = historyQS(effectiveDuration(c));
    var k = n + '|' + qs;
    (groups[k] = groups[k] || { name: n, qs: qs, canvases: [] }).canvases.push(c);
  });
  Object.keys(groups).forEach(function (k) {
    if (lineInflight[k]) return;
    lineInflight[k] = true;
    var g = groups[k];
    fetchJSON('/metrics/history?name=' + encodeURIComponent(g.name) + '&' + g.qs)
      .then(function (s) {
        var points = (s && s.points) || [];
        g.canvases.forEach(function (c) { drawChart(c, [{ points: points }], {}); });
      })
      .catch(function () {})
      .then(function () { delete lineInflight[k]; });
  });
}
drawLineCharts(); // initial pass for the server-rendered grid (scripts are deferred)

function applyFilter() {
  var q = (document.getElementById('search').value || '').toLowerCase();
  document.querySelectorAll('#charts [data-metric]').forEach(function (el) {
    el.style.display = (!q || el.dataset.metric.toLowerCase().indexOf(q) >= 0) ? '' : 'none';
  });
}

// sortCharts reorders the All Metrics cards to match the persisted order
// (unknown/new metrics appended at the end). Called after every HTMX swap so
// the server's order doesn't clobber the user's custom arrangement.
function sortCharts() {
  var grid = document.getElementById('charts');
  if (!grid) return;
  var order = dashCfg.order.all_metrics || [];
  var cards = Array.from(grid.querySelectorAll('[data-metric]'));
  if (cards.length <= 1) return;
  var idx = function (name) { var i = order.indexOf(name); return i < 0 ? Infinity : i; };
  cards.sort(function (a, b) { return idx(a.dataset.metric) - idx(b.dataset.metric); });
  // Cards live inside the section's grid wrapper div, not directly under
  // #charts — re-append to their real parent to keep the grid intact.
  var parent = cards[0].parentNode;
  cards.forEach(function (c) { parent.appendChild(c); });
}

// Drag-and-drop for the All Metrics grid. Uses event delegation on the
// persistent #charts container so listeners survive HTMX innerHTML swaps.
var dragCard = null;
function initMetricsDnD() {
  var grid = document.getElementById('charts');
  if (!grid) return;
  grid.addEventListener('dragstart', function (e) {
    if (!editMode()) return;
    var card = e.target.closest('[data-metric]');
    if (!card) return;
    dragCard = card;
    card.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
  });
  grid.addEventListener('dragend', function (e) {
    var card = e.target.closest('[data-metric]');
    if (card) { card.classList.remove('dragging'); }
    dragCard = null;
  });
  grid.addEventListener('dragover', function (e) {
    if (!dragCard) return;
    var target = e.target.closest('[data-metric]');
    if (!target || target === dragCard) return;
    e.preventDefault();
    var r = target.getBoundingClientRect();
    var after = (e.clientY - r.top) > r.height / 2;
    if (after) target.after(dragCard); else target.before(dragCard);
  });
  grid.addEventListener('drop', function (e) {
    if (!dragCard) return;
    e.preventDefault();
    saveOrder(metricNames());   // persist the new arrangement
  });
}

// After each HTMX swap: restore custom order, reapply search filter, refresh pins.
document.body.addEventListener('htmx:afterSwap', function () { sortCharts(); applyFilter(); renderPinned(); applyHookOrder(); });

function saveEpOrder(o) { dashCfg.order.endpoints = o; persistDash(); }

// Per-endpoint latency chart: polls /metrics/endpoints (JSON), renders
// p50/p95/p99 bars per endpoint, drag-to-reorder with persisted order.
function endpointLatency() {
  return {
    raw: [],
    bump: 0,
    drag: null,
    init() { this.load(); setInterval(() => { if (!document.hidden) this.load(); }, POLL_MS); },
    get endpoints() {
      this.bump; // reactive trigger for manual reorders
      var ord = dashCfg.order.endpoints || [];
      var key = function (e) { return e.method + ' ' + e.path; };
      var idx = function (e) { var i = ord.indexOf(key(e)); return i < 0 ? Infinity : i; };
      return this.raw.slice().sort(function (a, b) { return idx(a) - idx(b); });
    },
    load() {
      fetchJSON('/metrics/endpoints').then(d => { this.raw = d || []; }).catch(() => {});
    },
    start(i) { if (!editMode()) return; this.drag = i; },
    drop(i) {
      if (this.drag === null) return;
      var from = this.drag;
      this.drag = null;
      this.move(from, i - from);
    },
    move(i, d) {
      if (!editMode()) return;
      var keys = this.endpoints.map(function (e) { return e.method + ' ' + e.path; });
      if (moveCard(keys, i, i + d, function () { saveEpOrder(keys); })) this.bump++;
    },
    // Calculate height relative to each endpoint's p99 (the highest bar)
    h(endpoint, value) {
      var max = endpoint.p99 || 0.001;
      return Math.max(2, Math.round((value / max) * 100));
    },
    fmt(v) { if (v >= 1) return v.toFixed(2); if (v >= 0.01) return v.toFixed(3); return v.toFixed(4); }
  };
}

// Hooks section: reorder support for hook metrics. The grid's drag-and-drop
// is the delegated listener in setupDnD; move buttons share moveGridCard
// with the All Metrics grid (both render the same cards template).
function hooksSection() {
  return {
    init() { this.setupDnD(); },
    setupDnD() {
      var grid = document.getElementById('hooks');
      if (!grid) return;
      // Apply persisted order on load
      applyHookOrder();
      // Setup drag-and-drop
      var dragCard = null;
      grid.addEventListener('dragstart', function (e) {
        if (!editMode()) return;
        var card = e.target.closest('[data-metric]');
        if (!card) return;
        dragCard = card;
        card.classList.add('dragging');
        e.dataTransfer.effectAllowed = 'move';
      });
      grid.addEventListener('dragend', function (e) {
        var card = e.target.closest('[data-metric]');
        if (card) { card.classList.remove('dragging'); }
        dragCard = null;
      });
      grid.addEventListener('dragover', function (e) {
        if (!dragCard) return;
        var target = e.target.closest('[data-metric]');
        if (!target || target === dragCard) return;
        e.preventDefault();
        var r = target.getBoundingClientRect();
        var after = (e.clientY - r.top) > r.height / 2;
        if (after) target.after(dragCard); else target.before(dragCard);
      });
      grid.addEventListener('drop', function (e) {
        if (!dragCard) return;
        e.preventDefault();
        var names = Array.from(grid.querySelectorAll('[data-metric]')).map(function (el) { return el.dataset.metric; });
        saveHookOrder(names);
      });
    }
  };
}

// Hooks section ordering helpers, backed by the dashboard config document.
function saveHookOrder(names) { dashCfg.order.hooks = names; persistDash(); }

// Re-apply the persisted hook order to the (server-rendered) hooks grid.
// Called after HTMX swaps so the server's order doesn't clobber the user's.
function applyHookOrder() {
  var grid = document.getElementById('hooks');
  if (!grid) return;
  var saved = dashCfg.order.hooks || [];
  if (saved.length === 0) return;
  var cards = Array.from(grid.querySelectorAll('[data-metric]'));
  if (!cards.length) return;
  var idx = function (name) { var i = saved.indexOf(name); return i < 0 ? Infinity : i; };
  cards.sort(function (a, b) { return idx(a.dataset.metric) - idx(b.dataset.metric); });
  // Cards live inside the section's grid wrapper div, not directly under
  // #hooks — re-append to their real parent to keep the grid intact.
  var parent = cards[0].parentNode;
  cards.forEach(function (c) { parent.appendChild(c); });
}

// Cardinality panel: polls /metrics/cardinality and shows per-metric series
// stats. Visibility/collapse is owned by the generic section header, so the
// component only feeds data.
function cardinalityPanel(maxSeries) {
  return {
    metrics: [],
    maxSeries: maxSeries,
    init() { this.load(); setInterval(() => { if (!document.hidden) this.load(); }, POLL_MS); },
    load() {
      fetchJSON('/metrics/cardinality').then(d => { this.metrics = d || []; }).catch(() => {});
    },
    get totalSeries() { return this.metrics.reduce((s, m) => s + m.series, 0); },
    get totalDropped() { return this.metrics.reduce((s, m) => s + m.dropped, 0); },
    get metricCount() { return this.metrics.length; },
    usagePct(m) {
      if (!m.max_series) return 0;
      return Math.round((m.series / m.max_series) * 100);
    }
  };
}

// Boot: restore edit mode, wire the section UI, apply persisted All Metrics
// order + wire up its drag-and-drop, then load the server-side dashboard
// layout document (falls back to the localStorage boot state when
// unavailable). A pause from before a reload is restored so it survives
// refreshes.
document.addEventListener('DOMContentLoaded', function () {
  try { setEditMode(localStorage.getItem('golens.editmode') === '1'); } catch (e) { setEditMode(false); }
  initSectionsUI();
  sortCharts(); applyFilter(); initMetricsDnD(); loadDash(); renderPinned();
  try {
    if (sessionStorage.getItem('golens.paused') === '1') {
      setPaused(true);
      var check = document.getElementById('pause-check');
      if (check) check.checked = true;
    }
  } catch (e) {}
});

// setPaused gates every poll (HTMX trigger filters + Alpine intervals check
// body.dataset.paused) and persists across reloads via sessionStorage.
function setPaused(p) {
  if (p) document.body.dataset.paused = '1';
  else delete document.body.dataset.paused;
  try { sessionStorage.setItem('golens.paused', p ? '1' : ''); } catch (e) {}
}

// resetLayout clears the dashboard layout everywhere: an empty server
// document, the localStorage fallback keys, and the in-memory state. The
// section bands return to the server-rendered effective layout; collapse
// state resets with the layout (edit mode is a UI mode, not layout, and
// survives).
function resetLayout() {
  dashCfg = { version: 1, panels: [], order: { all_metrics: [], hooks: [], endpoints: [] }, http_card_view: '', timeframe: '', sections: bootSections() };
  tfStartSec = 0;
  ['golens.charts', 'golens.order', 'golens.hooksorder', 'golens.eporder', 'golens.httpview', 'golens.timeframe', 'golens.tfstart', 'golens.collapsed'].forEach(function (k) {
    try { localStorage.removeItem(k); } catch (e) {}
  });
  secCollapsed = {};
  if (window.Alpine && Alpine.store('ui')) Alpine.store('ui').charts = [];
  applyDash();
  if (!dashRemote) { showToast('layout reset', 'success'); return; }
  // DELETE (not an empty PUT): the server drops the saved document, which is
  // the seed-once reset state — presets re-apply on the next page load.
  fetchJSON('/metrics/config', { method: 'DELETE' })
    .then(function () { showToast('layout reset', 'success'); })
    .catch(function () { showToast('layout reset locally'); });
}

// "/" focuses the search box from anywhere (unless the user is typing).
document.addEventListener('keydown', function (e) {
  if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
  var t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
  var s = document.getElementById('search');
  if (s) { e.preventDefault(); s.focus(); }
});

// Pause all polling while the tab is hidden; resume on return. HTMX polling
// checks body.dataset.hidden; the Alpine components check document.hidden.
document.addEventListener('visibilitychange', function () {
  if (document.hidden) { document.body.dataset.hidden = '1'; } else { delete document.body.dataset.hidden; }
});

// --- alert status dot + pill ----------------------------------------------------
// Polls the alerter's firing count (when alerts are compiled in) and drives
// the header status dot and the Alerts-link count pill; the alerts page
// shares applyAlertStatus via its own poll.
function applyAlertStatus(n) {
  var dot = document.getElementById('brand-dot');
  if (dot) dot.classList.toggle('brand-dot-alert', n > 0);
  var pill = document.getElementById('alert-count');
  if (pill) {
    pill.textContent = n + ' firing';
    pill.hidden = n === 0;
  }
}

function pollAlertStatus() {
  if (!GoLensConfig.alertsEnabled || document.hidden) return;
  fetchJSON('/metrics/alerts/status').then(function (d) { applyAlertStatus((d && d.firing) || 0); }).catch(function () {});
}

setInterval(pollAlertStatus, POLL_MS);

// Cooldown ring: depletes over the poll interval, shows remaining seconds,
// resets on each HTMX refresh, and freezes while paused. Turns red when the
// previous refresh failed, green when successful.
(function () {
  var pollMs = POLL_MS;
  var ring = document.getElementById('cooldown-ring');
  var label = document.getElementById('cooldown-label');
  if (!ring || !label || !pollMs) return;
  var r = 14;
  var circ = 2 * Math.PI * r;
  ring.style.strokeDasharray = circ;
  ring.style.strokeDashoffset = 0;
  var start = performance.now();
  var pausedAt = 0;
  function frame(now) {
    if (document.body.dataset.paused) {
      if (!pausedAt) { pausedAt = now; label.textContent = 'II'; }
    } else {
      if (pausedAt) { start += now - pausedAt; pausedAt = 0; }
      var remaining = pollMs - (now - start);
      var frac = Math.max(0, Math.min(1, remaining / pollMs)); // 1 → 0
      ring.style.strokeDashoffset = circ * (1 - frac);
      label.textContent = Math.max(0, Math.ceil(remaining / 1000));
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);
  document.body.addEventListener('htmx:afterRequest', function (evt) {
    start = performance.now();
    var ok = evt.detail.successful;
    ring.setAttribute('stroke', ok ? '#34d399' : '#ef4444');
    pollAlertStatus(); // refresh the status dot/pill on the same poll tick
  });
})();

// --- shared canvas line chart -------------------------------------------------

function drawChart(canvasId, series, opts) {
  // Accepts a canvas element or an element id; line-chart panels pass
  // elements because a pinned clone duplicates the #charts card (and its id).
  var canvas = (typeof canvasId === 'string') ? document.getElementById(canvasId) : canvasId;
  if (!canvas) return;
  var ctx = canvas.getContext('2d');
  var dpr = window.devicePixelRatio || 1;
  var rect = canvas.getBoundingClientRect();
  canvas.width = rect.width * dpr;
  canvas.height = rect.height * dpr;
  ctx.scale(dpr, dpr);
  var W = rect.width, H = rect.height;
  var pad = { top: 8, right: opts.padRight || 8, bottom: 20, left: 50 };
  var cw = W - pad.left - pad.right;
  var ch = H - pad.top - pad.bottom;

  ctx.clearRect(0, 0, W, H);
  if (!series.length || !series[0].points.length) {
    ctx.fillStyle = '#64748b';
    ctx.font = '11px ui-monospace, monospace';
    ctx.textAlign = 'center';
    ctx.fillText('no data yet', W / 2, H / 2);
    return;
  }

  // compute global min/max across all series
  var gMin = Infinity, gMax = -Infinity;
  series.forEach(function (s) { s.points.forEach(function (p) {
    if (p.min !== undefined) gMin = Math.min(gMin, p.min);
    if (p.max !== undefined) gMax = Math.max(gMax, p.max);
    gMin = Math.min(gMin, p.v);
    gMax = Math.max(gMax, p.v);
  }); });
  if (gMin === gMax) { gMin *= 0.9; gMax *= 1.1; }
  if (gMin === 0 && gMax === 0) { gMax = 1; }
  var range = gMax - gMin;

  // time range
  var tMin = Infinity, tMax = -Infinity;
  series[0].points.forEach(function (p) { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
  var tRange = tMax - tMin || 1;

  function xPos(t) { return pad.left + ((t - tMin) / tRange) * cw; }
  function yPos(v) { return pad.top + ch - ((v - gMin) / range) * ch; }

  // grid lines
  ctx.strokeStyle = '#1e293b';
  ctx.lineWidth = 1;
  for (var i = 0; i <= 4; i++) {
    var y = pad.top + (ch * i / 4);
    ctx.beginPath(); ctx.moveTo(pad.left, y); ctx.lineTo(pad.left + cw, y); ctx.stroke();
  }

  // Y-axis labels
  ctx.fillStyle = '#64748b';
  ctx.font = '10px ui-monospace, monospace';
  ctx.textAlign = 'right';
  for (var j = 0; j <= 4; j++) {
    var v = gMax - (range * j / 4);
    var yy = pad.top + (ch * j / 4);
    ctx.fillText((opts.fmtY || fmtCount)(v), pad.left - 6, yy + 3);
  }

  // X-axis labels
  ctx.textAlign = 'center';
  var steps = Math.min(series[0].points.length, 6);
  for (var k = 0; k <= steps; k++) {
    var t = tMin + (tRange * k / steps);
    var x = xPos(t);
    var d = new Date(t * 1000);
    var label = d.getHours().toString().padStart(2, '0') + ':' + d.getMinutes().toString().padStart(2, '0');
    ctx.fillText(label, x, pad.top + ch + 14);
  }

  // draw each series
  var colors = ['#34d399', '#38bdf8', '#f59e0b', '#f472b6'];
  series.forEach(function (s, si) {
    var color = colors[si % colors.length];
    var pts = s.points;
    if (!pts.length) return;

    // min/max band
    if (pts[0].min !== undefined) {
      ctx.fillStyle = color + '15';
      ctx.beginPath();
      ctx.moveTo(xPos(pts[0].t), yPos(pts[0].max));
      pts.forEach(function (p) { ctx.lineTo(xPos(p.t), yPos(p.max)); });
      for (var m = pts.length - 1; m >= 0; m--) ctx.lineTo(xPos(pts[m].t), yPos(pts[m].min));
      ctx.closePath();
      ctx.fill();
    }

    // line
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    pts.forEach(function (p, i) {
      var x = xPos(p.t), y = yPos(p.v);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.stroke();

    // end dot
    var last = pts[pts.length - 1];
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(xPos(last.t), yPos(last.v), 3, 0, Math.PI * 2);
    ctx.fill();
  });
}

// --- Runtime Health: time-series charts ----------------------------------------

function runtimeHealth() {
  var metrics = [
    { key: 'go_memstats_sys_bytes' },
    { key: 'go_memstats_heap_inuse_bytes' },
    { key: 'go_memstats_alloc_bytes' },
    { key: 'go_memstats_heap_objects' },
    { key: 'cpu_usage_percent' },
    { key: 'go_goroutines' }
  ];
  // rtCards lists the runtime cards in current DOM order. Indexes are always
  // derived from it — never hard-coded — so drag and buttons stay correct
  // after a reorder.
  function rtCards() {
    return Array.from(document.getElementById('runtime-charts').querySelectorAll('.rt-card'));
  }
  return {
    duration: '1h',
    series: {},
    currentSysBytes: 0, currentHeapInuse: 0, currentMem: 0, currentHeapObj: 0, cpuPct: 0, currentGoro: 0,
    drag: null,
    init() { this.refresh(); setInterval(() => { if (!document.hidden) this.refresh(); }, POLL_MS); window.addEventListener('golens-tf', () => this.refresh()); },
    start(e) {
      if (!editMode()) return;
      var card = e.target.closest('.rt-card');
      if (!card) return;
      this.drag = rtCards().indexOf(card);
      card.classList.add('dragging');
    },
    dragEnd(e) {
      this.drag = null;
      var card = e.target.closest('.rt-card');
      if (card) card.classList.remove('dragging');
    },
    drop(e) {
      if (this.drag === null) return;
      var from = this.drag;
      this.drag = null;
      var card = e.target.closest('.rt-card');
      if (card) card.classList.remove('dragging');
      // after/before move nodes cleanly even when the two cards live in
      // different containers (the memory card is a direct child of
      // #runtime-charts, the CPU/goroutine cards sit inside the inner grid).
      var cards = rtCards();
      moveCard(cards, from, cards.indexOf(card), function () {});
    },
    move(e, d) {
      var card = e.target.closest('.rt-card');
      if (!card) return;
      var cards = rtCards();
      var from = cards.indexOf(card);
      moveCard(cards, from, neighborIndex(cards, from, d), function () {});
    },
    refresh() {
      // The charts anchor to their band's runtime block (block ids render at
      // most once, so the id is unique); re-resolved every pass so selector
      // changes land on the next refresh without a page reload.
      this.duration = effectiveDuration(document.getElementById('runtime-charts'));
      metrics.forEach(m => {
        fetchJSON('/metrics/history?name=' + m.key + '&' + historyQS(this.duration))
          .then(d => {
            this.series[m.key] = d.points || [];
            // update current values from last point
            if (d.points && d.points.length) {
              var last = d.points[d.points.length - 1].v;
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
      fetchJSON('/metrics/data?format=json').then(snaps => {
        (snaps || []).forEach(s => {
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
      var memByteSeries = [
        { points: this.series['go_memstats_sys_bytes'] || [] },
        { points: this.series['go_memstats_heap_inuse_bytes'] || [] },
        { points: this.series['go_memstats_alloc_bytes'] || [] }
      ];
      drawChart('rt-mem', memByteSeries, { fmtY: fmtBytes, padRight: 50 });
      // overlay heap objects on same canvas with right axis
      var heapObjPts = this.series['go_memstats_heap_objects'] || [];
      if (heapObjPts.length) {
        var canvas = document.getElementById('rt-mem');
        if (canvas) {
          var ctx = canvas.getContext('2d');
          var rect = canvas.getBoundingClientRect();
          var W = rect.width, H = rect.height;
          var pad = { top: 8, right: 50, bottom: 20, left: 50 };
          var cw = W - pad.left - pad.right;
          var ch = H - pad.top - pad.bottom;
          var tMin = Infinity, tMax = -Infinity;
          memByteSeries.forEach(s => s.points.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); }));
          heapObjPts.forEach(p => { tMin = Math.min(tMin, p.t); tMax = Math.max(tMax, p.t); });
          var tRange = tMax - tMin || 1;
          var gMin = Infinity, gMax = -Infinity;
          heapObjPts.forEach(p => { gMin = Math.min(gMin, p.v); gMax = Math.max(gMax, p.v); });
          if (gMin === gMax) { gMin *= 0.9; gMax *= 1.1; }
          var range = gMax - gMin;
          function xP(t) { return pad.left + ((t - tMin) / tRange) * cw; }
          function yP(v) { return pad.top + ch - ((v - gMin) / range) * ch; }
          ctx.strokeStyle = '#f59e0b';
          ctx.lineWidth = 1.5;
          ctx.setLineDash([4, 3]);
          ctx.beginPath();
          heapObjPts.forEach((p, i) => { i === 0 ? ctx.moveTo(xP(p.t), yP(p.v)) : ctx.lineTo(xP(p.t), yP(p.v)); });
          ctx.stroke();
          ctx.setLineDash([]);
          ctx.fillStyle = '#f59e0b';
          ctx.font = '10px ui-monospace, monospace';
          ctx.textAlign = 'left';
          for (var i = 0; i <= 4; i++) {
            var v = gMax - (range * i / 4);
            ctx.fillText(fmtCount(v), pad.left + cw + 4, pad.top + (ch * i / 4) + 3);
          }
        }
      }
      // CPU + goroutines (dual axis)
      var cpuPts = this.series['cpu_usage_percent'] || [];
      var goroPts = this.series['go_goroutines'] || [];
      drawChart('rt-cpu', [{ points: cpuPts }], { fmtY: v => v.toFixed(0) + '%', padRight: 40 });
      // overlay goroutines on same canvas
      if (goroPts.length) {
        var cpuCanvas = document.getElementById('rt-cpu');
        if (cpuCanvas) {
          var cctx = cpuCanvas.getContext('2d');
          var crect = cpuCanvas.getBoundingClientRect();
          var cW = crect.width, cH = crect.height;
          var cpad = { top: 8, right: 40, bottom: 20, left: 50 };
          var ccw = cW - cpad.left - cpad.right;
          var cch = cH - cpad.top - cpad.bottom;
          var ctMin = Infinity, ctMax = -Infinity;
          cpuPts.forEach(p => { ctMin = Math.min(ctMin, p.t); ctMax = Math.max(ctMax, p.t); });
          goroPts.forEach(p => { ctMin = Math.min(ctMin, p.t); ctMax = Math.max(ctMax, p.t); });
          var ctRange = ctMax - ctMin || 1;
          var cgMin = Infinity, cgMax = -Infinity;
          goroPts.forEach(p => { cgMin = Math.min(cgMin, p.v); cgMax = Math.max(cgMax, p.v); });
          if (cgMin === cgMax) { cgMin *= 0.9; cgMax *= 1.1; }
          var crange = cgMax - cgMin;
          function cxP(t) { return cpad.left + ((t - ctMin) / ctRange) * ccw; }
          function cyP(v) { return cpad.top + cch - ((v - cgMin) / crange) * cch; }
          cctx.strokeStyle = '#38bdf8';
          cctx.lineWidth = 1.5;
          cctx.setLineDash([4, 3]);
          cctx.beginPath();
          goroPts.forEach((p, i) => { i === 0 ? cctx.moveTo(cxP(p.t), cyP(p.v)) : cctx.lineTo(cxP(p.t), cyP(p.v)); });
          cctx.stroke();
          cctx.setLineDash([]);
          cctx.fillStyle = '#38bdf8';
          cctx.font = '10px ui-monospace, monospace';
          cctx.textAlign = 'left';
          for (var n = 0; n <= 4; n++) {
            var cv = cgMax - (crange * n / 4);
            cctx.fillText(Math.round(cv).toString(), cpad.left + ccw + 4, cpad.top + (cch * n / 4) + 3);
          }
        }
      }
      // Goroutines
      drawChart('rt-goro', [{ points: this.series['go_goroutines'] || [] }], { fmtY: v => Math.round(v).toString() });
    }
  };
}

// --- Histogram Time-Series component --------------------------------------------

// Bucket palette shared by the stacked-area chart and its legend swatches —
// one array, so a legend entry always matches its chart color.
var bucketColors = [
  'rgba(168, 85, 247, 0.7)',   // purple-500
  'rgba(139, 92, 246, 0.7)',   // violet-500
  'rgba(124, 58, 237, 0.7)',   // violet-600
  'rgba(109, 40, 217, 0.7)',   // violet-700
  'rgba(88, 28, 135, 0.7)',    // violet-900
  'rgba(192, 132, 252, 0.7)',  // purple-400
  'rgba(216, 180, 254, 0.7)',  // purple-300
];

function histogramTimeSeries() {
  return {
    duration: '1h',
    histogramMetrics: [],
    timer: null,

    init() {
      this.refresh();
      this.timer = setInterval(() => { if (!document.hidden) this.refresh(); }, POLL_MS);
      window.addEventListener('golens-tf', () => this.refresh());
    },

    async refresh() {
      // Re-resolve the effective timeframe every pass so selector changes
      // land on the next refresh without a page reload.
      this.duration = effectiveDuration(document.getElementById('histogram-timeseries'));
      try {
        // Fetch all histogram metrics from current data
        var metrics = await fetchJSON('/metrics/data?format=json');
        var histogramCards = (metrics || []).filter(c => c.type === 'histogram');

        // Build histogram metrics list. The overflow bucket serializes
        // upper_bound 0 as its marker — filter on the overflow flag, not the
        // bound value, or a legitimately registered 0 bound would drop from
        // the legend while the chart still draws it.
        this.histogramMetrics = histogramCards.map(card => ({
          name: card.name,
          description: card.description || '',
          bounds: (card.buckets || []).filter(b => !b.overflow).map(b => b.upper_bound)
        }));
        if (!this.histogramMetrics.length) return;

        // One batch request for every histogram series — the N+1 sequential
        // fetches this loop used to make are served by /metrics/history?names=.
        var names = this.histogramMetrics.map(m => m.name).join(',');
        var histories = await fetchJSON('/metrics/history?names=' + encodeURIComponent(names) + '&' + historyQS(this.duration));

        for (const metric of this.histogramMetrics) {
          var history = (histories || {})[metric.name] || {};
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
      var canvasId = 'hist-ts-' + metric.name;
      var canvas = document.getElementById(canvasId);
      if (!canvas || !metric.points.length) return;

      var ctx = canvas.getContext('2d');
      var rect = canvas.getBoundingClientRect();
      var W = rect.width, H = rect.height;

      // Handle high-DPI displays
      var dpr = window.devicePixelRatio || 1;
      canvas.width = W * dpr;
      canvas.height = H * dpr;
      ctx.scale(dpr, dpr);

      var pad = { top: 15, right: 20, bottom: 30, left: 45 };
      var cw = W - pad.left - pad.right;
      var ch = H - pad.top - pad.bottom;

      // Clear canvas
      ctx.clearRect(0, 0, W, H);

      // Find time range. A single-timestamp series (fresh process / sparse
      // traffic) has a zero-width x domain, which no tRange alone can widen
      // (t - tMin stays 0 for every vertex); anchor the domain at
      // [tMax - durSec, tMax] instead, so the point sits at the right edge
      // and the polygon and axis labels share one full-width domain.
      var tMin = Infinity, tMax = -Infinity;
      metric.points.forEach(p => {
        tMin = Math.min(tMin, p.t);
        tMax = Math.max(tMax, p.t);
      });
      var dm = /^(\d+)([mhdw])$/.exec(this.duration || '');
      var durSec = dm ? dm[1] * { m: 60, h: 3600, d: 86400, w: 604800 }[dm[2]] : 0;
      if (this.duration === 'custom' && tfStartSec) durSec = Math.max(1, Math.floor(Date.now() / 1000) - tfStartSec);
      if (tMax === tMin) {
        tMin = tMax - (durSec || 1);
      }
      var tRange = tMax - tMin;

      // Find max bucket count across all buckets for Y-axis scaling
      var maxCount = 0;
      metric.points.forEach(p => {
        if (p.histogram_buckets) {
          p.histogram_buckets.forEach(b => {
            maxCount = Math.max(maxCount, b.count);
          });
        }
      });
      if (maxCount === 0) maxCount = 1;

      // Helper functions
      function xP(t) { return pad.left + ((t - tMin) / tRange) * cw; }
      function yP(v) { return pad.top + ch - (v / maxCount) * ch; }

      // Draw stacked area chart for each bucket
      var numBuckets = metric.histogramBounds.length;
      for (var bucketIdx = 0; bucketIdx < numBuckets; bucketIdx++) {
        var bound = metric.histogramBounds[bucketIdx];
        var color = bucketColors[bucketIdx % bucketColors.length];

        ctx.beginPath();
        ctx.moveTo(xP(tMin), yP(0));

        // Build the stacked area
        for (const pt of metric.points) {
          if (!pt.histogram_buckets || !pt.histogram_buckets[bucketIdx]) continue;
          var cumulativeCount = 0;

          // Sum counts for this bucket and all below it
          for (var i = 0; i <= bucketIdx; i++) {
            if (pt.histogram_buckets[i]) {
              cumulativeCount += pt.histogram_buckets[i].count;
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
      ctx.strokeStyle = '#64748b';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(pad.left, pad.top);
      ctx.lineTo(pad.left, pad.top + ch);
      ctx.lineTo(pad.left + cw, pad.top + ch);
      ctx.stroke();

      // Draw Y-axis labels
      ctx.fillStyle = '#64748b';
      ctx.font = '10px ui-monospace, monospace';
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      for (var n = 0; n <= 4; n++) {
        var v = Math.round(maxCount * (1 - n / 4));
        ctx.fillText(v.toString(), pad.left - 8, pad.top + (ch * n / 4));
      }

      // Draw X-axis labels (time)
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      var timeLabels = 5;
      for (var m = 0; m < timeLabels; m++) {
        var t = tMin + (tRange * m / (timeLabels - 1));
        var x = pad.left + (cw * m / (timeLabels - 1));
        var date = new Date(t * 1000);
        var label = date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false });
        ctx.fillText(label, x, pad.top + ch + 8);
      }
    },

    getBucketColor(index) {
      return bucketColors[index % bucketColors.length];
    },

    formatBound(bound) {
      if (bound >= 1) return bound.toFixed(0);
      if (bound >= 0.01) return bound.toFixed(2);
      return bound.toFixed(3);
    }
  };
}
