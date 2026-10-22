// Command stdlibmux is a minimal example of mounting GoLens on a stdlib
// net/http server. Run it and open http://localhost:8080/metrics.
//
// This example demonstrates histogram time-series visualization:
// - Call /conn-distribution to generate histogram data
// - View "Histogram Time-Series" section in the dashboard to see bucket distribution evolution
// - Select different time ranges (5m, 30m, 1h, etc.) to analyze distribution trends
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	golens "github.com/GoLens-Project/golens-metrics"
)

func main() {
	useMailer := flag.Bool("mailer", false, "enable log email notifier")
	flag.Parse()

	cfg := golens.DefaultConfig()
	cfg.ProjectName = "Example API"
	cfg.DashboardSubtitle = "Example Dashboard"
	cfg.Debug = os.Getenv("GOLENS_DEBUG") == "true"
	cfg.RuntimeMetrics.Enabled = true
	cfg.Alerts.Enabled = true
	cfg.Alerts.EvaluationInterval = 30 * time.Second
	if path := os.Getenv("GOLENS_CONFIG"); path != "" {
		if loaded, err := golens.LoadConfig(path); err == nil {
			cfg = loaded
		}
	}

	registry, err := golens.New(cfg)
	if err != nil {
		log.Fatalf("golens: %v", err)
	}

	if *useMailer {
		registry.SetEmailNotifier(golens.NewLogNotifier())
		log.Println("mailer enabled: email notifications will be logged to stdout")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := registry.Start(ctx); err != nil {
		log.Fatalf("start: %v", err)
	}
	defer registry.Close()

	mux := http.NewServeMux()
	registry.MountUI(mux)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// simulate work
		time.Sleep(5 * time.Millisecond)
		if r.URL.Query().Get("fail") == "1" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from your app\n"))
	})

	// Debug endpoint to check what metrics are registered
	mux.HandleFunc("/debug/metrics", func(w http.ResponseWriter, r *http.Request) {
		snaps := registry.Snapshots()
		w.Header().Set("Content-Type", "text/plain")
		for _, s := range snaps {
			fmt.Fprintf(w, "%s (%s): %v\n", s.Name, s.Type, s.Value)
			if s.Type == "histogram" {
				fmt.Fprintf(w, "  Buckets: %d\n", len(s.Buckets))
				for _, b := range s.Buckets {
					fmt.Fprintf(w, "    %.2f: %d\n", b.UpperBound, b.Count)
				}
			}
		}
	})

	// Custom counter metric via fluent hook.
	orderTracker := registry.On("orders_created").
		Type(golens.CounterType).
		Description("orders created").
		Labels("sku").
		Extract(func(req *http.Request) (float64, []golens.Label) {
			return 1, []golens.Label{{Name: "sku", Value: req.URL.Query().Get("sku")}}
		})
	mux.Handle("/order", orderTracker(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))

	// Custom gauge metric for CPU usage percentage (demonstrates semi-circle gauge).
	cpuUsage := registry.On("cpu_usage_percent").
		Type(golens.GaugeType).
		Description("current CPU usage percentage").
		Labels("source").
		Min(0).   // CPU percentage: 0-100%
		Max(100). // CPU percentage: 0-100%
		Extract(func(req *http.Request) (float64, []golens.Label) {
			// Simulate varying CPU usage for demo
			usage := 30.0 + float64((len(req.URL.Path)*7)%60) // 30-90% range
			return usage, []golens.Label{{Name: "source", Value: "app"}}
		})
	mux.Handle("/cpu", cpuUsage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("CPU usage tracked\n"))
	})))

	// Custom gauge metric for tracking active connections.
	activeConnections := registry.On("active_connections").
		Type(golens.GaugeType).
		Description("currently active connections").
		Labels("endpoint").
		Min(0).  // Minimum 0 connections
		Max(50). // Maximum 50 connections for demo
		Extract(func(req *http.Request) (float64, []golens.Label) {
			// Simulate tracking active connections by endpoint
			endpoint := req.URL.Path
			if endpoint == "" {
				endpoint = "/"
			}
			// In a real app, you'd track actual connection count
			// For demo, we return varied mock values to show gauge changes
			connCount := 5.0 + float64(len(endpoint)%10) // Vary by endpoint
			switch endpoint {
			case "/order":
				connCount = 12.0
			case "/connections":
				connCount = 8.0
			case "/size":
				connCount = 3.0
			}
			return connCount, []golens.Label{{Name: "endpoint", Value: endpoint}}
		})
	mux.Handle("/connections", activeConnections(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("active connections tracked\n"))
	})))

	// Custom histogram metric for tracking active connections distribution.
	// This shows the SAME data as the gauge but as distribution bars AND time-series.
	activeConnHist := registry.On("active_connections_distribution").
		Type(golens.HistogramType).
		Description("active connections distribution over time").
		Labels("endpoint").
		Bounds(1, 5, 10, 20, 35, 50). // Connection count buckets
		Extract(func(req *http.Request) (float64, []golens.Label) {
			// Track the SAME values as the gauge for demonstration
			endpoint := req.URL.Path
			if endpoint == "" {
				endpoint = "/"
			}
			connCount := 5.0 + float64(len(endpoint)%10) // Vary by endpoint
			switch endpoint {
			case "/order":
				connCount = 12.0
			case "/connections":
				connCount = 8.0
			case "/size":
				connCount = 3.0
			}
			log.Printf("[histogram] recording conn=%d for endpoint=%s", int(connCount), endpoint)
			return connCount, []golens.Label{{Name: "endpoint", Value: endpoint}}
		})
	mux.Handle("/conn-distribution", activeConnHist(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("connection distribution tracked\n"))
	})))

	// Custom histogram metric for request processing time.
	requestDuration := registry.On("request_processing_time_ms").
		Type(golens.HistogramType).
		Description("request processing time in milliseconds").
		Labels("endpoint").
		Bounds(10, 25, 50, 100, 250, 500, 1000). // Millisecond buckets
		Extract(func(req *http.Request) (float64, []golens.Label) {
			// Simulate varying request processing times
			endpoint := req.URL.Path
			if endpoint == "" {
				endpoint = "/"
			}
			// Different endpoints have different processing times
			duration := 50.0 + float64((len(endpoint)*17)%150) // 50-200ms range
			switch endpoint {
			case "/order":
				duration = 120.0
			case "/connections":
				duration = 80.0
			case "/size":
				duration = 200.0
			case "/cpu":
				duration = 150.0
			}
			log.Printf("[histogram] processing time=%.0fms for endpoint=%s", duration, endpoint)
			return duration, []golens.Label{{Name: "endpoint", Value: endpoint}}
		})
	mux.Handle("/latency", requestDuration(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("request processing time tracked\n"))
	})))

	// Custom histogram metric for tracking response sizes with appropriate bounds.
	responseSizeHist := registry.On("response_size_bytes").
		Type(golens.HistogramType).
		Description("response size in bytes").
		Labels("endpoint").
		Bounds(128, 256, 512, 1024, 2048, 4096, 8192). // Byte-sized buckets
		Extract(func(req *http.Request) (float64, []golens.Label) {
			// Track the size of response we're about to send
			endpoint := req.URL.Path
			if endpoint == "" {
				endpoint = "/"
			}
			// Simulate different response sizes for demo
			size := 1024.0 + float64((len(endpoint)*128)%1024) // Varied sizes
			switch endpoint {
			case "/order":
				size = 512.0
			case "/connections":
				size = 256.0
			case "/size":
				size = 2048.0
			}
			log.Printf("[histogram] recording size=%.0f for endpoint=%s", size, endpoint)
			return size, []golens.Label{{Name: "endpoint", Value: endpoint}}
		})
	mux.Handle("/size", responseSizeHist(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("[histogram] /size endpoint called")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response size tracked\n"))
	})))

	// Demonstrate cardinality-aware labels: this counter tracks requests by
	// "tenant" — a label that could grow unbounded in a multi-tenant system.
	// GoLens's cardinality guard (configured via max_label_series_per_metric)
	// will automatically drop new tenant label combinations once the cap is
	// hit, preventing unbounded series growth. Check the "GoLens Internals"
	// section on the dashboard to see series counts and dropped samples.
	tenantRequests := registry.On("tenant_requests_total").
		Type(golens.CounterType).
		Description("requests per tenant (cardinality-bounded)").
		Labels("tenant").
		Extract(func(req *http.Request) (float64, []golens.Label) {
			tenant := req.URL.Query().Get("tenant")
			if tenant == "" {
				tenant = "default"
			}
			return 1, []golens.Label{{Name: "tenant", Value: tenant}}
		})
	mux.Handle("/tenant", tenantRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := r.URL.Query().Get("tenant")
		if tenant == "" {
			tenant = "default"
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tenant request tracked: " + tenant + "\n"))
	})))

	srv := &http.Server{Addr: ":8080", Handler: registry.Middleware(mux)}
	go func() {
		log.Println("GoLens (stdlib mux) listening on :8080 (dashboard at /metrics)")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
