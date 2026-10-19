package golens

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	htemplate "html/template"
	"log"
	"sync"
	"text/template"
	"time"
)

// AlertTemplate is a predefined email template for alert notifications.
type AlertTemplate struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// DefaultAlertTemplates ships with GoLens. Users can reference them by ID in
// config or select them from the UI.
var DefaultAlertTemplates = []AlertTemplate{
	{
		ID:   "minimal",
		Name: "Minimal",
		Subject: `[{{.ProjectName}}] Alert: {{.RuleName}}`,
		Body: `Alert: {{.RuleName}}
Metric: {{.Metric}}
Value: {{.Value}} {{.ConditionLabel}} {{.Threshold}}
Fired at: {{.FiredAt}}`,
	},
	{
		ID:   "html-default",
		Name: "HTML Default",
		Subject: `[{{.ProjectName}}] {{.RuleName}} — {{.Metric}} {{.ConditionLabel}} {{.Threshold}}`,
		Body: `<div style="font-family:system-ui,sans-serif;max-width:480px;margin:0 auto;padding:24px;">
  <h2 style="margin:0 0 8px;color:#ef4444;">⚠ Alert Fired</h2>
  <p style="margin:0 0 16px;color:#64748b;font-size:14px;">{{.ProjectName}} — {{.FiredAt}}</p>
  <table style="width:100%;border-collapse:collapse;font-size:14px;">
    <tr><td style="padding:6px 12px;color:#94a3b8;">Rule</td><td style="padding:6px 12px;font-weight:600;">{{.RuleName}}</td></tr>
    <tr style="background:#f8fafc;"><td style="padding:6px 12px;color:#94a3b8;">Metric</td><td style="padding:6px 12px;font-family:monospace;">{{.Metric}}</td></tr>
    <tr><td style="padding:6px 12px;color:#94a3b8;">Value</td><td style="padding:6px 12px;font-family:monospace;color:#ef4444;font-weight:600;">{{.Value}}</td></tr>
    <tr style="background:#f8fafc;"><td style="padding:6px 12px;color:#94a3b8;">Condition</td><td style="padding:6px 12px;font-family:monospace;">{{.ConditionLabel}} {{.Threshold}}</td></tr>
  </table>
</div>`,
	},
	{
		ID:   "html-detailed",
		Name: "HTML Detailed",
		Subject: `[ALERT] {{.RuleName}} triggered on {{.Metric}}`,
		Body: `<!DOCTYPE html>
<html><head><meta charset="utf-8"></head>
<body style="margin:0;padding:0;background:#f1f5f9;">
<div style="font-family:system-ui,sans-serif;max-width:520px;margin:24px auto;background:#ffffff;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,.1);">
  <div style="background:#ef4444;padding:16px 24px;">
    <h1 style="margin:0;color:#ffffff;font-size:18px;">⚠ Alert Triggered</h1>
    <p style="margin:4px 0 0;color:rgba(255,255,255,.8);font-size:13px;">{{.ProjectName}}</p>
  </div>
  <div style="padding:24px;">
    <p style="margin:0 0 16px;color:#334155;font-size:14px;">The alert rule <strong>{{.RuleName}}</strong> has been triggered.</p>
    <table style="width:100%;border-collapse:collapse;font-size:14px;margin-bottom:16px;">
      <tr><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;color:#64748b;width:40%;">Rule ID</td><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;font-family:monospace;">{{.RuleID}}</td></tr>
      <tr><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;color:#64748b;">Metric</td><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;font-family:monospace;">{{.Metric}}</td></tr>
      <tr><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;color:#64748b;">Current Value</td><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;font-family:monospace;color:#ef4444;font-weight:600;">{{.Value}}</td></tr>
      <tr><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;color:#64748b;">Condition</td><td style="padding:8px 12px;border-bottom:1px solid #e2e8f0;font-family:monospace;">{{.ConditionLabel}} {{.Threshold}}</td></tr>
      <tr><td style="padding:8px 12px;color:#64748b;">Fired At</td><td style="padding:8px 12px;font-family:monospace;">{{.FiredAt}}</td></tr>
    </table>
    <p style="margin:0;color:#94a3b8;font-size:12px;">This is an automated alert from GoLens.</p>
  </div>
</div>
</body></html>`,
	},
}

