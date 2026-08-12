package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

func TestCreateSubmissionDuplicateAndRevive(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("a", now)

	created, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v), want inserted download", created, inserted, err)
	}
	duplicate, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || inserted || duplicate.Hash != created.Hash {
		t.Fatalf("duplicate CreateSubmission() = (%+v, %t, %v), want existing download", duplicate, inserted, err)
	}

	if err := store.RequestDelete(ctx, []string{created.Hash}, false, now.Add(time.Minute)); err != nil {
		t.Fatalf("RequestDelete(): %v", err)
	}
	claim, err := store.ClaimDue(ctx, "worker", now.Add(time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v), want delete claim", claim, err)
	}
	next := claim.Download
	next.State = domain.StateDeleted
	next.UpdatedAt = now.Add(2 * time.Minute)
	if err := store.CommitClaim(ctx, *claim, next); err != nil {
		t.Fatalf("CommitClaim(delete): %v", err)
	}

	revivedSubmission := testSubmission("a", now.Add(3*time.Minute))
	revivedSubmission.Files = []domain.DownloadFile{{DownloadHash: revivedSubmission.Download.Hash, Index: 0, RelativePath: "replacement.mkv", Size: 42}}
	revived, inserted, err := store.CreateSubmission(ctx, revivedSubmission)
	if err != nil || !inserted || revived.State != domain.StateAccepted || revived.RowVersion <= created.RowVersion {
		t.Fatalf("revive CreateSubmission() = (%+v, %t, %v), want revived accepted download", revived, inserted, err)
	}
	files, err := store.ListDownloadFiles(ctx, revived.Hash)
	if err != nil || len(files) != 1 || files[0].RelativePath != "replacement.mkv" {
		t.Fatalf("ListDownloadFiles() = (%+v, %v), want replaced files", files, err)
	}
}

func TestRepositoryListsAndIntents(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	category := domain.Category{Name: "Movies", CloudPath: "/cloud/movies", SavePath: "/downloads/movies", Enabled: true, CreatedAt: now, UpdatedAt: now}
	storedCategory, err := store.UpsertCategory(ctx, category)
	if err != nil || storedCategory != category {
		t.Fatalf("UpsertCategory() = (%+v, %v), want exact category", storedCategory, err)
	}

	first := testSubmission("b", now)
	first.Download.Category = "Movies"
	second := testSubmission("c", now.Add(time.Second))
	if _, inserted, err := store.CreateSubmission(ctx, first); err != nil || !inserted {
		t.Fatalf("CreateSubmission(first): inserted=%t err=%v", inserted, err)
	}
	if _, inserted, err := store.CreateSubmission(ctx, second); err != nil || !inserted {
		t.Fatalf("CreateSubmission(second): inserted=%t err=%v", inserted, err)
	}
	all, err := store.ListDownloads(ctx, nil)
	if err != nil || len(all) != 2 || all[0].Hash != second.Download.Hash {
		t.Fatalf("ListDownloads(nil) = (%+v, %v), want newest first", all, err)
	}
	categoryName := "Movies"
	inCategory, err := store.ListDownloads(ctx, &categoryName)
	if err != nil || len(inCategory) != 1 || inCategory[0].Hash != first.Download.Hash {
		t.Fatalf("ListDownloads(category) = (%+v, %v), want exact category", inCategory, err)
	}

	stopped := testSubmission("d", now)
	stopped.Download.State = domain.StateStopped
	stopped.Download.NextRunAt = nil
	if _, inserted, err := store.CreateSubmission(ctx, stopped); err != nil || !inserted {
		t.Fatalf("CreateSubmission(stopped): inserted=%t err=%v", inserted, err)
	}
	before, err := store.GetDownload(ctx, stopped.Download.Hash)
	if err != nil {
		t.Fatalf("GetDownload(stopped): %v", err)
	}
	if err := store.Start(ctx, stopped.Download.Hash, now.Add(time.Minute)); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if err := store.Start(ctx, stopped.Download.Hash, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("idempotent Start(): %v", err)
	}
	after, err := store.GetDownload(ctx, stopped.Download.Hash)
	if err != nil || after.RowVersion != before.RowVersion+1 || after.State != domain.StateAccepted {
		t.Fatalf("started download = (%+v, %v), want one version increment", after, err)
	}
	if err := store.RequestDelete(ctx, []string{first.Download.Hash, first.Download.Hash}, false, now.Add(time.Minute)); err != nil {
		t.Fatalf("RequestDelete(): %v", err)
	}
	if err := store.RequestDelete(ctx, []string{first.Download.Hash}, true, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RequestDelete(escalate): %v", err)
	}
	deletedRequest, err := store.GetDownload(ctx, first.Download.Hash)
	if err != nil || !deletedRequest.DeleteFilesRequested || deletedRequest.RemovedAt == nil {
		t.Fatalf("delete request = (%+v, %v), want escalated hidden request", deletedRequest, err)
	}
	all, err = store.ListDownloads(ctx, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListDownloads() after delete = (%+v, %v), want request hidden", all, err)
	}
}

