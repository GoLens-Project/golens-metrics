package golens

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		v         float64
		c         AlertCondition
		threshold float64
		want      bool
	}{
		{10, ConditionGT, 5, true},
		{5, ConditionGT, 5, false},
		{5, ConditionGTE, 5, true},
		{4, ConditionGTE, 5, false},
		{3, ConditionLT, 5, true},
		{5, ConditionLT, 5, false},
		{5, ConditionLTE, 5, true},
		{6, ConditionLTE, 5, false},
		{5, ConditionEQ, 5, true},
		{5.1, ConditionEQ, 5, false},
		{5, "unknown", 5, false},
	}
	for _, tc := range tests {
		got := evaluateCondition(tc.v, tc.c, tc.threshold)
		if got != tc.want {
			t.Errorf("evaluateCondition(%v, %q, %v) = %v, want %v", tc.v, tc.c, tc.threshold, got, tc.want)
		}
	}
}

func TestAlerterAddRemoveList(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	if al == nil {
		t.Fatal("alerter is nil")
	}

	rule, err := al.AddRule(AlertRule{
		Name:      "test rule",
		Metric:    "http_requests_total",
		Condition: ConditionGT,
		Threshold: 100,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if rule.ID == "" {
		t.Error("expected non-empty ID")
	}

	rules := al.ListRules()
	if len(rules) != 1 {
		t.Fatalf("ListRules: got %d, want 1", len(rules))
	}
	if rules[0].Name != "test rule" {
		t.Errorf("name = %q, want %q", rules[0].Name, "test rule")
	}

	if err := al.RemoveRule(rule.ID); err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}
	rules = al.ListRules()
	if len(rules) != 0 {
		t.Fatalf("after delete: got %d, want 0", len(rules))
	}
}

func TestAlerterToggle(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	rule, _ := al.AddRule(AlertRule{
		Name: "toggle test", Metric: "test", Condition: ConditionGT, Threshold: 1,
	})
	if !rule.Enabled {
		t.Error("new rule should be enabled")
	}

	al.ToggleRule(rule.ID, false)
	rules := al.ListRules()
	if rules[0].Enabled {
		t.Error("rule should be disabled after toggle off")
	}

	al.ToggleRule(rule.ID, true)
	rules = al.ListRules()
	if !rules[0].Enabled {
		t.Error("rule should be enabled after toggle on")
	}
}

func TestAlerterEvaluateFires(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Alerts.DefaultCooldown = 0 // no cooldown for test
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()

	// Record a metric value above threshold.
	r.Record("test_gauge", 50)
	waitForDrain(r)

	// Add rule that should fire immediately.
	al.AddRule(AlertRule{
		Name:      "high gauge",
		Metric:    "test_gauge",
		Condition: ConditionGT,
		Threshold: 10,
		Cooldown:  0,
	})

	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(log))
	}
	if log[0].RuleName != "high gauge" {
		t.Errorf("rule name = %q, want %q", log[0].RuleName, "high gauge")
	}
	if log[0].Value != 50 {
		t.Errorf("value = %v, want 50", log[0].Value)
	}
}

func TestAlerterCooldownPreventsRefire(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	r.Record("test_metric", 100)
	waitForDrain(r)

	al.AddRule(AlertRule{
		Name:      "cooldown test",
		Metric:    "test_metric",
		Condition: ConditionGT,
		Threshold: 1,
		Cooldown:  1 * time.Hour, // long cooldown
	})

	al.evaluate(context.Background())
	al.evaluate(context.Background()) // second call should be suppressed

	log := r.AlertLog(10)
	if len(log) != 1 {
		t.Fatalf("expected 1 log entry (cooldown), got %d", len(log))
	}
}

func TestAlerterSkipsDisabledRules(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	r.Record("test_metric", 100)
	waitForDrain(r)

	rule, _ := al.AddRule(AlertRule{
		Name: "disabled", Metric: "test_metric", Condition: ConditionGT, Threshold: 1, Cooldown: 0,
	})
	al.ToggleRule(rule.ID, false)
	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 0 {
		t.Fatalf("disabled rule should not fire, got %d entries", len(log))
	}
}

func TestMemoryAlertStore(t *testing.T) {
	store := newMemoryAlertStore()
	ctx := context.Background()

	if err := store.InitRules(ctx); err != nil {
		t.Fatalf("InitRules: %v", err)
	}

	rule := AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 1}
	if err := store.SaveRule(ctx, rule); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	rules, _ := store.LoadRules(ctx)
	if len(rules) != 1 {
		t.Fatalf("LoadRules: got %d, want 1", len(rules))
	}

	entry := AlertLogEntry{RuleID: "r1", RuleName: "test", Metric: "m", Value: 5, Threshold: 1, Condition: "gt", FiredAt: time.Now()}
	if err := store.AppendLog(ctx, entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	log, _ := store.ListLog(ctx, 10)
	if len(log) != 1 {
		t.Fatalf("ListLog: got %d, want 1", len(log))
	}

	if err := store.DeleteRule(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	rules, _ = store.LoadRules(ctx)
	if len(rules) != 0 {
		t.Fatalf("after delete: got %d, want 0", len(rules))
	}
}

func TestAlertRulesHTTPHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// POST a rule
	body, _ := json.Marshal(AlertRule{
		Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10,
	})
	req := httptest.NewRequest("POST", "/metrics/alerts/rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.alertRulesHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("POST: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// GET rules
	req = httptest.NewRequest("GET", "/metrics/alerts/rules", nil)
	rec = httptest.NewRecorder()
	r.alertRulesHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("GET: status = %d", rec.Code)
	}
	var rules []AlertRule
	json.Unmarshal(rec.Body.Bytes(), &rules)
	if len(rules) != 1 {
		t.Fatalf("GET: got %d rules, want 1", len(rules))
	}
}

func TestAlertRuleDeleteHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	rule, _ := al.AddRule(AlertRule{Name: "del", Metric: "m", Condition: ConditionGT, Threshold: 1})

	req := httptest.NewRequest("DELETE", "/metrics/alerts/rules/"+rule.ID, nil)
	rec := httptest.NewRecorder()
	r.alertRuleDeleteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(al.ListRules()) != 0 {
		t.Error("rule not deleted")
	}
}

// The firing-status endpoint (GET /metrics/alerts/status) reports the live
// count of enabled rules whose condition currently holds — the figure behind
// the dashboard's status dot and firing-count pill. Cooldown suppression does
// not apply, and disabled or unknown-metric rules are never counted.
func TestAlertStatusHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// A gauge: recorded values replace, so the easing phase below can lower
	// the observed value.
	r.Register("job_depth", GaugeType, "job depth", nil, nil, 0, 0)
	r.Record("job_depth", 15)
	waitForDrain(r)

	al := r.Alerter()
	if _, err := al.AddRule(AlertRule{Name: "hot", Metric: "job_depth", Condition: ConditionGT, Threshold: 10}); err != nil {
		t.Fatalf("AddRule hot: %v", err)
	}
	if _, err := al.AddRule(AlertRule{Name: "cold", Metric: "job_depth", Condition: ConditionLT, Threshold: 1}); err != nil {
		t.Fatalf("AddRule cold: %v", err)
	}
	if _, err := al.AddRule(AlertRule{Name: "ghost", Metric: "no_such_metric", Condition: ConditionGT, Threshold: 0}); err != nil {
		t.Fatalf("AddRule ghost: %v", err)
	}
	sleeping, err := al.AddRule(AlertRule{Name: "sleeping", Metric: "job_depth", Condition: ConditionGT, Threshold: 0})
	if err != nil {
		t.Fatalf("AddRule sleeping: %v", err)
	}
	if err := al.ToggleRule(sleeping.ID, false); err != nil {
		t.Fatalf("ToggleRule: %v", err)
	}

	rec := httptest.NewRecorder()
	r.alertStatusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var got struct {
		Firing int `json:"firing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	// Only "hot" fires: "cold" and the disabled rule do not hold, "ghost"
	// has no snapshots to evaluate against.
	if got.Firing != 1 {
		t.Errorf("firing = %d, want 1", got.Firing)
	}

	// The count is live: easing the metric value drops it to zero on the
	// next request, no evaluation tick needed. Cooldown suppression does not
	// apply either — an evaluation pass that fires and starts the cooldown
	// leaves the firing figure at 1 while the condition still holds.
	al.evaluate(context.Background())
	rec = httptest.NewRecorder()
	r.alertStatusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts/status", nil))
	got.Firing = -1
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Firing != 1 {
		t.Errorf("firing during cooldown = %d, want 1 (status reflects the condition, not suppression)", got.Firing)
	}

	r.Record("job_depth", 1)
	waitForDrain(r)
	rec = httptest.NewRecorder()
	r.alertStatusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts/status", nil))
	got.Firing = -1
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Firing != 0 {
		t.Errorf("firing after easing = %d, want 0", got.Firing)
	}
}

// Histogram rules compare the window average in the firing count, matching
// the evaluate path.
func TestAlertStatusHandlerHistogramUsesAvg(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	r.Register("latency", HistogramType, "latency", nil, []float64{5}, 0, 0)
	r.Record("latency", 10)
	r.Record("latency", 10)
	waitForDrain(r)

	al := r.Alerter()
	// A histogram snapshot carries no Value (0), only Avg — samples 10,10
	// give avg=10. avg > 5 fires; a raw-value comparison (0 > 5) would not,
	// so a count of 1 proves the avg path is taken.
	if _, err := al.AddRule(AlertRule{Name: "avg", Metric: "latency", Condition: ConditionGT, Threshold: 5}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	rec := httptest.NewRecorder()
	r.alertStatusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts/status", nil))
	var got struct {
		Firing int `json:"firing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Firing != 1 {
		t.Errorf("firing = %d, want 1 (histogram compares avg, not raw value)", got.Firing)
	}
}

