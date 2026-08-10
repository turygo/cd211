package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

var completedStates = []domain.State{
	domain.StateSubmittingOffline,
	domain.StateWaitingOffline,
	domain.StateSubmittingCopy,
	domain.StateWaitingCopy,
	domain.StateVerifyingLocal,
	domain.StateCompleted,
}

func TestWebhookEndpointLifecycle(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	created, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "  Alerts ", URL: "https://example.com/hook?token=abc", SubscribeCompleted: true,
	}, now)
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint() error = %v", err)
	}
	if created.Name != "Alerts" || !created.Enabled || !created.SubscribeCompleted || created.SubscribeFailed {
		t.Errorf("created endpoint = %+v, want trimmed name, enabled, completed subscription", created)
	}
	if created.HMACSecret == "" {
		t.Errorf("created HMACSecret = %q, want generated secret", created.HMACSecret)
	}
	if created.URL != "" || created.DisplayURL != "https://example.com/hook?…" {
		t.Errorf("created endpoint URL = %q display %q, want masked URL", created.URL, created.DisplayURL)
	}
	if created.RowVersion != 0 || created.DeletedAt != nil {
		t.Errorf("created endpoint version/deleted = %d/%v, want 0/nil", created.RowVersion, created.DeletedAt)
	}

	if _, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "alerts", URL: "https://other.example/hook", SubscribeFailed: true,
	}, now); !errors.Is(err, outbox.ErrNameConflict) {
		t.Errorf("duplicate name error = %v, want outbox.ErrNameConflict", err)
	}
	if _, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "none", URL: "https://example.com/hook",
	}, now); err == nil {
		t.Error("create without subscription error = nil, want error")
	}
	if _, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "bad", URL: "not-a-url", SubscribeCompleted: true,
	}, now); err == nil {
		t.Error("create with relative URL error = nil, want error")
	}

	listed, err := store.ListWebhookEndpoints(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListWebhookEndpoints() = (%+v, %v), want 1 endpoint", listed, err)
	}
	for _, endpoint := range listed {
		if endpoint.HMACSecret != "" || endpoint.BearerToken != "" || endpoint.URL != "" {
			t.Errorf("list leaked secrets/URL: %+v", endpoint)
		}
	}
	got, err := store.GetWebhookEndpoint(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetWebhookEndpoint() error = %v", err)
	}
	if got.HMACSecret != "" || got.BearerToken != "" || got.URL != "" || got.DisplayURL != "https://example.com/hook?…" {
		t.Errorf("GetWebhookEndpoint() = %+v, want masked endpoint", got)
	}

	updated, err := store.UpdateWebhookEndpoint(ctx, created.ID, outbox.EndpointInput{
		Name: "Alerts", URL: "https://example.com/hook", SubscribeCompleted: true, BearerToken: "  tok123  ",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("UpdateWebhookEndpoint(bearer) error = %v", err)
	}
	if updated.DisplayURL != "https://example.com/hook" || updated.RowVersion != 1 {
		t.Errorf("updated endpoint = %+v, want new URL and version 1", updated)
	}
	raw, err := store.queries.GetEndpointRaw(ctx, created.ID)
	if err != nil || !raw.BearerToken.Valid || raw.BearerToken.String != "tok123" {
		t.Errorf("stored bearer = %+v (err %v), want trimmed tok123", raw.BearerToken, err)
	}

	if _, err := store.UpdateWebhookEndpoint(ctx, created.ID, outbox.EndpointInput{
		Name: "Alerts", URL: "", SubscribeCompleted: true,
	}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpdateWebhookEndpoint(preserve) error = %v", err)
	}
	raw, err = store.queries.GetEndpointRaw(ctx, created.ID)
	if err != nil || raw.Url != "https://example.com/hook" || !raw.BearerToken.Valid {
		t.Errorf("preserve-on-empty failed: url=%q bearer=%+v err=%v", raw.Url, raw.BearerToken, err)
	}

	if _, err := store.UpdateWebhookEndpoint(ctx, created.ID, outbox.EndpointInput{
		Name: "Alerts", URL: "", SubscribeCompleted: true, ClearBearerToken: true,
	}, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("UpdateWebhookEndpoint(clear bearer) error = %v", err)
	}
	raw, err = store.queries.GetEndpointRaw(ctx, created.ID)
	if err != nil || raw.BearerToken.Valid {
		t.Errorf("clear bearer failed: %+v err=%v", raw.BearerToken, err)
	}

	rotated, err := store.RotateWebhookEndpointSecret(ctx, created.ID, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("RotateWebhookEndpointSecret() error = %v", err)
	}
	if rotated.HMACSecret == "" || rotated.HMACSecret == created.HMACSecret {
		t.Errorf("rotated secret = %q, want replacement", rotated.HMACSecret)
	}

	if err := store.SetWebhookEndpointEnabled(ctx, created.ID, false, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	got, _ = store.GetWebhookEndpoint(ctx, created.ID)
	if got.Enabled {
		t.Error("endpoint still enabled after disable")
	}
	if err := store.SetWebhookEndpointEnabled(ctx, created.ID, true, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("enable endpoint: %v", err)
	}
	if err := store.SetWebhookEndpointEnabled(ctx, created.ID+99, false, now.Add(6*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("enable missing endpoint error = %v, want outbox.ErrNotFound", err)
	}

	if err := store.DeleteWebhookEndpoint(ctx, created.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatalf("DeleteWebhookEndpoint() error = %v", err)
	}
	if _, err := store.GetWebhookEndpoint(ctx, created.ID); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("get deleted endpoint error = %v, want outbox.ErrNotFound", err)
	}
	if err := store.DeleteWebhookEndpoint(ctx, created.ID, now.Add(8*time.Minute)); err != nil {
		t.Errorf("idempotent delete error = %v, want nil", err)
	}
	if err := store.DeleteWebhookEndpoint(ctx, created.ID+99, now.Add(8*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("delete missing endpoint error = %v, want outbox.ErrNotFound", err)
	}
	if _, err := store.UpdateWebhookEndpoint(ctx, created.ID, outbox.EndpointInput{
		Name: "Alerts", URL: "https://x.example", SubscribeCompleted: true,
	}, now.Add(9*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("update deleted endpoint error = %v, want outbox.ErrNotFound", err)
	}
	if err := store.SetWebhookEndpointEnabled(ctx, created.ID, true, now.Add(9*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("enable deleted endpoint error = %v, want outbox.ErrNotFound", err)
	}
	if _, err := store.RotateWebhookEndpointSecret(ctx, created.ID, now.Add(9*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("rotate deleted endpoint error = %v, want outbox.ErrNotFound", err)
	}
}

func TestDownloadEventEmission(t *testing.T) {
	t.Run("created duplicate and revive", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

		sub := testSubmission("a", now)
		created, inserted, err := store.CreateSubmission(ctx, sub)
		if err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%+v, %t, %v)", created, inserted, err)
		}
		events := eventsForDownload(t, store, created.Hash)
		if len(events) != 1 || events[0].Type != outbox.EventTypeCreated {
			t.Fatalf("fresh create events = %+v, want one created", events)
		}
		if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || inserted {
			t.Fatalf("duplicate CreateSubmission() = (%t, %v), want existing", inserted, err)
		}
		if events := eventsForDownload(t, store, created.Hash); len(events) != 1 {
			t.Fatalf("duplicate create emitted %d events, want 1", len(events))
		}

		if err := store.RequestDelete(ctx, []string{created.Hash}, false, now.Add(time.Minute)); err != nil {
			t.Fatalf("RequestDelete() error = %v", err)
		}
		claim, err := store.ClaimDue(ctx, "revive", now.Add(2*time.Minute), time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("ClaimDue(revive) = (%+v, %v)", claim, err)
		}
		deleted := claim.Download
		deleted.State = domain.StateDeleted
		deleted.UpdatedAt = now.Add(3 * time.Minute)
		if err := store.CommitClaim(ctx, *claim, deleted); err != nil {
			t.Fatalf("CommitClaim(deleted): %v", err)
		}
		revived, inserted, err := store.CreateSubmission(ctx, testSubmission("a", now.Add(4*time.Minute)))
		if err != nil || !inserted {
			t.Fatalf("revive CreateSubmission() = (%t, %v)", inserted, err)
		}
		reviveEvents := eventsForDownload(t, store, revived.Hash)
		// Deletion completion is itself a real transition and emits
		// download.state_changed, so the revived lifecycle holds four events:
		// created, delete requested, deleted, and the revival created.
		if len(reviveEvents) != 4 || reviveEvents[3].Type != outbox.EventTypeCreated {
			t.Fatalf("revive events = %+v, want final created", reviveEvents)
		}
		data := unmarshalPayload(t, reviveEvents[3].Payload)["data"].(map[string]any)
		if data["previous_state"] != string(domain.StateDeleted) {
			t.Errorf("revive previous_state = %v, want DELETED", data["previous_state"])
		}
	})

	t.Run("category change", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		sub := testSubmission("a", now)
		created, inserted, err := store.CreateSubmission(ctx, sub)
		if err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%+v, %t, %v)", created, inserted, err)
		}
		if err := store.SetCategory(ctx, created.Hash, "Movies", now.Add(time.Minute)); err != nil {
			t.Fatalf("SetCategory() error = %v", err)
		}
		if events := eventsForDownload(t, store, created.Hash); len(events) != 2 || events[1].Type != outbox.EventTypeCategoryChanged {
			t.Fatalf("category change events = %+v, want category_changed", events)
		}
		if err := store.SetCategory(ctx, created.Hash, "Movies", now.Add(2*time.Minute)); err != nil {
			t.Fatalf("same-value SetCategory() error = %v", err)
		}
		if events := eventsForDownload(t, store, created.Hash); len(events) != 2 {
			t.Fatalf("same-value SetCategory emitted %d events, want 2", len(events))
		}
	})

	t.Run("start", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		stopped := testSubmission("d", now)
		stopped.Download.State = domain.StateStopped
		stopped.Download.NextRunAt = nil
		if _, inserted, err := store.CreateSubmission(ctx, stopped); err != nil || !inserted {
			t.Fatalf("CreateSubmission(stopped) = (%t, %v)", inserted, err)
		}
		if err := store.Start(ctx, stopped.Download.Hash, now.Add(time.Minute)); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		if events := eventsForDownload(t, store, stopped.Download.Hash); len(events) != 2 || events[1].Type != outbox.EventTypeStateChanged {
			t.Fatalf("start events = %+v, want state_changed", events)
		}
		if err := store.Start(ctx, stopped.Download.Hash, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("idempotent Start() error = %v", err)
		}
		if events := eventsForDownload(t, store, stopped.Download.Hash); len(events) != 2 {
			t.Fatalf("idempotent Start emitted %d events, want 2", len(events))
		}
	})

	t.Run("retry failed", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		sub := testSubmission("f", now)
		if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%t, %v)", inserted, err)
		}
		claim, err := store.ClaimDue(ctx, "fail", now.Add(time.Minute), time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("ClaimDue(fail) = (%+v, %v)", claim, err)
		}
		next := claim.Download
		next.State = domain.StateFailed
		next.LastError = "boom"
		next.UpdatedAt = now.Add(2 * time.Minute)
		if err := store.CommitClaim(ctx, *claim, next); err != nil {
			t.Fatalf("CommitClaim(failed): %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 || events[1].Type != outbox.EventTypeFailed {
			t.Fatalf("failed events = %+v, want failed", events)
		}
		if err := store.Retry(ctx, sub.Download.Hash, domain.StateAccepted, now.Add(3*time.Minute)); err != nil {
			t.Fatalf("Retry(failed) error = %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 3 || events[2].Type != outbox.EventTypeStateChanged {
			t.Fatalf("retry events = %+v, want state_changed", events)
		}
	})

	t.Run("cleanup retry", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		sub := testSubmission("e", now)
		if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%t, %v)", inserted, err)
		}
		if err := store.RequestDelete(ctx, []string{sub.Download.Hash}, false, now.Add(time.Minute)); err != nil {
			t.Fatalf("RequestDelete() error = %v", err)
		}
		claim, err := store.ClaimDue(ctx, "cleanup", now.Add(2*time.Minute), time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("ClaimDue(cleanup) = (%+v, %v)", claim, err)
		}
		failed := claim.Download
		failed.LastError = "local deletion failed"
		failed.UpdatedAt = now.Add(3 * time.Minute)
		if err := store.CommitClaim(ctx, *claim, failed); err != nil {
			t.Fatalf("CommitClaim(cleanup failure): %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 {
			t.Fatalf("same-state cleanup failure emitted %d events, want 2", len(events))
		}
		if err := store.Retry(ctx, sub.Download.Hash, domain.StateDeleteRequested, now.Add(4*time.Minute)); err != nil {
			t.Fatalf("Retry(cleanup) error = %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 {
			t.Fatalf("cleanup retry emitted %d events, want 2", len(events))
		}
	})

	t.Run("cancel", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		sub := testSubmission("e", now)
		if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%t, %v)", inserted, err)
		}
		if err := store.Cancel(ctx, sub.Download.Hash, now.Add(time.Minute)); err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 || events[1].Type != outbox.EventTypeStateChanged {
			t.Fatalf("cancel events = %+v, want state_changed", events)
		}
		if err := store.Cancel(ctx, sub.Download.Hash, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("idempotent Cancel() error = %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 {
			t.Fatalf("idempotent Cancel emitted %d events, want 2", len(events))
		}
	})

	t.Run("request delete", func(t *testing.T) {
		ctx := context.Background()
		store := testStore(t)
		now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		sub := testSubmission("e", now)
		if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
			t.Fatalf("CreateSubmission() = (%t, %v)", inserted, err)
		}
		if err := store.RequestDelete(ctx, []string{sub.Download.Hash}, false, now.Add(time.Minute)); err != nil {
			t.Fatalf("RequestDelete() error = %v", err)
		}
		if err := store.RequestDelete(ctx, []string{sub.Download.Hash}, true, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("RequestDelete(escalate) error = %v", err)
		}
		if events := eventsForDownload(t, store, sub.Download.Hash); len(events) != 2 {
			t.Fatalf("strengthening delete emitted %d events, want 2", len(events))
		}
	})
}