func TestStartResumesFromLastCompletedStage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		seed      string
		status    string
		content   string
		wantState domain.State
	}{
		{"copy", "4", domain.UpstreamOfflineFinished, "", domain.StateSubmittingCopy},
		{"verification", "5", domain.UpstreamCopyCompleted, "", domain.StateVerifyingLocal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			submission := testSubmission(test.seed, now)
			multi := false
			submission.Download.State = domain.StateStopped
			submission.Download.NextRunAt = nil
			submission.Download.CloudSourcePath = "/cloud/downloads/download-" + test.seed
			submission.Download.IsMultiFile = &multi
			submission.Download.LastUpstreamStatus = test.status
			submission.Download.ContentPath = test.content
			if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
				t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
			}
			if err := store.Start(ctx, submission.Download.Hash, now.Add(time.Minute)); err != nil {
				t.Fatalf("Start(): %v", err)
			}
			resumed, err := store.GetDownload(ctx, submission.Download.Hash)
			if err != nil || resumed.State != test.wantState || resumed.PauseRequested {
				t.Fatalf("resumed download = (%+v, %v), want %s", resumed, err, test.wantState)
			}
		})
	}
}

func TestCleanupFailureRemainsVisibleAndRetryable(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("9", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	if err := store.RequestDelete(ctx, []string{submission.Download.Hash}, true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "cleanup", now.Add(time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(cleanup) = (%+v, %v)", claim, err)
	}
	failed := claim.Download
	failed.LastError = "local deletion failed"
	failed.NextRunAt = nil
	failed.UpdatedAt = now.Add(2 * time.Minute)
	if err := store.CommitClaim(ctx, *claim, failed); err != nil {
		t.Fatal(err)
	}
	visible, err := store.ListDownloads(ctx, nil)
	if err != nil || len(visible) != 1 || visible[0].Hash != failed.Hash {
		t.Fatalf("failed cleanup visibility = (%+v, %v)", visible, err)
	}
	if err := store.Retry(ctx, failed.Hash, domain.StateDeleteRequested, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("Retry(cleanup): %v", err)
	}
	retried, err := store.GetDownload(ctx, failed.Hash)
	if err != nil || retried.State != domain.StateDeleteRequested || retried.LastError != "" || retried.NextRunAt == nil {
		t.Fatalf("retried cleanup = (%+v, %v)", retried, err)
	}
}

func TestUserIntentPreservesLiveLeaseUntilExternalOperationEnds(t *testing.T) {
	tests := []struct {
		name      string
		seed      string
		mutate    func(*Store, context.Context, string, time.Time) error
		wantState domain.State
		wantPause bool
	}{
		{"pause", "6", func(store *Store, ctx context.Context, hash string, now time.Time) error {
			return store.Pause(ctx, hash, now)
		}, domain.StateCancelRequested, true},
		{"cancel", "7", func(store *Store, ctx context.Context, hash string, now time.Time) error {
			return store.Cancel(ctx, hash, now)
		}, domain.StateCancelRequested, false},
		{"delete", "8", func(store *Store, ctx context.Context, hash string, now time.Time) error {
			return store.RequestDelete(ctx, []string{hash}, true, now)
		}, domain.StateDeleteRequested, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := testStore(t)
			now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
			submission := testSubmission(test.seed, now)
			if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
				t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
			}
			claim, err := store.ClaimDue(ctx, "external-operation", now, time.Minute)
			if err != nil || claim == nil {
				t.Fatalf("ClaimDue(external operation) = (%+v, %v)", claim, err)
			}
			if err := test.mutate(store, ctx, submission.Download.Hash, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if cleanup, err := store.ClaimDue(ctx, "cleanup", now.Add(2*time.Second), time.Minute); err != nil || cleanup != nil {
				t.Fatalf("cleanup bypassed live lease: (%+v, %v)", cleanup, err)
			}
			if err := store.CommitClaim(ctx, *claim, claim.Download); !errors.Is(err, ErrClaimLost) {
				t.Fatalf("superseded external commit = %v, want ErrClaimLost", err)
			}
			cleanup, err := store.ClaimDue(ctx, "cleanup", now.Add(time.Minute), time.Minute)
			if err != nil || cleanup == nil || cleanup.Download.State != test.wantState || cleanup.Download.PauseRequested != test.wantPause {
				t.Fatalf("cleanup after lease = (%+v, %v), want %s pause=%t", cleanup, err, test.wantState, test.wantPause)
			}
		})
	}
}

func TestClaimDueAndCommitCAS(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("e", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "first", now, time.Minute)
	if err != nil || claim == nil || claim.Owner != "first" || claim.State != domain.StateAccepted {
		t.Fatalf("ClaimDue() = (%+v, %v), want accepted first-owner claim", claim, err)
	}
	if second, err := store.ClaimDue(ctx, "second", now.Add(30*time.Second), time.Minute); err != nil || second != nil {
		t.Fatalf("ClaimDue while leased = (%+v, %v), want nil", second, err)
	}
	if err := store.CommitClaim(ctx, Claim{Download: claim.Download, Owner: "other", State: claim.State, Version: claim.Version}, claim.Download); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("CommitClaim(stale owner) error = %v, want ErrClaimLost", err)
	}
	next := claim.Download
	next.LastUpstreamStatus = "queued"
	next.UpdatedAt = now.Add(time.Second)
	next.Name = "metadata-name"
	if err := store.CommitClaim(ctx, *claim, next); err != nil {
		t.Fatalf("CommitClaim(same state): %v", err)
	}
	stored, err := store.GetDownload(ctx, next.Hash)
	if err != nil || stored.Name != "metadata-name" {
		t.Fatalf("trusted metadata name was not persisted: (%+v, %v)", stored, err)
	}
	if err := store.CommitClaim(ctx, *claim, next); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("CommitClaim(stale version) error = %v, want ErrClaimLost", err)
	}

	expired, err := store.ClaimDue(ctx, "second", now.Add(2*time.Minute), time.Minute)
	if err != nil || expired == nil || expired.Owner != "second" {
		t.Fatalf("ClaimDue(expired) = (%+v, %v), want second-owner claim", expired, err)
	}
	mutated := expired.Download
	mutated.CloudFolder = "/other"
	if err := store.CommitClaim(ctx, *expired, mutated); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CommitClaim(immutable mutation) error = %v, want ErrInvalidTransition", err)
	}
}

