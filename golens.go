// Package golens is a lightweight, declarative observability middleware for Go
// applications. It provides a "Swagger-like" developer experience by enabling
// automatic metric collection through HTTP middleware, while decoupling the
// Collection (Middleware), Registry (Source of Truth), and Exposition
// (UI/OTLP) layers.
//
// Quick start:
//
//	cfg := golens.DefaultConfig()
//	cfg.OTLP.Enabled = true
//	r, _ := golens.New(cfg)
//	r.Start(context.Background())
//	defer r.Close()
//
//	mux := http.NewServeMux()
//	r.MountUI(mux)
//	mux.HandleFunc("/", handler)
//	http.ListenAndServe(":8080", r.Middleware(mux))
//
// GoLens is zero-config for small services (an in-memory-only registry works
// out of the box) and production-ready via standard OpenTelemetry (OTLP/HTTP)
// integration.
package golens

import (
	"context"
	"log"
	"net/http"
)

// Version is the library semantic version.
const Version = "0.1.0"

// Handler is a convenience that wraps an http.Handler with the GoLens
// middleware, mounts the UI, and starts the background loop. The caller must
// Close the returned Registry on shutdown. Useful for the simplest "mount and
// go" workflow.
func Handler(cfg Config, next http.Handler) (*Registry, http.Handler) {
	r, err := New(cfg)
	if err != nil {
		// Fall back to defaults on misconfiguration so the app still runs —
		// but never silently: the fallback can drop configured auth.
		log.Printf("[golens] config rejected, falling back to defaults: %v", err)
		r, _ = New(DefaultConfig())
	}
	_ = r.Start(context.Background())
	mux := http.NewServeMux()
	r.MountUI(mux)
	mux.Handle("/", next)
	return r, r.Middleware(mux)
}
