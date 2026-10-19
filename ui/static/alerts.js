// GoLens alerts page script. Server-rendered values (notifier flag) arrive
// via window.GoLensConfig, set by an inline bootstrap before this file loads.
// Theme, toast, and fetch helpers live in common.js.
'use strict';

var GoLensConfig = window.GoLensConfig || {};
var HAS_NOTIFIER = !!GoLensConfig.hasNotifier;

// Alpine store for the add-alert modal (mirrors the dashboard's store).
document.addEventListener('alpine:init', function () {
  Alpine.store('ui', {
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

    selectMetric: function (name) {
      this.alertMetric = name;
      this.alertForm.name = name + ' alert';
      this.alertStep = 2;
    },

    closeAlert: function () {
      this.alertModal = false;
      this.alertStep = 1;
    },

    applyTemplate: function (id) {
      if (!id) return;
      var t = this.alertTemplates.find(function (x) { return x.id === id; });
      if (t) {
        this.alertForm.email_subject = t.subject;
        this.alertForm.email_body = t.body;
      }
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
          document.dispatchEvent(new CustomEvent('alert:created', { detail: { metric: self.alertMetric } }));
        })
        .catch(function (e) {
          document.dispatchEvent(new CustomEvent('alert:error', { detail: { message: e.message || 'Failed to create alert' } }));
        });
    }
  });
});

// MutationObserver to open the alert modal from the header button (script is
// deferred, so the body exists).
var moAlert = new MutationObserver(function () {
  if (document.body.dataset.alertAdd === '1') {
    Alpine.store('ui').openAlert();
    delete document.body.dataset.alertAdd;
  }
});
moAlert.observe(document.body, { attributes: true, attributeFilter: ['data-alert-add'] });

function alertsApp() {
  return {
    rules: [],
    log: [],
    paused: false,        // pauses the 10s refresh (the dashboard's pause control, mirrored)
    confirmId: '',        // rule awaiting its second "Confirm delete" click
    confirmTimer: null,

    init: function () {
      this.refreshRules();
      this.refreshLog();
      this.refreshStatus();
      // Auto-refresh rules and log every 10s so last_fired and the rules
      // list never freeze on screen; gated on tab visibility and the pause
      // control.
      setInterval(function () {
        if (document.hidden || this.paused) return;
        this.refreshRules();
        this.refreshLog();
        this.refreshStatus();
      }.bind(this), 10000);
      // Listen for alert creation / failure from the modal store
      var self = this;
      document.addEventListener('alert:created', function (e) {
        self.refreshRules();
        showToast('Alert created for ' + ((e.detail && e.detail.metric) || 'metric'), 'success');
      });
      document.addEventListener('alert:error', function (e) {
        showToast((e.detail && e.detail.message) || 'Failed to create alert');
      });
    },

    refreshRules: function () {
      var self = this;
      fetchJSON('/metrics/alerts/rules', { headers: { 'Accept': 'application/json' } })
        .then(function (d) { self.rules = d || []; })
        .catch(function () {});
    },

    refreshLog: function () {
      var self = this;
      fetchJSON('/metrics/alerts/log', { headers: { 'Accept': 'application/json' } })
        .then(function (d) { self.log = d || []; })
        .catch(function () { showToast('Failed to load alert log'); });
    },

    // refreshStatus drives the header dot: emerald when nothing is firing,
    // rose while any enabled rule's condition holds.
    refreshStatus: function () {
      fetchJSON('/metrics/alerts/status').then(function (d) {
        var dot = document.getElementById('brand-dot');
        if (dot) dot.classList.toggle('brand-dot-alert', (d && d.firing) > 0);
      }).catch(function () {});
    },

    deleteRule: function (id) {
      var self = this;
      // Two-phase: first click arms the button for ~3s, second click fires.
      if (this.confirmId !== id) {
        this.confirmId = id;
        clearTimeout(this.confirmTimer);
        this.confirmTimer = setTimeout(function () { self.confirmId = ''; }, 3000);
        return;
      }
      clearTimeout(this.confirmTimer);
      this.confirmId = '';
      fetch('/metrics/alerts/rules/' + id, { method: 'DELETE' })
        .then(function (r) {
          if (!r.ok) throw new Error('Delete failed');
          self.rules = self.rules.filter(function (x) { return x.id !== id; });
          showToast('Rule deleted', 'success');
        })
        .catch(function (e) { showToast(e.message || 'Failed to delete rule'); });
    },

    toggleRule: function (r) {
      var prev = r.enabled;
      var updated = Object.assign({}, r, { enabled: !r.enabled });
      fetch('/metrics/alerts/rules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updated)
      }).then(function (resp) {
        if (!resp.ok) throw new Error('Toggle failed');
        r.enabled = !r.enabled;
      }).catch(function (e) {
        r.enabled = prev;
        showToast(e.message || 'Failed to toggle rule');
      });
    },

    condLabel: function (c) {
      return { 'gt': '>', 'gte': '>=', 'lt': '<', 'lte': '<=', 'eq': '=' }[c] || c;
    },

    fmtDur: function (d) {
      if (!d) return '—';
      if (typeof d === 'string') return d;
      // Go duration JSON: nanoseconds as number
      var s = Math.round(d / 1e9);
      if (s < 60) return s + 's';
      if (s < 3600) return Math.floor(s / 60) + 'm' + (s % 60 ? (s % 60) + 's' : '');
      return Math.floor(s / 3600) + 'h' + Math.floor((s % 3600) / 60) + 'm';
    },

    fmtTime: function (t) {
      if (!t) return '—';
      var d = new Date(t);
      // Full date + time: hour:minute:second alone is ambiguous the moment
      // an entry is more than a day old.
      return d.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
    },

    timeAgo: function (t) {
      if (!t) return '—';
      var d = new Date(t);
      var s = Math.round((Date.now() - d.getTime()) / 1000);
      if (s < 60) return s + 's ago';
      if (s < 3600) return Math.floor(s / 60) + 'm ago';
      if (s < 86400) return Math.floor(s / 3600) + 'h ago';
      return Math.floor(s / 86400) + 'd ago';
    }
  };
}
