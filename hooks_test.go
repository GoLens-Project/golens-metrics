package golens

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFluentHookRecordsCustomMetric(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	mw := r.On("user_signup_event").
		Type(CounterType).
		Description("user signups").
		Labels("plan").
		Extract(func(req *http.Request) (float64, []Label) {
			return 1, []Label{{Name: "plan", Value: req.URL.Query().Get("plan")}}
		})

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("POST", "/signup?plan=pro", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	s, ok := r.Snapshot("user_signup_event")
	if !ok {
		t.Fatal("custom metric not registered")
	}
	if s.Value != 1 {
		t.Errorf("custom metric value = %v, want 1", s.Value)
	}
	if s.Description != "user signups" {
		t.Errorf("description = %q", s.Description)
	}
}

func TestFluentHookHistogramWithBounds(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	mw := r.On("order_value").
		Type(HistogramType).
		Bounds(10, 50, 100).
		Extract(func(req *http.Request) (float64, []Label) {
			return 42, nil
		})

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("GET", "/order", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	s, _ := r.Snapshot("order_value")
	if s.Count != 1 || s.Sum != 42 {
		t.Errorf("histogram = count %d sum %v", s.Count, s.Sum)
	}
}

func TestFluentHookSkipsExcluded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ExcludePatterns = []string{"^/skip$"}
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	mw := r.On("m").Extract(func(*http.Request) (float64, []Label) { return 1, nil })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))

	req := httptest.NewRequest("GET", "/skip", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	if s, _ := r.Snapshot("m"); s.Value != 0 {
		t.Errorf("excluded hook recorded: %v", s.Value)
	}
}

func TestRequestCountMiddleware(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.RequestCountMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	}
	waitForDrain(r)
	s, _ := r.Snapshot("http_requests_total")
	if s.Value != 3 {
		t.Errorf("count = %v, want 3", s.Value)
	}
}

// The dashboard's HTTP status card reads fixed per-class child counters by
// exact name; a mount of the convenience chain that fed only the labeled
// parent rendered a live total over zeroed 2xx-5xx columns. The chain must
// record the same children the canonical middleware does, so the card's
// breakdown populates (this failed before RequestCountMiddleware wrapped the
// writer and recorded the class counters), and the columns must sum to the
// parent total.
func TestRequestCountMiddlewareFeedsHTTPCardBreakdown(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	statuses := map[string]int{"/ok": 200, "/moved": 301, "/missing": 404, "/boom": 500}
	h := r.RequestCountMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(statuses[req.URL.Path])
	}))
	for path := range statuses {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil)) // second 2xx
	waitForDrain(r)

	s, _ := r.Snapshot("http_requests_total")
	if s.Value != 5 {
		t.Fatalf("count = %v, want 5", s.Value)
	}
	want := map[string]float64{
		"http_requests_total_2xx": 2,
		"http_requests_total_3xx": 1,
		"http_requests_total_4xx": 1,
		"http_requests_total_5xx": 1,
	}
	for name, wantN := range want {
		if got, _ := r.Snapshot(name); got.Value != wantN {
			t.Errorf("%s = %v, want %v", name, got.Value, wantN)
		}
	}

	cards := buildHTTPCard(r.Snapshots())
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	colMap := map[string]float64{}
	var classSum float64
	for _, col := range cards[0].HTTPColumns {
		colMap[col.Label] = col.Value
		if col.Label != "total" {
			classSum += col.Value
		}
	}
	for label, wantN := range map[string]float64{"2xx": 2, "3xx": 1, "4xx": 1, "5xx": 1, "total": 5} {
		if colMap[label] != wantN {
			t.Errorf("column %s = %v, want %v", label, colMap[label], wantN)
		}
	}
	if classSum != colMap["total"] {
		t.Errorf("class columns sum to %v, want the parent total %v", classSum, colMap["total"])
	}
}

// The three-piece chain must record exactly what the canonical Middleware
// records — counts plus per-class children here, duration plus the endpoint
// histogram in the latency member, errors in the error member — so nothing is
// double-counted across members.
func TestConvenienceChainMatchesCanonicalMiddleware(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/missing" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	h := r.RequestCountMiddleware(r.LatencyMiddleware(r.ErrorRateMiddleware(next)))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/missing", nil))
	waitForDrain(r)

	want := map[string]float64{
		"http_requests_total":       3,
		"http_requests_total_2xx":   2,
		"http_requests_total_4xx":   1,
		"http_request_errors_total": 1,
	}
	for name, wantN := range want {
		if got, _ := r.Snapshot(name); got.Value != wantN {
			t.Errorf("%s = %v, want %v", name, got.Value, wantN)
		}
	}
	if dur, _ := r.Snapshot("http_request_duration_seconds"); dur.Count != 3 {
		t.Errorf("duration count = %d, want 3", dur.Count)
	}
	// One latency sample per request across the whole chain: the endpoint
	// histogram must not be observed twice.
	var observed int64
	for _, e := range r.endpoints.Snapshots() {
		observed += e.Count
	}
	if observed != 3 {
		t.Errorf("endpoint observations = %d, want 3", observed)
	}
}

func TestLatencyMiddleware(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.LatencyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	waitForDrain(r)
	s, _ := r.Snapshot("http_request_duration_seconds")
	if s.Count != 1 {
		t.Errorf("count = %d, want 1", s.Count)
	}
}

func TestErrorRateMiddleware(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.ErrorRateMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(404) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	waitForDrain(r)
	s, _ := r.Snapshot("http_request_errors_total")
	if s.Value != 1 {
		t.Errorf("errors = %v, want 1", s.Value)
	}
}

func TestHookBuilderMinMax(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	mw := r.On("gauge_metric").
		Type(GaugeType).
		Min(10).
		Max(90).
		Extract(func(req *http.Request) (float64, []Label) {
			return 50, nil
		})

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	waitForDrain(r)

	s, ok := r.Snapshot("gauge_metric")
	if !ok {
		t.Fatal("gauge_metric not registered")
	}
	if s.GaugeMin != 10 {
		t.Errorf("GaugeMin = %v, want 10", s.GaugeMin)
	}
	if s.GaugeMax != 90 {
		t.Errorf("GaugeMax = %v, want 90", s.GaugeMax)
	}
}
