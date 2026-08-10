package main

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
)

// applyTestConfig returns a valid runtime configuration whose cloud endpoint
// is never contacted (clouddrive.Dial is lazy and the reconciler only touches
// the local store).
func applyTestConfig(t *testing.T) settings.Config {
	t.Helper()
	return settings.Config{
		CD2Address:     "127.0.0.1:12345",
		CD2Username:    "admin",
		CD2Password:    "secret",
		CD2Insecure:    true,
		CloudRoot:      "/torrents",
		LocalRoot:      t.TempDir(),
		OfflineTimeout: 24 * time.Hour,
		CopyTimeout:    72 * time.Hour,
		VerifyTimeout:  10 * time.Minute,
	}
}

func openApplyStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestManagerApplySwapsGenerations(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	hash, err := creds.HashPassword("adminadmin123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	cfg := applyTestConfig(t)
	if err := st.CompleteSetup(ctx, hash, settings.Values(cfg), now); err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(reconcile.RealClock{}, rand.Reader, sessionTTL, sessionCapacity)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	root := &switchHandler{}
	m := newManager(root, st, sessions, reconcile.RealClock{}, logger)

	first, err := m.build(ctx, cfg)
	if err != nil {
		t.Fatalf("build first generation: %v", err)
	}
	m.activate(first)

	// The active generation serves the full runtime mux, where /setup
	// redirects back to the operator interface.
	recorder := httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/" {
		t.Fatalf("/setup after activate = %d %q, want 303 /", recorder.Code, recorder.Header().Get("Location"))
	}

	// Apply builds, swaps in, and starts a new generation, then retires the
	// first without touching the HTTP root's liveness.
	if err := m.Apply(ctx, cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if m.currentGeneration() == first {
		t.Error("current generation unchanged after Apply")
	}
	select {
	case <-first.done:
	default:
		t.Error("previous generation was not stopped by Apply")
	}

	recorder = httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/" {
		t.Errorf("/setup after Apply = %d %q, want 303 /", recorder.Code, recorder.Header().Get("Location"))
	}

	second := m.currentGeneration()
	if second == nil {
		t.Fatal("no current generation after Apply")
	}
	m.shutdown()
	select {
	case <-second.done:
	default:
		t.Error("current generation was not stopped by shutdown")
	}
}

func TestManagerApplyRejectsRebuildAfterShutdown(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := newManager(&switchHandler{}, st, nil, reconcile.RealClock{}, logger)

	m.shutdown()
	if err := m.Apply(ctx, cfg); err == nil {
		t.Error("Apply after shutdown succeeded, want error")
	}
}

// TestConfiguredRuntimeMountsNativeAPI verifies the Phase 2 routing: a built
// runtime mounts the authenticated native API at /api/v1/ (401 without a
// token) while the qBittorrent surface keeps serving.
func TestConfiguredRuntimeMountsNativeAPI(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(reconcile.RealClock{}, rand.Reader, sessionTTL, sessionCapacity)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	root := &switchHandler{}
	m := newManager(root, st, sessions, reconcile.RealClock{}, logger)

	generation, err := m.build(ctx, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	m.activate(generation)
	defer m.shutdown()

	// Without a configured API token every /api/v1/* request is the stable 401.
	recorder := httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/downloads/0123456789abcdef0123456789abcdef01234567", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("native api status = %d, want 401; body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"unauthorized"`) {
		t.Errorf("native api body = %q, want unauthorized JSON", recorder.Body.String())
	}

	// Unknown /api/v1/* paths are authenticated before routing.
	recorder = httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unknown native api status = %d, want 401", recorder.Code)
	}

	// The qBittorrent surface is untouched by the new mount.
	recorder = httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("qBittorrent add status = %d, want 403 without SID", recorder.Code)
	}
}