func TestCommitClaimTerminalFanoutAndAtomicity(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	testEndpoint(t, store, "alerts", true, true, now)

	sub := testSubmission("a", now)
	created, inserted, err := store.CreateSubmission(ctx, sub)
	if err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v)", created, inserted, err)
	}
	walkDownload(t, store, created.Hash, completedStates, now)

	events := eventsForDownload(t, store, created.Hash)
	if len(events) != 7 || events[len(events)-1].Type != outbox.EventTypeCompleted {
		t.Fatalf("completed walk events = %d, want 7 with final completed", len(events))
	}
	completedData := unmarshalPayload(t, events[len(events)-1].Payload)["data"].(map[string]any)
	if completedData["state"] != string(domain.StateCompleted) || completedData["previous_state"] != string(domain.StateVerifyingLocal) {
		t.Errorf("completed payload state = %v / %v, want COMPLETED / VERIFYING_LOCAL", completedData["state"], completedData["previous_state"])
	}
	if completedData["progress"] != float64(1) {
		t.Errorf("completed payload progress = %v, want 1", completedData["progress"])
	}
	if _, ok := completedData["completed_at"]; !ok {
		t.Errorf("completed payload must include completed_at")
	}
	stored, err := store.GetDownload(ctx, created.Hash)
	if err != nil || completedData["download_version"] != float64(stored.RowVersion) {
		t.Errorf("completed payload download_version = %v, want %d", completedData["download_version"], stored.RowVersion)
	}

	failedHash := failDownload(t, store, "f", now.Add(10*time.Second), "tracker passkey=secret rejected")
	failedEvents := eventsForDownload(t, store, failedHash)
	if len(failedEvents) != 2 || failedEvents[1].Type != outbox.EventTypeFailed {
		t.Fatalf("failed events = %+v, want download.failed", failedEvents)
	}
	failedData := unmarshalPayload(t, failedEvents[1].Payload)["data"].(map[string]any)
	if failedData["error"] != domain.RedactedErrorText {
		t.Errorf("failed payload error = %v, want %q", failedData["error"], domain.RedactedErrorText)
	}

	plainHash := failDownload(t, store, "e", now.Add(20*time.Second), "disk full")
	plainEvents := eventsForDownload(t, store, plainHash)
	plainData := unmarshalPayload(t, plainEvents[1].Payload)["data"].(map[string]any)
	if plainData["error"] != "disk full" {
		t.Errorf("plain failed payload error = %v, want disk full", plainData["error"])
	}

	deliveries := allDeliveries(t, store)
	if len(deliveries) != 3 {
		t.Fatalf("deliveries = %d, want exactly 3 (completed + 2 failed)", len(deliveries))
	}
	for _, delivery := range deliveries {
		if delivery.EndpointName != "alerts" || delivery.Status != outbox.StatusPending || delivery.NextAttemptAt == nil {
			t.Errorf("fanout delivery = %+v, want pending snapshot for alerts", delivery)
		}
	}

	// Atomicity: a lost claim leaves neither an event nor a delivery.
	atomicSub := testSubmission("c", now.Add(30*time.Second))
	if _, inserted, err := store.CreateSubmission(ctx, atomicSub); err != nil || !inserted {
		t.Fatalf("CreateSubmission(atomic) = (%t, %v)", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "atomic", now.Add(31*time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(atomic) = (%+v, %v)", claim, err)
	}
	if err := store.CommitClaim(ctx, Claim{Download: claim.Download, Owner: "other", State: claim.State, Version: claim.Version}, claim.Download); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale commit error = %v, want ErrClaimLost", err)
	}
	if events := eventsForDownload(t, store, atomicSub.Download.Hash); len(events) != 1 {
		t.Fatalf("stale commit created %d events, want 1", len(events))
	}
	mutated := claim.Download
	mutated.CloudFolder = "/other"
	if err := store.CommitClaim(ctx, *claim, mutated); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("immutable mutation error = %v, want ErrInvalidTransition", err)
	}
	if events := eventsForDownload(t, store, atomicSub.Download.Hash); len(events) != 1 {
		t.Fatalf("invalid transition created %d events, want 1", len(events))
	}
	sameState := claim.Download
	sameState.Name = "metadata"
	sameState.UpdatedAt = now.Add(33 * time.Second)
	if err := store.CommitClaim(ctx, *claim, sameState); err != nil {
		t.Fatalf("same-state commit error = %v", err)
	}
	if events := eventsForDownload(t, store, atomicSub.Download.Hash); len(events) != 1 {
		t.Fatalf("same-state commit created %d events, want 1", len(events))
	}
}