// UnmarshalJSON handles Cooldown as either a JSON string ("5m", "1h") or a
// number (nanoseconds). Go's time.Duration normally only accepts numbers.
func (r *AlertRule) UnmarshalJSON(data []byte) error {
	// Alias to avoid recursion.
	type rawRule AlertRule
	var tmp struct {
		rawRule
		RawCooldown json.RawMessage `json:"cooldown"`
	}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*r = AlertRule(tmp.rawRule)

	switch {
	case len(tmp.RawCooldown) == 0 || string(tmp.RawCooldown) == "null":
		// zero value, leave default
	case tmp.RawCooldown[0] == '"':
		var s string
		if err := json.Unmarshal(tmp.RawCooldown, &s); err != nil {
			return fmt.Errorf("cooldown: %w", err)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("cooldown: %w", err)
		}
		r.Cooldown = d
	default:
		var ns int64
		if err := json.Unmarshal(tmp.RawCooldown, &ns); err != nil {
			// Try float (e.g. 3e8)
			var f float64
			if err2 := json.Unmarshal(tmp.RawCooldown, &f); err2 != nil {
				return fmt.Errorf("cooldown: expected string or number: %v", err)
			}
			ns = int64(f)
		}
		r.Cooldown = time.Duration(ns)
	}
	return nil
}

// AlertCondition is a comparison operator for threshold-based alert rules.
type AlertCondition string

const (
	ConditionGT  AlertCondition = "gt"  // greater than
	ConditionGTE AlertCondition = "gte" // greater than or equal
	ConditionLT  AlertCondition = "lt"  // less than
	ConditionLTE AlertCondition = "lte" // less than or equal
	ConditionEQ  AlertCondition = "eq"  // equal
)

// AlertRule defines a threshold-based alert on a single metric.
type AlertRule struct {
	ID           string         `json:"id" yaml:"id"`
	Name         string         `json:"name" yaml:"name"`
	Metric       string         `json:"metric" yaml:"metric"`
	Condition    AlertCondition `json:"condition" yaml:"condition"`
	Threshold    float64        `json:"threshold" yaml:"threshold"`
	Cooldown     time.Duration  `json:"cooldown" yaml:"cooldown"`
	Enabled      bool           `json:"enabled" yaml:"enabled"`
	EmailTo      []string       `json:"email_to" yaml:"email_to"`
	EmailSubject string         `json:"email_subject" yaml:"email_subject"`
	EmailBody    string         `json:"email_body" yaml:"email_body"`
	EmailHTML    bool           `json:"email_html" yaml:"email_html"`
	LastFired    time.Time      `json:"last_fired" yaml:"-"`
}

// AlertData holds the fields available inside email subject and body templates.
// Use {{.FieldName}} in text bodies and subjects, or {{.FieldName}} in HTML
// bodies (Go's html/template auto-escapes).
//
// Available fields:
//
//	{{.RuleName}}        — alert rule name
//	{{.RuleID}}          — alert rule ID
//	{{.Metric}}          — metric name that triggered
//	{{.Value}}           — current metric value (string, 2 decimal places)
//	{{.Threshold}}       — threshold value (string, 2 decimal places)
//	{{.Condition}}       — raw condition token (gt, lt, gte, lte, eq)
//	{{.ConditionLabel}}  — human-readable condition (>, <, >=, <=, ==)
//	{{.FiredAt}}         — timestamp the alert fired (time.Time)
//	{{.ProjectName}}     — project name from config
type AlertData struct {
	RuleName       string
	RuleID         string
	Metric         string
	Value          string
	Threshold      string
	Condition      string
	ConditionLabel string
	FiredAt        time.Time
	ProjectName    string
}