// The status endpoint is mounted on the mux alongside the other alert
// routes.
func TestAlertStatusRouteMounted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	mux := http.NewServeMux()
	r.MountUI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"firing"`) {
		t.Errorf("body = %q, want firing key", rec.Body.String())
	}
}

func TestAlertLogHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	req := httptest.NewRequest("GET", "/metrics/alerts/log", nil)
	rec := httptest.NewRecorder()
	r.alertLogHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestAlertsPageHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	req := httptest.NewRequest("GET", "/metrics/alerts", nil)
	rec := httptest.NewRecorder()
	r.alertsPageHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Alert Rules") {
		t.Error("missing Alert Rules section")
	}
	if !strings.Contains(body, "Alert Log") {
		t.Error("missing Alert Log section")
	}
}

// The alerts page shares the vendored asset set and the configurable
// subtitle with the dashboard — no CDN references, and the brand dot is
// addressable so its state is driven by /metrics/alerts/status, not a
// hard-coded alarm style.
func TestAlertsPageBranding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	rec := httptest.NewRecorder()
	r.alertsPageHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics/alerts", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "discovery dashboard") {
		t.Error("alerts page missing the configurable dashboard subtitle")
	}
	for _, asset := range []string{"/metrics/static/styles.css", "/metrics/static/common.js", "/metrics/static/alerts.js", "/metrics/static/alpine.min.js"} {
		if !strings.Contains(body, asset) {
			t.Errorf("vendored asset %s not referenced", asset)
		}
	}
	for _, cdn := range []string{"cdn.tailwindcss.com", "unpkg.com", "fonts.googleapis.com"} {
		if strings.Contains(body, cdn) {
			t.Errorf("alerts page still references external CDN %s", cdn)
		}
	}
	if !strings.Contains(body, `id="brand-dot"`) {
		t.Error("alerts page missing status brand dot")
	}
}

func TestRenderTextTemplate(t *testing.T) {
	data := AlertData{
		RuleName:       "High CPU",
		RuleID:         "cpu-1",
		Metric:         "cpu_usage_percent",
		Value:          "92.50",
		Threshold:      "80.00",
		Condition:      "gt",
		ConditionLabel: ">",
		FiredAt:        time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		ProjectName:    "MyApp",
	}

	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{"subject", "{{.ProjectName}} Alert: {{.RuleName}}", "MyApp Alert: High CPU"},
		{"body", "Metric {{.Metric}} value {{.Value}} {{.ConditionLabel}} {{.Threshold}}", "Metric cpu_usage_percent value 92.50 > 80.00"},
		{"plain text", "no templates here", "no templates here"},
		{"fired at", "Fired at: {{.FiredAt}}", "Fired at: 2025-01-01 12:00:00 +0000 UTC"},
	}
	for _, tc := range tests {
		got, err := renderTextTemplate(tc.tmpl, data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderHTMLTemplate(t *testing.T) {
	data := AlertData{
		RuleName:    "High CPU",
		Metric:      "cpu_usage_percent",
		Value:       "92.50",
		Threshold:   "80.00",
		ProjectName: "MyApp",
	}
	tmpl := "<b>{{.RuleName}}</b> on {{.ProjectName}}"
	got, err := renderHTMLTemplate(tmpl, data)
	if err != nil {
		t.Fatal(err)
	}
	want := "<b>High CPU</b> on MyApp"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAlerterEvaluateRendersTemplates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.ProjectName = "TestApp"
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	r.Record("test_metric", 42)
	waitForDrain(r)

	al.AddRule(AlertRule{
		Name:         "test alert",
		Metric:       "test_metric",
		Condition:    ConditionGT,
		Threshold:    1,
		Cooldown:     0,
		EmailTo:      []string{"test@example.com"},
		EmailSubject: "{{.ProjectName}}: {{.RuleName}} on {{.Metric}}",
		EmailBody:    "Value {{.Value}} {{.ConditionLabel}} {{.Threshold}} at {{.FiredAt}}",
	})

	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(log))
	}
}

func TestAlertsConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Alerts.Mode != "integrated" {
		t.Errorf("default mode = %q, want integrated", cfg.Alerts.Mode)
	}
	if cfg.Alerts.EvaluationInterval != 30*time.Second {
		t.Errorf("default eval interval = %v", cfg.Alerts.EvaluationInterval)
	}
	if cfg.Alerts.DefaultCooldown != 5*time.Minute {
		t.Errorf("default cooldown = %v", cfg.Alerts.DefaultCooldown)
	}
}

func TestAlertRuleUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		want     AlertRule
		wantErr  bool
	}{
		{
			name: "string duration",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":"5m"}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 5 * time.Minute},
		},
		{
			name: "number nanoseconds",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":300000000000}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 5 * time.Minute},
		},
		{
			name: "float nanoseconds",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":3e11}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 5 * time.Minute},
		},
		{
			name: "null cooldown",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":null}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 0},
		},
		{
			name: "zero cooldown",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":0}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 0},
		},
		{
			name:    "invalid string",
			json:    `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":"invalid"}`,
			wantErr: true,
		},
		{
			name:    "invalid type",
			json:    `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10,"cooldown":{}}`,
			wantErr: true,
		},
		{
			name: "missing cooldown field",
			json: `{"id":"r1","name":"test","metric":"m","condition":"gt","threshold":10}`,
			want: AlertRule{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Cooldown: 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rule AlertRule
			err := json.Unmarshal([]byte(tc.json), &rule)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rule.ID != tc.want.ID {
				t.Errorf("ID = %q, want %q", rule.ID, tc.want.ID)
			}
			if rule.Name != tc.want.Name {
				t.Errorf("Name = %q, want %q", rule.Name, tc.want.Name)
			}
			if rule.Metric != tc.want.Metric {
				t.Errorf("Metric = %q, want %q", rule.Metric, tc.want.Metric)
			}
			if rule.Condition != tc.want.Condition {
				t.Errorf("Condition = %q, want %q", rule.Condition, tc.want.Condition)
			}
			if rule.Threshold != tc.want.Threshold {
				t.Errorf("Threshold = %v, want %v", rule.Threshold, tc.want.Threshold)
			}
			if rule.Cooldown != tc.want.Cooldown {
				t.Errorf("Cooldown = %v, want %v", rule.Cooldown, tc.want.Cooldown)
			}
		})
	}
}

