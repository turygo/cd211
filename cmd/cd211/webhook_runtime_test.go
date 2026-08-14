package main

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/webhook"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartWebhookDispatcherRejectsNilDependencies(t *testing.T) {
	if _, err := startWebhookDispatcher(context.Background(), nil, discardLogger()); err == nil {
		t.Error("nil repository accepted")
	}
	if _, err := startWebhookDispatcher(context.Background(), &blockingClaimRepository{}, nil); err == nil {
		t.Error("nil logger accepted")
	}
}

func TestWebhookProcessStartsAndStops(t *testing.T) {
	dispatcher, err := startWebhookDispatcher(context.Background(), openApplyStore(t), discardLogger())
	if err != nil {
		t.Fatalf("startWebhookDispatcher: %v", err)
	}
	// A healthy dispatcher runs until it is cancelled; an early exit is the
	// fatal condition the main serve loop would surface.
	select {
	case <-dispatcher.done:
		t.Fatal("dispatcher exited while the process is active")
	default:
	}
	if err := dispatcher.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// Shutdown is idempotent: a repeated call observes the same completion and
	// returns the same stored result without waiting for a second value.
	if err := dispatcher.shutdown(); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestWebhookProcessSurvivesGenerationSwaps(t *testing.T) {
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

	logger := discardLogger()
	sessions, err := session.New(st, reconcile.RealClock{}, rand.Reader, sessionTTL, sessionRefreshInterval, sessionCapacity)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	root := &switchHandler{}
	m := newManager(root, st, sessions, reconcile.RealClock{}, logger)

	// The dispatcher is process-owned: it starts before any generation and is
	// never attached to the manager.
	dispatcher, err := startWebhookDispatcher(ctx, st, logger)
	if err != nil {
		t.Fatalf("startWebhookDispatcher: %v", err)
	}
	defer func() {
		if err := dispatcher.shutdown(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	first, err := m.build(ctx, cfg)
	if err != nil {
		t.Fatalf("build first generation: %v", err)
	}
	m.activate(first)

	// The configured runtime exposes the Webhook UI through the root; the
	// unauthenticated route redirects to the login page.
	recorder := httptest.NewRecorder()
	root.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/webhooks", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Errorf("/webhooks after activate = %d %q, want 303 /login", recorder.Code, recorder.Header().Get("Location"))
	}

	// A settings hot swap rebuilds and replaces the generation...
	if err := m.Apply(ctx, cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if m.currentGeneration() == first {
		t.Error("current generation unchanged after Apply")
	}

	// ...and must leave the dispatcher untouched: no restart, no cancel.
	select {
	case <-dispatcher.done:
		t.Fatal("dispatcher stopped during generation swap")
	default:
	}

	// Retiring the current generation likewise never touches the dispatcher.
	m.shutdown()
	select {
	case <-dispatcher.done:
		t.Fatal("dispatcher stopped during manager shutdown")
	default:
	}
}

// blockingClaimRepository blocks every claim until the context is cancelled,
// which lets the test observe shutdown waiting for in-flight repository work.
type blockingClaimRepository struct {
	webhook.Repository
	block func()
}

func (r *blockingClaimRepository) ClaimWebhookDue(ctx context.Context, _ string, _ time.Time, _ time.Duration) (*outbox.Claim, error) {
	if r.block != nil {
		r.block()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, nil
}

func (r *blockingClaimRepository) NextWebhookDue(ctx context.Context, _ time.Time) (*time.Time, error) {
	return nil, nil
}

func (r *blockingClaimRepository) PruneWebhookDeliveries(ctx context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestWebhookProcessShutdownWaitsForWorkers(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	repo := &blockingClaimRepository{block: func() {
		once.Do(func() { close(entered) })
		<-release
	}}
	dispatcher, err := startWebhookDispatcher(context.Background(), repo, discardLogger())
	if err != nil {
		t.Fatalf("startWebhookDispatcher: %v", err)
	}

	// Wait until a worker is blocked inside the repository, then shut down.
	<-entered
	done := make(chan struct{})
	go func() {
		_ = dispatcher.shutdown()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("shutdown returned while a worker was still inside the repository")
	case <-time.After(100 * time.Millisecond):
	}
	// Releasing the in-flight claim is the only thing that can let the worker
	// exit, so shutdown must wait for it.
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not wait for the in-flight worker")
	}

	// Shutdown is idempotent: a repeated call observes the same completion and
	// returns without waiting on the already-exited workers again.
	second := make(chan struct{})
	go func() {
		if err := dispatcher.shutdown(); err != nil {
			t.Errorf("second shutdown: %v", err)
		}
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("second shutdown did not return")
	}
}

// completedWebhookProcess builds a process whose dispatcher goroutine has
// already exited with the given result, exactly as the serve loop observes it
// once done is closed.
func completedWebhookProcess(result error) *webhookProcess {
	w := &webhookProcess{done: make(chan struct{}), result: result}
	close(w.done)
	return w
}

// TestWebhookServeLoopObservesRootCancelAsNormal drives a real dispatcher
// through the serve loop's observation path: root cancellation closes done,
// the completion classifies as normal shutdown, and the deferred shutdown
// still returns the stored result instead of waiting for a second value.
func TestWebhookServeLoopObservesRootCancelAsNormal(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	dispatcher, err := startWebhookDispatcher(root, openApplyStore(t), discardLogger())
	if err != nil {
		t.Fatalf("startWebhookDispatcher: %v", err)
	}
	cancelRoot()
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not stop after root cancellation")
	}
	if err := dispatcher.exitError(root); err != nil {
		t.Fatalf("exitError after root cancellation = %v, want normal shutdown", err)
	}
	if err := dispatcher.shutdown(); err != nil {
		t.Fatalf("shutdown after observed completion: %v", err)
	}
}

// TestWebhookServeLoopObservesEarlyExitAsFatal drives a real dispatcher whose
// context is cancelled without root cancellation (the only way Run returns),
// then classifies the completion the way the serve loop does: a nil stored
// result while the root context is still active is the fatal
// "webhook dispatcher stopped unexpectedly" error. The observation must not
// consume the completion, so the deferred shutdown still returns.
func TestWebhookServeLoopObservesEarlyExitAsFatal(t *testing.T) {
	dispatcher, err := startWebhookDispatcher(context.Background(), openApplyStore(t), discardLogger())
	if err != nil {
		t.Fatalf("startWebhookDispatcher: %v", err)
	}
	// Simulate an external shutdown that never reaches the serve loop: the
	// dispatcher exits while the root context is still active.
	dispatcher.cancel()
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not exit after cancel")
	}
	err = dispatcher.exitError(context.Background())
	if err == nil || err.Error() != "webhook dispatcher stopped unexpectedly" {
		t.Fatalf("exitError = %v, want fatal %q", err, "webhook dispatcher stopped unexpectedly")
	}
	// Store closure is reachable after a fatal observation: shutdown observes
	// the same closed done and returns the stored result.
	if err := dispatcher.shutdown(); err != nil {
		t.Fatalf("shutdown after fatal observation: %v", err)
	}
}

// TestWebhookServeLoopTreatsStoredErrorAsFatal pins the serve loop's handling
// of a non-nil stored result: it is fatal as-is while the root context is
// still active.
func TestWebhookServeLoopTreatsStoredErrorAsFatal(t *testing.T) {
	sentinel := errors.New("dispatcher invariant broken")
	if err := completedWebhookProcess(sentinel).exitError(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("exitError = %v, want the stored error", err)
	}
}

// TestWebhookServeLoopRootCancelWinsCompletionRace pins the root-cancel vs
// completion race: even when done closed with a stored error, a cancelled
// root context classifies the completion as normal shutdown.
func TestWebhookServeLoopRootCancelWinsCompletionRace(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	cancel()
	sentinel := errors.New("dispatcher invariant broken")
	if err := completedWebhookProcess(sentinel).exitError(root); err != nil {
		t.Fatalf("exitError with cancelled root = %v, want normal shutdown", err)
	}
}
