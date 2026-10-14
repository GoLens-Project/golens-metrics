package golens

import (
	"context"
	"time"
)

// HistogramBucket represents a single histogram bucket's count at a specific boundary.
type HistogramBucket struct {
	UpperBound float64 `json:"upper_bound"`
	Count      int64   `json:"count"`
}

// AggregatedMetric is a roll-up bucket written to a persistence backend.
type AggregatedMetric struct {
	Name             string
	Type             string
	Labels           map[string]string
	Count            int64
	Sum              float64
	Min              float64
	Max              float64
	WindowStart      time.Time
	WindowEnd        time.Time
	HistogramBuckets []HistogramBucket // Histogram bucket distributions (only for histogram types)
}

// Query filters historical roll-ups.
type Query struct {
	Name  string
	From  time.Time
	To    time.Time
	Limit int
}

// Storage is the persistence abstraction. The in-memory implementation is the
// default; SQLite is the optional summary store. Additional backends can
// satisfy this interface in the future (plugin architecture, deferred).
type Storage interface {
	Store(ctx context.Context, m AggregatedMetric) error
	Query(ctx context.Context, q Query) ([]AggregatedMetric, error)
	Close() error
}
