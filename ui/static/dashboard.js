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

// Boot from the localStorage cache; when the server document loads it
// replaces this state (loadDash).
var dashCfg = {
  version: 1,
  panels: lsJSON('golens.charts').map(function (n) { return { id: n, metric: n }; }),
  order: { all_metrics: lsJSON('golens.order'), hooks: lsJSON('golens.hooksorder'), endpoints: lsJSON('golens.eporder') },
  http_card_view: localStorage.getItem('golens.httpview') || ''
};
var dashRemote = false;

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

function persistDash() {
  // localStorage write-through cache first, so the layout survives even if
  // the server PUT fails.
  try {
    localStorage.setItem('golens.charts', JSON.stringify(dashCfg.panels.map(function (p) { return p.metric; })));
    localStorage.setItem('golens.order', JSON.stringify(dashCfg.order.all_metrics || []));
    localStorage.setItem('golens.hooksorder', JSON.stringify(dashCfg.order.hooks || []));
    localStorage.setItem('golens.eporder', JSON.stringify(dashCfg.order.endpoints || []));
    localStorage.setItem('golens.httpview', dashCfg.http_card_view || '');
  } catch (e) {}
  if (!dashRemote) return;
  fetch('/metrics/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(dashCfg)
  }).then(function (r) {
    if (!r.ok) { dashRemote = false; showToast('changes are local only'); }
  }).catch(function () { dashRemote = false; showToast('changes are local only'); });
}

// applyDash re-hydrates the UI from the current config document.
function applyDash() {
  if (window.Alpine && Alpine.store('ui')) {
    Alpine.store('ui').charts = dashCfg.panels.map(function (p) { return p.metric; });
  }
  sortCharts(); applyFilter(); renderPinned(); applyHookOrder();
}