func TestDestinationReservationIsUniqueAndReleasedOnDelete(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	first := testSubmission("a", now)
	second := testSubmission("b", now)
	for _, submission := range []domain.Submission{first, second} {
		if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
			t.Fatalf("CreateSubmission(%s): inserted=%t err=%v", submission.Download.Hash, inserted, err)
		}
	}

	firstClaim, err := store.ClaimDue(ctx, "first", now, time.Minute)
	if err != nil || firstClaim == nil || firstClaim.Download.Hash != first.Download.Hash {
		t.Fatalf("first ClaimDue() = (%+v, %v)", firstClaim, err)
	}
	firstReserved := firstClaim.Download
	firstReserved.Name = "shared"
	firstReserved.DestinationName = "shared"
	firstReserved.UpdatedAt = now.Add(time.Second)
	nextRun := now.Add(time.Hour)
	firstReserved.NextRunAt = &nextRun
	if err := store.CommitClaim(ctx, *firstClaim, firstReserved); err != nil {
		t.Fatalf("reserve first destination: %v", err)
	}

	secondClaim, err := store.ClaimDue(ctx, "second", now, time.Minute)
	if err != nil || secondClaim == nil || secondClaim.Download.Hash != second.Download.Hash {
		t.Fatalf("second ClaimDue() = (%+v, %v)", secondClaim, err)
	}
	secondReserved := secondClaim.Download
	secondReserved.Name = "shared"
	secondReserved.DestinationName = "shared"
	secondReserved.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *secondClaim, secondReserved); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("duplicate reservation error = %v, want ErrDestinationConflict", err)
	}

	if err := store.RequestDelete(ctx, []string{first.Download.Hash}, false, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RequestDelete(first): %v", err)
	}
	deleteClaim, err := store.ClaimDue(ctx, "cleanup", now.Add(2*time.Second), time.Minute)
	if err != nil || deleteClaim == nil || deleteClaim.Download.Hash != first.Download.Hash {
		t.Fatalf("delete ClaimDue() = (%+v, %v)", deleteClaim, err)
	}
	deleted := deleteClaim.Download
	deleted.State = domain.StateDeleted
	deleted.UpdatedAt = now.Add(3 * time.Second)
	if err := store.CommitClaim(ctx, *deleteClaim, deleted); err != nil {
		t.Fatalf("release deleted destination: %v", err)
	}
	if err := store.CommitClaim(ctx, *secondClaim, secondReserved); err != nil {
		t.Fatalf("reserve released destination: %v", err)
	}
}

