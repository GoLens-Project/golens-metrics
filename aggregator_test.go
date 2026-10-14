package golens

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAggregatorTracksWindow(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)

	a.add("m", 2, nil)
	a.add("m", 4, nil)
	a.add("m", 6, nil)

	// Peek into the window directly.
	a.mu.Lock()
	w := a.windows["m"]
	a.mu.Unlock()
	if w == nil || w.count != 3 || w.sum != 12 || w.min != 2 || w.max != 6 {
		t.Errorf("window = %+v", w)
	}
}

func TestAggregatorFlushAllWritesToStorage(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	r := &Registry{storage: storage, ctx: context.Background(), cfg: DefaultConfig()}

	a.add("flushed", 1, nil)
	a.add("flushed", 3, nil)
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "flushed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(res))
	}
	if res[0].Count != 2 || res[0].Sum != 4 {
		t.Errorf("stored = %+v", res[0])
	}
	if res[0].Min != 1 || res[0].Max != 3 {
		t.Errorf("min/max = %v/%v", res[0].Min, res[0].Max)
	}
}

func TestAggregatorFlushAllEmpty(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	r := &Registry{storage: storage, ctx: context.Background(), cfg: DefaultConfig()}
	a.flushAll(r) // no windows: no-op, no panic
	res, _ := storage.Query(context.Background(), Query{})
	if len(res) != 0 {
		t.Errorf("expected empty, got %d", len(res))
	}
}

func TestAggregatorConcurrent(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.add("c", 1, nil)
		}()
	}
	wg.Wait()
	a.mu.Lock()
	w := a.windows["c"]
	a.mu.Unlock()
	if w.count != 100 {
		t.Errorf("count = %d, want 100", w.count)
	}
}

// histCount returns a bucket's count in a stored roll-up, or -1 if absent.
func histCount(agg AggregatedMetric, bound float64) int64 {
	for _, b := range agg.HistogramBuckets {
		if b.UpperBound == bound {
			return b.Count
		}
	}
	return -1
}

func newHistAggregator(t *testing.T) (*aggregator, *memoryStorage, *Registry) {
	t.Helper()
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	r := &Registry{storage: storage, ctx: context.Background(), cfg: DefaultConfig()}
	return a, storage, r
}

func TestAggregatorHistogramPerWindowDeltas(t *testing.T) {
	bounds := []float64{1, 5, 10}
	m := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	a, storage, r := newHistAggregator(t)

	m.Record(2)
	a.add("h", 2, m)
	a.flushAll(r)

	m.Record(2)
	m.Record(2)
	a.add("h", 2, m)
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 windows, got %d", len(res))
	}
	// Each window holds only its own observations, not the growing lifetime
	// total: window 1 saw one sample in bound 5, window 2 saw two more.
	if got := histCount(res[0], 5); got != 1 {
		t.Errorf("window 1 bound-5 count = %d, want 1", got)
	}
	if got := histCount(res[1], 5); got != 2 {
		t.Errorf("window 2 bound-5 count = %d, want 2 (per-window delta)", got)
	}
}

func TestAggregatorHistogramCounterRestart(t *testing.T) {
	bounds := []float64{1, 5, 10}
	m := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	a, storage, r := newHistAggregator(t)

	m.Record(2)
	m.Record(2)
	m.Record(2)
	a.add("h", 2, m)
	a.flushAll(r)

	// Metric evicted and re-registered: counters restart at zero and fall
	// below the aggregator's last-seen value. The delta must clamp to the
	// current counter instead of going negative.
	fresh := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	fresh.Record(2)
	a.add("h", 2, fresh)
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 windows, got %d", len(res))
	}
	if got := histCount(res[1], 5); got != 1 {
		t.Errorf("window 2 bound-5 count = %d, want 1 (clamped delta)", got)
	}
}

func TestAggregatorHistogramCounterRestartCaughtUp(t *testing.T) {
	bounds := []float64{1, 5, 10}
	m := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	a, storage, r := newHistAggregator(t)

	m.Record(2) // old lifetime: last-seen bound-5 counter = 1
	a.add("h", 2, m)
	a.flushAll(r)

	// Re-registered metric whose new counter grows PAST the old last-seen
	// value: value-delta accounting would record 5-1=4; identity reset must
	// record the full 5.
	fresh := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	for i := 0; i < 5; i++ {
		fresh.Record(2)
	}
	a.add("h", 2, fresh)
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 windows, got %d", len(res))
	}
	if got := histCount(res[1], 5); got != 5 {
		t.Errorf("window 2 bound-5 count = %d, want 5 (identity reset, not value delta)", got)
	}
}

func TestAggregatorPartial(t *testing.T) {
	bounds := []float64{1, 5, 10}
	m := newMetric("h", HistogramType, "test", nil, bounds, 0, 0)
	a, _, _ := newHistAggregator(t)

	if _, ok := a.partial("h"); ok {
		t.Error("partial returned ok for unknown metric")
	}

	m.Record(2)
	m.Record(8)
	a.add("h", 2, m)
	a.add("h", 8, m)

	pw, ok := a.partial("h")
	if !ok {
		t.Fatal("partial returned !ok for open window")
	}
	if pw.Name != "h" || pw.Count != 2 || pw.Sum != 10 || pw.Min != 2 || pw.Max != 8 {
		t.Errorf("partial = %+v", pw)
	}
	// 3 finite bounds + overflow bucket.
	if len(pw.HistogramBuckets) != 4 {
		t.Fatalf("partial buckets = %+v, want 4 entries", pw.HistogramBuckets)
	}
	if got := histCount(pw, 5); got != 1 {
		t.Errorf("partial bound-5 count = %d, want 1", got)
	}
	if got := histCount(pw, 10); got != 1 {
		t.Errorf("partial bound-10 count = %d, want 1", got)
	}

	a.flushAll(&Registry{storage: newMemoryStorage(0), ctx: context.Background(), cfg: DefaultConfig()})
	if _, ok := a.partial("h"); ok {
		t.Error("partial returned ok after flush cleared the window")
	}
}
