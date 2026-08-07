package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHandler(t *testing.T) {
	dependencyError := errors.New("private database diagnostic")
	tests := []struct {
		name            string
		method          string
		path            string
		healthErr       error
		readyErr        error
		cloudErr        error
		wantStatus      int
		wantType        string
		wantBody        string
		wantCloudStatus string
		wantHealth      int
		wantReady       int
		wantCloud       int
		wantNoPrivate   bool
	}{
		{
			name:       "health success",
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantType:   "text/plain; charset=utf-8",
			wantBody:   "ok\n",
			wantHealth: 1,
		},
		{
			name:            "readiness success",
			method:          http.MethodGet,
			path:            "/readyz",
			wantStatus:      http.StatusOK,
			wantType:        "text/plain; charset=utf-8",
			wantBody:        "ready\n",
			wantReady:       1,
			wantCloud:       1,
			wantCloudStatus: "ready",
		},
		{
			name:            "cloud outage is readiness detail",
			method:          http.MethodGet,
			path:            "/readyz",
			cloudErr:        dependencyError,
			wantStatus:      http.StatusOK,
			wantType:        "text/plain; charset=utf-8",
			wantBody:        "ready\n",
			wantReady:       1,
			wantCloud:       1,
			wantCloudStatus: "unavailable",
			wantNoPrivate:   true,
		},
		{
			name:          "health failure is generic",
			method:        http.MethodGet,
			path:          "/healthz",
			healthErr:     dependencyError,
			wantStatus:    http.StatusServiceUnavailable,
			wantType:      "text/plain; charset=utf-8",
			wantBody:      "unhealthy\n",
			wantHealth:    1,
			wantNoPrivate: true,
		},
		{
			name:          "readiness failure is generic",
			method:        http.MethodGet,
			path:          "/readyz",
			readyErr:      dependencyError,
			wantStatus:    http.StatusServiceUnavailable,
			wantType:      "text/plain; charset=utf-8",
			wantBody:      "not ready\n",
			wantReady:     1,
			wantNoPrivate: true,
		},
		{
			name:       "head health invokes checker",
			method:     http.MethodHead,
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantType:   "text/plain; charset=utf-8",
			wantBody:   "ok\n",
			wantHealth: 1,
		},
		{
			name:       "rejects unsupported endpoint method",
			method:     http.MethodPost,
			path:       "/healthz",
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
		{
			name:       "rejects unknown path",
			method:     http.MethodGet,
			path:       "/unknown",
			wantStatus: http.StatusNotFound,
			wantType:   "text/plain; charset=utf-8",
			wantBody:   "404 page not found\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			healthCalls := 0
			readyCalls := 0
			cloudCalls := 0
			handler := NewHandler(
				func(context.Context) error {
					healthCalls++
					return test.healthErr
				},
				func(context.Context) error {
					readyCalls++
					return test.readyErr
				},
				func(context.Context) error {
					cloudCalls++
					return test.cloudErr
				},
			)
			request := httptest.NewRequest(test.method, test.path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)
			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if got := response.Header.Get("Content-Type"); got != test.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, test.wantType)
			}
			if body := recorder.Body.String(); body != test.wantBody {
				t.Fatalf("body = %q, want %q", body, test.wantBody)
			}
			if healthCalls != test.wantHealth || readyCalls != test.wantReady {
				t.Fatalf("checker calls = health:%d ready:%d, want health:%d ready:%d", healthCalls, readyCalls, test.wantHealth, test.wantReady)
			}
			if cloudCalls != test.wantCloud {
				t.Fatalf("cloud checker calls = %d, want %d", cloudCalls, test.wantCloud)
			}
			if got := response.Header.Get("X-CD211-CloudDrive"); got != test.wantCloudStatus {
				t.Fatalf("X-CD211-CloudDrive = %q, want %q", got, test.wantCloudStatus)
			}
			if test.wantNoPrivate && strings.Contains(recorder.Body.String(), dependencyError.Error()) {
				t.Fatalf("body leaked dependency error: %q", recorder.Body.String())
			}
		})
	}
}

func TestNewHandlerRejectsNilChecks(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHandler did not panic for nil health check")
		}
	}()
	NewHandler(nil, func(context.Context) error { return nil }, func(context.Context) error { return nil })
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := NewHTTPServer(":8080", handler)

	if server.Addr != ":8080" || server.Handler == nil {
		t.Fatal("server address or handler was not retained")
	}
	if server.ReadHeaderTimeout.Seconds() != 10 || server.ReadTimeout.Seconds() != 30 || server.WriteTimeout.Seconds() != 30 || server.IdleTimeout.Seconds() != 120 {
		t.Fatalf("server timeouts = header:%s read:%s write:%s idle:%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 1<<20)
	}
}