func TestClaimWebhookDueOrderingAndLease(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	testEndpoint(t, store, "alerts", true, false, now)

	subA := testSubmission("a", now)
	if _, inserted, err := store.CreateSubmission(ctx, subA); err != nil || !inserted {
		t.Fatalf("CreateSubmission(a) = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, subA.Download.Hash, completedStates, now)

	subB := testSubmission("b", now)
	if _, inserted, err := store.CreateSubmission(ctx, subB); err != nil || !inserted {
		t.Fatalf("CreateSubmission(b) = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, subB.Download.Hash, completedStates, now.Add(10*time.Second))

	claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(10*time.Second), 30*time.Second)
	if err != nil || claim == nil {
		t.Fatalf("ClaimWebhookDue() = (%+v, %v)", claim, err)
	}
	deliveryA, err := store.GetWebhookDelivery(ctx, claim.DeliveryID)
	if err != nil || deliveryA.AggregateID != subA.Download.Hash {
		t.Fatalf("first claim delivery = %+v (err %v), want download a", deliveryA, err)
	}
	if deliveryA.Status != outbox.StatusDelivering || deliveryA.AttemptCount != 1 || deliveryA.FirstAttemptAt == nil || deliveryA.LeaseOwner != "worker" {
		t.Errorf("claimed delivery = %+v, want delivering attempt 1 with lease", deliveryA)
	}
	if second, err := store.ClaimWebhookDue(ctx, "worker-2", now.Add(11*time.Second), 30*time.Second); err != nil || second != nil {
		t.Fatalf("claim while leased = (%+v, %v), want nil", second, err)
	}

	// An expired lease is reclaimable: same row, attempt 2.
	expired, err := store.ClaimWebhookDue(ctx, "worker-2", now.Add(41*time.Second), 30*time.Second)
	if err != nil || expired == nil || expired.DeliveryID != claim.DeliveryID || expired.AttemptCount != 2 {
		t.Fatalf("expired lease claim = (%+v, %v), want attempt 2 on same delivery", expired, err)
	}
	if err := store.CommitWebhookClaim(ctx, *expired, successAt(now.Add(42*time.Second)), now.Add(42*time.Second)); err != nil {
		t.Fatalf("CommitWebhookClaim(succeeded) error = %v", err)
	}

	// Claim B and hold it delivering; a second lifecycle delivery for the same
	// endpoint+aggregate must not be claimable until the earlier row resolves.
	claimB, err := store.ClaimWebhookDue(ctx, "worker", now.Add(43*time.Second), 30*time.Second)
	if err != nil || claimB == nil {
		t.Fatalf("claim B = (%+v, %v)", claimB, err)
	}
	recycleDownload(t, store, subB.Download.Hash, now.Add(50*time.Second))
	if blocked, err := store.ClaimWebhookDue(ctx, "worker", now.Add(70*time.Second), 30*time.Second); err != nil || blocked != nil {
		t.Fatalf("claim while earlier row delivering = (%+v, %v), want nil", blocked, err)
	}
	reclaim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(74*time.Second), 30*time.Second)
	if err != nil || reclaim == nil || reclaim.DeliveryID != claimB.DeliveryID {
		t.Fatalf("expired-lease reclaim = (%+v, %v), want same B delivery", reclaim, err)
	}
	if err := store.CommitWebhookClaim(ctx, *reclaim, successAt(now.Add(75*time.Second)), now.Add(75*time.Second)); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	nextClaim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(76*time.Second), 30*time.Second)
	if err != nil || nextClaim == nil {
		t.Fatalf("claim after earlier resolution = (%+v, %v)", nextClaim, err)
	}
	nextDelivery, err := store.GetWebhookDelivery(ctx, nextClaim.DeliveryID)
	if err != nil || nextDelivery.AggregateID != subB.Download.Hash || nextDelivery.EventID == claimB.EventID {
		t.Fatalf("post-recycle claim = %+v (err %v), want the recycled b delivery", nextDelivery, err)
	}
}

func TestWebhookDisableAndDeletePause(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "alerts", true, false, now)

	sub := testSubmission("a", now)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, sub.Download.Hash, completedStates, now)

	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, false, now.Add(30*time.Second)); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(31*time.Second), 30*time.Second); err != nil || claim != nil {
		t.Fatalf("claim on disabled endpoint = (%+v, %v), want nil", claim, err)
	}
	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, true, now.Add(32*time.Second)); err != nil {
		t.Fatalf("enable endpoint: %v", err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(33*time.Second), 30*time.Second); err != nil || claim == nil {
		t.Fatalf("claim after re-enable = (%+v, %v), want delivery", claim, err)
	} else if err := store.CommitWebhookClaim(ctx, *claim, successAt(now.Add(34*time.Second)), now.Add(34*time.Second)); err != nil {
		t.Fatalf("commit after re-enable: %v", err)
	}

	// An in-flight delivery must be claimed while its endpoint is still
	// enabled; its commit then resolves to cancelled. Walk sub3 before sub2 so
	// sub3's delivery is due earliest and the claim below selects it.
	sub3 := testSubmission("c", now.Add(35*time.Second))
	if _, inserted, err := store.CreateSubmission(ctx, sub3); err != nil || !inserted {
		t.Fatalf("CreateSubmission(c) = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, sub3.Download.Hash, completedStates, now.Add(35*time.Second))
	inflight, err := store.ClaimWebhookDue(ctx, "worker", now.Add(50*time.Second), 30*time.Second)
	if err != nil || inflight == nil {
		t.Fatalf("in-flight claim = (%+v, %v)", inflight, err)
	}

	// Soft delete cancels pending deliveries and pauses new claims.
	sub2 := testSubmission("b", now.Add(40*time.Second))
	if _, inserted, err := store.CreateSubmission(ctx, sub2); err != nil || !inserted {
		t.Fatalf("CreateSubmission(b) = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, sub2.Download.Hash, completedStates, now.Add(40*time.Second))
	deliveries := allDeliveries(t, store)
	if len(deliveries) != 3 {
		t.Fatalf("deliveries = %d, want 3", len(deliveries))
	}
	if err := store.DeleteWebhookEndpoint(ctx, endpoint.ID, now.Add(60*time.Second)); err != nil {
		t.Fatalf("DeleteWebhookEndpoint() error = %v", err)
	}
	// The still-pending delivery (newest, from sub2) is cancelled; the earlier
	// succeeded delivery is untouched; the in-flight delivery survives until
	// its commit resolves it to cancelled.
	cancelled, err := store.GetWebhookDelivery(ctx, deliveries[0].ID)
	if err != nil || cancelled.Status != outbox.StatusCancelled {
		t.Errorf("pending delivery after endpoint delete = %+v (err %v), want cancelled", cancelled, err)
	}
	settled, err := store.GetWebhookDelivery(ctx, deliveries[2].ID)
	if err != nil || settled.Status != outbox.StatusSucceeded {
		t.Errorf("succeeded delivery after endpoint delete = %+v (err %v), want unchanged", settled, err)
	}
	still, err := store.GetWebhookDelivery(ctx, inflight.DeliveryID)
	if err != nil || still.Status != outbox.StatusDelivering {
		t.Errorf("in-flight delivery after endpoint delete = %+v (err %v), want still delivering", still, err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(61*time.Second), 30*time.Second); err != nil || claim != nil {
		t.Fatalf("claim on deleted endpoint = (%+v, %v), want nil", claim, err)
	}
	if err := store.CommitWebhookClaim(ctx, *inflight, successAt(now.Add(62*time.Second)), now.Add(62*time.Second)); err != nil {
		t.Fatalf("commit after endpoint delete error = %v", err)
	}
	resolved, err := store.GetWebhookDelivery(ctx, inflight.DeliveryID)
	if err != nil || resolved.Status != outbox.StatusCancelled {
		t.Errorf("in-flight delivery after delete = %+v (err %v), want cancelled", resolved, err)
	}
}

func TestCommitWebhookClaimOutcomes(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	testEndpoint(t, store, "alerts", true, false, now)

	hashS := completeDownload(t, store, "e", now.Add(10*time.Second))
	hashR := completeDownload(t, store, "c", now.Add(70*time.Second))
	hashD := completeDownload(t, store, "d", now.Add(130*time.Second))
	completeDownload(t, store, "a", now.Add(190*time.Second))
	completeDownload(t, store, "b", now.Add(250*time.Second))

	claimS, err := store.ClaimWebhookDue(ctx, "worker", now.Add(30*time.Second), 30*time.Second)
	if err != nil || claimS == nil {
		t.Fatalf("claim S = (%+v, %v)", claimS, err)
	}
	if delivery, _ := store.GetWebhookDelivery(ctx, claimS.DeliveryID); delivery.AggregateID != hashS {
		t.Fatalf("claim S delivery = %s, want %s", delivery.AggregateID, hashS)
	}
	deliveredAt := now.Add(31 * time.Second)
	if err := store.CommitWebhookClaim(ctx, *claimS, outbox.Result{Status: outbox.StatusSucceeded, LastHTTPStatus: 200, DeliveredAt: &deliveredAt}, deliveredAt); err != nil {
		t.Fatalf("commit succeeded: %v", err)
	}
	succeeded, err := store.GetWebhookDelivery(ctx, claimS.DeliveryID)
	if err != nil || succeeded.Status != outbox.StatusSucceeded || succeeded.LastHTTPStatus != 200 ||
		succeeded.DeliveredAt == nil || !succeeded.DeliveredAt.Equal(deliveredAt) ||
		succeeded.LeaseOwner != "" || succeeded.LeaseUntil != nil {
		t.Errorf("succeeded delivery = %+v (err %v), want terminal success", succeeded, err)
	}

	claimR, err := store.ClaimWebhookDue(ctx, "worker", now.Add(90*time.Second), 30*time.Second)
	if err != nil || claimR == nil {
		t.Fatalf("claim R = (%+v, %v)", claimR, err)
	}
	if delivery, _ := store.GetWebhookDelivery(ctx, claimR.DeliveryID); delivery.AggregateID != hashR {
		t.Fatalf("claim R delivery = %s, want %s", delivery.AggregateID, hashR)
	}
	retryAt := now.Add(92 * time.Second)
	if err := store.CommitWebhookClaim(ctx, *claimR, outbox.Result{
		Status: outbox.StatusPending, NextAttemptAt: &retryAt, LastHTTPStatus: 500, LastError: "HTTP 500",
	}, now.Add(91*time.Second)); err != nil {
		t.Fatalf("commit retry: %v", err)
	}
	retried, err := store.GetWebhookDelivery(ctx, claimR.DeliveryID)
	if err != nil || retried.Status != outbox.StatusPending || retried.NextAttemptAt == nil ||
		!retried.NextAttemptAt.Equal(retryAt) || retried.LastError != "HTTP 500" || retried.LastHTTPStatus != 500 ||
		retried.LeaseOwner != "" {
		t.Errorf("retried delivery = %+v (err %v), want pending scheduled retry", retried, err)
	}

	// The retry row is reclaimed and resolved before the next delivery.
	claimR2, err := store.ClaimWebhookDue(ctx, "worker", now.Add(150*time.Second), 30*time.Second)
	if err != nil || claimR2 == nil || claimR2.DeliveryID != claimR.DeliveryID || claimR2.AttemptCount != 2 {
		t.Fatalf("retry reclaim = (%+v, %v), want attempt 2 on R", claimR2, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claimR2, successAt(now.Add(151*time.Second)), now.Add(151*time.Second)); err != nil {
		t.Fatalf("commit retry success: %v", err)
	}

	claimD, err := store.ClaimWebhookDue(ctx, "worker", now.Add(152*time.Second), 30*time.Second)
	if err != nil || claimD == nil {
		t.Fatalf("claim D = (%+v, %v)", claimD, err)
	}
	if delivery, _ := store.GetWebhookDelivery(ctx, claimD.DeliveryID); delivery.AggregateID != hashD {
		t.Fatalf("claim D delivery = %s, want %s", delivery.AggregateID, hashD)
	}
	if err := store.CommitWebhookClaim(ctx, *claimD, outbox.Result{
		Status: outbox.StatusDead, LastHTTPStatus: 502, LastError: "HTTP 502",
	}, now.Add(153*time.Second)); err != nil {
		t.Fatalf("commit dead: %v", err)
	}
	dead, err := store.GetWebhookDelivery(ctx, claimD.DeliveryID)
	if err != nil || dead.Status != outbox.StatusDead || dead.LastHTTPStatus != 502 || dead.DeliveredAt != nil {
		t.Errorf("dead delivery = %+v (err %v), want dead", dead, err)
	}

	claimX, err := store.ClaimWebhookDue(ctx, "worker", now.Add(212*time.Second), 30*time.Second)
	if err != nil || claimX == nil {
		t.Fatalf("claim X = (%+v, %v)", claimX, err)
	}
	stale := *claimX
	stale.Version++
	if err := store.CommitWebhookClaim(ctx, stale, successAt(now.Add(213*time.Second)), now.Add(213*time.Second)); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatalf("stale commit error = %v, want outbox.ErrClaimLost", err)
	}
	still, err := store.GetWebhookDelivery(ctx, claimX.DeliveryID)
	if err != nil || still.Status != outbox.StatusDelivering {
		t.Errorf("delivery after stale commit = %+v (err %v), want still delivering", still, err)
	}

	claimX2, err := store.ClaimWebhookDue(ctx, "worker", now.Add(270*time.Second), 30*time.Second)
	if err != nil || claimX2 == nil || claimX2.DeliveryID != claimX.DeliveryID {
		t.Fatalf("expired-lease reclaim X = (%+v, %v)", claimX2, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claimX2, successAt(now.Add(271*time.Second)), now.Add(271*time.Second)); err != nil {
		t.Fatalf("commit X: %v", err)
	}

	claimY, err := store.ClaimWebhookDue(ctx, "worker", now.Add(272*time.Second), 30*time.Second)
	if err != nil || claimY == nil {
		t.Fatalf("claim Y = (%+v, %v)", claimY, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claimY, outbox.Result{Status: "bogus"}, now.Add(273*time.Second)); err == nil {
		t.Error("commit with invalid status error = nil, want error")
	}
	if err := store.CommitWebhookClaim(ctx, *claimY, outbox.Result{Status: outbox.StatusPending}, now.Add(274*time.Second)); err == nil {
		t.Error("commit pending without next attempt error = nil, want error")
	}
	if err := store.CommitWebhookClaim(ctx, *claimY, outbox.Result{Status: outbox.StatusSucceeded, LastHTTPStatus: 200}, now.Add(275*time.Second)); err == nil {
		t.Error("commit succeeded without delivered time error = nil, want error")
	}
	if err := store.CommitWebhookClaim(ctx, *claimY, outbox.Result{Status: outbox.StatusDead, NextAttemptAt: &retryAt}, now.Add(276*time.Second)); err == nil {
		t.Error("commit dead with next attempt error = nil, want error")
	}
	if err := store.CommitWebhookClaim(ctx, *claimY, successAt(now.Add(277*time.Second)), now.Add(277*time.Second)); err != nil {
		t.Fatalf("resolve Y: %v", err)
	}
}

func TestReplayWebhookDelivery(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "alerts", true, false, now)

	completeDownload(t, store, "a", now.Add(10*time.Second))
	completeDownload(t, store, "b", now.Add(70*time.Second))

	claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(30*time.Second), 30*time.Second)
	if err != nil || claim == nil {
		t.Fatalf("claim = (%+v, %v)", claim, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claim, outbox.Result{
		Status: outbox.StatusDead, LastHTTPStatus: 500, LastError: "HTTP 500",
	}, now.Add(31*time.Second)); err != nil {
		t.Fatalf("commit dead: %v", err)
	}

	replayed, err := store.ReplayWebhookDelivery(ctx, claim.DeliveryID, now.Add(40*time.Second))
	if err != nil {
		t.Fatalf("ReplayWebhookDelivery() error = %v", err)
	}
	if replayed.Status != outbox.StatusPending || replayed.AttemptCount != 0 || replayed.FirstAttemptAt != nil ||
		replayed.LastError != "" || replayed.LastHTTPStatus != 0 || replayed.LeaseOwner != "" ||
		replayed.NextAttemptAt == nil || !replayed.NextAttemptAt.Equal(now.Add(40*time.Second)) {
		t.Errorf("replayed delivery = %+v, want fresh pending window", replayed)
	}

	// The reopened row is claimable again and retries from a fresh window.
	reclaim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(41*time.Second), 30*time.Second)
	if err != nil || reclaim == nil || reclaim.DeliveryID != claim.DeliveryID {
		t.Fatalf("reclaim after replay = (%+v, %v), want same delivery", reclaim, err)
	}
	if reclaim.AttemptCount != 1 {
		t.Errorf("reclaimed attempt = %d, want 1", reclaim.AttemptCount)
	}
	if err := store.CommitWebhookClaim(ctx, *reclaim, successAt(now.Add(42*time.Second)), now.Add(42*time.Second)); err != nil {
		t.Fatalf("commit replayed: %v", err)
	}
	if _, err := store.ReplayWebhookDelivery(ctx, claim.DeliveryID, now.Add(50*time.Second)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("replay succeeded error = %v, want outbox.ErrNotFound", err)
	}

	// Replay is refused while the endpoint is disabled.
	claimB, err := store.ClaimWebhookDue(ctx, "worker", now.Add(90*time.Second), 30*time.Second)
	if err != nil || claimB == nil {
		t.Fatalf("claim B = (%+v, %v)", claimB, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claimB, outbox.Result{Status: outbox.StatusDead, LastHTTPStatus: 500, LastError: "HTTP 500"}, now.Add(91*time.Second)); err != nil {
		t.Fatalf("commit dead B: %v", err)
	}
	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, false, now.Add(92*time.Second)); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if _, err := store.ReplayWebhookDelivery(ctx, claimB.DeliveryID, now.Add(93*time.Second)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("replay on disabled endpoint error = %v, want outbox.ErrNotFound", err)
	}
}

func TestEnqueueTestDelivery(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "alerts", true, false, now)

	delivery, err := store.EnqueueTestDelivery(ctx, endpoint.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("EnqueueTestDelivery() error = %v", err)
	}
	if delivery.Status != outbox.StatusPending || delivery.EventType != outbox.EventTypeTest ||
		delivery.EndpointID != endpoint.ID || delivery.EndpointName != "alerts" ||
		delivery.AggregateType != outbox.AggregateWebhookEndpoint || delivery.AggregateID != "1" {
		t.Errorf("test delivery = %+v, want targeted webhook.test row", delivery)
	}
	event, err := store.queries.GetEvent(ctx, delivery.EventID)
	if err != nil {
		t.Fatalf("GetEvent() error = %v", err)
	}
	if event.Type != outbox.EventTypeTest || event.AggregateVersion != 0 {
		t.Errorf("test event = %+v, want webhook.test with endpoint version 0", event)
	}
	payload := unmarshalPayload(t, event.Payload)
	data := payload["data"].(map[string]any)
	if payload["type"] != outbox.EventTypeTest || data["endpoint_id"] != float64(endpoint.ID) ||
		data["endpoint_name"] != "alerts" || data["message"] != outbox.TestMessage {
		t.Errorf("test payload = %v, want exact envelope", payload)
	}

	// The test delivery travels the normal outbox path.
	claimedAt := now.Add(2 * time.Minute)
	leaseDuration := 30 * time.Second
	claim, err := store.ClaimWebhookDue(ctx, "worker", claimedAt, leaseDuration)
	if err != nil || claim == nil || claim.EventType != outbox.EventTypeTest {
		t.Fatalf("test claim = (%+v, %v), want webhook.test delivery", claim, err)
	}
	if claim.Owner != "worker" || claim.AttemptCount != 1 || claim.FirstAttemptAt == nil || !claim.FirstAttemptAt.Equal(claimedAt) {
		t.Errorf("test claim = %+v, want first attempt and worker lease identity", claim)
	}
	if string(claim.Payload) != string(event.Payload) {
		t.Error("claimed payload differs from persisted event payload")
	}
	claimedDelivery, err := store.GetWebhookDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery(claimed test) error = %v", err)
	}
	if claimedDelivery.Status != outbox.StatusDelivering || claimedDelivery.AttemptCount != 1 ||
		claimedDelivery.LeaseOwner != "worker" || claimedDelivery.LeaseUntil == nil ||
		!claimedDelivery.LeaseUntil.Equal(claimedAt.Add(leaseDuration)) {
		t.Errorf("claimed test delivery = %+v, want persisted delivering lease", claimedDelivery)
	}
	if err := store.CommitWebhookClaim(ctx, *claim, successAt(now.Add(3*time.Minute)), now.Add(3*time.Minute)); err != nil {
		t.Fatalf("commit test delivery: %v", err)
	}

	// Enqueueing while disabled pauses the test delivery.
	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, false, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if _, err := store.EnqueueTestDelivery(ctx, endpoint.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("enqueue on disabled endpoint error = %v", err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(6*time.Minute), 30*time.Second); err != nil || claim != nil {
		t.Fatalf("claim paused test delivery = (%+v, %v), want nil", claim, err)
	}
	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, true, now.Add(7*time.Minute)); err != nil {
		t.Fatalf("enable endpoint: %v", err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(8*time.Minute), 30*time.Second); err != nil || claim == nil {
		t.Fatalf("claim resumed test delivery = (%+v, %v)", claim, err)
	}

	// Deleted endpoints cannot receive test deliveries.
	if err := store.DeleteWebhookEndpoint(ctx, endpoint.ID, now.Add(9*time.Minute)); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	if _, err := store.EnqueueTestDelivery(ctx, endpoint.ID, now.Add(10*time.Minute)); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("enqueue on deleted endpoint error = %v, want outbox.ErrNotFound", err)
	}
}

func TestWebhookDeliveryListingAndPruning(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpointA := testEndpoint(t, store, "alpha", true, false, now)
	testEndpoint(t, store, "beta", false, true, now)

	completeDownload(t, store, "a", now.Add(10*time.Second))
	completeDownload(t, store, "b", now.Add(70*time.Second))
	completeDownload(t, store, "c", now.Add(130*time.Second))
	failDownload(t, store, "d", now.Add(190*time.Second), "transient")

	if rows, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{EndpointID: &endpointA.ID}); err != nil || len(rows) != 3 {
		t.Fatalf("ListWebhookDeliveries(alpha) = (%d, %v), want 3", len(rows), err)
	}
	if rows, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{EventType: outbox.EventTypeFailed}); err != nil || len(rows) != 1 {
		t.Fatalf("ListWebhookDeliveries(failed) = (%d, %v), want 1", len(rows), err)
	}
	if rows, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Status: outbox.StatusPending}); err != nil || len(rows) != 4 {
		t.Fatalf("ListWebhookDeliveries(pending) = (%d, %v), want 4", len(rows), err)
	}
	if rows, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Status: outbox.StatusSucceeded}); err != nil || len(rows) != 0 {
		t.Fatalf("ListWebhookDeliveries(succeeded) = (%d, %v), want 0", len(rows), err)
	}

	// Cursor pagination: two pages of two, newest first.
	first, page, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Limit: 2})
	if err != nil || len(first) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page = (%d rows, %+v, %v), want 2 with next cursor", len(first), page, err)
	}
	if first[0].ID < first[1].ID {
		t.Error("listing is not newest-first")
	}
	second, page, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(second) != 2 || page.HasMore {
		t.Fatalf("second page = (%d rows, %+v, %v), want 2 terminal rows", len(second), page, err)
	}
	seen := map[int64]bool{}
	for _, delivery := range append(first, second...) {
		if seen[delivery.ID] {
			t.Errorf("delivery %d appears on two pages", delivery.ID)
		}
		seen[delivery.ID] = true
	}

	if _, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Cursor: "garbage"}); err == nil {
		t.Error("invalid cursor error = nil, want error")
	}
	if _, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Limit: 101}); err == nil {
		t.Error("limit 101 error = nil, want error")
	}
	if _, _, err := store.ListWebhookDeliveries(ctx, outbox.DeliveryFilter{Status: "bogus"}); err == nil {
		t.Error("invalid status error = nil, want error")
	}

	// Pruning removes only aged succeeded/cancelled rows; events stay forever.
	claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(17*time.Second), 30*time.Second)
	if err != nil || claim == nil {
		t.Fatalf("claim a = (%+v, %v)", claim, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claim, successAt(now.Add(18*time.Second)), now.Add(18*time.Second)); err != nil {
		t.Fatalf("commit a succeeded: %v", err)
	}
	claim, err = store.ClaimWebhookDue(ctx, "worker", now.Add(77*time.Second), 30*time.Second)
	if err != nil || claim == nil {
		t.Fatalf("claim b = (%+v, %v)", claim, err)
	}
	if err := store.CommitWebhookClaim(ctx, *claim, outbox.Result{Status: outbox.StatusDead, LastHTTPStatus: 502, LastError: "HTTP 502"}, now.Add(78*time.Second)); err != nil {
		t.Fatalf("commit b dead: %v", err)
	}
	deliveries := allDeliveries(t, store)
	if len(deliveries) != 4 {
		t.Fatalf("deliveries = %d, want 4", len(deliveries))
	}
	var succeededID, deadID, pendingID int64
	for _, delivery := range deliveries {
		switch delivery.Status {
		case outbox.StatusSucceeded:
			succeededID = delivery.ID
		case outbox.StatusDead:
			deadID = delivery.ID
		case outbox.StatusPending:
			pendingID = delivery.ID
		}
	}
	old := now.AddDate(0, 0, -91)
	for _, id := range []int64{succeededID, deadID, pendingID} {
		// Age created_at together with updated_at so the aged rows remain
		// valid (updated_at must not precede created_at); pruning still keys
		// only on the terminal updated_at of succeeded/cancelled rows.
		if _, err := store.db.ExecContext(ctx, "UPDATE webhook_deliveries SET created_at = ?, updated_at = ? WHERE id = ?", old, old, id); err != nil {
			t.Fatalf("age delivery %d: %v", id, err)
		}
	}
	pruned, err := store.PruneWebhookDeliveries(ctx, now)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneWebhookDeliveries() = (%d, %v), want 1 aged succeeded removed", pruned, err)
	}
	if _, err := store.GetWebhookDelivery(ctx, succeededID); !errors.Is(err, outbox.ErrNotFound) {
		t.Errorf("aged succeeded delivery survived pruning (err %v)", err)
	}
	if _, err := store.GetWebhookDelivery(ctx, deadID); err != nil {
		t.Errorf("aged dead delivery was pruned: %v", err)
	}
	if _, err := store.GetWebhookDelivery(ctx, pendingID); err != nil {
		t.Errorf("aged pending delivery was pruned: %v", err)
	}
	var eventCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_events").Scan(&eventCount); err != nil {
		t.Fatalf("count domain_events: %v", err)
	}
	if eventCount < 8 {
		t.Errorf("domain_events = %d, want events retained after pruning", eventCount)
	}
}

