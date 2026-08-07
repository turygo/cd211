// Package server provides CD211's HTTP health endpoints.
package server

import (
	"context"
	"net/http"
	"time"
)

// CheckFunc checks a runtime dependency for an HTTP endpoint.
type CheckFunc func(context.Context) error

// NewHandler returns health and readiness endpoints. CloudDrive2 availability
// is reported as readiness detail but never makes durable HTTP state unavailable.
func NewHandler(health CheckFunc, ready CheckFunc, cloud CheckFunc) http.Handler {
	if health == nil || ready == nil || cloud == nil {
		panic("server check functions must not be nil")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var check CheckFunc
		var successBody, failureBody string

		switch r.URL.Path {
		case "/healthz":
			check = health
			successBody = "ok\n"
			failureBody = "unhealthy\n"
		case "/readyz":
			check = ready
			successBody = "ready\n"
			failureBody = "not ready\n"
		default:
			http.NotFound(w, r)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := check(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(failureBody))
			return
		}
		if r.URL.Path == "/readyz" {
			cloudStatus := "ready"
			statusContext, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			if err := cloud(statusContext); err != nil {
				cloudStatus = "unavailable"
			}
			cancel()
			w.Header().Set("X-CD211-CloudDrive", cloudStatus)
		}
		_, _ = w.Write([]byte(successBody))
	})
}

// NewHTTPServer constructs the bounded-timeout HTTP server.
func NewHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
