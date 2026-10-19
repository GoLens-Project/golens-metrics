package golens

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// MetricType enumerates the supported metric kinds for the MVP.
type MetricType int

const (
	CounterType MetricType = iota
	GaugeType
	HistogramType
)

func (t MetricType) String() string {
	switch t {
	case CounterType:
		return "counter"
	case GaugeType:
		return "gauge"
	case HistogramType:
		return "histogram"
	default:
		return "unknown"
	}
}

// ChartType reports the default dashboard chart for a metric kind.
func (t MetricType) ChartType() string {
	switch t {
	case CounterType:
		return "line"
	case GaugeType:
		return "sparkline"
	case HistogramType:
		return "bar"
	default:
		return "line"
	}
}

// Label is a single key/value attribute attached to a metric sample.
type Label struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Metric is the user-facing definition of a registered metric. It is the
// shared, type-erased handle stored in the Registry; the actual sample
// values live in the thread-safe value underneath.
type Metric struct {
	Name        string
	Type        MetricType
	Description string
	Labels      []string
	value       metricValue
	gaugeMin    float64 // minimum value for gauge scale (0 = auto)
	gaugeMax    float64 // maximum value for gauge scale (0 = auto)
}

// metricValue is the concurrent-safe numeric payload. Counters and gauges use
// the atomics directly; histograms keep per-bucket counters. lastLabels holds
// the most recent label set observed (lock-free) so the UI can show a metric's
// dimensions without expanding per-label series (cardinality stays bounded).
type metricValue struct {
	mu         sync.RWMutex
	counter    atomic.Int64 // fixed-point (scaled) for counter/gauge
	gauge      atomic.Int64
	hist       map[float64]*atomic.Int64 // bucket boundary -> count
	overflow   atomic.Int64              // +Inf bucket count; kept out of hist so a real 0.0 bound cannot share it
	bounds     []float64
	sum        atomic.Int64
	count      atomic.Int64
	last       atomic.Int64            // unix nano of last update
	lastLabels atomic.Pointer[[]Label] // last observed label values
}

const scaleFactor = 1_000_000 // 6 decimal places of precision in fixed-point

// newMetric allocates a metric with the right value structure for its type.
func newMetric(name string, mtype MetricType, desc string, labels []string, bounds []float64, gaugeMin, gaugeMax float64) *Metric {
	m := &Metric{
		Name:        name,
		Type:        mtype,
		Description: desc,
		Labels:      labels,
		gaugeMin:    gaugeMin,
		gaugeMax:    gaugeMax,
	}
	if mtype == HistogramType {
		if len(bounds) == 0 {
			bounds = DefaultHistogramBounds
		}
		m.value.bounds = bounds
		m.value.hist = make(map[float64]*atomic.Int64, len(bounds))
		for _, b := range bounds {
			v := atomic.Int64{}
			m.value.hist[b] = &v
		}
	}
	return m
}

// Record adds a sample to the metric. For counters the value is added; for
// gauges it replaces; for histograms it is bucketed.
func (m *Metric) Record(v float64) {
	scaled := int64(v * scaleFactor)
	now := time.Now().UnixNano()
	m.value.last.Store(now)

	switch m.Type {
	case CounterType:
		m.value.counter.Add(scaled)
	case GaugeType:
		m.value.gauge.Store(scaled)
	case HistogramType:
		m.value.count.Add(1)
		m.value.sum.Add(scaled)
		placed := false
		for _, b := range m.value.bounds {
			if v <= b {
				if c, ok := m.value.hist[b]; ok {
					c.Add(1)
					placed = true
				}
				break
			}
		}
		if !placed {
			m.value.overflow.Add(1) // +Inf bucket
		}
	}
}

// setLabels stores the most recently observed label values (lock-free). It is
// called from the background loop, not the request path, so the small
// allocation is acceptable.
func (m *Metric) setLabels(labels []Label) {
	if len(labels) == 0 {
		return
	}
	cp := append([]Label(nil), labels...)
	m.value.lastLabels.Store(&cp)
}

// Snapshot returns a point-in-time view of the metric's current value(s).
func (m *Metric) Snapshot() MetricSnapshot {
	s := MetricSnapshot{
		Name:        m.Name,
		Type:        m.Type.String(),
		Description: m.Description,
		Labels:      m.Labels,
		Chart:       m.Type.ChartType(),
		UpdatedAt:   m.value.last.Load(),
	}
	if p := m.value.lastLabels.Load(); p != nil {
		s.LabelValues = *p
	}
	switch m.Type {
	case CounterType:
		s.Value = float64(m.value.counter.Load()) / scaleFactor
	case GaugeType:
		s.Value = float64(m.value.gauge.Load()) / scaleFactor
	case HistogramType:
		s.Count = m.value.count.Load()
		s.Sum = float64(m.value.sum.Load()) / scaleFactor
		if s.Count > 0 {
			s.Avg = s.Sum / float64(s.Count)
		}
		s.Buckets = m.histogramBuckets()
	}
	s.GaugeMin = m.gaugeMin
	s.GaugeMax = m.gaugeMax
	return s
}

func (m *Metric) histogramBuckets() []BucketSnapshot {
	m.value.mu.RLock()
	defer m.value.mu.RUnlock()
	out := make([]BucketSnapshot, 0, len(m.value.bounds)+1)
	for _, b := range m.value.bounds {
		if c, ok := m.value.hist[b]; ok {
			out = append(out, BucketSnapshot{UpperBound: b, Count: c.Load()})
		}
	}
	out = append(out, BucketSnapshot{UpperBound: 0, Count: m.value.overflow.Load(), Overflow: true})
	return out
}

// histOverflowBound marks the +Inf bucket in per-window and stored bucket
// slices (aggregator.add / orderedBuckets). It must survive encoding/json —
// the SQLite store persists bucket slices verbatim — so +Inf itself is
// unusable.
// ponytail: any finite sentinel can in principle collide with a user
// registered bound of the same value; removing that entirely needs an
// overflow flag on the stored bucket row.
const histOverflowBound = math.MaxFloat64

// MetricSnapshot is a serializable view of a metric, used by the UI and OTLP.
// The JSON tags are the stable public API contract for /metrics/data.
type MetricSnapshot struct {
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Description string           `json:"description,omitempty"`
	Labels      []string         `json:"labels,omitempty"`       // declared label names
	LabelValues []Label          `json:"label_values,omitempty"` // last observed label values (display only)
	Chart       string           `json:"chart,omitempty"`
	Value       float64          `json:"value"`
	Count       int64            `json:"count,omitempty"`
	Sum         float64          `json:"sum,omitempty"`
	Avg         float64          `json:"avg,omitempty"`
	Buckets     []BucketSnapshot `json:"buckets,omitempty"`
	UpdatedAt   int64            `json:"updated_at,omitempty"`
	GaugeMin    float64          `json:"gauge_min,omitempty"` // minimum value for gauge scale
	GaugeMax    float64          `json:"gauge_max,omitempty"` // maximum value for gauge scale
}

// BucketSnapshot is one histogram bucket.
type BucketSnapshot struct {
	UpperBound float64 `json:"upper_bound"`
	Count      int64   `json:"count"`
	Overflow   bool    `json:"overflow"`
}

// DefaultHistogramBounds used when a histogram is registered without bounds.
var DefaultHistogramBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
