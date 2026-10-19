package golens

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryStorageStoreAndQuery(t *testing.T) {
	s := newMemoryStorage(0)
	ctx := context.Background()

	now := time.Now()
	a := AggregatedMetric{Name: "m", Count: 3, Sum: 6, WindowStart: now, WindowEnd: now.Add(time.Minute)}
	if err := s.Store(ctx, a); err != nil {
		t.Fatal(err)
	}

	res, err := s.Query(ctx, Query{Name: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Count != 3 {
		t.Errorf("query = %+v", res)
	}
}

func TestMemoryStorageQueryByNameFilters(t *testing.T) {
	s := newMemoryStorage(0)
	ctx := context.Background()
	s.Store(ctx, AggregatedMetric{Name: "a"})
	s.Store(ctx, AggregatedMetric{Name: "b"})

	res, _ := s.Query(ctx, Query{Name: "a"})
	if len(res) != 1 {
		t.Errorf("want 1, got %d", len(res))
	}
}

func TestMemoryStorageQueryByWindow(t *testing.T) {
	s := newMemoryStorage(0)
	ctx := context.Background()
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)
	s.Store(ctx, AggregatedMetric{Name: "x", WindowStart: t0, WindowEnd: t1})
	s.Store(ctx, AggregatedMetric{Name: "x", WindowStart: t2, WindowEnd: t2.Add(time.Second)})

	res, _ := s.Query(ctx, Query{Name: "x", From: time.Unix(2500, 0), To: time.Unix(3500, 0)})
	if len(res) != 1 {
		t.Errorf("window query = %d, want 1", len(res))
	}
}

func TestMemoryStorageRingDrop(t *testing.T) {
	s := newMemoryStorage(0)
	s.max = 2
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s.Store(ctx, AggregatedMetric{Name: "m"})
	}
	res, _ := s.Query(ctx, Query{})
	if len(res) != 2 {
		t.Errorf("after overflow want 2, got %d", len(res))
	}
}

func TestSQLiteStorageRoundTrip(t *testing.T) {
	s, err := newSQLiteStorage(filepathJoin(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	now := time.Now()
	m := AggregatedMetric{
		Name: "http_requests_total", Type: "counter",
		Labels: map[string]string{"path": "/x"},
		Count:  4, Sum: 4, Min: 1, Max: 1,
		WindowStart: now, WindowEnd: now.Add(time.Minute),
	}
	if err := s.Store(ctx, m); err != nil {
		t.Fatalf("store: %v", err)
	}
	res, err := s.Query(ctx, Query{Name: "http_requests_total"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res) != 1 || res[0].Count != 4 {
		t.Errorf("result = %+v", res)
	}
	if res[0].Labels["path"] != "/x" {
		t.Errorf("labels = %+v", res[0].Labels)
	}
}

func TestSQLiteStorageQueryWindow(t *testing.T) {
	s, err := newSQLiteStorage(filepathJoin(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	s.Store(ctx, AggregatedMetric{Name: "n", WindowStart: time.Unix(100, 0), WindowEnd: time.Unix(200, 0)})
	s.Store(ctx, AggregatedMetric{Name: "n", WindowStart: time.Unix(500, 0), WindowEnd: time.Unix(600, 0)})

	res, _ := s.Query(ctx, Query{Name: "n", From: time.Unix(300, 0), To: time.Unix(700, 0)})
	if len(res) != 1 {
		t.Errorf("window query = %d, want 1", len(res))
	}
}

func TestSQLiteStorageDefaultPath(t *testing.T) {
	// Should not error with empty path (uses default golens.db). Clean up after.
	s, err := newSQLiteStorage("")
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	s.Close()
}

func TestSQLiteStorageLimit(t *testing.T) {
	s, _ := newSQLiteStorage(filepathJoin(t.TempDir(), "t.db"))
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s.Store(ctx, AggregatedMetric{Name: "n"})
	}
	res, _ := s.Query(ctx, Query{Name: "n", Limit: 2})
	if len(res) != 2 {
		t.Errorf("limit query = %d, want 2", len(res))
	}
}

// filepathJoin is a tiny helper to keep imports tidy in tests.
func filepathJoin(parts ...string) string {
	out := ""
	for i, p := range parts {
		if i == 0 {
			out = p
		} else {
			out += "/" + p
		}
	}
	return out
}

// Legacy rows persisted before the snake_case rename carry TitleCase keys
// ("UpperBound", "Count"). JSON key matching folds case but not underscores,
// so plain tags would decode the legacy bound as 0 — the boundary silently
// lost. The decode must accept both key styles, prefer the current keys when
// both are present, and leave null elements untouched.
func TestHistogramBucketUnmarshalLegacyKeys(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantLen        int
		wantBound      float64
		wantCount      int64
		wantErr        bool
		wantNullKeptAt int // index of a null element decoded as the zero bucket; -1 = none
	}{
		{
			name:      "pre-upgrade legacy keys",
			input:     `[{"UpperBound":5,"Count":7}]`,
			wantLen:   1,
			wantBound: 5, wantCount: 7,
			wantNullKeptAt: -1,
		},
		{
			name:      "current wire keys",
			input:     `[{"upper_bound":5,"count":7}]`,
			wantLen:   1,
			wantBound: 5, wantCount: 7,
			wantNullKeptAt: -1,
		},
		{
			name:      "mixed keys: current wins over legacy",
			input:     `[{"upper_bound":1,"UpperBound":5,"count":2,"Count":7}]`,
			wantLen:   1,
			wantBound: 1, wantCount: 2,
			wantNullKeptAt: -1,
		},
		{
			name:      "null element is a no-op",
			input:     `[{"UpperBound":5,"Count":7},null]`,
			wantLen:   2,
			wantBound: 5, wantCount: 7,
			wantNullKeptAt: 1,
		},
		{
			name:    "malformed json errors",
			input:   `[{"UpperBound":}]`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buckets []HistogramBucket
			err := json.Unmarshal([]byte(tc.input), &buckets)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("input %s: want error, got buckets %+v", tc.input, buckets)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal %s: %v", tc.input, err)
			}
			if len(buckets) != tc.wantLen {
				t.Fatalf("got %d buckets, want %d", len(buckets), tc.wantLen)
			}
			if buckets[0].UpperBound != tc.wantBound || buckets[0].Count != tc.wantCount {
				t.Errorf("bucket = %+v, want {bound %v, count %d}", buckets[0], tc.wantBound, tc.wantCount)
			}
			if tc.wantNullKeptAt >= 0 && buckets[tc.wantNullKeptAt] != (HistogramBucket{}) {
				t.Errorf("null element mutated: %+v", buckets[tc.wantNullKeptAt])
			}
		})
	}
}

// Marshaling always emits the current snake_case wire keys — the shape every
// post-upgrade persisted row carries. Pinning the exact bytes stops a future
// tag rename from silently re-breaking rows at rest.
func TestHistogramBucketMarshalWireKeys(t *testing.T) {
	out, err := json.Marshal([]HistogramBucket{{UpperBound: 5, Count: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `[{"upper_bound":5,"count":7}]` {
		t.Errorf("marshal = %s, want exact wire shape", got)
	}
}
