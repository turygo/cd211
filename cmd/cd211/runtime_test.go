package main

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/domain"
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
func persistedRootAlias(t *testing.T, root, name string) string {
	t.Helper()
	target := filepath.Join(root, ".cd211", "0123456789012345678901234567890123456789")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll reserved target: %v", err)
	}
	alias := filepath.Join(root, name)
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("Symlink persisted root alias: %v", err)
	}
	return alias
}

func TestManagerBuildRejectsPersistedRootAliasBeforeActivation(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)
	alias := persistedRootAlias(t, cfg.LocalRoot, "legacy")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if _, err := st.UpsertCategory(ctx, domain.Category{
		Name: "legacy", CloudPath: "/cloud/legacy", SavePath: alias,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertCategory: %v", err)
	}

	m := newManager(&switchHandler{}, st, nil, reconcile.RealClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := m.build(ctx, cfg); err == nil {
		t.Fatal("build accepted persisted root alias")
	}
	if m.currentGeneration() != nil {
		t.Fatal("failed build activated a runtime generation")
	}
}

func TestManagerApplyRejectsPersistedRootAliasWithoutSwapping(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	m := newManager(&switchHandler{}, st, sessions, reconcile.RealClock{}, logger)
	first, err := m.build(ctx, cfg)
	if err != nil {
		t.Fatalf("build first generation: %v", err)
	}
	m.activate(first)
	defer m.shutdown()

	alias := persistedRootAlias(t, cfg.LocalRoot, "legacy")
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if _, err := st.UpsertCategory(ctx, domain.Category{
		Name: "legacy", CloudPath: "/cloud/legacy", SavePath: alias,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertCategory: %v", err)
	}
	if err := m.Apply(ctx, cfg); err == nil {
		t.Fatal("Apply accepted persisted root alias")
	}
	if got := m.currentGeneration(); got != first {
		t.Fatal("failed Apply swapped out the active runtime generation")
	}
}

func TestManagerBuildAllowsPersistedRootEqualityAndNearName(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)
	near := filepath.Join(cfg.LocalRoot, ".cd211-backup")
	if err := os.Mkdir(near, 0o755); err != nil {
		t.Fatalf("Mkdir near-name root: %v", err)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for name, savePath := range map[string]string{"root": cfg.LocalRoot, "near": near} {
		if _, err := st.UpsertCategory(ctx, domain.Category{
			Name: name, CloudPath: "/cloud/" + name, SavePath: savePath,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertCategory(%s): %v", name, err)
		}
	}

	sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	m := newManager(&switchHandler{}, st, sessions, reconcile.RealClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	generation, err := m.build(ctx, cfg)
	if err != nil {
		t.Fatalf("build valid persisted roots: %v", err)
	}
	m.activate(generation)
	m.shutdown()
}

func TestManagerBuildAppliesRetainedDownloadPreflightPredicate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	t.Run("live alias rejected", func(t *testing.T) {
		st := openApplyStore(t)
		cfg := applyTestConfig(t)
		alias := persistedRootAlias(t, cfg.LocalRoot, "legacy")
		hash := "0123456789012345678901234567890123456789"
		nextRun := now
		_, _, err := st.CreateSubmission(ctx, domain.Submission{
			Download: domain.Download{
				Hash: hash, Name: "live", SourceKind: domain.SourceMagnet,
				SubmissionURI: "magnet:?xt=urn:btih:" + hash, CloudFolder: "/cloud",
				SavePath: alias, TotalSize: 1, State: domain.StateAccepted,
				PhaseStartedAt: now, NextRunAt: &nextRun, CreatedAt: now, UpdatedAt: now,
			},
			Files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "file", Size: 1}},
		})
		if err != nil {
			t.Fatalf("CreateSubmission: %v", err)
		}

		m := newManager(&switchHandler{}, st, nil, reconcile.RealClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if _, err := m.build(ctx, cfg); err == nil {
			t.Fatal("build accepted live download alias")
		}
	})

	t.Run("ordinary deleted alias exempt", func(t *testing.T) {
		st := openApplyStore(t)
		cfg := applyTestConfig(t)
		alias := persistedRootAlias(t, cfg.LocalRoot, "legacy")
		hash := "1234567890123456789012345678901234567890"
		nextRun := now
		created, inserted, err := st.CreateSubmission(ctx, domain.Submission{
			Download: domain.Download{
				Hash: hash, Name: "deleted", SourceKind: domain.SourceMagnet,
				SubmissionURI: "magnet:?xt=urn:btih:" + hash, CloudFolder: "/cloud",
				SavePath: alias, TotalSize: 1, State: domain.StateAccepted,
				PhaseStartedAt: now, NextRunAt: &nextRun, CreatedAt: now, UpdatedAt: now,
			},
			Files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "file", Size: 1}},
		})
		if err != nil || !inserted {
			t.Fatalf("CreateSubmission: inserted=%t err=%v", inserted, err)
		}
		if err := st.RequestDelete(ctx, []string{created.Hash}, false, now.Add(time.Minute)); err != nil {
			t.Fatalf("RequestDelete: %v", err)
		}
		claim, err := st.ClaimDue(ctx, "runtime-test", now.Add(2*time.Minute), time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("ClaimDue: claim=%+v err=%v", claim, err)
		}
		deleted := claim.Download
		deleted.State = domain.StateDeleted
		deleted.UpdatedAt = now.Add(3 * time.Minute)
		if err := st.CommitClaim(ctx, *claim, deleted); err != nil {
			t.Fatalf("CommitClaim: %v", err)
		}

		sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
		if err != nil {
			t.Fatalf("session.New: %v", err)
		}
		m := newManager(&switchHandler{}, st, sessions, reconcile.RealClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		generation, err := m.build(ctx, cfg)
		if err != nil {
			t.Fatalf("build rejected ordinary deleted alias: %v", err)
		}
		m.activate(generation)
		m.shutdown()
	})

	t.Run("retained deleted alias rejected", func(t *testing.T) {
		st := openApplyStore(t)
		cfg := applyTestConfig(t)
		alias := persistedRootAlias(t, cfg.LocalRoot, "legacy")
		hash := "2345678901234567890123456789012345678901"
		nextRun := now
		created, inserted, err := st.CreateSubmission(ctx, domain.Submission{
			Download: domain.Download{
				Hash: hash, Name: "retained", SourceKind: domain.SourceMagnet,
				SubmissionURI: "magnet:?xt=urn:btih:" + hash, CloudFolder: "/cloud",
				SavePath: alias, TotalSize: 1, State: domain.StateAccepted,
				PhaseStartedAt: now, NextRunAt: &nextRun, CreatedAt: now, UpdatedAt: now,
			},
			Files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "file", Size: 1}},
		})
		if err != nil || !inserted {
			t.Fatalf("CreateSubmission: inserted=%t err=%v", inserted, err)
		}
		if err := st.RequestDelete(ctx, []string{created.Hash}, false, now.Add(time.Minute)); err != nil {
			t.Fatalf("RequestDelete: %v", err)
		}
		claim, err := st.ClaimDue(ctx, "runtime-test", now.Add(2*time.Minute), time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("ClaimDue: claim=%+v err=%v", claim, err)
		}
		deleted := claim.Download
		deleted.State = domain.StateDeleted
		deleted.ContentPath = "/cloud/content"
		deleted.UpdatedAt = now.Add(3 * time.Minute)
		if err := st.CommitClaim(ctx, *claim, deleted); err != nil {
			t.Fatalf("CommitClaim: %v", err)
		}

		m := newManager(&switchHandler{}, st, nil, reconcile.RealClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if _, err := m.build(ctx, cfg); err == nil {
			t.Fatal("build accepted retained deleted download alias")
		}
	})
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
	sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
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

// TestConfiguredRuntimeMountsAPIs verifies that the built runtime keeps the
// native and qBittorrent authentication boundaries independent.
func TestConfiguredRuntimeMountsAPIs(t *testing.T) {
	ctx := context.Background()
	st := openApplyStore(t)
	cfg := applyTestConfig(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
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

	qbtSecret, err := st.GenerateQBTAPIKey(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateQBTAPIKey: %v", err)
	}
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v2/app/version", nil)
	request.Header.Set("Authorization", "Bearer "+string(qbtSecret))
	root.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("qBittorrent Bearer status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
}
