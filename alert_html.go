package golens

import "html/template"

// alertsPageSrc is the alerts dashboard. Markup uses the semantic classes
// defined in ui/static/styles.css; behavior lives in ui/static/{common,alerts}.js
// and the pinned Alpine build, all served from /metrics/static/. The theme
// guard stays inline because it must run before first paint.
const alertsPageSrc = `<!DOCTYPE html>
<html lang="en" x-data="{}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" type="image/x-icon" href="/favicon.ico">
<title>{{.ProjectName}} Alerts</title>
<link rel="stylesheet" href="/metrics/static/styles.css">
<script>
  window.GoLensConfig = { hasNotifier: {{if .HasNotifier}}true{{else}}false{{end}} };
</script>
<script>
  // Apply the saved theme before paint to avoid a flash of the wrong theme.
  (function(){
    var t = 'dark';
    try { t = localStorage.getItem('golens.theme') || 'dark'; } catch(e){}
    var c = document.documentElement.classList;
    c.add(t === 'light' ? 'light' : 'dark');
    c.remove(t === 'light' ? 'dark' : 'light');
  })();
</script>
<script src="/metrics/static/common.js" defer></script>
<script src="/metrics/static/alerts.js" defer></script>
<script defer src="/metrics/static/alpine.min.js"></script>
</head>
<body>

<!-- Header -->
<header class="topbar">
  <div class="wrap topbar-inner row">
    <div class="brand grow">
      <span class="brand-dot" id="brand-dot"></span>
      <h1 class="brand-title">{{.ProjectName}}</h1>
      <span class="brand-sub">{{.DashboardSubtitle}}</span>
      {{if .HasNotifier}}
      <span class="status-pill status-pill-ok"><span class="status-dot status-dot-ok"></span> mailer connected</span>
      {{else}}
      <span class="status-pill"><span class="status-dot"></span> no mailer</span>
      {{end}}
    </div>
    <a href="/metrics" class="btn">&larr; Dashboard</a>
    <button onclick="document.body.dataset.alertAdd='1'" class="btn">+ Add Rule</button>
    <button id="theme-toggle" title="toggle theme" onclick="toggleTheme()" class="btn btn-icon">🌙</button>
  </div>
</header>

<main class="wrap stack" x-data="alertsApp()" x-init="init()">

  <!-- Alert Rules -->
  <section class="stack">
    <div class="row">
      <h2 class="section-title">Alert Rules</h2>
      <span class="meta-faint" x-text="'· ' + rules.length + ' rules'"></span>
    </div>
    <div class="data-card">
      <div class="table-scroll">
        <table class="data-table data-table-page">
          <thead>
            <tr>
              <th>Status</th>
              <th>Name</th>
              <th>Metric</th>
              <th>Condition</th>
              <th class="cell-num">Threshold</th>
              <th class="cell-num">Cooldown</th>
              <th class="cell-num">Last Fired</th>
              <th class="cell-num">Actions</th>
            </tr>
          </thead>
          <tbody>
            <template x-for="r in rules" :key="r.id">
              <tr>
                <td>
                  <button type="button" @click="toggleRule(r)" class="check-switch" role="switch"
                          :aria-checked="r.enabled ? 'true' : 'false'" :aria-label="r.name"
                          :class="r.enabled ? 'check-switch-on' : ''">
                    <span class="check-knob" :class="r.enabled ? 'check-knob-on' : ''"></span>
                  </button>
                </td>
                <td class="cell-strong" x-text="r.name"></td>
                <td class="cell-mono" x-text="r.metric"></td>
                <td class="cell-dim" x-text="condLabel(r.condition)"></td>
                <td class="cell-num" x-text="r.threshold"></td>
                <td class="cell-dim" x-text="fmtDur(r.cooldown)"></td>
                <td class="cell-dim" x-text="r.last_fired ? timeAgo(r.last_fired) : '—'"></td>
                <td class="cell-num">
                  <!-- Two-phase delete: first click asks, second click fires. -->
                  <button @click="deleteRule(r.id)" class="btn btn-danger-hover"
                          x-text="confirmId === r.id ? 'Confirm delete' : 'Delete'"></button>
                </td>
              </tr>
            </template>
            <tr x-show="rules.length === 0">
              <td colspan="8" class="cell-empty">No alert rules configured. Click "+ Add Rule" to create one.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>

  <!-- Alert Log -->
  <section class="stack">
    <div class="row">
      <h2 class="section-title">Alert Log</h2>
      <span class="meta-faint" x-text="'· ' + log.length + ' events'"></span>
      <button @click="refreshLog()" class="btn btn-link grow">Refresh</button>
      <label class="pause-toggle">
        <input type="checkbox" class="check" x-model="paused"> pause
      </label>
    </div>
    <div class="data-card">
      <div class="table-scroll">
        <table class="data-table data-table-page">
          <thead>
            <tr>
              <th>Time</th>
              <th>Rule</th>
              <th>Metric</th>
              <th class="cell-num">Value</th>
              <th class="cell-center">Condition</th>
              <th class="cell-num">Threshold</th>
            </tr>
          </thead>
          <tbody>
            <template x-for="e in log" :key="e.id">
              <tr>
                <td class="cell-dim" x-text="fmtTime(e.fired_at)"></td>
                <td class="cell-strong" x-text="e.rule_name"></td>
                <td class="cell-mono" x-text="e.metric"></td>
                <td class="cell-num" :class="e.value > e.threshold ? 'cell-rose' : 'cell-ok'" x-text="e.value.toFixed(2)"></td>
                <td class="cell-center cell-dim" x-text="condLabel(e.condition)"></td>
                <td class="cell-dim" x-text="e.threshold.toFixed(2)"></td>
              </tr>
            </template>
            <tr x-show="log.length === 0">
              <td colspan="6" class="cell-empty">No alerts fired yet.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>

  <!-- Add-alert modal (Alpine): step 1 = pick metric, step 2 = configure rule -->
  <div x-data="{
          get filteredMetrics(){
            var q = ($store.ui.alertSearch||'').toLowerCase();
            return ($store.ui.alertMetrics||[]).filter(function(m){
              return m.name.toLowerCase().indexOf(q) >= 0;
            });
          }
       }"
       x-show="$store.ui.alertModal" x-cloak
       class="modal-overlay"
       @keydown.escape.window="$store.ui.closeAlert()">
    <div class="modal modal-lg" @click.outside="$store.ui.closeAlert()">

      <!-- Step 1: Pick metric -->
      <template x-if="$store.ui.alertStep === 1">
        <div class="stack">
          <h3 class="modal-title">New Alert — select a metric</h3>
          <input type="text" placeholder="search metrics..." x-model="$store.ui.alertSearch" autofocus class="input">
          <div class="modal-list">
            <template x-for="m in filteredMetrics" :key="m.name">
              <button type="button" @click="$store.ui.selectMetric(m.name)" class="modal-item">
                <span x-text="m.name"></span>
                <span class="badge" x-text="m.type"></span>
              </button>
            </template>
            <div x-show="filteredMetrics.length === 0" class="modal-empty">no matching metrics</div>
          </div>
          <div class="modal-footer">
            <button @click="$store.ui.closeAlert()" class="btn">Cancel</button>
          </div>
        </div>
      </template>

      <!-- Step 2: Configure rule -->
      <template x-if="$store.ui.alertStep === 2">
        <div>
          <div class="row-tight form-row">
            <button @click="$store.ui.alertStep=1" class="modal-back">&larr; Back</button>
            <h3 class="modal-title">Configure Alert</h3>
            <span class="badge" x-text="$store.ui.alertMetric"></span>
          </div>
          <form @submit.prevent="$store.ui.submitAlert()">
            <div class="form-row">
              <label class="field-label">Name</label>
              <input type="text" x-model="$store.ui.alertForm.name" required placeholder="e.g. High Error Rate" class="input">
            </div>
            <div class="form-cols">
              <div class="field">
                <label class="field-label">Condition</label>
                <select x-model="$store.ui.alertForm.condition" class="input">
                  <option value="gt">&gt; Greater than</option>
                  <option value="gte">&ge; Greater or equal</option>
                  <option value="lt">&lt; Less than</option>
                  <option value="lte">&le; Less or equal</option>
                  <option value="eq">= Equal</option>
                </select>
              </div>
              <div class="field field-narrow">
                <label class="field-label">Threshold</label>
                <input type="number" step="any" x-model.number="$store.ui.alertForm.threshold" required placeholder="0" class="input">
              </div>
            </div>
            <div class="form-row">
              <label class="field-label">Cooldown (number + ms|s|m|h, e.g. 5m)</label>
              <input type="text" x-model="$store.ui.alertForm.cooldown" pattern="[0-9]+(ms|s|m|h)" title="Duration: a number followed by ms, s, m, or h" placeholder="5m" class="input">
            </div>
            {{if .HasNotifier}}
            <hr class="form-sep">
            <p class="form-note">Email Notification (optional)</p>
            <div class="form-row">
              <label class="field-label">To (comma-separated)</label>
              <input type="text" x-model="$store.ui.alertForm.email_to" placeholder="team@example.com" class="input">
            </div>
            <div class="form-row" x-show="$store.ui.alertTemplates.length > 0">
              <label class="field-label">Format</label>
              <select x-model="$store.ui.alertForm.email_html" class="input">
                <option value="false">Text</option>
                <option value="true">HTML</option>
              </select>
            </div>
            <div class="form-row" x-show="$store.ui.alertForm.email_html === 'true' && $store.ui.alertTemplates.length > 0">
              <label class="field-label">Template</label>
              <select @change="$store.ui.applyTemplate($event.target.value)" class="input">
                <option value="">Custom (edit below)</option>
                <template x-for="t in $store.ui.alertTemplates" :key="t.id">
                  <option :value="t.id" x-text="t.name"></option>
                </template>
              </select>
            </div>
            <div class="form-row">
              <label class="field-label">Subject</label>
              <input type="text" x-model="$store.ui.alertForm.email_subject" placeholder="Alert: {{"{{.RuleName}}"}}" class="input">
            </div>
            <div class="form-row">
              <label class="field-label">Body</label>
              <textarea x-model="$store.ui.alertForm.email_body" rows="4" placeholder="Custom alert message..." class="input"></textarea>
            </div>
            <details class="tpl-help" x-show="$store.ui.alertForm.email_html === 'true'">
              <summary>Available template variables</summary>
              <div class="tpl-vars">
                <div><span class="tpl-var-name">{{"{{.RuleName}}"}}</span> — rule name</div>
                <div><span class="tpl-var-name">{{"{{.RuleID}}"}}</span> — rule ID</div>
                <div><span class="tpl-var-name">{{"{{.Metric}}"}}</span> — metric name</div>
                <div><span class="tpl-var-name">{{"{{.Value}}"}}</span> — current value</div>
                <div><span class="tpl-var-name">{{"{{.Threshold}}"}}</span> — threshold</div>
                <div><span class="tpl-var-name">{{"{{.Condition}}"}}</span> — raw condition (gt, lt, gte, lte, eq)</div>
                <div><span class="tpl-var-name">{{"{{.ConditionLabel}}"}}</span> — human-readable (&gt;, &lt;, &gt;=, &lt;=, ==)</div>
                <div><span class="tpl-var-name">{{"{{.FiredAt}}"}}</span> — fire timestamp</div>
                <div><span class="tpl-var-name">{{"{{.ProjectName}}"}}</span> — project name</div>
              </div>
            </details>
            {{end}}
            <div class="modal-footer">
              <button type="button" @click="$store.ui.closeAlert()" class="btn">Cancel</button>
              <button type="submit" class="btn-primary">Create Alert</button>
            </div>
          </form>
        </div>
      </template>

    </div>
  </div>

  <!-- Toast (driven by common.js showToast) -->
  <div id="app-toast" class="toast" aria-live="polite"></div>

</main>

</body>
</html>`

var alertsPageTpl = template.Must(template.New("alerts").Parse(alertsPageSrc))