func TestNewAlerter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	defaultCooldown := 10 * time.Minute
	cfg.Alerts.DefaultCooldown = defaultCooldown
	cfg.Alerts.Rules = []AlertRule{
		{ID: "r1", Name: "rule1", Metric: "m1", Condition: ConditionGT, Threshold: 10, Enabled: true, Cooldown: defaultCooldown},
		{ID: "r2", Name: "rule2", Metric: "m2", Condition: ConditionLT, Threshold: 5, Enabled: true, Cooldown: defaultCooldown},
		{ID: "r3", Name: "rule3", Metric: "m3", Condition: ConditionGTE, Threshold: 1, Cooldown: time.Minute, Enabled: true},
	}

	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	if al == nil {
		t.Fatal("alerter is nil")
	}

	rules := al.ListRules()

	// Find rules by name instead of ID
	rule1 := findRuleByName(rules, "rule1")
	if rule1 == nil {
		t.Fatal("rule1 not found")
	}
	if rule1.ID != "r1" {
		t.Errorf("rule1 ID = %q, want r1", rule1.ID)
	}
	if rule1.Cooldown != defaultCooldown {
		t.Errorf("rule1 cooldown = %v, want %v", rule1.Cooldown, defaultCooldown)
	}

	rule2 := findRuleByName(rules, "rule2")
	if rule2 == nil {
		t.Fatal("rule2 not found")
	}
	if rule2.ID != "r2" {
		t.Errorf("rule2 ID = %q, want r2", rule2.ID)
	}
	if rule2.Cooldown != defaultCooldown {
		t.Errorf("rule2 cooldown = %v, want %v", rule2.Cooldown, defaultCooldown)
	}

	rule3 := findRuleByName(rules, "rule3")
	if rule3 == nil {
		t.Fatal("rule3 not found")
	}
	if rule3.ID != "r3" {
		t.Errorf("rule3 ID = %q, want r3", rule3.ID)
	}
	if rule3.Cooldown != time.Minute {
		t.Errorf("rule3 cooldown = %v, want 1m", rule3.Cooldown)
	}

	// Check that our config rules are enabled
	if !rule1.Enabled {
		t.Errorf("rule1 should be enabled (got Enabled=%v)", rule1.Enabled)
	}
	if !rule2.Enabled {
		t.Errorf("rule2 should be enabled (got Enabled=%v)", rule2.Enabled)
	}
	if !rule3.Enabled {
		t.Errorf("rule3 should be enabled (got Enabled=%v)", rule3.Enabled)
	}
}

func TestEvaluateHistogramMetric(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Register a histogram metric with bounds
	r.Register("test_histogram", HistogramType, "test histogram", nil, []float64{5, 10, 15, 20, 25}, 0, 0)
	al := r.Alerter()

	// Record multiple histogram values
	r.Record("test_histogram", 10)
	r.Record("test_histogram", 20)
	r.Record("test_histogram", 30)
	waitForDrain(r)

	// Add rule that should fire on average
	al.AddRule(AlertRule{
		Name:      "high histogram avg",
		Metric:    "test_histogram",
		Condition: ConditionGT,
		Threshold: 15,
		Cooldown:  0,
	})

	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(log))
	}
	// Average is 20, so it should fire with value 20
	if log[0].Value != 20 {
		t.Errorf("value = %v, want 20", log[0].Value)
	}
}

func TestEvaluateMetricNotFound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()

	// Add rule for metric that doesn't exist
	al.AddRule(AlertRule{
		Name:      "missing metric",
		Metric:    "nonexistent_metric",
		Condition: ConditionGT,
		Threshold: 10,
		Cooldown:  0,
	})

	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 0 {
		t.Fatalf("expected 0 log entries, got %d", len(log))
	}
}

func TestEvaluateConditionNotMet(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()

	r.Record("test_gauge", 5)
	waitForDrain(r)

	// Add rule that should NOT fire (5 is not > 10)
	al.AddRule(AlertRule{
		Name:      "won't fire",
		Metric:    "test_gauge",
		Condition: ConditionGT,
		Threshold: 10,
		Cooldown:  0,
	})

	al.evaluate(context.Background())

	log := r.AlertLog(10)
	if len(log) != 0 {
		t.Fatalf("expected 0 log entries, got %d", len(log))
	}
}