func TestRetryCleanupPreservesLiveLease(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("c", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	if err := store.RequestDelete(ctx, []string{submission.Download.Hash}, false, now); err != nil {
		t.Fatal(err)
	}
	failureClaim, err := store.ClaimDue(ctx, "failure", now, time.Minute)
	if err != nil || failureClaim == nil {
		t.Fatalf("failure ClaimDue() = (%+v, %v)", failureClaim, err)
	}
	failed := failureClaim.Download
	failed.LastError = "temporary cleanup failure"
	failed.AttemptCount = 1
	nextRun := now.Add(2 * time.Minute)
	failed.NextRunAt = &nextRun
	failed.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *failureClaim, failed); err != nil {
		t.Fatalf("persist cleanup failure: %v", err)
	}

	retryClaim, err := store.ClaimDue(ctx, "retrying", nextRun, time.Minute)
	if err != nil || retryClaim == nil || retryClaim.Download.LastError == "" {
		t.Fatalf("retry ClaimDue() = (%+v, %v)", retryClaim, err)
	}
	if err := store.Retry(ctx, submission.Download.Hash, domain.StateDeleteRequested, nextRun.Add(time.Second)); err != nil {
		t.Fatalf("Retry(cleanup): %v", err)
	}
	if concurrent, err := store.ClaimDue(ctx, "concurrent", nextRun.Add(2*time.Second), time.Minute); err != nil || concurrent != nil {
		t.Fatalf("Retry bypassed live lease: (%+v, %v)", concurrent, err)
	}
	if err := store.CommitClaim(ctx, *retryClaim, retryClaim.Download); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("superseded cleanup commit = %v, want ErrClaimLost", err)
	}
	afterLease, err := store.ClaimDue(ctx, "after", nextRun.Add(time.Minute), time.Minute)
	if err != nil || afterLease == nil || afterLease.Download.State != domain.StateDeleteRequested {
		t.Fatalf("cleanup after lease = (%+v, %v)", afterLease, err)
	}
}

