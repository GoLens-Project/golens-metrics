package golens

import (
	"net/http"
	"strconv"
	"time"
)

// statusRecorder captures the response status code for RED collection.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Middleware is the canonical HTTP middleware. It records RED metrics for
// every request that passes the include/exclude filters. It is compatible
// with any router that accepts a standard func(http.Handler) http.Handler
// (gorilla/mux, chi, gin via adapter, stdlib, etc.).
func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.shouldTrack(req.URL.Path) {
			next.ServeHTTP(w, req)
			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(rec, req)
		duration := time.Since(start).Seconds()

		labels := []Label{
			{Name: "method", Value: req.Method},
			{Name: "path", Value: req.URL.Path},
			{Name: "status", Value: strconv.Itoa(rec.status)},
		}

		r.Record("http_requests_total", 1, labels...)

		// Per-class child counters (fixed names, zero label cardinality) power
		// the dashboard's HTTP status breakdown. The labeled parent carries
		// only the last-seen label set, so the breakdown cannot be derived
		// from it. Statuses outside 2xx-5xx (e.g. 1xx) land in the parent
		// total only.
		if class := statusClassName(rec.status); class != "" {
			r.Record("http_requests_total_"+class, 1)
		}

		if rec.status >= 400 {
			r.Record("http_request_errors_total", 1,
				Label{Name: "method", Value: req.Method},
				Label{Name: "path", Value: req.URL.Path},
			)
		}
		r.Record("http_request_duration_seconds", duration,
			Label{Name: "method", Value: req.Method},
			Label{Name: "path", Value: req.URL.Path},
		)
		// Per-endpoint latency histogram (cardinality-bounded via path
		// normalization) powering the endpoint percentile chart.
		r.endpoints.Observe(req.Method, req.URL.Path, duration)
	})
}

// statusClassName maps a response status to its 2xx-5xx class name, used in
// the fixed http_requests_total_<class> child counters. Empty for statuses
// outside those classes.
func statusClassName(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	}
	return ""
}

// MiddlewareFunc is the same middleware exposed as a standalone function for
// routers that prefer a free function over a method.
func (r *Registry) MiddlewareFunc(next http.Handler) http.Handler {
	return r.Middleware(next)
}
