package golens

import (
	"context"
	"encoding/json"
	"time"
)

// HistogramBucket represents a single histogram bucket's count at a specific boundary.
type HistogramBucket struct {
	UpperBound float64 `json:"upper_bound"`
	Count      int64   `json:"count"`
}

// UnmarshalJSON accepts the legacy TitleCase keys ("UpperBound", "Count") that
// binaries predating the snake_case rename marshaled into persisted history
// rows, alongside the current snake_case keys. JSON key matching folds case
// but not underscores, so without this the legacy bound decodes as 0 and the
// bucket's boundary is silently lost. Marshaling is untouched and always
// emits the snake_case wire keys.
func (b *HistogramBucket) UnmarshalJSON(data []byte) error {
	var raw struct {
		Bound       *float64 `json:"upper_bound"`
		LegacyBound *float64 `json:"UpperBound"`
		Count       *int64   `json:"count"`
		LegacyCount *int64   `json:"Count"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Bound != nil {
		b.UpperBound = *raw.Bound
	} else if raw.LegacyBound != nil {
		b.UpperBound = *raw.LegacyBound
	}
	if raw.Count != nil {
		b.Count = *raw.Count
	} else if raw.LegacyCount != nil {
		b.Count = *raw.LegacyCount
	}
	return nil
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