func TestWebhookNextDue(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "alerts", true, false, now)

	if due, err := store.NextWebhookDue(ctx, now); err != nil || due != nil {
		t.Fatalf("NextWebhookDue(empty) = (%v, %v), want nil", due, err)
	}
	completeDownload(t, store, "a", now.Add(10*time.Second))
	due, err := store.NextWebhookDue(ctx, now.Add(20*time.Second))
	if err != nil || due == nil || !due.Equal(now.Add(16*time.Second)) {
		t.Fatalf("NextWebhookDue(due) = (%v, %v), want %v", due, err, now.Add(16*time.Second))
	}
	if err := store.SetWebhookEndpointEnabled(ctx, endpoint.ID, false, now.Add(21*time.Second)); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if due, err := store.NextWebhookDue(ctx, now.Add(22*time.Second)); err != nil || due != nil {
		t.Fatalf("NextWebhookDue(disabled) = (%v, %v), want nil", due, err)
	}
}

func TestWebhookNextDueOrderingBlockedLaterRow(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	testEndpoint(t, store, "alerts", true, false, now)

	// Two lifecycle deliveries for the same endpoint+aggregate: the earlier
	// row A is created at now+16s, the later row B at now+39s. B becomes due
	// while A is still leased, so a blocked later row must never drive the
	// next-due computation (which would busy-loop the dispatcher).
	hash := completeDownload(t, store, "a", now.Add(10*time.Second))
	recycleDownload(t, store, hash, now.Add(30*time.Second))

	// Claim A; B is now due (next_attempt_at now+39s) but ordering-blocked by
	// A's active lease.
	claimA, err := store.ClaimWebhookDue(ctx, "worker", now.Add(40*time.Second), 30*time.Second)
	if err != nil || claimA == nil {
		t.Fatalf("claim A = (%+v, %v)", claimA, err)
	}
	deliveryA, err := store.GetWebhookDelivery(ctx, claimA.DeliveryID)
	if err != nil || deliveryA.CreatedAt.After(now.Add(16*time.Second)) {
		t.Fatalf("claim A delivery = %+v (err %v), want the earlier row", deliveryA, err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(41*time.Second), 30*time.Second); err != nil || claim != nil {
		t.Fatalf("claim while A leased = (%+v, %v), want nil (B blocked)", claim, err)
	}
	due, err := store.NextWebhookDue(ctx, now.Add(41*time.Second))
	if err != nil || due == nil || !due.Equal(now.Add(70*time.Second)) {
		t.Fatalf("NextWebhookDue(blocked later row) = (%v, %v), want A lease expiry %v", due, err, now.Add(70*time.Second))
	}

	// After the lease expires A is due again; B remains blocked until A is
	// terminal, so the next due stays on A.
	reclaimA, err := store.ClaimWebhookDue(ctx, "worker", now.Add(70*time.Second), 30*time.Second)
	if err != nil || reclaimA == nil || reclaimA.DeliveryID != claimA.DeliveryID {
		t.Fatalf("reclaim A = (%+v, %v), want the earlier row", reclaimA, err)
	}
	deliveredAt := now.Add(71 * time.Second)
	if err := store.CommitWebhookClaim(ctx, *reclaimA, outbox.Result{Status: outbox.StatusSucceeded, LastHTTPStatus: 200, DeliveredAt: &deliveredAt}, deliveredAt); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	claimB, err := store.ClaimWebhookDue(ctx, "worker", now.Add(72*time.Second), 30*time.Second)
	if err != nil || claimB == nil {
		t.Fatalf("claim B after A resolves = (%+v, %v)", claimB, err)
	}
	deliveryB, err := store.GetWebhookDelivery(ctx, claimB.DeliveryID)
	if err != nil || deliveryB.CreatedAt.Before(now.Add(30*time.Second)) {
		t.Fatalf("claim B delivery = %+v (err %v), want the later row", deliveryB, err)
	}
}

func TestEndpointEnabledAtomicCreateUpdate(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	// A nil Enabled on create keeps the backward-compatible default: enabled.
	created, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "alerts", URL: "https://example.invalid/hook", SubscribeCompleted: true,
	}, now)
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint(default) error = %v", err)
	}
	if !created.Enabled {
		t.Errorf("nil Enabled create = disabled, want default enabled")
	}

	// An explicit disabled create persists atomically with the rest of the row.
	disabled := false
	off, err := store.CreateWebhookEndpoint(ctx, outbox.EndpointInput{
		Name: "paused", URL: "https://example.invalid/hook", SubscribeCompleted: true, Enabled: &disabled,
	}, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint(disabled) error = %v", err)
	}
	if off.Enabled {
		t.Errorf("explicit false create = enabled, want disabled")
	}
	got, err := store.GetWebhookEndpoint(ctx, off.ID)
	if err != nil || got.Enabled {
		t.Errorf("GetWebhookEndpoint(after disabled create) = %+v (err %v), want disabled", got, err)
	}

	// A delivery targeting the disabled endpoint stays paused until the
	// update re-enables it atomically.
	if _, err := store.EnqueueTestDelivery(ctx, off.ID, now.Add(20*time.Second)); err != nil {
		t.Fatalf("EnqueueTestDelivery(disabled) error = %v", err)
	}
	if claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(30*time.Second), 30*time.Second); err != nil || claim != nil {
		t.Fatalf("claim on disabled endpoint = (%+v, %v), want nil", claim, err)
	}

	// Update: a non-nil Enabled is applied atomically with the other fields.
	enabled := true
	updated, err := store.UpdateWebhookEndpoint(ctx, off.ID, outbox.EndpointInput{
		Name: "paused-renamed", URL: "https://example.invalid/renamed", SubscribeCompleted: true, Enabled: &enabled,
	}, now.Add(40*time.Second))
	if err != nil {
		t.Fatalf("UpdateWebhookEndpoint(enabled) error = %v", err)
	}
	if !updated.Enabled || updated.Name != "paused-renamed" || updated.DisplayURL != "https://example.invalid/renamed" {
		t.Errorf("atomic update = %+v, want renamed + enabled in one transaction", updated)
	}
	if updated.RowVersion != 1 {
		t.Errorf("updated version = %d, want 1 (single atomic statement)", updated.RowVersion)
	}
	// The paused delivery resumes after the atomic re-enable.
	claim, err := store.ClaimWebhookDue(ctx, "worker", now.Add(50*time.Second), 30*time.Second)
	if err != nil || claim == nil {
		t.Fatalf("claim after atomic re-enable = (%+v, %v), want delivery", claim, err)
	}
	deliveredAt := now.Add(51 * time.Second)
	if err := store.CommitWebhookClaim(ctx, *claim, outbox.Result{Status: outbox.StatusSucceeded, LastHTTPStatus: 200, DeliveredAt: &deliveredAt}, deliveredAt); err != nil {
		t.Fatalf("commit resumed delivery: %v", err)
	}

	// A nil Enabled on update preserves the stored enabled state.
	preserved, err := store.UpdateWebhookEndpoint(ctx, off.ID, outbox.EndpointInput{
		Name: "paused-renamed", URL: "", SubscribeCompleted: true,
	}, now.Add(60*time.Second))
	if err != nil {
		t.Fatalf("UpdateWebhookEndpoint(preserve) error = %v", err)
	}
	if !preserved.Enabled {
		t.Errorf("nil Enabled update = disabled, want preserved enabled")
	}

	// An explicit false update disables atomically.
	updatedOff, err := store.UpdateWebhookEndpoint(ctx, off.ID, outbox.EndpointInput{
		Name: "paused-renamed", URL: "", SubscribeCompleted: true, Enabled: &disabled,
	}, now.Add(70*time.Second))
	if err != nil {
		t.Fatalf("UpdateWebhookEndpoint(disable) error = %v", err)
	}
	if updatedOff.Enabled {
		t.Errorf("explicit false update = enabled, want disabled")
	}
}