function loadDash() {
  fetchJSON('/metrics/config', { headers: { 'Accept': 'application/json' } })
    .then(function (d) {
      if (!d || d.version !== 1) return;
      if (!d.panels) d.panels = [];
      if (!d.order) d.order = { all_metrics: [], hooks: [], endpoints: [] };
      if (!d.http_card_view) d.http_card_view = '';
      // First connect against an empty server doc: promote the localStorage
      // layout (still in dashCfg) so existing pins, section orderings, and the
      // HTTP card view are migrated, even when only orderings exist.
      var promoted = !(d.panels.length || dashHasOrder(d)) && (dashCfg.panels.length || dashHasOrder(dashCfg));
      if (promoted) { d.panels = dashCfg.panels; d.order = dashCfg.order; d.http_card_view = dashCfg.http_card_view; }
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
      if (n && !this.has(n)) {
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
          }
        }
        persistDash();
      }
    },
    remove: function (n) {
      this.charts = this.charts.filter(function (x) { return x !== n; });
      dashCfg.panels = chartsToPanels(this.charts);
      persistDash();
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

// Add the searched/selected metric as a pinned chart (no navigation).
function addChart() {
  var st = Alpine.store('ui');
  var n = st.name;
  st.add = false;
  if (n) st.pin(n);
  st.name = '';
}

function saveOrder(o) { dashCfg.order.all_metrics = o; persistDash(); }

function metricNames() {
  return Array.from(document.querySelectorAll('#charts [data-metric]')).map(function (el) { return el.dataset.metric; });
}

function cssEsc(s) { return (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&'); }

// pinSizeClass returns the grid span class configured for a pinned panel.
// Mirrors the server's mapping: only md/lg widen the cell; sm (and empty)
// render as a single cell with no span class.
function pinSizeClass(name) {
  for (var i = 0; i < dashCfg.panels.length; i++) {
    var p = dashCfg.panels[i];
    if (p.metric === name && (p.size === 'md' || p.size === 'lg')) return 'card-' + p.size;
  }
  return '';
}

// Clone each pinned metric's server-rendered card from #charts into its slot.
function renderPinned() {
  document.querySelectorAll('#pinned [data-pin]').forEach(function (body) {
    var name = body.getAttribute('data-pin');
    var slot = body.closest('.pin-slot');
    if (slot) slot.className = 'pin-slot card card-grab ' + pinSizeClass(name);
    var card = document.querySelector('#charts [data-metric="' + cssEsc(name) + '"]');
    if (card) { body.innerHTML = card.innerHTML; }
    else { body.innerHTML = '<div class="pin-missing">metric unavailable</div>'; }
  });
  drawLineCharts();
}

// --- line-chart panels (chart_type "line") -------------------------------------

// Draw every canvas[data-history] on the page: one history fetch per metric
// name, rendered into each matching canvas (a pinned clone duplicates the
// #charts canvas, so both must draw). The window is fixed at 1h. htmx swaps
// recreate the canvases, so this re-runs after every swap via renderPinned.
var lineInflight = {};
function drawLineCharts() {
  var byName = {};
  document.querySelectorAll('canvas[data-history]').forEach(function (c) {
    var n = c.getAttribute('data-history');
    (byName[n] = byName[n] || []).push(c);
  });
  Object.keys(byName).forEach(function (n) {
    if (lineInflight[n]) return;
    lineInflight[n] = true;
    var canvases = byName[n];
    fetchJSON('/metrics/history?name=' + encodeURIComponent(n) + '&duration=1h')
      .then(function (s) {
        var points = (s && s.points) || [];
        canvases.forEach(function (c) { drawChart(c, [{ points: points }], {}); });
      })
      .catch(function () {})
      .then(function () { delete lineInflight[n]; });
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
    start(i) { this.drag = i; },
    drop(i) {
      if (this.drag === null || this.drag === i) { this.drag = null; return; }
      var keys = this.endpoints.map(function (e) { return e.method + ' ' + e.path; });
      var moved = keys.splice(this.drag, 1)[0];
      keys.splice(i, 0, moved);
      saveEpOrder(keys);
      this.drag = null;
      this.bump++;
    },
    // Calculate height relative to each endpoint's p99 (the highest bar)
    h(endpoint, value) {
      var max = endpoint.p99 || 0.001;
      return Math.max(2, Math.round((value / max) * 100));
    },
    fmt(v) { if (v >= 1) return v.toFixed(2); if (v >= 0.01) return v.toFixed(3); return v.toFixed(4); }
  };
}

// Hooks section: provides drag-and-drop for hook metrics
function hooksSection() {
  return {
    drag: null,
    init() { this.setupDnD(); },
    start(i) { this.drag = i; },
    drop(i) {
      if (this.drag === null || this.drag === i) { this.drag = null; return; }
      var grid = document.getElementById('hooks');
      var cards = Array.from(grid.querySelectorAll('[data-metric]'));
      if (this.drag < cards.length && i < cards.length) {
        var dragged = cards[this.drag];
        var target = cards[i];
        var parent = dragged.parentNode;
        var draggedNext = dragged.nextSibling;
        var targetNext = target.nextSibling;
        parent.insertBefore(dragged, targetNext);
        parent.insertBefore(target, draggedNext);
        // Save new order
        var names = Array.from(grid.querySelectorAll('[data-metric]')).map(function (el) { return el.dataset.metric; });
        saveHookOrder(names);
      }
      this.drag = null;
    },
    setupDnD() {
      var grid = document.getElementById('hooks');
      if (!grid) return;
      // Apply persisted order on load
      applyHookOrder();
      // Setup drag-and-drop
      var dragCard = null;
      grid.addEventListener('dragstart', function (e) {
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

// Cardinality panel: polls /metrics/cardinality and shows per-metric series stats.
function cardinalityPanel(maxSeries) {
  return {
    open: false,
    metrics: [],
    maxSeries: maxSeries,
    init() { this.load(); setInterval(() => { if (this.open && !document.hidden) this.load(); }, POLL_MS); },
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

// Boot: apply persisted All Metrics order + wire up its drag-and-drop, then
// load the server-side dashboard layout document (falls back to the
// localStorage boot state when unavailable).
document.addEventListener('DOMContentLoaded', function () { sortCharts(); applyFilter(); initMetricsDnD(); loadDash(); });

// Pause all polling while the tab is hidden; resume on return. HTMX polling
// checks body.dataset.hidden; the Alpine components check document.hidden.
document.addEventListener('visibilitychange', function () {
  if (document.hidden) { document.body.dataset.hidden = '1'; } else { delete document.body.dataset.hidden; }
});

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
  return {
    duration: '1h',
    series: {},
    currentSysBytes: 0, currentHeapInuse: 0, currentMem: 0, currentHeapObj: 0, cpuPct: 0, currentGoro: 0,
    drag: null,
    init() { this.refresh(); setInterval(() => { if (!document.hidden) this.refresh(); }, POLL_MS); },
    start(i) { this.drag = i; },
    drop(i) {
      if (this.drag === null || this.drag === i) { this.drag = null; return; }
      // Reorder the runtime charts by moving the dragged card relative to
      // the target card. Selecting .rt-card (not every nested div) keeps the
      // wired 0..2 indexes aligned with the actual cards, and after/before
      // move nodes cleanly even when the two cards live in different
      // containers (the memory card is a direct child of #runtime-charts,
      // the CPU/goroutine cards sit inside the inner grid).
      var cards = document.getElementById('runtime-charts').querySelectorAll('.rt-card');
      if (this.drag < cards.length && i < cards.length) {
        var dragged = cards[this.drag];
        var target = cards[i];
        if (this.drag < i) { target.after(dragged); } else { target.before(dragged); }
      }
      this.drag = null;
    },
    refresh() {
      metrics.forEach(m => {
        fetchJSON('/metrics/history?name=' + m.key + '&duration=' + this.duration)
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

function histogramTimeSeries() {
  return {
    duration: '1h',
    histogramMetrics: [],
    timer: null,

    init() {
      this.refresh();
      this.timer = setInterval(() => { if (!document.hidden) this.refresh(); }, POLL_MS);
    },

    async refresh() {
      try {
        // Fetch all histogram metrics from current data
        var metrics = await fetchJSON('/metrics/data?format=json');
        var histogramCards = (metrics || []).filter(c => c.type === 'histogram');

        // Build histogram metrics list
        this.histogramMetrics = histogramCards.map(card => ({
          name: card.name,
          description: card.description || '',
          bounds: (card.buckets || []).map(b => b.upper_bound).filter(b => b > 0) // Extract bounds from buckets
        }));
        if (!this.histogramMetrics.length) return;

        // One batch request for every histogram series — the N+1 sequential
        // fetches this loop used to make are served by /metrics/history?names=.
        var names = this.histogramMetrics.map(m => m.name).join(',');
        var histories = await fetchJSON('/metrics/history?names=' + encodeURIComponent(names) + '&duration=' + this.duration);

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

      // Purple color palette for buckets
      var bucketColors = [
        'rgba(168, 85, 247, 0.7)',   // purple-500
        'rgba(139, 92, 246, 0.7)',   // violet-500
        'rgba(124, 58, 237, 0.7)',   // violet-600
        'rgba(109, 40, 217, 0.7)',   // violet-700
        'rgba(88, 28, 135, 0.7)',    // violet-900
        'rgba(192, 132, 252, 0.7)',  // purple-400
        'rgba(216, 180, 254, 0.7)',  // purple-300
      ];

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
      var colors = [
        'bucket-swatch-0', 'bucket-swatch-1', 'bucket-swatch-2',
        'bucket-swatch-3', 'bucket-swatch-4', 'bucket-swatch-5'
      ];
      return colors[index % colors.length];
    },

    formatBound(bound) {
      if (bound >= 1) return bound.toFixed(0);
      if (bound >= 0.01) return bound.toFixed(2);
      return bound.toFixed(3);
    }
  };
}
