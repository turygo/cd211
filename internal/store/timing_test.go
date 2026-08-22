package store

import (
	"context"
	"testing"
	"time"
)

func TestDownloadTimingPersistsAcrossCreateAndClaimCommit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	offlineStarted := now.Add(-time.Hour)
	copyCompleted := now.Add(-time.Minute)
	submission := testSubmission("a", now)
	submission.Download.OfflineStartedAt = &offlineStarted
	submission.Download.CopyCompletedAt = &copyCompleted

	created, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v)", created, inserted, err)
	}
	stored, err := store.GetDownload(ctx, created.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OfflineStartedAt == nil || !stored.OfflineStartedAt.Equal(offlineStarted) || stored.CopyCompletedAt == nil || !stored.CopyCompletedAt.Equal(copyCompleted) {
		t.Fatalf("created timing = %v/%v, want %v/%v", stored.OfflineStartedAt, stored.CopyCompletedAt, offlineStarted, copyCompleted)
	}

	claim, err := store.ClaimDue(ctx, "timing-test", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	updated := claim.Download
	updated.OfflineStartedAt = &offlineStarted
	updated.CopyCompletedAt = &copyCompleted
	updated.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *claim, updated); err != nil {
		t.Fatalf("CommitClaim() = %v", err)
	}
	stored, err = store.GetDownload(ctx, created.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OfflineStartedAt == nil || !stored.OfflineStartedAt.Equal(offlineStarted) || stored.CopyCompletedAt == nil || !stored.CopyCompletedAt.Equal(copyCompleted) {
		t.Fatalf("committed timing = %v/%v, want %v/%v", stored.OfflineStartedAt, stored.CopyCompletedAt, offlineStarted, copyCompleted)
	}
}
