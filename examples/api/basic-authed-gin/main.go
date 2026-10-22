// Command authed-gin demonstrates the "Custom Middleware / API-Only" security
// mode: GoLens's built-in declarative auth is left DISABLED, and the raw,
// unprotected dashboard handlers (DashboardHandler / DataHandler) are mounted
// behind the application's OWN Gin authentication middleware.
//
// This is the pattern to use when your app already has a security stack
// (HTTP Basic, JWT, sessions, OAuth, ...) that must gate /metrics the same way
// it gates every other route. Swap basicAuthMiddleware below for a JWT/session
// middleware and nothing else changes.
//
// Run:
//
//	GOLENS_ADMIN_USER=admin GOLENS_ADMIN_PASS=secret \
//	  go run examples/authed-gin/main.go
//
// Then:
//
//	curl -u admin:secret http://localhost:8080/metrics        # dashboard HTML
//	curl -u admin:secret http://localhost:8080/metrics/data    # JSON (Accept: application/json)
//	curl http://localhost:8080/metrics                         # 401 (no credentials)
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	golens "github.com/GoLens-Project/golens-metrics"
)

func main() {
	useMailer := flag.Bool("mailer", false, "enable log email notifier")
	flag.Parse()

	// Built-in auth is intentionally left off — the Gin middleware below owns
	// access control. This is what "API-Only mode" means in the README.
	cfg := golens.DefaultConfig()
	cfg.ProjectName = "Example API"
	cfg.DashboardSubtitle = "Example Dashboard"
	cfg.RuntimeMetrics.Enabled = true
	cfg.Alerts.Enabled = true
	cfg.Alerts.EvaluationInterval = 30 * time.Second

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

	user := "test"
	pass := "testpw"
	if user == "" || pass == "" {
		log.Println("warning: GOLENS_ADMIN_USER / GOLENS_ADMIN_PASS not set; /metrics is unauthenticated")
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// --- public application routes (not instrumented for the demo) ---
	r.GET("/", func(c *gin.Context) {
		time.Sleep(5 * time.Millisecond)
		c.String(http.StatusOK, "hello from authed-gin\n")
	})

	// --- protected /metrics group: raw GoLens handlers behind our auth ---
	metrics := r.Group("/metrics", basicAuthMiddleware(user, pass))
	{
		// Main dashboard HTML.
		metrics.GET("", gin.WrapF(registry.DashboardHandler))
		// HTMX polling target (JSON or HTML fragment).
		metrics.GET("/data", gin.WrapF(registry.DataHandler))
		// Per-endpoint latency JSON for the percentile chart.
		metrics.GET("/endpoints", gin.WrapH(registry.EndpointsHTTPHandler()))
		// Runtime metrics history for time-series charts.
		metrics.GET("/history", gin.WrapH(registry.HistoryHTTPHandler()))
		// Alert subsystem.
		metrics.GET("/alerts", gin.WrapH(registry.AlertsPageHTTPHandler()))
		metrics.GET("/alerts/rules", gin.WrapH(registry.AlertRulesHTTPHandler()))
		metrics.POST("/alerts/rules", gin.WrapH(registry.AlertRulesHTTPHandler()))
		metrics.DELETE("/alerts/rules/:id", gin.WrapH(registry.AlertRulesDeleteHTTPHandler()))
		metrics.GET("/alerts/log", gin.WrapH(registry.AlertLogHTTPHandler()))
		metrics.GET("/alerts/templates", gin.WrapH(registry.AlertTemplatesHTTPHandler()))
	}

	// GoLens's RED middleware still wraps the whole engine.
	srv := &http.Server{Addr: ":8080", Handler: registry.Middleware(r)}
	go func() {
		log.Println("GoLens (authed-gin) listening on :8080 — /metrics requires basic auth")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// basicAuthMiddleware is an application-owned HTTP Basic Auth middleware. It is
// intentionally separate from GoLens's built-in auth to demonstrate the custom
// workflow. Replace its body with a JWT/session/OAuth check and the raw
// GoLens handlers stay exactly the same.
func basicAuthMiddleware(user, pass string) gin.HandlerFunc {
	wantUser := []byte(user)
	wantPass := []byte(pass)
	configured := len(wantUser) > 0 && len(wantPass) > 0
	return func(c *gin.Context) {
		if !configured {
			c.Next() // no creds configured: allow (demo convenience)
			return
		}
		u, p, ok := c.Request.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), wantUser) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), wantPass) != 1 {

			c.Header("WWW-Authenticate", `Basic realm="GoLens admin", charset="UTF-8"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}
