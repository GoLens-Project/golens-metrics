package golens

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func startTestRegistry(t *testing.T) (*Registry, func()) {
	t.Helper()
	r := newTestRegistry(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cleanup := func() {
		cancel()
		r.Close()
	}
	return r, cleanup
}

func TestMiddlewareRecordsRequestCount(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/orders", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	waitForDrain(r)

	s, _ := r.Snapshot("http_requests_total")
	if s.Value != 1 {
		t.Errorf("requests_total = %v, want 1", s.Value)
	}
}

func TestMiddlewareRecordsErrors(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(500)
	}))

	req := httptest.NewRequest("GET", "/fail", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	errs, _ := r.Snapshot("http_request_errors_total")
	if errs.Value != 1 {
		t.Errorf("errors_total = %v, want 1", errs.Value)
	}
}

func TestMiddlewareRecordsDuration(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/slow", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	dur, _ := r.Snapshot("http_request_duration_seconds")
	if dur.Count != 1 {
		t.Errorf("duration count = %d, want 1", dur.Count)
	}
	if dur.Sum <= 0 {
		t.Errorf("duration sum = %v, want > 0", dur.Sum)
	}
}

func TestMiddlewareExcludesPaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ExcludePatterns = []string{"^/health$"}
	r := newTestRegistry(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() { cancel(); r.Close() }()

	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	s, _ := r.Snapshot("http_requests_total")
	if s.Value != 0 {
		t.Errorf("excluded path recorded: value = %v", s.Value)
	}
}

func TestMiddlewarePassesThroughBody(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("POST", "/create", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestMiddlewareFuncEquivalent(t *testing.T) {
	r, cleanup := startTestRegistry(t)
	defer cleanup()

	h := r.MiddlewareFunc(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	waitForDrain(r)

	s, _ := r.Snapshot("http_requests_total")
	if s.Value != 1 {
		t.Errorf("MiddlewareFunc did not record: %v", s.Value)
	}
}

func TestStatusClassName(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{100, ""}, {199, ""}, // informational statuses have no class counter
		{200, "2xx"}, {204, "2xx"}, {299, "2xx"},
		{301, "3xx"}, {399, "3xx"},
		{400, "4xx"}, {404, "4xx"}, {499, "4xx"},
		{500, "5xx"}, {503, "5xx"}, {599, "5xx"},
		{600, ""},
	}
	for _, tc := range tests {
		if got := statusClassName(tc.status); got != tc.want {
			t.Errorf("statusClassName(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// The middleware maintains the fixed per-class child counters that power the
// dashboard's HTTP status breakdown; statuses outside 2xx-5xx land in the
// parent total only.
func TestMiddlewareRecordsStatusClassCounters(t *testing.T) {
	tests := []struct {
		status    int
		wantClass string
	}{
		{200, "2xx"},
		{302, "3xx"},
		{404, "4xx"},
		{500, "5xx"},
		{101, ""}, // switching protocols: parent total only
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			r, cleanup := startTestRegistry(t)
			defer cleanup()

			h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.status)
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/orders", nil))
			waitForDrain(r)

			total, _ := r.Snapshot("http_requests_total")
			if total.Value != 1 {
				t.Errorf("http_requests_total = %v, want 1", total.Value)
			}
			for _, class := range []string{"2xx", "3xx", "4xx", "5xx"} {
				want := 0.0
				if class == tc.wantClass {
					want = 1
				}
				s, _ := r.Snapshot("http_requests_total_" + class)
				if s.Value != want {
					t.Errorf("http_requests_total_%s = %v, want %v", class, s.Value, want)
				}
			}
		})
	}
}
