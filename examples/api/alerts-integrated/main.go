// Command alerts-integrated demonstrates GoLens alerting with mode="integrated".
// The alert evaluation ticker runs inside the Registry's single background loop
// goroutine — no extra goroutines, consistent with the flush/export pattern.
//
// Run:
//
//	go run examples/api/alerts-integrated/main.go [-mailer]
//
// Pass -mailer to enable the log email notifier (visible in the alerts UI).
// Then open http://localhost:8080/metrics/alerts to manage alert rules.
// The /load endpoint generates traffic to trigger the seeded alert rule.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
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
	cfg.Debug = true
	cfg.RuntimeMetrics.Enabled = true

	// Enable alerts in integrated mode (single-loop evaluation).
	cfg.Alerts.Enabled = true
	cfg.Alerts.Mode = "integrated"
	cfg.Alerts.EvaluationInterval = 10 * time.Second
	cfg.Alerts.DefaultCooldown = 1 * time.Minute

	// Seed a config-file rule: fire when http_requests_total > 50.
	cfg.Alerts.Rules = []golens.AlertRule{
		{
			ID:        "high-traffic",
			Name:      "High Traffic",
			Metric:    "http_requests_total",
			Condition: golens.ConditionGT,
			Threshold: 50,
			Cooldown:  1 * time.Minute,
			Enabled:   true,
		},
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

	// Application routes.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from alerts-integrated\n"))
	})

	// /load generates traffic to trigger the alert.
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			registry.Record("http_requests_total", 1,
				golens.Label{Name: "method", Value: "GET"},
				golens.Label{Name: "path", Value: "/load"},
				golens.Label{Name: "status", Value: "200"},
			)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("recorded 100 requests — check /metrics/alerts/log\n"))
	})

	srv := &http.Server{Addr: ":8080", Handler: registry.Middleware(mux)}
	go func() {
		log.Println("GoLens (alerts-integrated) listening on :8080")
		log.Println("  Dashboard: http://localhost:8080/metrics")
		log.Println("  Alerts:    http://localhost:8080/metrics/alerts")
		log.Println("  Trigger:   http://localhost:8080/load")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