func TestDestinationReservationDoesNotContainCategorySaveRoot(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertCategory(ctx, domain.Category{
		Name: "movies", CloudPath: "/cloud/movies", SavePath: "/downloads/movies",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	submission := testSubmission("a", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "worker", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	reserved := claim.Download
	reserved.Name = "movies"
	reserved.DestinationName = "movies"
	reserved.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *claim, reserved); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("category-root reservation error = %v, want ErrDestinationConflict", err)
	}
}

func TestCategorySaveRootDoesNotEnterLiveDestination(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("b", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "worker", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	reserved := claim.Download
	reserved.Name = "library"
	reserved.DestinationName = "library"
	reserved.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *claim, reserved); err != nil {
		t.Fatalf("CommitClaim(reservation): %v", err)
	}
	if _, err := store.UpsertCategory(ctx, domain.Category{
		Name: "shows", CloudPath: "/cloud/shows", SavePath: "/downloads/library/shows",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("nested category error = %v, want ErrDestinationConflict", err)
	}
}

func TestCategorySaveRootDoesNotEnterRetainedDeletedDestination(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertCategory(ctx, domain.Category{
		Name: "shows", CloudPath: "/cloud/shows", SavePath: "/downloads/shows",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	submission := testSubmission("d", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "worker", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	reserved := claim.Download
	reserved.Name = "library"
	reserved.DestinationName = "library"
	reserved.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *claim, reserved); err != nil {
		t.Fatalf("CommitClaim(reservation): %v", err)
	}
	deleteAt := now.Add(2 * time.Second)
	if err := store.RequestDelete(ctx, []string{submission.Download.Hash}, false, deleteAt); err != nil {
		t.Fatalf("RequestDelete(retain): %v", err)
	}
	deleteClaim, err := store.ClaimDue(ctx, "cleanup", deleteAt, time.Minute)
	if err != nil || deleteClaim == nil {
		t.Fatalf("delete ClaimDue() = (%+v, %v)", deleteClaim, err)
	}
	retained := deleteClaim.Download
	retained.State = domain.StateDeleted
	retained.ContentPath = "/downloads/library"
	retained.CompletedAt = new(now)
	retained.RemovedAt = new(deleteAt)
	retained.NextRunAt = nil
	retained.UpdatedAt = deleteAt
	if err := store.CommitClaim(ctx, *deleteClaim, retained); err != nil {
		t.Fatalf("CommitClaim(retained deletion): %v", err)
	}
	if _, err := store.UpsertCategory(ctx, domain.Category{
		Name: "movies", CloudPath: "/cloud/movies", SavePath: "/downloads/library/movies",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("retained destination insert error = %v, want ErrDestinationConflict", err)
	}
	if _, err := store.UpsertCategory(ctx, domain.Category{
		Name: "shows", CloudPath: "/cloud/shows", SavePath: "/downloads/library/shows",
		Enabled: true, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("retained destination update error = %v, want ErrDestinationConflict", err)
	}
}

func TestSubmissionErrorsDoNotExposeURI(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	submission := testSubmission("f", time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	submission.Download.Name = "bad/name"
	submission.Download.SubmissionURI = "magnet-secret-should-not-leak"
	_, _, err := store.CreateSubmission(ctx, submission)
	if err == nil || strings.Contains(err.Error(), submission.Download.SubmissionURI) {
		t.Fatalf("CreateSubmission() error = %v, must not expose submission URI", err)
	}
}

func TestNextDue(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	due, err := store.NextDue(ctx, now)
	if err != nil || due != nil {
		t.Fatalf("NextDue(empty) = (%v, %v), want nil", due, err)
	}

	late := testSubmission("a", now)
	lateRun := now.Add(10 * time.Minute)
	late.Download.NextRunAt = &lateRun
	early := testSubmission("b", now)
	earlyRun := now.Add(time.Minute)
	early.Download.NextRunAt = &earlyRun
	for _, submission := range []domain.Submission{late, early} {
		if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
			t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
		}
	}
	due, err = store.NextDue(ctx, now)
	if err != nil || due == nil || !due.Equal(earlyRun) {
		t.Fatalf("NextDue(sorted) = (%v, %v), want %v", due, err, earlyRun)
	}

	claim, err := store.ClaimDue(ctx, "worker", earlyRun, time.Hour)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(): (%+v, %v), want early claim", claim, err)
	}
	due, err = store.NextDue(ctx, earlyRun)
	if err != nil || due == nil || !due.Equal(lateRun) {
		t.Fatalf("NextDue(active lease) = (%v, %v), want deferred %v", due, err, lateRun)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/store.db")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store
}

func testSubmission(seed string, now time.Time) domain.Submission {
	hash := strings.Repeat(seed, 40)
	nextRun := now
	return domain.Submission{
		Download: domain.Download{
			Hash: hash, Name: "download-" + seed, SourceKind: domain.SourceMagnet, SubmissionURI: "magnet:?xt=urn:btih:" + hash,
			CloudFolder: "/cloud/downloads", SavePath: "/downloads", TotalSize: 1, State: domain.StateAccepted,
			PhaseStartedAt: now, NextRunAt: &nextRun, CreatedAt: now, UpdatedAt: now,
		},
		Files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "file.mkv", Size: 1}},
	}
}
