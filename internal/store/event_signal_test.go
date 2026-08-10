package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/outbox"
)

// signalClosed reports whether a snapshotted event channel has been closed.
func signalClosed(channel <-chan struct{}) bool {
	select {
	case _, ok := <-channel:
		return !ok
	default:
		return false
	}
}

func TestEventSignalNotifiedAfterCommittedEvents(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	// A fresh store's signal has no pending notification.
	if signalClosed(store.EventSignal().Snapshot()) {
		t.Fatal("fresh event signal is already closed")
	}

	// A submission that inserts download.created notifies after commit.
	snapshot := store.EventSignal().Snapshot()
	sub := testSubmission("a", now)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%t, %v), want inserted", inserted, err)
	}
	if !signalClosed(snapshot) {
		t.Error("event signal not notified after a committed download event")
	}

	// The next snapshot is open again, and a terminal claim commit notifies.
	next := store.EventSignal().Snapshot()
	if signalClosed(next) {
		t.Fatal("snapshot after notify is already closed")
	}
	walkDownload(t, store, sub.Download.Hash, completedStates, now)
	if !signalClosed(next) {
		t.Error("event signal not notified after the completed claim commit")
	}
}

func TestEventSignalNotifiedAfterWebhookTestCommit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "alerts", true, false, now)

	snapshot := store.EventSignal().Snapshot()
	if _, err := store.EnqueueTestDelivery(ctx, endpoint.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("EnqueueTestDelivery() error = %v", err)
	}
	if !signalClosed(snapshot) {
		t.Error("event signal not notified after a committed webhook.test event")
	}

	// Enqueueing on a deleted endpoint fails before commit: no notify.
	if err := store.DeleteWebhookEndpoint(ctx, endpoint.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("DeleteWebhookEndpoint() error = %v", err)
	}
	failed := store.EventSignal().Snapshot()
	if _, err := store.EnqueueTestDelivery(ctx, endpoint.ID, now.Add(3*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Fatalf("EnqueueTestDelivery(deleted) error = %v, want outbox.ErrNotFound", err)
	}
	if signalClosed(failed) {
		t.Error("failed test delivery notified the event signal")
	}
}

func TestEventSignalNotNotifiedOnNoOpMutations(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	sub := testSubmission("b", now)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%t, %v), want inserted", inserted, err)
	}

	// A duplicate live submission is a no-op: no event, no notify.
	snapshot := store.EventSignal().Snapshot()
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || inserted {
		t.Fatalf("duplicate CreateSubmission() = (%t, %v), want existing", inserted, err)
	}
	if signalClosed(snapshot) {
		t.Error("no-op duplicate submission notified the event signal")
	}

	// A same-value SetCategory is also silent.
	if err := store.SetCategory(ctx, sub.Download.Hash, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("SetCategory(same) error = %v", err)
	}
	if signalClosed(snapshot) {
		t.Error("no-op SetCategory notified the event signal")
	}

	// An idempotent Start on an accepted download emits nothing.
	if err := store.Start(ctx, sub.Download.Hash, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("Start(accepted) error = %v", err)
	}
	if signalClosed(snapshot) {
		t.Error("idempotent Start notified the event signal")
	}
}

func TestEventSignalNotNotifiedOnRollback(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	sub := testSubmission("c", now)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%t, %v), want inserted", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "worker", now.Add(time.Minute), 3*time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v), want a claim", claim, err)
	}
	// Bump the row version between claim and commit so the commit CAS fails
	// and the transaction rolls back.
	if err := store.SetCategory(ctx, sub.Download.Hash, "movies", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("SetCategory() error = %v", err)
	}

	snapshot := store.EventSignal().Snapshot()
	if err := store.CommitClaim(ctx, *claim, claim.Download); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale CommitClaim() error = %v, want ErrClaimLost", err)
	}
	if signalClosed(snapshot) {
		t.Error("rolled-back claim commit notified the event signal")
	}
}
