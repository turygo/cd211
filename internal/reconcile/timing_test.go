package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
)

func TestWorkflowTimingStartsAtOfflineSubmission(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.ensureOffline = func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{InfoHash: "0123456789012345678901234567890123456789", State: clouddrive.OfflineInit}, nil
	}
	download := baseDownload(domain.StateSubmittingOffline, now.Add(-30*time.Minute))
	got := step(t, testScheduler(t, clock, repo, cloud, files), repo, download)
	if got.OfflineStartedAt == nil || !got.OfflineStartedAt.Equal(now) {
		t.Fatalf("offline start = %v, want submission time %v", got.OfflineStartedAt, now)
	}
}

func TestWorkflowTimingEndsWhenCopyCompletes(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyCompleted, Progress: 1}, nil
	}
	got := step(t, testScheduler(t, clock, repo, cloud, files), repo, copySubmission(now.Add(-30*time.Minute)))
	if got.CopyCompletedAt == nil || !got.CopyCompletedAt.Equal(now) {
		t.Fatalf("copy completion = %v, want copy completion observation time %v", got.CopyCompletedAt, now)
	}
}