func TestUpdateEndpointRemovedSubscriptionCancelsDeliveries(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	endpoint := testEndpoint(t, store, "both", true, true, now)

	completeDownload(t, store, "a", now.Add(10*time.Second))
	failDownload(t, store, "f", now.Add(70*time.Second), "boom")

	if _, err := store.UpdateWebhookEndpoint(ctx, endpoint.ID, outbox.EndpointInput{
		Name: "both", URL: "", SubscribeCompleted: true, SubscribeFailed: false,
	}, now.Add(130*time.Second)); err != nil {
		t.Fatalf("UpdateWebhookEndpoint(remove failed) error = %v", err)
	}
	for _, delivery := range allDeliveries(t, store) {
		want := outbox.StatusPending
		if delivery.EventType == outbox.EventTypeFailed {
			want = outbox.StatusCancelled
		}
		if delivery.Status != want {
			t.Errorf("delivery %s status = %s, want %s", delivery.EventType, delivery.Status, want)
		}
	}
}

func TestLatestEventSequence(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	high, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence(empty) error = %v", err)
	}
	if high != 0 {
		t.Errorf("LatestEventSequence(empty) = %d, want 0", high)
	}

	hashA := completeDownload(t, store, "a", now)
	afterA, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence(completed) error = %v", err)
	}
	if want := int64(len(eventsForDownload(t, store, hashA))); afterA != want {
		t.Errorf("LatestEventSequence(completed) = %d, want %d", afterA, want)
	}

	failDownload(t, store, "f", now.Add(time.Minute), "disk full")
	afterF, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence(failed) error = %v", err)
	}
	if afterF != afterA+2 {
		t.Errorf("LatestEventSequence(failed) = %d, want %d", afterF, afterA+2)
	}
}