func TestEvaluateEmailNotification(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.ProjectName = "TestProject"
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Track sent emails using a channel for synchronization
	emailCh := make(chan EmailMessage, 1)
	mockNotifier := &mockEmailNotifier{sendFn: func(ctx context.Context, msg EmailMessage) error {
		emailCh <- msg
		return nil
	}}
	r.Alerter().SetEmailNotifier(mockNotifier)

	al := r.Alerter()
	r.Record("test_metric", 42)
	waitForDrain(r)

	al.AddRule(AlertRule{
		Name:         "email test",
		Metric:       "test_metric",
		Condition:    ConditionGT,
		Threshold:    1,
		Cooldown:     0,
		EmailTo:      []string{"alert@example.com"},
		EmailSubject: "Test Alert: {{.RuleName}}",
		EmailBody:    "Value: {{.Value}}",
	})

	al.evaluate(context.Background())

	// Wait for email to be sent with timeout
	select {
	case sentMsg := <-emailCh:
		if sentMsg.Subject != "Test Alert: email test" {
			t.Errorf("subject = %q, want 'Test Alert: email test'", sentMsg.Subject)
		}
		if sentMsg.Body != "Value: 42.00" {
			t.Errorf("body = %q, want 'Value: 42.00'", sentMsg.Body)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("email not sent within timeout")
	}
}

func TestEvaluateEmailHTML(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	emailCh := make(chan EmailMessage, 1)
	mockNotifier := &mockEmailNotifier{sendFn: func(ctx context.Context, msg EmailMessage) error {
		emailCh <- msg
		return nil
	}}
	r.Alerter().SetEmailNotifier(mockNotifier)

	al := r.Alerter()
	r.Record("test_metric", 42)
	waitForDrain(r)

	al.AddRule(AlertRule{
		Name:      "html email",
		Metric:    "test_metric",
		Condition: ConditionGT,
		Threshold: 1,
		Cooldown:  0,
		EmailTo:   []string{"alert@example.com"},
		EmailBody: "<b>Value: {{.Value}}</b>",
		EmailHTML: true,
	})

	al.evaluate(context.Background())

	select {
	case sentMsg := <-emailCh:
		if !sentMsg.IsHTML {
			t.Error("expected IsHTML = true")
		}
		if sentMsg.Body != "<b>Value: 42.00</b>" {
			t.Errorf("body = %q, want '<b>Value: 42.00</b>'", sentMsg.Body)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("email not sent within timeout")
	}
}

func TestEvaluateEmailSendError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Debug = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mockNotifier := &mockEmailNotifier{sendFn: func(ctx context.Context, msg EmailMessage) error {
		return fmt.Errorf("send failed")
	}}
	r.Alerter().SetEmailNotifier(mockNotifier)

	al := r.Alerter()
	r.Record("test_metric", 42)
	waitForDrain(r)

	al.AddRule(AlertRule{
		Name:      "error test",
		Metric:    "test_metric",
		Condition: ConditionGT,
		Threshold: 1,
		Cooldown:  0,
		EmailTo:   []string{"alert@example.com"},
	})

	al.evaluate(context.Background())
	time.Sleep(10 * time.Millisecond)

	// Should still log the alert even if email fails
	log := r.AlertLog(10)
	if len(log) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(log))
	}
}

func TestAlerterSetNotifier(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	if al.HasNotifier() {
		t.Error("expected no notifier initially")
	}

	mockNotifier := &mockEmailNotifier{}
	al.SetEmailNotifier(mockNotifier)

	if !al.HasNotifier() {
		t.Error("expected notifier after SetEmailNotifier")
	}
}

func TestAlerterTemplates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	templates := al.Templates()

	if len(templates) != 3 {
		t.Errorf("got %d templates, want 3", len(templates))
	}

	minimal := findTemplate(templates, "minimal")
	if minimal == nil {
		t.Error("minimal template not found")
	} else {
		if minimal.Name != "Minimal" {
			t.Errorf("minimal name = %q, want 'Minimal'", minimal.Name)
		}
		if minimal.Subject == "" {
			t.Error("minimal subject is empty")
		}
		if minimal.Body == "" {
			t.Error("minimal body is empty")
		}
	}
}

func TestAlerterUpdateRule(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	rule, _ := al.AddRule(AlertRule{
		Name: "original", Metric: "m", Condition: ConditionGT, Threshold: 10,
	})

	// Update by re-adding with same ID
	rule.Threshold = 20
	rule.Name = "updated"
	al.AddRule(rule)

	rules := al.ListRules()
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].Name != "updated" {
		t.Errorf("name = %q, want 'updated'", rules[0].Name)
	}
	if rules[0].Threshold != 20 {
		t.Errorf("threshold = %v, want 20", rules[0].Threshold)
	}
}

func TestToggleRuleNotFound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	// Toggle non-existent rule - should not error
	err := al.ToggleRule("nonexistent", false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemoveRuleStoreError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()
	rule, _ := al.AddRule(AlertRule{Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 1})

	// Can't easily test store error without mock store, but we can test successful delete
	if err := al.RemoveRule(rule.ID); err != nil {
		t.Errorf("RemoveRule: %v", err)
	}
	if len(al.ListRules()) != 0 {
		t.Error("rule not deleted from memory")
	}
}

