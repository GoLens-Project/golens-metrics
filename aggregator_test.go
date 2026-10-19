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

// Histogram windows record per-window deltas, not lifetime totals: over a
// growing counter, consecutive windows show the new samples' distribution
// instead of plateauing at the same lifetime counts.
func TestAggregatorHistogramDeltaWindows(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	r := &Registry{storage: storage, ctx: context.Background(), cfg: DefaultConfig()}
	m := newMetric("lat", HistogramType, "", nil, []float64{1}, 0, 0)

	m.Record(0.5) // ≤1 bucket
	m.Record(2)   // overflow bucket
	a.add("lat", 0.5, m)
	a.flushAll(r)

	m.Record(0.5) // lifetime ≤1 count grows to 2; overflow stays 1
	a.add("lat", 0.5, m)
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "lat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d stored windows, want 2", len(res))
	}
	// Each window gained exactly one ≤1 sample — window 2 as a delta of 1
	// over the baseline, never the lifetime count of 2. Exactly one window
	// saw the overflow sample.
	for _, agg := range res {
		for _, b := range agg.HistogramBuckets {
			if b.UpperBound == 1 && b.Count != 1 {
				t.Errorf("window bucket bound 1: count = %d, want per-window delta 1 (lifetime total is 2)", b.Count)
			}
		}
	}
	overflowWindows := 0
	for _, agg := range res {
		for _, b := range agg.HistogramBuckets {
			if b.UpperBound == 0 && b.Count > 0 {
				overflowWindows++
			}
		}
	}
	if overflowWindows != 1 {
		t.Errorf("windows carrying the overflow sample = %d, want 1", overflowWindows)
	}
}

// A re-registered histogram (fresh instance whose counters restarted at 0)
// resets the delta baseline: its first window counts its samples fully,
// instead of netting them against the old instance's last-seen values.
func TestAggregatorHistogramReregisterResetsBaseline(t *testing.T) {
	storage := newMemoryStorage(0)
	a := newAggregator(storage, time.Millisecond)
	r := &Registry{storage: storage, ctx: context.Background(), cfg: DefaultConfig()}

	m1 := newMetric("lat", HistogramType, "", nil, []float64{1}, 0, 0)
	m1.Record(0.5)
	a.add("lat", 0.5, m1)
	a.flushAll(r)

	m2 := newMetric("lat", HistogramType, "", nil, []float64{1}, 0, 0)
	m2.Record(0.5)
	a.add("lat", 0.5, m2) // baseline reset: delta 1, not 1-1=0
	a.flushAll(r)

	res, err := storage.Query(context.Background(), Query{Name: "lat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d stored windows, want 2", len(res))
	}
	for i, agg := range res {
		found := false
		for _, b := range agg.HistogramBuckets {
			if b.UpperBound == 1 {
				found = true
				if b.Count != 1 {
					t.Errorf("window %d bound 1: count = %d, want 1 (baseline resets on the new instance)", i, b.Count)
				}
			}
		}
		if !found {
			t.Errorf("window %d missing bound-1 bucket: %+v", i, agg.HistogramBuckets)
		}
	}
}
