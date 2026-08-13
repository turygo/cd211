package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSwitchHandlerSwapUnderConcurrentRequests(t *testing.T) {
	handlerA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("A"))
	})
	handlerB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("B"))
	})

	root := &switchHandler{}
	server := httptest.NewServer(root)
	defer server.Close()

	// Before any handler is stored the root answers 503.
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET before Store: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("before Store status = %d, want 503", response.StatusCode)
	}

	root.Store(handlerA)

	stop := make(chan struct{})
	var wait sync.WaitGroup
	const workers, requestsPerWorker = 8, 250
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client := server.Client()
			for range requestsPerWorker {
				select {
				case <-stop:
					return
				default:
				}
				response, err := client.Get(server.URL + "/")
				if err != nil {
					t.Errorf("request failed: %v", err)
					return
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Errorf("read body: %v", err)
					return
				}
				if len(body) != 1 || (body[0] != 'A' && body[0] != 'B') {
					t.Errorf("response body = %q, want exactly A or B", body)
					return
				}
			}
		}()
	}

	// Swap while requests are in flight; every response must still be one
	// complete handler, never a mix.
	for range 64 {
		root.Store(handlerB)
		root.Store(handlerA)
	}
	close(stop)
	wait.Wait()

	// The final store wins and is served by subsequent requests.
	root.Store(handlerB)
	response, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET after swap: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "B" {
		t.Errorf("after swap body = %q, want B", body)
	}
}

func TestSetupModeMux(t *testing.T) {
	setup := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("setup handler"))
	})
	mux := setupModeMux(setup)

	tests := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantBody     string
		wantLocation string
	}{
		{name: "healthz", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok\n"},
		{name: "readyz", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusServiceUnavailable, wantBody: "not ready\n"},
		{name: "api root", method: http.MethodGet, path: "/api/v2/", wantStatus: http.StatusServiceUnavailable, wantBody: "setup in progress\n"},
		{name: "api sub", method: http.MethodPost, path: "/api/v2/downloads", wantStatus: http.StatusServiceUnavailable, wantBody: "setup in progress\n"},
		{name: "native api", method: http.MethodGet, path: "/api/v1/downloads", wantStatus: http.StatusServiceUnavailable, wantBody: "{\"error\":{\"code\":\"setup_incomplete\",\"message\":\"Setup is incomplete\"}}\n"},
		{name: "native api sub", method: http.MethodPost, path: "/api/v1/downloads/0123456789abcdef0123456789abcdef01234567", wantStatus: http.StatusServiceUnavailable, wantBody: "{\"error\":{\"code\":\"setup_incomplete\",\"message\":\"Setup is incomplete\"}}\n"},
		{name: "root redirects", method: http.MethodGet, path: "/", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		{name: "unknown redirects", method: http.MethodGet, path: "/login", wantStatus: http.StatusSeeOther, wantLocation: "/setup"},
		{name: "setup exact", method: http.MethodGet, path: "/setup", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup sub path", method: http.MethodPost, path: "/setup/password", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup language", method: http.MethodGet, path: "/lang", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup css", method: http.MethodGet, path: "/static/app.css", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup javascript", method: http.MethodGet, path: "/static/app.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup theme initializer", method: http.MethodGet, path: "/static/theme-init.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup motion module", method: http.MethodGet, path: "/static/motion.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup actions module", method: http.MethodGet, path: "/static/actions-motion.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup transition module", method: http.MethodGet, path: "/static/setup-motion.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
		{name: "setup motion vendor", method: http.MethodGet, path: "/static/vendor/motion-mini.js", wantStatus: http.StatusOK, wantBody: "setup handler"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)
			mux.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Errorf("%s %s status = %d, want %d", tt.method, tt.path, recorder.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && recorder.Body.String() != tt.wantBody {
				t.Errorf("%s %s body = %q, want %q", tt.method, tt.path, recorder.Body.String(), tt.wantBody)
			}
			if got := recorder.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("%s %s Location = %q, want %q", tt.method, tt.path, got, tt.wantLocation)
			}
		})
	}
}

func TestSetupModeNativeAPIPlaceholder(t *testing.T) {
	mux := setupModeMux(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("setup placeholder invoked the setup handler")
	}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/downloads/0123456789abcdef0123456789abcdef01234567", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	want := "{\"error\":{\"code\":\"setup_incomplete\",\"message\":\"Setup is incomplete\"}}\n"
	if got := recorder.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