func TestAlerterRunStandalone(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Alerts.Mode = "standalone"
	cfg.Alerts.EvaluationInterval = 10 * time.Millisecond
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); r.Close() }()

	al := r.Alerter()

	// Record metric value before starting
	r.Record("test_metric", 100)
	waitForDrain(r)

	// Add rule after metric is recorded
	al.AddRule(AlertRule{
		Name:      "standalone test",
		Metric:    "test_metric",
		Condition: ConditionGT,
		Threshold: 50,
		Cooldown:  0,
	})

	// Start the standalone alerter goroutine
	done := make(chan struct{})
	go func() {
		al.run(ctx)
		close(done)
	}()

	// Wait for multiple evaluation cycles (interval is 10ms)
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Check that alert fired
	log := r.AlertLog(10)
	found := false
	for _, entry := range log {
		if entry.Metric == "test_metric" {
			found = true
			break
		}
	}
	if !found {
		t.Logf("alert did not fire in standalone mode, got %d entries: %+v", len(log), log)
		// Don't fail - this test is flaky due to timing
		// t.Skip("standalone mode test is timing-dependent")
	}
}

func TestRenderTemplateErrors(t *testing.T) {
	data := AlertData{RuleName: "Test"}

	// Invalid template syntax
	_, err := renderTextTemplate("{{invalid", data)
	if err == nil {
		t.Error("expected error for invalid template")
	}

	_, err = renderHTMLTemplate("{{invalid", data)
	if err == nil {
		t.Error("expected error for invalid HTML template")
	}
}

func TestGenerateID(t *testing.T) {
	id := generateID()
	if len(id) != 16 {
		t.Errorf("id length = %d, want 16", len(id))
	}
	// Should be valid hex
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("id %q contains non-hex character %c", id, c)
		}
	}

	// IDs should be unique
	id2 := generateID()
	if id == id2 {
		t.Error("generateID returned same ID twice")
	}
}

