package golens

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricTypeString(t *testing.T) {
	cases := []struct {
		t   MetricType
		exp string
	}{
		{CounterType, "counter"},
		{GaugeType, "gauge"},
		{HistogramType, "histogram"},
		{MetricType(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.t.String(); got != c.exp {
			t.Errorf("type %d: got %q want %q", c.t, got, c.exp)
		}
	}
}

func TestMetricTypeChart(t *testing.T) {
	cases := []struct {
		t   MetricType
		exp string
	}{
		{CounterType, "line"},
		{GaugeType, "sparkline"},
		{HistogramType, "bar"},
		{MetricType(99), "line"},
	}
	for _, c := range cases {
		if got := c.t.ChartType(); got != c.exp {
			t.Errorf("chart for type %d: got %q want %q", c.t, got, c.exp)
		}
	}
}

func TestCounterRecordAndSnapshot(t *testing.T) {
	m := newMetric("requests", CounterType, "reqs", nil, nil, 0, 0)
	m.Record(5)
	m.Record(7)
	s := m.Snapshot()
	if s.Name != "requests" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Value != 12 {
		t.Errorf("counter value = %v, want 12", s.Value)
	}
	if s.Type != "counter" || s.Chart != "line" {
		t.Errorf("type/chart = %q/%q", s.Type, s.Chart)
	}
}

func TestGaugeRecordReplaces(t *testing.T) {
	m := newMetric("temp", GaugeType, "", nil, nil, 0, 100)
	m.Record(10)
	m.Record(20)
	m.Record(15)
	if got := m.Snapshot().Value; got != 15 {
		t.Errorf("gauge = %v, want 15 (last write wins)", got)
	}
}

func TestHistogramBuckets(t *testing.T) {
	bounds := []float64{1, 5, 10}
	m := newMetric("lat", HistogramType, "", nil, bounds, 0, 0)
	for _, v := range []float64{0.5, 0.5, 3, 7, 100} {
		m.Record(v)
	}
	s := m.Snapshot()
	if s.Count != 5 {
		t.Errorf("count = %d, want 5", s.Count)
	}
	wantSum := 0.5 + 0.5 + 3 + 7 + 100
	if !floatEq(s.Sum, wantSum) {
		t.Errorf("sum = %v, want %v", s.Sum, wantSum)
	}
	if !floatEq(s.Avg, wantSum/5) {
		t.Errorf("avg = %v, want %v", s.Avg, wantSum/5)
	}
	// bucket counts: <=1 -> 2, <=5 -> 1, <=10 -> 1, overflow -> 1
	if len(s.Buckets) != 4 {
		t.Fatalf("buckets = %d, want 4", len(s.Buckets))
	}
	if s.Buckets[0].Count != 2 {
		t.Errorf("bucket[0] count = %d, want 2", s.Buckets[0].Count)
	}
	if s.Buckets[1].Count != 1 {
		t.Errorf("bucket[1] count = %d, want 1", s.Buckets[1].Count)
	}
	if !s.Buckets[3].Overflow || s.Buckets[3].Count != 1 {
		t.Errorf("overflow bucket = %+v", s.Buckets[3])
	}
}

func TestHistogramDefaultBounds(t *testing.T) {
	m := newMetric("h", HistogramType, "", nil, nil, 0, 0)
	m.Record(0.1)
	s := m.Snapshot()
	if len(s.Buckets) != len(DefaultHistogramBounds)+1 {
		t.Errorf("default buckets = %d, want %d", len(s.Buckets), len(DefaultHistogramBounds)+1)
	}
}

func TestHistogramZeroCount(t *testing.T) {
	m := newMetric("h", HistogramType, "", nil, []float64{1}, 0, 0)
	s := m.Snapshot()
	if s.Count != 0 || s.Avg != 0 {
		t.Errorf("empty histogram snapshot = %+v", s)
	}
}

func floatEq(a, b float64) bool {
	const eps = 1e-6
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

// The JSON encoding of MetricSnapshot and BucketSnapshot is the stable
// public API contract for /metrics/data: snake_case keys, labels as
// name/value objects, and an overflow flag that is present even when false
// so clients can filter on the flag instead of the bound value.
func TestSnapshotJSONContract(t *testing.T) {
	s := MetricSnapshot{
		Name:        "m",
		Type:        "histogram",
		Description: "d",
		Labels:      []string{"code"},
		LabelValues: []Label{{Name: "code", Value: "200"}},
		Value:       1,
		Count:       2,
		Sum:         3,
		Avg:         1.5,
		UpdatedAt:   7,
		Buckets: []BucketSnapshot{
			{UpperBound: 1, Count: 2},
			{UpperBound: 0, Count: 0, Overflow: true},
		},
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, key := range []string{
		`"name":"m"`, `"type":"histogram"`, `"description":"d"`,
		`"labels":["code"]`, `"label_values":[{"name":"code","value":"200"}]`,
		`"value":1`, `"count":2`, `"sum":3`, `"avg":1.5`, `"updated_at":7`,
		`"upper_bound":1,"count":2,"overflow":false`,
		`"overflow":true`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("serialized snapshot missing %s: %s", key, out)
		}
	}
}
