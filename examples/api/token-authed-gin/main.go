// Command api-auth-gin demonstrates the "API-custom auth" security mode.
//
// GoLens's built-in declarative auth is DISABLED. Access to /metrics is owned
// entirely by an application-supplied Gin middleware that validates an API
// token. This is the pattern for API-only or programmatic access where you do
// not want HTTP Basic Auth: callers present a bearer token, and the same
// middleware can be swapped for JWT/OAuth/session validation later.
//
// A token can be presented three ways:
//
//	Authorization: Bearer <token>     (REST clients)
//	X-API-Key: <token>                (API gateways)
//	?token=<token> query parameter    (browser one-click unlock)
//
// On the first valid presentation the server sets an HMAC-signed session
// cookie, so the dashboard's HTMX polling keeps working without resending the
// token on every request. Without that bridge, token auth and a browser UI are
// incompatible — HTMX/fetch cannot attach the header automatically.
//
// Run:
//
//	GOLENS_API_TOKEN=s3cret go run examples/api-auth-gin/main.go
//
// Then:
//
//	# API-style (no cookie, no browser):
//	curl -H "Authorization: Bearer s3cret" http://localhost:8080/metrics/data
//	curl -H "X-API-Key: s3cret" http://localhost:8080/metrics/endpoints
//
//	# Browser: open the one-click unlock URL, then the dashboard works:
//	#   http://localhost:8080/metrics?token=s3cret
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	golens "github.com/GoLens-Project/golens-metrics"
)

const (
	sessionCookie = "golens_admin"
	sessionTTL    = 12 * time.Hour
)

func main() {
	useMailer := flag.Bool("mailer", false, "enable log email notifier")
	flag.Parse()

	// Built-in auth OFF: the custom middleware below owns access control.
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

	token := os.Getenv("GOLENS_API_TOKEN")
	if token == "" {
		// Generate an ephemeral token for the demo so the server is never open.
		token = randomToken()
		log.Printf("warning: GOLENS_API_TOKEN not set; generated ephemeral token: %s", token)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// Public application route.
	r.GET("/", func(c *gin.Context) {
		time.Sleep(5 * time.Millisecond)
		c.String(http.StatusOK, "hello from api-auth-gin\n")
	})

	// Protected /metrics group: raw GoLens handlers behind the API-token guard.
	auth := apiTokenAuth(token)
	metrics := r.Group("/metrics", auth)
	{
		metrics.GET("", gin.WrapF(registry.DashboardHandler))
		metrics.GET("/data", gin.WrapF(registry.DataHandler))
		metrics.GET("/endpoints", gin.WrapH(registry.EndpointsHTTPHandler()))
		metrics.GET("/history", gin.WrapH(registry.HistoryHTTPHandler()))
		// Alert subsystem.
		metrics.GET("/alerts", gin.WrapH(registry.AlertsPageHTTPHandler()))
		metrics.GET("/alerts/rules", gin.WrapH(registry.AlertRulesHTTPHandler()))
		metrics.POST("/alerts/rules", gin.WrapH(registry.AlertRulesHTTPHandler()))
		metrics.DELETE("/alerts/rules/:id", gin.WrapH(registry.AlertRulesDeleteHTTPHandler()))
		metrics.GET("/alerts/log", gin.WrapH(registry.AlertLogHTTPHandler()))
		metrics.GET("/alerts/templates", gin.WrapH(registry.AlertTemplatesHTTPHandler()))
	}

	srv := &http.Server{Addr: ":8080", Handler: registry.Middleware(r)}
	go func() {
		log.Println("GoLens (api-auth-gin) listening on :8080 — /metrics requires a bearer/api token")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// apiTokenAuth returns a Gin middleware that enforces the API token. It accepts
// the token from a Bearer header, an X-API-Key header, or a ?token= query
// parameter. After the first valid presentation it issues an HMAC-signed cookie
// so subsequent same-origin requests (HTMX polling, fetch) stay authenticated.
//
// Replace the body of this function with JWT/session/OAuth verification and the
// raw GoLens handlers need no changes.
func apiTokenAuth(token string) gin.HandlerFunc {
	want := []byte(token)
	signingKey := []byte(token) // derive the cookie-signing key from the token

	return func(c *gin.Context) {
		// 1. Already authenticated via a previously issued session cookie?
		if cookie, err := c.Cookie(sessionCookie); err == nil {
			if ok, _ := verifySession(signingKey, cookie); ok {
				c.Next()
				return
			}
		}

		// 2. Collect the presented token from any supported location.
		presented := ""
		switch {
		case strings.HasPrefix(c.GetHeader("Authorization"), "Bearer "):
			presented = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		case c.GetHeader("X-API-Key") != "":
			presented = c.GetHeader("X-API-Key")
		case c.Query("token") != "":
			presented = c.Query("token")
		}

		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), want) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="GoLens"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing API token"})
			return
		}

		// 3. Token valid: mint a session cookie so the browser dashboard keeps
		//    working for subsequent HTMX/fetch requests.
		signed, _ := signSession(signingKey, time.Now().Add(sessionTTL).Unix())
		http.SetCookie(c.Writer, &http.Cookie{
			Name: sessionCookie, Value: signed, Path: "/", HttpOnly: true,
			MaxAge: int(sessionTTL / time.Second), SameSite: http.SameSiteLaxMode,
		})
		// If unlocked via ?token=, redirect to the clean dashboard URL.
		if c.Query("token") != "" {
			c.Redirect(http.StatusFound, c.Request.URL.Path)
			c.Abort()
			return
		}
		c.Next()
	}
}

// --- signed-cookie session helpers (HMAC-SHA256, no third-party deps) -------
//
// Cookie format:  base64(exp).base64(hmac(signingKey, exp))

func signSession(key []byte, exp int64) (string, error) {
	expB := []byte(strconv.FormatInt(exp, 10))
	mac := hmac.New(sha256.New, key)
	mac.Write(expB)
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(expB) + "." + base64.RawURLEncoding.EncodeToString(sum), nil
}

func verifySession(key []byte, cookie string) (bool, error) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return false, errors.New("malformed session cookie")
	}
	expB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false, err
	}
	sum, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(expB)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return false, errors.New("bad session signature")
	}
	exp, err := strconv.ParseInt(string(expB), 10, 64)
	if err != nil {
		return false, err
	}
	if time.Now().Unix() >= exp {
		return false, errors.New("session expired")
	}
	return true, nil
}

func randomToken() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "dev-only-fallback-token"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
