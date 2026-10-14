package golens

import (
	"context"
	"log"
	"sync"
	"time"
)

// aggregator maintains in-flight tumbling-window roll-ups. On each flush it
// computes count/sum/min/max per metric window and writes the result to the
// configured Storage, then starts a fresh window.
type aggregator struct {
	mu      sync.Mutex
	windows map[string]*window
	// lastHist holds the last-seen lifetime counter value per bucket for each
	// histogram metric, so windows record per-window deltas instead of
	// lifetime totals. State is tied to the *Metric instance so a
	// re-registered metric (always a fresh pointer) resets its baseline
	// instead of undercounting. Guarded by mu. Entries linger for evicted
	// metrics and the map grows one entry per distinct name ever seen — small
	// int64 maps only; cleaned up only if churn ever makes that a real cost.
	lastHist map[string]*histState
}

// histState pairs a metric's per-bucket last-seen lifetime counts with the
// instance they were observed on, so re-registration resets the baseline.
type histState struct {
	m    *Metric
	last map[float64]int64
}

type window struct {
	count            int64
	sum              float64
	min              float64
	max              float64
	start            time.Time
	histogramBuckets map[float64]int64 // histogram bucket counts (boundary -> count)
}

func newAggregator(_ Storage, _ time.Duration) *aggregator {
	return &aggregator{windows: make(map[string]*window), lastHist: make(map[string]*histState)}
}

func (a *aggregator) add(name string, v float64, m *Metric) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w, ok := a.windows[name]
	if !ok {
		w = &window{start: time.Now(), min: v, max: v, histogramBuckets: make(map[float64]int64)}
		a.windows[name] = w
	}
	if v < w.min {
		w.min = v
	}
	if v > w.max {
		w.max = v
	}
	w.count++
	w.sum += v

	// Capture histogram bucket counts for histogram metrics. Buckets hold the
	// window's delta (current lifetime value minus last-seen value), not the
	// lifetime total, so consecutive windows show the distribution evolve
	// instead of plateauing at the same growing totals.
	if m != nil && m.Type == HistogramType {
		m.value.mu.RLock()
		state := a.lastHist[name]
		if state == nil || state.m != m {
			// New metric or a re-registered one (fresh instance whose
			// counters restarted at 0): baseline from zero, all current
			// counts belong to this window.
			state = &histState{m: m, last: make(map[float64]int64)}
			a.lastHist[name] = state
		}
		for bound, counter := range m.value.hist {
			cur := counter.Load()
			delta := cur - state.last[bound]
			if delta < 0 {
				delta = cur // defensive: counter went backwards on this instance
			}
			state.last[bound] = cur
			w.histogramBuckets[bound] += delta
		}
		m.value.mu.RUnlock()
	}
}

// buckets converts the window's histogram counts to storage slice format.
func (w *window) buckets() []HistogramBucket {
	out := make([]HistogramBucket, 0, len(w.histogramBuckets))
	for bound, count := range w.histogramBuckets {
		out = append(out, HistogramBucket{UpperBound: bound, Count: count})
	}
	return out
}

// partial returns a snapshot of the metric's currently open (unflushed)
// window, if one exists, so history reads can include the in-flight point.
func (a *aggregator) partial(name string) (AggregatedMetric, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w, ok := a.windows[name]
	if !ok {
		return AggregatedMetric{}, false
	}
	return AggregatedMetric{
		Name:             name,
		Count:            w.count,
		Sum:              w.sum,
		Min:              w.min,
		Max:              w.max,
		WindowStart:      w.start,
		WindowEnd:        time.Now(),
		HistogramBuckets: w.buckets(),
	}, true
}

// flushAll materializes every open window into the storage backend.
func (a *aggregator) flushAll(r *Registry) {
	a.mu.Lock()
	wins := a.windows
	a.windows = make(map[string]*window, len(wins))
	a.mu.Unlock()

	if len(wins) == 0 {
		return
	}
	now := time.Now()
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for name, w := range wins {
		agg := AggregatedMetric{
			Name:             name,
			Count:            w.count,
			Sum:              w.sum,
			Min:              w.min,
			Max:              w.max,
			WindowStart:      w.start,
			WindowEnd:        now,
			HistogramBuckets: w.buckets(),
		}
		if err := r.storage.Store(ctx, agg); err != nil {
			if r.debug {
				log.Printf("[golens] storage flush failed for %s: %v", name, err)
			}
		}
	}
}