// AlertLogEntry records a single alert firing event.
type AlertLogEntry struct {
	ID        int64     `json:"id"`
	RuleID    string    `json:"rule_id"`
	RuleName  string    `json:"rule_name"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	Condition string    `json:"condition"`
	FiredAt   time.Time `json:"fired_at"`
}

// EmailMessage is the payload for sending an alert email notification.
type EmailMessage struct {
	To      []string
	CC      []string
	BCC     []string
	ReplyTo []string
	Subject string
	Body    string
	IsHTML  bool
}

// EmailNotifier is the pluggable interface for sending alert notifications.
// Users provide their own implementation (SMTP, SendGrid, etc.) and inject it
// via Registry.SetEmailNotifier. When nil, alerts fire silently (log only).
type EmailNotifier interface {
	Send(ctx context.Context, msg EmailMessage) error
}

// evaluateCondition checks whether the given metric value satisfies the rule's
// condition and threshold.
func evaluateCondition(v float64, c AlertCondition, threshold float64) bool {
	switch c {
	case ConditionGT:
		return v > threshold
	case ConditionGTE:
		return v >= threshold
	case ConditionLT:
		return v < threshold
	case ConditionLTE:
		return v <= threshold
	case ConditionEQ:
		return v == threshold
	default:
		return false
	}
}

// alerter evaluates alert rules against current metric snapshots on a
// configurable interval. It supports two modes:
//   - integrated: ticker channel consumed by Registry.loop() (single goroutine)
//   - standalone: runs its own goroutine via run()
type alerter struct {
	registry *Registry
	store    AlertStore
	notifier EmailNotifier

	mu    sync.RWMutex
	rules map[string]*AlertRule // keyed by rule ID

	interval time.Duration
	cooldown time.Duration
	debug    bool

	ticker *time.Ticker // nil until started
}

// newAlerter creates an alerter seeded with rules from the config and/or store.
func newAlerter(r *Registry, store AlertStore, cfg AlertsConfig) *alerter {
	a := &alerter{
		registry: r,
		store:    store,
		interval: cfg.EvaluationInterval,
		cooldown: cfg.DefaultCooldown,
		debug:    r.debug,
		rules:    make(map[string]*AlertRule),
	}

	// Seed from config-file rules.
	for _, rule := range cfg.Rules {
		if rule.ID == "" {
			rule.ID = generateID()
		}
		if rule.Cooldown == 0 {
			rule.Cooldown = cfg.DefaultCooldown
		}
		rule.Enabled = true
		a.rules[rule.ID] = &rule
	}

	return a
}

// SetEmailNotifier injects a notifier for alert delivery. Safe to call at any time.
func (a *alerter) SetEmailNotifier(n EmailNotifier) {
	a.mu.Lock()
	a.notifier = n
	a.mu.Unlock()
}

// HasNotifier reports whether an email notifier has been configured.
func (a *alerter) HasNotifier() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.notifier != nil
}

// Templates returns the available predefined email templates.
func (a *alerter) Templates() []AlertTemplate {
	return DefaultAlertTemplates
}

// AddRule registers a new alert rule or updates an existing one.
// New rules default to Enabled=true; updates preserve the caller's Enabled field.
func (a *alerter) AddRule(rule AlertRule) (AlertRule, error) {
	if rule.ID == "" {
		rule.ID = generateID()
	}
	if rule.Cooldown == 0 {
		rule.Cooldown = a.cooldown
	}

	a.mu.Lock()
	_, exists := a.rules[rule.ID]
	if !exists {
		rule.Enabled = true // new rules default to enabled
	}
	a.rules[rule.ID] = &rule
	a.mu.Unlock()

	if a.store != nil {
		if err := a.store.SaveRule(context.Background(), rule); err != nil {
			if a.debug {
				log.Printf("[golens] alert store save rule failed: %v", err)
			}
		}
	}
	return rule, nil
}

// RemoveRule deletes a rule by ID. Removes from store if available.
func (a *alerter) RemoveRule(id string) error {
	a.mu.Lock()
	delete(a.rules, id)
	a.mu.Unlock()

	if a.store != nil {
		if err := a.store.DeleteRule(context.Background(), id); err != nil {
			return err
		}
	}
	return nil
}

// ListRules returns a snapshot of all rules.
func (a *alerter) ListRules() []AlertRule {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AlertRule, 0, len(a.rules))
	for _, r := range a.rules {
		out = append(out, *r)
	}
	return out
}

// FiringCount returns the number of enabled rules whose condition currently
// holds against the latest metric snapshots. Evaluated on request from the
// same value logic as evaluate (histograms compare their average), it is the
// live "firing now" figure behind the dashboard status dot; cooldown
// suppression does not apply.
func (a *alerter) FiringCount() int {
	a.mu.RLock()
	rules := make([]AlertRule, 0, len(a.rules))
	for _, r := range a.rules {
		if r.Enabled {
			rules = append(rules, *r)
		}
	}
	a.mu.RUnlock()

	snapshots := a.registry.Snapshots()
	byName := make(map[string]MetricSnapshot, len(snapshots))
	for _, s := range snapshots {
		byName[s.Name] = s
	}

	n := 0
	for _, r := range rules {
		snap, ok := byName[r.Metric]
		if !ok {
			continue
		}
		v := snap.Value
		if snap.Type == "histogram" && snap.Count > 0 {
			v = snap.Avg
		}
		if evaluateCondition(v, r.Condition, r.Threshold) {
			n++
		}
	}
	return n
}

// ToggleRule enables or disables a rule by ID.
func (a *alerter) ToggleRule(id string, enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.rules[id]
	if !ok {
		return nil
	}
	r.Enabled = enabled

	if a.store != nil {
		if err := a.store.SaveRule(context.Background(), *r); err != nil {
			return err
		}
	}
	return nil
}

// C returns the ticker channel for integrated mode. The caller reads from this
// channel in a select alongside other Registry.loop() cases.
func (a *alerter) C() <-chan time.Time {
	if a.ticker == nil {
		a.ticker = time.NewTicker(a.interval)
	}
	return a.ticker.C
}

// run starts a standalone evaluation loop. It blocks until the context is
// cancelled. Use this when mode="standalone".
func (a *alerter) run(ctx context.Context) {
	a.ticker = time.NewTicker(a.interval)
	defer a.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.ticker.C:
			a.evaluate(ctx)
		}
	}
}

// evaluate checks all enabled rules against current metric snapshots.
// This is the single evaluation pass, called either from Registry.loop()
// (integrated mode) or from the standalone goroutine.
func (a *alerter) evaluate(ctx context.Context) {
	a.mu.RLock()
	rules := make([]*AlertRule, 0, len(a.rules))
	for _, r := range a.rules {
		if r.Enabled {
			rules = append(rules, r)
		}
	}
	a.mu.RUnlock()

	if len(rules) == 0 {
		return
	}

	snapshots := a.registry.Snapshots()
	snapMap := make(map[string]MetricSnapshot, len(snapshots))
	for _, s := range snapshots {
		snapMap[s.Name] = s
	}

	now := time.Now()
	for _, rule := range rules {
		snap, ok := snapMap[rule.Metric]
		if !ok {
			continue
		}

		v := snap.Value
		// For counters, use the raw value. For histograms, use the average.
		if snap.Type == "histogram" && snap.Count > 0 {
			v = snap.Avg
		}

		if !evaluateCondition(v, rule.Condition, rule.Threshold) {
			continue
		}

		// Cooldown check.
		a.mu.RLock()
		lastFired := rule.LastFired
		a.mu.RUnlock()
		if rule.Cooldown > 0 && now.Sub(lastFired) < rule.Cooldown {
			continue
		}

		// Fire the alert.
		a.mu.Lock()
		rule.LastFired = now
		a.mu.Unlock()

		entry := AlertLogEntry{
			RuleID:    rule.ID,
			RuleName:  rule.Name,
			Metric:    rule.Metric,
			Value:     v,
			Threshold: rule.Threshold,
			Condition: string(rule.Condition),
			FiredAt:   now,
		}

		if a.store != nil {
			if err := a.store.AppendLog(ctx, entry); err != nil {
				if a.debug {
					log.Printf("[golens] alert log append failed: %v", err)
				}
			}
		}

		if a.debug {
			log.Printf("[golens] ALERT FIRED: %s (%s %s %.2f, value=%.2f)",
				rule.Name, rule.Metric, rule.Condition, rule.Threshold, v)
		}

		// Send email notification if notifier is configured.
		if a.notifier != nil && len(rule.EmailTo) > 0 {
			data := buildAlertData(rule, v, now, a.registry.cfg.ProjectName)

			subject := rule.EmailSubject
			if subject == "" {
				subject = "GoLens Alert: {{.RuleName}}"
			}
			subject, err := renderTextTemplate(subject, data)
			if err != nil && a.debug {
				log.Printf("[golens] alert subject template error: %v", err)
			}

			body := rule.EmailBody
			if body == "" {
				body = "Alert: {{.RuleName}}\nMetric: {{.Metric}}\nValue: {{.Value}} {{.ConditionLabel}} {{.Threshold}}\nFired at: {{.FiredAt}}"
			}
			var renderedBody string
			var bodyErr error
			if rule.EmailHTML {
				renderedBody, bodyErr = renderHTMLTemplate(body, data)
			} else {
				renderedBody, bodyErr = renderTextTemplate(body, data)
			}
			if bodyErr != nil && a.debug {
				log.Printf("[golens] alert body template error: %v", bodyErr)
			}

			msg := EmailMessage{
				To:      rule.EmailTo,
				Subject: subject,
				Body:    renderedBody,
				IsHTML:  rule.EmailHTML,
			}
			go func(m EmailMessage) {
				if err := a.notifier.Send(ctx, m); err != nil {
					if a.debug {
						log.Printf("[golens] alert email send failed: %v", err)
					}
				}
			}(msg)
		}
	}
}

// condLabel maps a raw AlertCondition to a human-readable operator.
func condLabel(c AlertCondition) string {
	switch c {
	case ConditionGT:
		return ">"
	case ConditionGTE:
		return ">="
	case ConditionLT:
		return "<"
	case ConditionLTE:
		return "<="
	case ConditionEQ:
		return "=="
	default:
		return string(c)
	}
}

// buildAlertData populates an AlertData struct from a rule and its firing context.
func buildAlertData(rule *AlertRule, v float64, now time.Time, projectName string) AlertData {
	return AlertData{
		RuleName:       rule.Name,
		RuleID:         rule.ID,
		Metric:         rule.Metric,
		Value:          fmt.Sprintf("%.2f", v),
		Threshold:      fmt.Sprintf("%.2f", rule.Threshold),
		Condition:      string(rule.Condition),
		ConditionLabel: condLabel(rule.Condition),
		FiredAt:        now,
		ProjectName:    projectName,
	}
}

// renderTextTemplate executes a text/template string with the given data.
func renderTextTemplate(tmplStr string, data AlertData) (string, error) {
	t, err := template.New("").Parse(tmplStr)
	if err != nil {
		return tmplStr, err // return raw on parse error
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tmplStr, err
	}
	return buf.String(), nil
}

// renderHTMLTemplate executes an html/template string with the given data.
func renderHTMLTemplate(tmplStr string, data AlertData) (string, error) {
	t, err := htemplate.New("").Parse(tmplStr)
	if err != nil {
		return tmplStr, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tmplStr, err
	}
	return buf.String(), nil
}

// generateID produces a random 16-char hex ID for new rules.
func generateID() string {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		panic("golens: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