func TestListDownloadEventsFeed(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	hashA := completeDownload(t, store, "a", now)
	hashF := failDownload(t, store, "f", now.Add(10*time.Second), "disk full")
	high, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence() error = %v", err)
	}
	completedEvents := eventsForDownload(t, store, hashA)
	completed := completedEvents[len(completedEvents)-1]
	failedEvents := eventsForDownload(t, store, hashF)
	failed := failedEvents[len(failedEvents)-1]

	// Both types: exactly the two terminal events, ascending by sequence, with
	// the hidden created/state_changed rows skipped.
	events, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil {
		t.Fatalf("ListDownloadEvents(both) error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListDownloadEvents(both) = %d events, want 2 (hidden types skipped)", len(events))
	}
	for index, event := range events {
		if event.Type != outbox.EventTypeCompleted && event.Type != outbox.EventTypeFailed {
			t.Errorf("feed event %d leaked hidden type %q", index, event.Type)
		}
		if index > 0 && events[index-1].Sequence >= event.Sequence {
			t.Errorf("feed events not ascending by sequence: %d, %d", events[index-1].Sequence, event.Sequence)
		}
	}
	if events[0].ID != completed.ID || events[0].Type != outbox.EventTypeCompleted {
		t.Errorf("first feed event = %+v, want completed %q", events[0], completed.ID)
	}
	if events[1].ID != failed.ID || events[1].Type != outbox.EventTypeFailed {
		t.Errorf("second feed event = %+v, want failed %q", events[1], failed.ID)
	}

	// Type filters select exactly the requested event type.
	completedOnly, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high, IncludeCompleted: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil || len(completedOnly) != 1 || completedOnly[0].Type != outbox.EventTypeCompleted {
		t.Errorf("ListDownloadEvents(completed only) = (%+v, %v), want one completed", completedOnly, err)
	}
	failedOnly, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil || len(failedOnly) != 1 || failedOnly[0].Type != outbox.EventTypeFailed {
		t.Errorf("ListDownloadEvents(failed only) = (%+v, %v), want one failed", failedOnly, err)
	}

	// Hash filter narrows to one download's terminal event.
	byHash, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, AggregateID: hashA, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil || len(byHash) != 1 || byHash[0].ID != completed.ID {
		t.Errorf("ListDownloadEvents(hash %s) = (%+v, %v), want one completed for %s", hashA, byHash, err, hashA)
	}

	// Cursor semantics: sequence strictly greater than after.
	after, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: completed.Sequence, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil || len(after) != 1 || after[0].ID != failed.ID {
		t.Errorf("ListDownloadEvents(after completed) = (%+v, %v), want the failed event", after, err)
	}

	// Limit bounds the returned page.
	limited, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, Limit: 1,
	})
	if err != nil || len(limited) != 1 || limited[0].ID != completed.ID {
		t.Errorf("ListDownloadEvents(limit 1) = (%+v, %v), want only the completed event", limited, err)
	}

	// A through snapshot excludes events inserted after it; the widened scan
	// then sees them.
	snapshot := high
	completeDownload(t, store, "b", now.Add(20*time.Second))
	afterLater, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence(later) error = %v", err)
	}
	frozen, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: snapshot,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil {
		t.Fatalf("ListDownloadEvents(through snapshot) error = %v", err)
	}
	if len(frozen) != 2 {
		t.Errorf("ListDownloadEvents(through snapshot) = %d events, want 2 (later insert excluded)", len(frozen))
	}
	wide, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: afterLater,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil || len(wide) != 3 || wide[2].Type != outbox.EventTypeCompleted {
		t.Errorf("ListDownloadEvents(through later) = (%d events, %v), want 3 ending in completed", len(wide), err)
	}
}