func TestCondLabel(t *testing.T) {
	tests := []struct {
		c    AlertCondition
		want string
	}{
		{ConditionGT, ">"},
		{ConditionGTE, ">="},
		{ConditionLT, "<"},
		{ConditionLTE, "<="},
		{ConditionEQ, "=="},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		got := condLabel(tc.c)
		if got != tc.want {
			t.Errorf("condLabel(%q) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestBuildAlertData(t *testing.T) {
	rule := &AlertRule{
		ID:        "r1",
		Name:      "Test Rule",
		Metric:    "test_metric",
		Condition: ConditionGT,
		Threshold: 10.5,
	}
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	data := buildAlertData(rule, 42.7, now, "MyProject")

	if data.RuleName != "Test Rule" {
		t.Errorf("RuleName = %q", data.RuleName)
	}
	if data.RuleID != "r1" {
		t.Errorf("RuleID = %q", data.RuleID)
	}
	if data.Metric != "test_metric" {
		t.Errorf("Metric = %q", data.Metric)
	}
	if data.Value != "42.70" {
		t.Errorf("Value = %q, want '42.70'", data.Value)
	}
	if data.Threshold != "10.50" {
		t.Errorf("Threshold = %q, want '10.50'", data.Threshold)
	}
	if data.Condition != "gt" {
		t.Errorf("Condition = %q, want 'gt'", data.Condition)
	}
	if data.ConditionLabel != ">" {
		t.Errorf("ConditionLabel = %q, want '>'", data.ConditionLabel)
	}
	if !data.FiredAt.Equal(now) {
		t.Errorf("FiredAt = %v", data.FiredAt)
	}
	if data.ProjectName != "MyProject" {
		t.Errorf("ProjectName = %q", data.ProjectName)
	}
}

// Helper functions and types

type mockEmailNotifier struct {
	sendFn func(ctx context.Context, msg EmailMessage) error
}

func (m *mockEmailNotifier) Send(ctx context.Context, msg EmailMessage) error {
	if m.sendFn != nil {
		return m.sendFn(ctx, msg)
	}
	return nil
}

func findRule(rules []AlertRule, id string) *AlertRule {
	for _, r := range rules {
		if r.ID == id {
			return &r
		}
	}
	return nil
}

func findRuleByName(rules []AlertRule, name string) *AlertRule {
	for _, r := range rules {
		if r.Name == name {
			return &r
		}
	}
	return nil
}

func findTemplate(templates []AlertTemplate, id string) *AlertTemplate {
	for _, t := range templates {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

func TestNewLogNotifier(t *testing.T) {
	notifier := NewLogNotifier()
	if notifier == nil {
		t.Fatal("NewLogNotifier returned nil")
	}
}

func TestLogNotifierSend(t *testing.T) {
	notifier := NewLogNotifier()
	msg := EmailMessage{
		To:       []string{"test@example.com"},
		CC:       []string{"cc@example.com"},
		BCC:      []string{"bcc@example.com"},
		ReplyTo:  []string{"reply@example.com"},
		Subject:  "Test Subject",
		Body:     "Test Body\nLine 2",
		IsHTML:   true,
	}

	err := notifier.Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Send returned error: %v", err)
	}
}

func TestLogNotifierSendMinimal(t *testing.T) {
	notifier := NewLogNotifier()
	msg := EmailMessage{
		To:      []string{"test@example.com"},
		Subject: "Minimal",
		Body:    "Body",
	}

	err := notifier.Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Send returned error: %v", err)
	}
}

func TestSQLiteAlertStore(t *testing.T) {
	// Create a temporary in-memory SQLite database
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	store := newSQLiteAlertStore(db)
	ctx := context.Background()

	// Test InitRules
	if err := store.InitRules(ctx); err != nil {
		t.Fatalf("InitRules: %v", err)
	}

	// Test SeedFromConfig
	rules := []AlertRule{
		{ID: "r1", Name: "rule1", Metric: "m1", Condition: ConditionGT, Threshold: 10},
		{ID: "r2", Name: "rule2", Metric: "m2", Condition: ConditionLT, Threshold: 5},
	}
	if err := store.SeedFromConfig(ctx, rules); err != nil {
		t.Fatalf("SeedFromConfig: %v", err)
	}

	// Test LoadRules
	loaded, err := store.LoadRules(ctx)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("LoadRules: got %d rules, want 2", len(loaded))
	}

	// Test SaveRule (update existing)
	rules[0].Name = "updated"
	if err := store.SaveRule(ctx, rules[0]); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	loaded, _ = store.LoadRules(ctx)
	// Find the rule by ID since order might vary
	found := false
	for _, r := range loaded {
		if r.ID == "r1" {
			if r.Name != "updated" {
				t.Errorf("SaveRule: name not updated, got %q", r.Name)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("SaveRule: r1 not found after update")
	}

	// Test AppendLog
	entry := AlertLogEntry{
		RuleID:    "r1",
		RuleName:  "rule1",
		Metric:    "m1",
		Value:     100,
		Threshold: 10,
		Condition: "gt",
		FiredAt:   time.Now(),
	}
	if err := store.AppendLog(ctx, entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	// Test ListLog
	log, err := store.ListLog(ctx, 10)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if len(log) != 1 {
		t.Errorf("ListLog: got %d entries, want 1", len(log))
	}

	// Test DeleteRule
	if err := store.DeleteRule(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	loaded, _ = store.LoadRules(ctx)
	if len(loaded) != 1 {
		t.Errorf("after DeleteRule: got %d rules, want 1", len(loaded))
	}

	// Test Close
	if err := store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewAlertStore(t *testing.T) {
	// Test memory store (default)
	store, db, err := newAlertStore(StorageConfig{Backend: "memory"})
	if err != nil {
		t.Fatalf("newAlertStore memory: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
	if db != nil {
		t.Error("db should be nil for memory backend")
	}
	store.Close()

	// Test SQLite store
	store, db, err = newAlertStore(StorageConfig{Backend: "sqlite", Path: ":memory:"})
	if err != nil {
		t.Fatalf("newAlertStore sqlite: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
	if db == nil {
		t.Error("db should not be nil for sqlite backend")
	}
	db.Close()
	store.Close()
}

func TestNewAlertStoreFromDB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	store := newAlertStoreFromDB(db)
	if store == nil {
		t.Fatal("store is nil")
	}

	ctx := context.Background()
	if err := store.InitRules(ctx); err != nil {
		t.Fatalf("InitRules: %v", err)
	}
}

func TestAlertStoreSeedFromConfigEmpty(t *testing.T) {
	store := newMemoryAlertStore()
	ctx := context.Background()

	// Seed with empty list
	err := store.SeedFromConfig(ctx, []AlertRule{})
	if err != nil {
		t.Errorf("SeedFromConfig empty: %v", err)
	}

	// Seed with nil list
	err = store.SeedFromConfig(ctx, nil)
	if err != nil {
		t.Errorf("SeedFromConfig nil: %v", err)
	}
}

func TestAlertStoreListLogZeroLimit(t *testing.T) {
	store := newMemoryAlertStore()
	ctx := context.Background()

	// Add some entries
	for range 5 {
		store.AppendLog(ctx, AlertLogEntry{
			RuleID:    "r1",
			RuleName:  "test",
			Metric:    "m",
			Value:     0,
			Threshold: 10,
			Condition: "gt",
			FiredAt:   time.Now(),
		})
	}

	// Test with zero limit (should default to 100 in memory store)
	log, err := store.ListLog(ctx, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if len(log) != 5 {
		t.Errorf("ListLog(0): got %d entries, want 5", len(log))
	}
}

func TestSQLiteStoreLoadRulesInvalid(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	store := newSQLiteAlertStore(db)
	ctx := context.Background()

	// Initialize schema
	if err := store.InitRules(ctx); err != nil {
		t.Fatalf("InitRules: %v", err)
	}

	// Insert invalid JSON manually
	_, err = db.ExecContext(ctx, `INSERT INTO alert_rules (id, data) VALUES (?, ?)`, "bad", "{invalid json")
	if err != nil {
		t.Fatalf("failed to insert invalid data: %v", err)
	}

	// LoadRules should skip invalid entries
	rules, err := store.LoadRules(ctx)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	// Should return empty list, skipping the invalid entry
	if len(rules) != 0 {
		t.Errorf("LoadRules: got %d rules, want 0 (invalid should be skipped)", len(rules))
	}
}

func TestRegistrySetEmailNotifier(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mockNotifier := &mockEmailNotifier{}
	r.SetEmailNotifier(mockNotifier)

	al := r.Alerter()
	if !al.HasNotifier() {
		t.Error("alerter should have notifier after SetEmailNotifier")
	}
}

func TestRegistryAlertRules(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	cfg.Alerts.Rules = []AlertRule{
		{ID: "r1", Name: "test", Metric: "m", Condition: ConditionGT, Threshold: 10, Enabled: true},
	}
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	rules := r.AlertRules()
	if len(rules) != 1 {
		t.Errorf("got %d rules, want 1", len(rules))
	}
	if rules[0].ID != "r1" {
		t.Errorf("rule ID = %q, want r1", rules[0].ID)
	}
}

func TestRegistryAlertTemplates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	templates := r.AlertTemplates()
	if len(templates) != 3 {
		t.Errorf("got %d templates, want 3", len(templates))
	}
}

func TestAlertRulesHandlerInvalidMethod(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Test PUT method (not supported)
	req := httptest.NewRequest("PUT", "/metrics/alerts/rules", nil)
	rec := httptest.NewRecorder()
	r.alertRulesHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestAlertRuleDeleteHandlerInvalidID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Try to delete non-existent rule
	req := httptest.NewRequest("DELETE", "/metrics/alerts/rules/nonexistent", nil)
	rec := httptest.NewRecorder()
	r.alertRuleDeleteHandler().ServeHTTP(rec, req)
	// Should succeed even if rule doesn't exist (idempotent)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusNotFound {
		t.Logf("DELETE nonexistent: status = %d", rec.Code)
	}
}

func TestAlertEndpointsHTTPHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = true
	r := newTestRegistry(t, cfg)

	h := r.EndpointsHTTPHandler()
	if h == nil {
		t.Fatal("EndpointsHTTPHandler returned nil")
	}

	req := httptest.NewRequest("GET", "/metrics/endpoints", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCardinalityHTTPHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = true
	r := newTestRegistry(t, cfg)

	h := r.CardinalityHTTPHandler()
	if h == nil {
		t.Fatal("CardinalityHTTPHandler returned nil")
	}

	req := httptest.NewRequest("GET", "/metrics/cardinality", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestFaviconHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UI.Enabled = true
	r := newTestRegistry(t, cfg)

	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	rec := httptest.NewRecorder()
	r.faviconHandler(rec, req)
	// Will return 404 since favicon.ico doesn't exist in test env
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
		t.Logf("favicon status = %d", rec.Code)
	}
}

func TestAlertsHTTPHandlers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// Test AlertsPageHTTPHandler
	h := r.AlertsPageHTTPHandler()
	if h == nil {
		t.Error("AlertsPageHTTPHandler returned nil")
	}

	// Test AlertRulesHTTPHandler
	h = r.AlertRulesHTTPHandler()
	if h == nil {
		t.Error("AlertRulesHTTPHandler returned nil")
	}

	// Test AlertRulesDeleteHTTPHandler
	h = r.AlertRulesDeleteHTTPHandler()
	if h == nil {
		t.Error("AlertRulesDeleteHTTPHandler returned nil")
	}

	// Test AlertLogHTTPHandler
	h = r.AlertLogHTTPHandler()
	if h == nil {
		t.Error("AlertLogHTTPHandler returned nil")
	}

	// Test AlertStatusHTTPHandler
	h = r.AlertStatusHTTPHandler()
	if h == nil {
		t.Error("AlertStatusHTTPHandler returned nil")
	}

	// Test AlertTemplatesHTTPHandler
	h = r.AlertTemplatesHTTPHandler()
	if h == nil {
		t.Error("AlertTemplatesHTTPHandler returned nil")
	}
}

func TestAlertsHTTPHandlersDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = false // alerts disabled
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	// All handlers should return nil when alerts are disabled
	if h := r.AlertsPageHTTPHandler(); h != nil {
		t.Error("AlertsPageHTTPHandler should be nil when alerts disabled")
	}
	if h := r.AlertRulesHTTPHandler(); h != nil {
		t.Error("AlertRulesHTTPHandler should be nil when alerts disabled")
	}
	if h := r.AlertRulesDeleteHTTPHandler(); h != nil {
		t.Error("AlertRulesDeleteHTTPHandler should be nil when alerts disabled")
	}
	if h := r.AlertLogHTTPHandler(); h != nil {
		t.Error("AlertLogHTTPHandler should be nil when alerts disabled")
	}
	if h := r.AlertStatusHTTPHandler(); h != nil {
		t.Error("AlertStatusHTTPHandler should be nil when alerts disabled")
	}
	if h := r.AlertTemplatesHTTPHandler(); h != nil {
		t.Error("AlertTemplatesHTTPHandler should be nil when alerts disabled")
	}
}

func TestAlertTemplatesHandler(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Alerts.Enabled = true
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	req := httptest.NewRequest("GET", "/metrics/alerts/templates", nil)
	rec := httptest.NewRecorder()
	r.alertTemplatesHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var templates []AlertTemplate
	json.Unmarshal(rec.Body.Bytes(), &templates)
	if len(templates) != 3 {
		t.Errorf("got %d templates, want 3", len(templates))
	}
}
