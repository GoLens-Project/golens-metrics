package golens

import (
	"net/http"
	"time"
)

// HookBuilder provides a fluent API for registering domain-specific metrics.
//
//	golens.On("user_signup_event").
//	    Type(CounterType).
//	    Description("user signups").
//	    Labels("plan", "source").
//	    Extract(func(r *http.Request) (float64, []Label) {
//	        return 1, []Label{
//	            {Name: "plan", Value: r.FormValue("plan")},
//	            {Name: "source", Value: r.Header.Get("X-Source")},
//	        }
//	    })
//
// The builder and the middleware-chain middleware both feed into the same
// Registry; both styles are supported.
type HookBuilder struct {
	registry *Registry
	name     string
	mtype    MetricType
	desc     string
	labels   []string
	extract  ExtractFunc
	bounds   []float64 // histogram bounds
	gaugeMax float64   // gauge maximum value (0 = auto)
	gaugeMin float64   // gauge minimum value (default 0)
}

// ExtractFunc returns the value to record and any labels for a given request.
type ExtractFunc func(*http.Request) (float64, []Label)

// On begins a fluent hook registration for the named metric.
func (r *Registry) On(name string) *HookBuilder {
	return &HookBuilder{
		registry: r,
		name:     name,
		mtype:    CounterType,
	}
}

// Type sets the metric kind.
func (h *HookBuilder) Type(t MetricType) *HookBuilder {
	h.mtype = t
	return h
}

// Description sets the human-readable description.
func (h *HookBuilder) Description(d string) *HookBuilder {
	h.desc = d
	return h
}

// Labels declares the label names this metric carries.
func (h *HookBuilder) Labels(names ...string) *HookBuilder {
	h.labels = names
	return h
}

// Bounds sets histogram bucket boundaries (histograms only).
func (h *HookBuilder) Bounds(b ...float64) *HookBuilder {
	h.bounds = b
	return h
}

// Max sets the maximum value for gauges (0 = auto-scale).
func (h *HookBuilder) Max(max float64) *HookBuilder {
	h.gaugeMax = max
	return h
}

// Min sets the minimum value for gauges (default 0).
func (h *HookBuilder) Min(min float64) *HookBuilder {
	h.gaugeMin = min
	return h
}

// Extract attaches the value extraction function and finalizes registration,
// returning an http.Handler middleware that records the metric when applied.
func (h *HookBuilder) Extract(fn ExtractFunc) func(http.Handler) http.Handler {
	h.extract = fn
	h.registry.Register(h.name, h.mtype, h.desc, h.labels, h.bounds, h.gaugeMin, h.gaugeMax)
	name := h.name
	registry := h.registry
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if registry.shouldTrack(r.URL.Path) {
				v, labels := fn(r)
				registry.Record(name, v, labels...)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Convenience chain middlewares (the "middleware chain" style). Together the
// three record every series the canonical Middleware records — counts plus
// per-class children here, duration plus the endpoint histogram in
// LatencyMiddleware, errors in ErrorRateMiddleware — so any mount of the
// chain feeds the dashboard's HTTP status breakdown. The chain's labeled
// parent omits the status label the canonical one carries.
func (r *Registry) RequestCountMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.shouldTrack(req.URL.Path) {
			next.ServeHTTP(w, req)
			return
		}
		rec := newStatusRecorder(w)
		next.ServeHTTP(rec, req)
		r.Record("http_requests_total", 1,
			Label{Name: "method", Value: req.Method},
			Label{Name: "path", Value: req.URL.Path},
		)
		if class := statusClassName(rec.status); class != "" {
			r.Record("http_requests_total_"+class, 1)
		}
	})
}

func (r *Registry) LatencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, req)
		if r.shouldTrack(req.URL.Path) {
			duration := time.Since(start).Seconds()
			r.Record("http_request_duration_seconds", duration,
				Label{Name: "method", Value: req.Method},
				Label{Name: "path", Value: req.URL.Path},
			)
			r.endpoints.Observe(req.Method, req.URL.Path, duration)
		}
	})
}

func (r *Registry) ErrorRateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := newStatusRecorder(w)
		next.ServeHTTP(rec, req)
		if rec.status >= 400 && r.shouldTrack(req.URL.Path) {
			r.Record("http_request_errors_total", 1,
				Label{Name: "method", Value: req.Method},
				Label{Name: "path", Value: req.URL.Path},
			)
		}
	})
}