func TestListDownloadEventsSameTimestampOrder(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	hashA := completeDownload(t, store, "a", now)
	hashB := completeDownload(t, store, "b", now)
	high, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence() error = %v", err)
	}
	events, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil {
		t.Fatalf("ListDownloadEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("feed = %d events, want 2 completed", len(events))
	}
	if !events[0].OccurredAt.Equal(events[1].OccurredAt) {
		t.Fatalf("test setup: completed events occurred_at differ (%v, %v)", events[0].OccurredAt, events[1].OccurredAt)
	}
	if events[0].Sequence >= events[1].Sequence {
		t.Errorf("same-timestamp feed not ascending by sequence: %d, %d", events[0].Sequence, events[1].Sequence)
	}
	lastA := eventsForDownload(t, store, hashA)
	lastB := eventsForDownload(t, store, hashB)
	if events[0].ID != lastA[len(lastA)-1].ID || events[1].ID != lastB[len(lastB)-1].ID {
		t.Errorf("same-timestamp feed order = %q, %q, want %q, %q",
			events[0].ID, events[1].ID, lastA[len(lastA)-1].ID, lastB[len(lastB)-1].ID)
	}
}

func TestListDownloadEventsPreservation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	hashA := completeDownload(t, store, "a", now)
	failDownload(t, store, "f", now.Add(10*time.Second), "boom")
	high, err := store.LatestEventSequence(ctx)
	if err != nil {
		t.Fatalf("LatestEventSequence() error = %v", err)
	}
	events, err := store.ListDownloadEvents(ctx, outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: high,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	})
	if err != nil {
		t.Fatalf("ListDownloadEvents() error = %v", err)
	}
	stored := eventsForDownload(t, store, hashA)
	want := stored[len(stored)-1]
	got := events[0]
	if got.Sequence != want.Sequence || got.ID != want.ID || got.Type != want.Type ||
		got.AggregateType != want.AggregateType || got.AggregateID != want.AggregateID ||
		got.AggregateVersion != want.AggregateVersion || !got.OccurredAt.Equal(want.OccurredAt) ||
		!bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("feed event = %+v, want exact stored row %+v", got, want)
	}
	if len(got.Payload) == 0 || !json.Valid(got.Payload) {
		t.Errorf("feed payload is not valid JSON: %q", got.Payload)
	}
}

