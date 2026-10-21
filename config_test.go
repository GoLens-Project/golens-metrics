package golens

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Storage.Backend != "memory" {
		t.Errorf("default backend = %q, want memory", c.Storage.Backend)
	}
	if c.OTLP.Protocol != "http" {
		t.Errorf("default protocol = %q, want http", c.OTLP.Protocol)
	}
	if c.UI.PollInterval != 5*time.Second {
		t.Errorf("poll = %v, want 5s", c.UI.PollInterval)
	}
	if c.IngestQueueSize <= 0 || c.MaxMetrics <= 0 {
		t.Errorf("queue/max not set: %d/%d", c.IngestQueueSize, c.MaxMetrics)
	}
}

func TestApplyDefaultsFillsBlanks(t *testing.T) {
	c := Config{}
	c.applyDefaults()
	if c.Storage.Backend != "memory" {
		t.Errorf("backend not defaulted")
	}
	if c.OTLP.Endpoint == "" {
		t.Errorf("endpoint not defaulted")
	}
	if c.UI.PollInterval != 5*time.Second {
		t.Errorf("poll not defaulted")
	}
	if c.FlushInterval == 0 {
		t.Errorf("flush not defaulted")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c.Storage.Backend != "memory" {
		t.Errorf("missing file should yield defaults, got backend=%q", c.Storage.Backend)
	}
}

func TestLoadConfigPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golens.yaml")
	content := []byte("storage:\n  backend: sqlite\n  path: ./t.db\notlp:\n  enabled: true\ndebug: true\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Storage.Backend != "sqlite" || c.Storage.Path != "./t.db" {
		t.Errorf("storage not parsed: %+v", c.Storage)
	}
	if !c.OTLP.Enabled {
		t.Errorf("otlp.enabled not parsed")
	}
	if !c.Debug {
		t.Errorf("debug not parsed")
	}
	// unspecified fields fall back to defaults
	if c.UI.PollInterval != 5*time.Second {
		t.Errorf("ui.poll_interval should default, got %v", c.UI.PollInterval)
	}
	if c.OTLP.Protocol != "http" {
		t.Errorf("protocol should default to http, got %q", c.OTLP.Protocol)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("storage: [unclosed"), 0o644)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

// The YAML fallback surface parses ui.dashboard_sections into strongly-typed
// sections (ids, optional names, item lists). No preset configured leaves the
// field nil, so seeding falls to the Go API or the built-in default.
func TestLoadConfigDashboardSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golens.yaml")
	content := []byte("ui:\n" +
		"  dashboard_sections:\n" +
		"    - id: overview\n" +
		"      name: Overview\n" +
		"      items: [runtime-health, endpoint-latency]\n" +
		"    - id: tail\n" +
		"      items: [internals]\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []DashboardSection{
		{ID: "overview", Name: "Overview", Items: []string{"runtime-health", "endpoint-latency"}},
		{ID: "tail", Items: []string{"internals"}},
	}
	if !reflect.DeepEqual(c.UI.DashboardSections, want) {
		t.Errorf("dashboard_sections = %+v, want %+v", c.UI.DashboardSections, want)
	}
	if DefaultConfig().UI.DashboardSections != nil {
		t.Error("default config must not preset dashboard sections")
	}
}
