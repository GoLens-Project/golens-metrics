package golens

import "html/template"

const alertsPageSrc = `<!DOCTYPE html>
<html lang="en" x-data="{}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" type="image/x-icon" href="/favicon.ico">
<title>{{.ProjectName}} Alerts</title>
<script src="https://cdn.tailwindcss.com"></script>
<script>tailwind.config={theme:{extend:{fontFamily:{sans:['Inter','system-ui','sans-serif'],mono:['JetBrains Mono','ui-monospace','SFMono-Regular','Menlo','monospace']}}}}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<script defer src="https://unpkg.com/alpinejs@3.14.1/dist/cdn.min.js"></script>
<script>
  (function(){
    var t = 'dark';
    try { t = localStorage.getItem('golens.theme') || 'dark'; } catch(e){}
    var c = document.documentElement.classList;
    c.add(t === 'light' ? 'light' : 'dark');
    c.remove(t === 'light' ? 'dark' : 'light');
  })();
</script>
<style>
  [x-cloak]{display:none}
  ::-webkit-scrollbar{height:8px;width:8px}
  ::-webkit-scrollbar-thumb{background:#334155;border-radius:4px}
  html.light{color-scheme:light}
  html.light body{background:#ffffff;color:#0f172a}
  html.light .bg-slate-950{background-color:#ffffff !important}
  html.light .bg-slate-950\/80{background-color:rgba(255,255,255,.8) !important}
  html.light .bg-slate-900{background-color:#f8fafc !important}
  html.light .bg-slate-800{background-color:#e2e8f0 !important}
  html.light .bg-slate-800\/80{background-color:rgba(226,232,240,.9) !important}
  html.light .hover\:bg-slate-800:hover{background-color:#e2e8f0 !important}
  html.light .border-slate-800{border-color:#e2e8f0 !important}
  html.light .border-slate-800\/80{border-color:rgba(226,232,240,.8) !important}
  html.light .border-slate-700{border-color:#cbd5e1 !important}
  html.light .text-white{color:#0f172a !important}
  html.light .text-slate-100{color:#0f172a !important}
  html.light .text-slate-200{color:#1e293b !important}
  html.light .text-slate-300{color:#334155 !important}
  html.light .text-slate-400{color:#475569 !important}
  html.light .text-slate-500{color:#64748b !important}
  html.light .text-slate-600{color:#94a3b8 !important}
  html.light ::-webkit-scrollbar-thumb{background:#cbd5e1}
</style>
</head>
<body class="bg-slate-950 min-h-screen text-slate-100 font-sans antialiased">

<!-- Header -->
<header class="border-b border-slate-800/80 bg-slate-950/80 backdrop-blur sticky top-0 z-20">
  <div class="max-w-7xl mx-auto px-6 py-4 flex items-center gap-4 flex-wrap">
    <div class="flex items-center gap-2 mr-auto">
      <span class="inline-block w-2.5 h-2.5 rounded-full bg-rose-400 animate-pulse"></span>
      <h1 class="text-lg font-semibold tracking-tight">{{.ProjectName}}</h1>
      <span class="text-xs text-slate-500 font-mono">alerting</span>
      {{if .HasNotifier}}
      <span class="inline-flex items-center gap-1.5 text-[10px] font-mono px-2 py-0.5 rounded-full border border-emerald-500/30 bg-emerald-500/10 text-emerald-400">
        <span class="inline-block w-1.5 h-1.5 rounded-full bg-emerald-400"></span> mailer connected
      </span>
      {{else}}
      <span class="inline-flex items-center gap-1.5 text-[10px] font-mono px-2 py-0.5 rounded-full border border-slate-700 bg-slate-800/50 text-slate-500">
        <span class="inline-block w-1.5 h-1.5 rounded-full bg-slate-600"></span> no mailer
      </span>
      {{end}}
    </div>
    <a href="/metrics" class="text-xs font-mono px-3 py-1.5 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 transition text-slate-400 hover:text-slate-200">
      &larr; Dashboard
    </a>
    <button onclick="document.body.dataset.alertAdd='1'"
            class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 transition">+ Add Rule</button>
    <button id="theme-toggle" title="toggle theme"
            onclick="toggleTheme()"
            class="w-9 h-9 rounded-lg border border-slate-800 bg-slate-900 hover:bg-slate-800 text-slate-300 text-base flex items-center justify-center transition">🌙</button>
  </div>
</header>

<main class="max-w-7xl mx-auto px-6 py-6" x-data="alertsApp()" x-init="init()">

  <!-- Alert Rules Section -->
  <section class="mb-8">
    <div class="flex items-center gap-2 mb-4">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">Alert Rules</h2>
      <span class="text-[10px] font-mono text-slate-600" x-text="'· ' + rules.length + ' rules'"></span>
    </div>
    <div class="bg-slate-900 border border-slate-800 rounded-xl overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-slate-800 text-left text-xs text-slate-500 font-mono">
            <th class="px-5 py-3">Status</th>
            <th class="px-5 py-3">Name</th>
            <th class="px-5 py-3">Metric</th>
            <th class="px-5 py-3">Condition</th>
            <th class="px-5 py-3 text-right">Threshold</th>
            <th class="px-5 py-3 text-right">Cooldown</th>
            <th class="px-5 py-3 text-right">Last Fired</th>
            <th class="px-5 py-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          <template x-for="r in rules" :key="r.id">
            <tr class="border-t border-slate-800/50 hover:bg-slate-800/30 transition">
              <td class="px-5 py-3">
                <button @click="toggleRule(r)"
                        :class="r.enabled ? 'bg-emerald-500' : 'bg-slate-600'"
                        class="relative inline-flex h-5 w-9 items-center rounded-full transition-colors">
                  <span :class="r.enabled ? 'translate-x-4' : 'translate-x-0.5'"
                        class="inline-block h-4 w-4 rounded-full bg-white transition-transform"></span>
                </button>
              </td>
              <td class="px-5 py-3 text-slate-200 font-medium" x-text="r.name"></td>
              <td class="px-5 py-3 text-slate-400 font-mono text-xs" x-text="r.metric"></td>
              <td class="px-5 py-3 text-slate-400 font-mono text-xs" x-text="condLabel(r.condition)"></td>
              <td class="px-5 py-3 text-right text-slate-300 font-mono text-xs" x-text="r.threshold"></td>
              <td class="px-5 py-3 text-right text-slate-500 font-mono text-xs" x-text="fmtDur(r.cooldown)"></td>
              <td class="px-5 py-3 text-right text-slate-500 font-mono text-xs" x-text="r.last_fired ? timeAgo(r.last_fired) : '—'"></td>
              <td class="px-5 py-3 text-right">
                <button @click="deleteRule(r.id)"
                        class="text-xs px-2 py-1 rounded border border-slate-800 text-slate-500 hover:text-rose-400 hover:border-rose-500/40 transition">Delete</button>
              </td>
            </tr>
          </template>
          <tr x-show="rules.length === 0">
            <td colspan="8" class="px-5 py-8 text-center text-slate-600 font-mono text-sm">No alert rules configured. Click "+ Add Rule" to create one.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <!-- Alert Log Section -->
  <section>
    <div class="flex items-center gap-2 mb-4">
      <h2 class="text-xs font-mono uppercase tracking-wider text-slate-500">Alert Log</h2>
      <span class="text-[10px] font-mono text-slate-600" x-text="'· ' + log.length + ' events'"></span>
      <button @click="refreshLog()" class="ml-auto text-xs font-mono px-2 py-1 rounded border border-slate-800 text-slate-500 hover:bg-slate-800 transition">Refresh</button>
    </div>
    <div class="bg-slate-900 border border-slate-800 rounded-xl overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-slate-800 text-left text-xs text-slate-500 font-mono">
            <th class="px-5 py-3">Time</th>
            <th class="px-5 py-3">Rule</th>
            <th class="px-5 py-3">Metric</th>
            <th class="px-5 py-3 text-right">Value</th>
            <th class="px-5 py-3 text-center">Condition</th>
            <th class="px-5 py-3 text-right">Threshold</th>
          </tr>
        </thead>
        <tbody>
          <template x-for="e in log" :key="e.id">
            <tr class="border-t border-slate-800/50 hover:bg-slate-800/30 transition">
              <td class="px-5 py-3 text-slate-400 font-mono text-xs" x-text="fmtTime(e.fired_at)"></td>
              <td class="px-5 py-3 text-slate-200" x-text="e.rule_name"></td>
              <td class="px-5 py-3 text-slate-400 font-mono text-xs" x-text="e.metric"></td>
              <td class="px-5 py-3 text-right font-mono text-xs" :class="e.value > e.threshold ? 'text-rose-400' : 'text-emerald-400'" x-text="e.value.toFixed(2)"></td>
              <td class="px-5 py-3 text-center text-slate-400 font-mono text-xs" x-text="condLabel(e.condition)"></td>
              <td class="px-5 py-3 text-right text-slate-500 font-mono text-xs" x-text="e.threshold.toFixed(2)"></td>
            </tr>
          </template>
          <tr x-show="log.length === 0">
            <td colspan="6" class="px-5 py-8 text-center text-slate-600 font-mono text-sm">No alerts fired yet.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <!-- Add-alert modal -->
  <div x-data="{
          get filteredMetrics(){
            var q = ($store.ui.alertSearch||'').toLowerCase();
            return ($store.ui.alertMetrics||[]).filter(function(m){
              return m.name.toLowerCase().indexOf(q) >= 0;
            });
          }
       }"
       x-show="$store.ui.alertModal" x-cloak
       class="fixed inset-0 z-30 flex items-center justify-center bg-black/60"
       @keydown.escape.window="$store.ui.closeAlert()">
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-6 w-full max-w-lg shadow-xl"
         @click.outside="$store.ui.closeAlert()">

      <!-- Step 1: Pick metric -->
      <template x-if="$store.ui.alertStep === 1">
        <div>
          <h3 class="text-sm font-semibold mb-3">New Alert — select a metric</h3>
          <input type="text" placeholder="search metrics..." x-model="$store.ui.alertSearch" autofocus
                 class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
          <div class="mt-2 max-h-64 overflow-y-auto rounded-lg border border-slate-800 bg-slate-950">
            <template x-for="m in filteredMetrics" :key="m.name">
              <button type="button" @click="$store.ui.selectMetric(m.name)"
                      class="block w-full text-left text-sm font-mono px-3 py-2 hover:bg-slate-800 transition flex items-center justify-between">
                <span class="text-slate-300" x-text="m.name"></span>
                <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-500" x-text="m.type"></span>
              </button>
            </template>
            <div x-show="filteredMetrics.length === 0" class="px-3 py-4 text-center text-xs text-slate-600 font-mono">no matching metrics</div>
          </div>
          <div class="flex justify-end gap-2 mt-4">
            <button @click="$store.ui.closeAlert()" class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 hover:bg-slate-800">Cancel</button>
          </div>
        </div>
      </template>

      <!-- Step 2: Configure rule -->
      <template x-if="$store.ui.alertStep === 2">
        <div>
          <div class="flex items-center gap-2 mb-4">
            <button @click="$store.ui.alertStep=1" class="text-xs text-slate-500 hover:text-slate-300 transition">&larr; Back</button>
            <h3 class="text-sm font-semibold">Configure Alert</h3>
            <span class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-500" x-text="$store.ui.alertMetric"></span>
          </div>
          <form @submit.prevent="$store.ui.submitAlert()">
            <div class="mb-3">
              <label class="block text-xs text-slate-500 font-mono mb-1">Name</label>
              <input type="text" x-model="$store.ui.alertForm.name" required placeholder="e.g. High Error Rate"
                     class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
            </div>
            <div class="flex gap-3 mb-3">
              <div class="flex-1">
                <label class="block text-xs text-slate-500 font-mono mb-1">Condition</label>
                <select x-model="$store.ui.alertForm.condition"
                        class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
                  <option value="gt">&gt; Greater than</option>
                  <option value="gte">&ge; Greater or equal</option>
                  <option value="lt">&lt; Less than</option>
                  <option value="lte">&le; Less or equal</option>
                  <option value="eq">= Equal</option>
                </select>
              </div>
              <div class="w-32">
                <label class="block text-xs text-slate-500 font-mono mb-1">Threshold</label>
                <input type="number" step="any" x-model.number="$store.ui.alertForm.threshold" required placeholder="0"
                       class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
              </div>
            </div>
            <div class="mb-3">
              <label class="block text-xs text-slate-500 font-mono mb-1">Cooldown (e.g. 5m, 1h)</label>
              <input type="text" x-model="$store.ui.alertForm.cooldown" placeholder="5m"
                     class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
            </div>
            {{if .HasNotifier}}
            <div class="border-t border-slate-800 my-4"></div>
            <p class="text-xs text-slate-500 font-mono mb-3">Email Notification (optional)</p>
            <div class="mb-3">
              <label class="block text-xs text-slate-500 font-mono mb-1">To (comma-separated)</label>
              <input type="text" x-model="$store.ui.alertForm.email_to" placeholder="team@example.com"
                     class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
            </div>
            <div class="mb-3" x-show="$store.ui.alertTemplates.length > 0">
              <label class="block text-xs text-slate-500 font-mono mb-1">Format</label>
              <select x-model="$store.ui.alertForm.email_html"
                      class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
                <option value="false">Text</option>
                <option value="true">HTML</option>
              </select>
            </div>
            <div class="mb-3" x-show="$store.ui.alertForm.email_html === 'true' && $store.ui.alertTemplates.length > 0">
              <label class="block text-xs text-slate-500 font-mono mb-1">Template</label>
              <select @change="$store.ui.applyTemplate($event.target.value)"
                      class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 focus:outline-none focus:border-slate-600">
                <option value="">Custom (edit below)</option>
                <template x-for="t in $store.ui.alertTemplates" :key="t.id">
                  <option :value="t.id" x-text="t.name"></option>
                </template>
              </select>
            </div>
            <div class="mb-3">
              <label class="block text-xs text-slate-500 font-mono mb-1">Subject</label>
              <input type="text" x-model="$store.ui.alertForm.email_subject" placeholder="Alert: {{"{{.RuleName}}"}}"
                     class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600">
            </div>
            <div class="mb-4">
              <label class="block text-xs text-slate-500 font-mono mb-1">Body</label>
              <textarea x-model="$store.ui.alertForm.email_body" rows="4" placeholder="Custom alert message..."
                        class="w-full text-sm font-mono px-3 py-2 rounded-lg bg-slate-950 border border-slate-800 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-slate-600 resize-none"></textarea>
            </div>
            <details class="mb-4" x-show="$store.ui.alertForm.email_html === 'true'">
              <summary class="text-xs text-slate-500 font-mono cursor-pointer hover:text-slate-400 transition">Available template variables</summary>
              <div class="mt-2 bg-slate-950 border border-slate-800 rounded-lg p-3 text-[11px] font-mono text-slate-400 space-y-0.5">
                <div><span class="text-emerald-400">{{"{{.RuleName}}"}}</span> — rule name</div>
                <div><span class="text-emerald-400">{{"{{.RuleID}}"}}</span> — rule ID</div>
                <div><span class="text-emerald-400">{{"{{.Metric}}"}}</span> — metric name</div>
                <div><span class="text-emerald-400">{{"{{.Value}}"}}</span> — current value</div>
                <div><span class="text-emerald-400">{{"{{.Threshold}}"}}</span> — threshold</div>
                <div><span class="text-emerald-400">{{"{{.Condition}}"}}</span> — raw condition (gt, lt, gte, lte, eq)</div>
                <div><span class="text-emerald-400">{{"{{.ConditionLabel}}"}}</span> — human-readable (&gt;, &lt;, &gt;=, &lt;=, ==)</div>
                <div><span class="text-emerald-400">{{"{{.FiredAt}}"}}</span> — fire timestamp</div>
                <div><span class="text-emerald-400">{{"{{.ProjectName}}"}}</span> — project name</div>
              </div>
            </details>
            {{end}}
            <div class="flex justify-end gap-2">
              <button type="button" @click="$store.ui.closeAlert()"
                      class="text-sm px-3 py-1.5 rounded-lg border border-slate-800 hover:bg-slate-800">Cancel</button>
              <button type="submit"
                      class="text-sm px-3 py-1.5 rounded-lg bg-emerald-500/90 hover:bg-emerald-500 text-slate-950 font-medium">Create Alert</button>
            </div>
          </form>
        </div>
      </template>

    </div>
  </div>

  <!-- Toast -->
  <div x-show="toastVisible" x-cloak
       x-transition:enter="transition ease-out duration-200"
       x-transition:enter-start="opacity-0 translate-y-2"
       x-transition:enter-end="opacity-100 translate-y-0"
       x-transition:leave="transition ease-in duration-150"
       x-transition:leave-start="opacity-100"
       x-transition:leave-end="opacity-0"
       class="fixed bottom-6 right-6 z-40 max-w-sm px-4 py-2.5 rounded-lg text-sm font-mono shadow-lg border"
       :class="toastType === 'success' ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-400' : 'bg-rose-500/10 border-rose-500/30 text-rose-400'"
       x-text="toastMsg">
  </div>

</main>

<script>
  function applyTheme(t){
    var c = document.documentElement.classList;
    c.toggle('light', t === 'light');
    c.toggle('dark', t !== 'light');
    try { localStorage.setItem('golens.theme', t); } catch(e){}
    var btn = document.getElementById('theme-toggle');
    if (btn) btn.textContent = (t === 'light') ? '☀️' : '🌙';
  }
  function toggleTheme(){
    var light = document.documentElement.classList.contains('light');
    applyTheme(light ? 'dark' : 'light');
  }
  document.addEventListener('DOMContentLoaded', function(){
    var t = 'dark'; try { t = localStorage.getItem('golens.theme') || 'dark'; } catch(e){}
    applyTheme(t);
  });

  // Alpine store for alert modal (same as dashboard)
  document.addEventListener('alpine:init', () => {
    Alpine.store('ui', {
      alertModal: false,
      alertStep: 1,
      alertMetric: '',
      alertSearch: '',
      alertMetrics: [],
      alertTemplates: [],
      alertForm: { name:'', condition:'gt', threshold:0, cooldown:'5m', email_to:'', email_subject:'', email_body:'', email_html:'false' },

      openAlert(){
        this.alertStep = 1;
        this.alertMetric = '';
        this.alertSearch = '';
        this.alertForm = { name:'', condition:'gt', threshold:0, cooldown:'5m', email_to:'', email_subject:'', email_body:'', email_html:'false' };
        var self = this;
        fetch('/metrics/data', {headers:{'Accept':'application/json'}})
          .then(function(r){ return r.json(); })
          .then(function(d){ self.alertMetrics = (d||[]).map(function(s){ return {name:s.name, type:s.type}; }); })
          .catch(function(){});
        fetch('/metrics/alerts/templates', {headers:{'Accept':'application/json'}})
          .then(function(r){ return r.json(); })
          .then(function(d){ self.alertTemplates = d || []; })
          .catch(function(){ self.alertTemplates = []; });
        this.alertModal = true;
      },

      selectMetric(name){
        this.alertMetric = name;
        this.alertForm.name = name + ' alert';
        this.alertStep = 2;
      },

      closeAlert(){
        this.alertModal = false;
        this.alertStep = 1;
      },

      applyTemplate(id){
        if (!id) return;
        var t = this.alertTemplates.find(function(x){ return x.id === id; });
        if (t) {
          this.alertForm.email_subject = t.subject;
          this.alertForm.email_body = t.body;
        }
      },

      submitAlert(){
        var body = {
          name: this.alertForm.name,
          metric: this.alertMetric,
          condition: this.alertForm.condition,
          threshold: this.alertForm.threshold,
          cooldown: this.alertForm.cooldown,
          enabled: true
        };
        var hasNotifier = {{if .HasNotifier}}true{{else}}false{{end}};
        if (hasNotifier) {
          body.email_to = this.alertForm.email_to ? this.alertForm.email_to.split(',').map(function(s){ return s.trim(); }).filter(Boolean) : [];
          body.email_subject = this.alertForm.email_subject;
          body.email_body = this.alertForm.email_body;
          body.email_html = this.alertForm.email_html === 'true';
        }
        var self = this;
        fetch('/metrics/alerts/rules', {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify(body)
        }).then(function(r){
          if (!r.ok) return r.json().then(function(e){ throw new Error(e.error || 'Server error'); });
          return r.json();
        }).then(function(rule){
          self.closeAlert();
          document.dispatchEvent(new CustomEvent('alert:created', { detail: { metric: self.alertMetric } }));
        }).catch(function(e){
          document.dispatchEvent(new CustomEvent('alert:error', { detail: { message: e.message || 'Failed to create alert' } }));
        });
      }
    });
  });

  // MutationObserver to open alert modal from header button
  document.addEventListener('DOMContentLoaded', function(){
    var moAlert = new MutationObserver(function(){
      if (document.body.dataset.alertAdd === '1') {
        Alpine.store('ui').openAlert();
        delete document.body.dataset.alertAdd;
      }
    });
    moAlert.observe(document.body, { attributes: true, attributeFilter: ['data-alert-add'] });
  });

  function alertsApp(){
    return {
      rules: [],
      log: [],
      toastMsg: '',
      toastType: 'error',
      toastVisible: false,
      _toastTimer: null,
      form: {
        name: '', metric: '', condition: 'gt', threshold: 0,
        cooldown: '5m', email_to: '', email_subject: '',
        email_body: '', email_html: 'false'
      },

      init(){
        this.refreshRules();
        this.refreshLog();
        // Auto-refresh log every 10s
        setInterval(() => this.refreshLog(), 10000);
        // Listen for alert creation from the modal
        var self = this;
        document.addEventListener('alert:created', function(e){
          self.refreshRules();
          self.showToast('Alert created for ' + (e.detail && e.detail.metric || 'metric'), 'success');
        });
        document.addEventListener('alert:error', function(e){
          self.showToast((e.detail && e.detail.message) || 'Failed to create alert');
        });
      },

      showToast(msg, type){
        this.toastMsg = msg;
        this.toastType = type || 'error';
        this.toastVisible = true;
        if (this._toastTimer) clearTimeout(this._toastTimer);
        this._toastTimer = setTimeout(() => { this.toastVisible = false; }, 4000);
      },

      refreshRules(){
        fetch('/metrics/alerts/rules', {headers:{'Accept':'application/json'}})
          .then(r => { if (!r.ok) throw new Error(r.statusText); return r.json(); })
          .then(d => { this.rules = d || []; })
          .catch(() => { this.showToast('Failed to load rules'); });
      },

      refreshLog(){
        fetch('/metrics/alerts/log', {headers:{'Accept':'application/json'}})
          .then(r => { if (!r.ok) throw new Error(r.statusText); return r.json(); })
          .then(d => { this.log = d || []; })
          .catch(() => { this.showToast('Failed to load alert log'); });
      },

      deleteRule(id){
        fetch('/metrics/alerts/rules/' + id, {method:'DELETE'})
          .then(r => {
            if (!r.ok) throw new Error('Delete failed');
            this.rules = this.rules.filter(r => r.id !== id);
            this.showToast('Rule deleted', 'success');
          })
          .catch(e => { this.showToast(e.message || 'Failed to delete rule'); });
      },

      toggleRule(r){
        var prev = r.enabled;
        var updated = Object.assign({}, r, {enabled: !r.enabled});
        fetch('/metrics/alerts/rules', {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify(updated)
        }).then(resp => {
          if (!resp.ok) throw new Error('Toggle failed');
          r.enabled = !r.enabled;
        })
          .catch(e => {
            r.enabled = prev;
            this.showToast(e.message || 'Failed to toggle rule');
          });
      },

      condLabel(c){
        return {'gt':'>','gte':'>=','lt':'<','lte':'<=','eq':'='}[c] || c;
      },

      fmtDur(d){
        if (!d) return '—';
        if (typeof d === 'string') return d;
        // Go duration JSON: nanoseconds as number
        var s = Math.round(d / 1e9);
        if (s < 60) return s + 's';
        if (s < 3600) return Math.floor(s/60) + 'm' + (s%60 ? (s%60)+'s' : '');
        return Math.floor(s/3600) + 'h' + Math.floor((s%3600)/60) + 'm';
      },

      fmtTime(t){
        if (!t) return '—';
        var d = new Date(t);
        return d.toLocaleTimeString('en-US', {hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false});
      },

      timeAgo(t){
        if (!t) return '—';
        var d = new Date(t);
        var s = Math.round((Date.now() - d.getTime()) / 1000);
        if (s < 60) return s + 's ago';
        if (s < 3600) return Math.floor(s/60) + 'm ago';
        if (s < 86400) return Math.floor(s/3600) + 'h ago';
        return Math.floor(s/86400) + 'd ago';
      }
    };
  }
</script>
</body>
</html>`

var alertsPageTpl = template.Must(template.New("alerts").Parse(alertsPageSrc))