func TestListDownloadEventsInvalid(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	base := outbox.EventQuery{
		AfterSequence: 0, ThroughSequence: 100,
		IncludeCompleted: true, IncludeFailed: true, Limit: outbox.MaxEventFeedLimit,
	}
	invalid := []struct {
		name  string
		query outbox.EventQuery
	}{
		{"negative after", withEventQuery(base, func(q *outbox.EventQuery) { q.AfterSequence = -1 })},
		{"negative through", withEventQuery(base, func(q *outbox.EventQuery) { q.ThroughSequence = -1 })},
		{"inverted range", withEventQuery(base, func(q *outbox.EventQuery) { q.AfterSequence = 10; q.ThroughSequence = 5 })},
		{"zero limit", withEventQuery(base, func(q *outbox.EventQuery) { q.Limit = 0 })},
		{"negative limit", withEventQuery(base, func(q *outbox.EventQuery) { q.Limit = -5 })},
		{"limit over max", withEventQuery(base, func(q *outbox.EventQuery) { q.Limit = outbox.MaxEventFeedLimit + 1 })},
		{"no types", withEventQuery(base, func(q *outbox.EventQuery) { q.IncludeCompleted = false; q.IncludeFailed = false })},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.ListDownloadEvents(ctx, test.query); err == nil {
				t.Errorf("ListDownloadEvents(%+v) error = nil, want error", test.query)
			}
		})
	}

	// after == through is the valid empty long-poll scan.
	empty, err := store.ListDownloadEvents(ctx, withEventQuery(base, func(q *outbox.EventQuery) {
		q.AfterSequence = 7
		q.ThroughSequence = 7
	}))
	if err != nil {
		t.Fatalf("ListDownloadEvents(after == through) error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListDownloadEvents(after == through) = %d events, want 0", len(empty))
	}
	// The lookahead limit is accepted.
	if _, err := store.ListDownloadEvents(ctx, base); err != nil {
		t.Errorf("ListDownloadEvents(limit %d) error = %v, want nil", outbox.MaxEventFeedLimit, err)
	}
}

func withEventQuery(base outbox.EventQuery, mutate func(*outbox.EventQuery)) outbox.EventQuery {
	query := base
	mutate(&query)
	return query
}

// successAt builds a valid succeeded commit result stamped with at; the
// store now requires a non-nil DeliveredAt for succeeded commits.
func successAt(at time.Time) outbox.Result {
	return outbox.Result{Status: outbox.StatusSucceeded, LastHTTPStatus: 200, DeliveredAt: &at}
}

func testEndpoint(t *testing.T, store *Store, name string, completed, failed bool, now time.Time) outbox.Endpoint {
	t.Helper()
	endpoint, err := store.CreateWebhookEndpoint(context.Background(), outbox.EndpointInput{
		Name: name, URL: "https://example.invalid/webhook", SubscribeCompleted: completed, SubscribeFailed: failed,
	}, now)
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint(%q): %v", name, err)
	}
	return endpoint
}

func walkDownload(t *testing.T, store *Store, hash string, states []domain.State, base time.Time) {
	t.Helper()
	ctx := context.Background()
	for index, state := range states {
		at := base.Add(time.Duration(index+1) * time.Second)
		claim, err := store.ClaimDue(ctx, "walk-worker", at, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("walk claim %d (%s): claim=%+v err=%v", index, state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.CloudSourcePath = "/cloud/downloads/" + hash
		next.UpdatedAt = at
		if state == domain.StateCompleted {
			next.ContentPath = "/downloads/" + hash
			completedAt := at
			next.CompletedAt = &completedAt
		}
		if err := store.CommitClaim(ctx, *claim, next); err != nil {
			t.Fatalf("walk commit %s: %v", state, err)
		}
	}
}

func failDownload(t *testing.T, store *Store, seed string, base time.Time, lastError string) string {
	t.Helper()
	ctx := context.Background()
	sub := testSubmission(seed, base)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission(fail %s) = (%t, %v)", seed, inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "fail-worker", base.Add(time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(fail %s) = (%+v, %v)", seed, claim, err)
	}
	next := claim.Download
	next.State = domain.StateFailed
	next.LastError = lastError
	next.UpdatedAt = base.Add(2 * time.Second)
	if err := store.CommitClaim(ctx, *claim, next); err != nil {
		t.Fatalf("CommitClaim(fail %s): %v", seed, err)
	}
	return sub.Download.Hash
}

func recycleDownload(t *testing.T, store *Store, hash string, base time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := store.RequestDelete(ctx, []string{hash}, false, base); err != nil {
		t.Fatalf("RequestDelete(recycle): %v", err)
	}
	claim, err := store.ClaimDue(ctx, "recycle-worker", base.Add(time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(recycle) = (%+v, %v)", claim, err)
	}
	next := claim.Download
	next.State = domain.StateDeleted
	next.UpdatedAt = base.Add(2 * time.Second)
	if err := store.CommitClaim(ctx, *claim, next); err != nil {
		t.Fatalf("CommitClaim(recycle delete): %v", err)
	}
	revived := testSubmission(string(hash[0]), base.Add(3*time.Second))
	if _, inserted, err := store.CreateSubmission(ctx, revived); err != nil || !inserted {
		t.Fatalf("revive CreateSubmission(recycle) = (%t, %v)", inserted, err)
	}
	walkDownload(t, store, hash, completedStates, base.Add(3*time.Second))
}

func completeDownload(t *testing.T, store *Store, seed string, base time.Time) string {
	t.Helper()
	ctx := context.Background()
	sub := testSubmission(seed, base)
	if _, inserted, err := store.CreateSubmission(ctx, sub); err != nil || !inserted {
		t.Fatalf("CreateSubmission(complete %s) = (%t, %v)", seed, inserted, err)
	}
	walkDownload(t, store, sub.Download.Hash, completedStates, base)
	return sub.Download.Hash
}

func eventsForDownload(t *testing.T, store *Store, hash string) []storedb.DomainEvent {
	t.Helper()
	rows, err := store.queries.ListEventsByAggregate(context.Background(), storedb.ListEventsByAggregateParams{
		AggregateType: outbox.AggregateDownload, AggregateID: hash,
	})
	if err != nil {
		t.Fatalf("ListEventsByAggregate(%s): %v", hash, err)
	}
	return rows
}

func allDeliveries(t *testing.T, store *Store) []outbox.Delivery {
	t.Helper()
	deliveries, page, err := store.ListWebhookDeliveries(context.Background(), outbox.DeliveryFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListWebhookDeliveries(): %v", err)
	}
	if page.HasMore {
		t.Fatalf("ListWebhookDeliveries(): more than 100 deliveries in test")
	}
	return deliveries
}

func unmarshalPayload(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	return envelope
}
