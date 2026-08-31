package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/authn"
)

func TestRotatingWriterAppendsAndRotates(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 6, 23, 0, 0, 0, time.UTC)
	w, err := NewRotatingWriterWithClock(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := w.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cd211-2026-08-06.jsonl", "cd211-2026-08-07.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestRotatingWriterConcurrentLines(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, writeErr := w.Write([]byte("{}\n"))
			errs <- writeErr
		}()
	}
	wg.Wait()
	close(errs)
	for writeErr := range errs {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, entry := range entries {
		if ownedName(entry.Name()) {
			data, err = os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 8 {
		t.Fatalf("wrote %d lines, want 8", len(lines))
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
	}
}

func TestMiddlewareRedactsAndCapturesOneCompletion(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(&safeHandler{Handler: slog.NewJSONHandler(&out, nil)})
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"Conflict"}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add?token=do-not-log", nil)
	req.Header.Set("Authorization", "Bearer do-not-log")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "http request" || record["status"] != float64(409) {
		t.Fatalf("record = %#v", record)
	}
	if strings.Contains(out.String(), "do-not-log") {
		t.Fatal("secret leaked")
	}
}

func TestMiddlewareEmitsBoundaryAuthResults(t *testing.T) {
	tests := []struct {
		name, surface, attempt, principal, mode string
	}{
		{"basic", "qbt", "basic", "operator", "basic"},
		{"qbt key", "qbt", "qbt_key", "qbt_client", "qbt_key"},
		{"native token", "native", "native_token", "native_client", "native_token"},
		{"web session", "web", "session", "operator", "session"},
		{"qB session", "qbt", "session", "qbt_client", "session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := slog.New(&safeHandler{Handler: slog.NewJSONHandler(&out, nil)})
			handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				SetAuthAttempt(r, test.surface, test.attempt)
				SetAuthSuccess(r, authn.Principal{Kind: authn.PrincipalKind(test.principal), Method: authn.Method(test.mode)})
				w.WriteHeader(http.StatusNoContent)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/audit", nil))

			var record map[string]any
			if err := json.Unmarshal(out.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"auth_surface": test.surface, "auth_attempt": test.attempt,
				"auth_principal": test.principal, "auth_mode": test.mode,
			} {
				if record[key] != want {
					t.Errorf("%s = %#v, want %q", key, record[key], want)
				}
			}
		})
	}
}

func TestMiddlewareEmitsFailedAuthorizationAttemptWithoutSecrets(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(&safeHandler{Handler: slog.NewJSONHandler(&out, nil)})
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetAuthAttempt(r, "qbt", "authorization")
		w.WriteHeader(http.StatusForbidden)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/app/version?token=do-not-log", nil)
	req.Header.Set("Authorization", "Bearer do-not-log")
	req.AddCookie(&http.Cookie{Name: "SID", Value: "sid-do-not-log"})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["auth_surface"] != "qbt" || record["auth_attempt"] != "authorization" ||
		record["auth_principal"] != "anonymous" || record["auth_mode"] != "none" {
		t.Fatalf("failed auth fields = %#v", record)
	}
	if strings.Contains(out.String(), "do-not-log") {
		t.Fatal("secret leaked")
	}
}
