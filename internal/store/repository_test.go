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
	submission.Download.NameOverridden = true
	submission.Download.SubmissionURI = "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	submission.Download.CloudTaskName = "cloud-original"

	created, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || !inserted {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v), want inserted download", created, inserted, err)
	}
	if created.SubmissionURI != submission.Download.SubmissionURI || created.CloudTaskName != submission.Download.CloudTaskName || !created.NameOverridden {
		t.Fatalf("CreateSubmission() lost durable identity fields: %+v", created)
	}
	duplicate, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || inserted || duplicate.Hash != created.Hash || !duplicate.NameOverridden {
		t.Fatalf("duplicate CreateSubmission() = (%+v, %t, %v), want existing download with name override", duplicate, inserted, err)
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
	stored, err := store.GetDownload(ctx, created.Hash)
	if err != nil || !stored.NameOverridden || stored.SubmissionURI != submission.Download.SubmissionURI || stored.CloudTaskName != submission.Download.CloudTaskName {
		t.Fatalf("CommitClaim() lost durable identity fields: (%+v, %v)", stored, err)
	}
	revivedSubmission := testSubmission("a", now.Add(3*time.Minute))
	revivedSubmission.Download.SubmissionURI = "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	revivedSubmission.Download.CloudFolder = "/cloud/revived"
	revivedSubmission.Download.CloudTaskName = "cloud-revived"
	revivedSubmission.Files = []domain.DownloadFile{{DownloadHash: revivedSubmission.Download.Hash, Index: 0, RelativePath: "replacement.mkv", Size: 42}}
	revived, inserted, err := store.CreateSubmission(ctx, revivedSubmission)
	if err != nil || !inserted || revived.State != domain.StateAccepted || revived.RowVersion <= created.RowVersion || revived.SubmissionURI != revivedSubmission.Download.SubmissionURI || revived.CloudFolder != revivedSubmission.Download.CloudFolder || revived.CloudTaskName != revivedSubmission.Download.CloudTaskName {
		t.Fatalf("revive CreateSubmission() = (%+v, %t, %v), want revived accepted download", revived, inserted, err)
	}
	files, err := store.ListDownloadFiles(ctx, revived.Hash)
	if err != nil || len(files) != 1 || files[0].RelativePath != "replacement.mkv" {
		t.Fatalf("ListDownloadFiles() = (%+v, %v), want replaced files", files, err)
	}
}

func TestCreateSubmissionRoundTripsWorkspacePath(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("a", now)
	submission.Download.WorkspacePath = "/downloads/.cd211/" + submission.Download.Hash
	created, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || !inserted || created.WorkspacePath != submission.Download.WorkspacePath {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v), want workspace path round trip", created, inserted, err)
	}
	stored, err := store.GetDownload(ctx, submission.Download.Hash)
	if err != nil || stored.WorkspacePath != submission.Download.WorkspacePath {
		t.Fatalf("GetDownload() = (%+v, %v), want workspace path round trip", stored, err)
	}
}

func TestCreateSubmissionRejectsWorkspaceNestingButAllowsSharedSaveRoot(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	ancestor := testSubmission("a", now)
	ancestor.Download.WorkspacePath = "/downloads/.cd211/" + ancestor.Download.Hash
	if _, inserted, err := store.CreateSubmission(ctx, ancestor); err != nil || !inserted {
		t.Fatalf("CreateSubmission(ancestor): inserted=%t err=%v", inserted, err)
	}

	nestedSave := testSubmission("b", now.Add(time.Second))
	nestedSave.Download.SavePath = ancestor.Download.WorkspacePath
	nestedSave.Download.WorkspacePath = nestedSave.Download.SavePath + "/.cd211/" + nestedSave.Download.Hash
	if _, inserted, err := store.CreateSubmission(ctx, nestedSave); !errors.Is(err, ErrDestinationConflict) || inserted {
		t.Fatalf("CreateSubmission(nested save): inserted=%t err=%v, want destination conflict", inserted, err)
	}

	workspaceOwner := testSubmission("c", now.Add(2*time.Second))
	workspaceOwner.Download.SavePath = "/downloads/library/owner"
	workspaceOwner.Download.WorkspacePath = workspaceOwner.Download.SavePath + "/.cd211/" + workspaceOwner.Download.Hash
	if _, inserted, err := store.CreateSubmission(ctx, workspaceOwner); err != nil || !inserted {
		t.Fatalf("CreateSubmission(workspace owner): inserted=%t err=%v", inserted, err)
	}

	nestedWorkspace := testSubmission("d", now.Add(3*time.Second))
	nestedWorkspace.Download.SavePath = ancestor.Download.WorkspacePath + "/child"
	nestedWorkspace.Download.WorkspacePath = nestedWorkspace.Download.SavePath + "/.cd211/" + nestedWorkspace.Download.Hash
	if _, inserted, err := store.CreateSubmission(ctx, nestedWorkspace); !errors.Is(err, ErrDestinationConflict) || inserted {
		t.Fatalf("CreateSubmission(nested workspace): inserted=%t err=%v, want destination conflict", inserted, err)
	}

	for _, seed := range []string{"e", "f"} {
		shared := testSubmission(seed, now.Add(4*time.Second))
		shared.Download.WorkspacePath = "/downloads/.cd211/" + shared.Download.Hash
		if _, inserted, err := store.CreateSubmission(ctx, shared); err != nil || !inserted {
			t.Fatalf("CreateSubmission(shared save root %s): inserted=%t err=%v", seed, inserted, err)
		}
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

func TestListAllDownloadsEnumeratesEveryState(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	states := []domain.State{
		domain.StateAccepted,
		domain.StateStopped,
		domain.StateSubmittingOffline,
		domain.StateWaitingOffline,
		domain.StateSubmittingCopy,
		domain.StateWaitingCopy,
		domain.StateVerifyingLocal,
		domain.StateCompleted,
		domain.StateFailed,
		domain.StateCancelRequested,
		domain.StateCancelled,
		domain.StateDeleteRequested,
		domain.StateDeleted,
	}
	seeds := "123456789abcd"
	for index, state := range states {
		at := now.Add(time.Duration(index) * time.Second)
		submission := testSubmission(string(seeds[index]), at)
		submission.Download.Name = "download-" + string(seeds[index])
		if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
			t.Fatalf("CreateSubmission(%s): inserted=%t err=%v", state, inserted, err)
		}

		var (
			cloudResultPath any
			copySourcePath  any
			contentPath     any
			completedAt     any
			removedAt       any
			nextRunAt       any = at
		)
		switch state {
		case domain.StateSubmittingCopy:
			cloudResultPath = "/cloud/result"
		case domain.StateWaitingCopy, domain.StateVerifyingLocal:
			cloudResultPath = "/cloud/result"
			copySourcePath = cloudResultPath
		case domain.StateCompleted:
			cloudResultPath = "/cloud/result"
			copySourcePath = cloudResultPath
			contentPath = "/downloads/content"
			completedAt = at
		}
		if state == domain.StateStopped || state == domain.StateFailed || state == domain.StateCancelled ||
			state == domain.StateDeleteRequested || state == domain.StateDeleted {
			nextRunAt = nil
		}
		if state == domain.StateDeleteRequested || state == domain.StateDeleted {
			removedAt = at
		}
		if _, err := store.db.ExecContext(ctx, `
			UPDATE downloads
			SET state = ?, cloud_result_path = ?, copy_source_path = ?, content_path = ?,
				completed_at = ?, removed_at = ?, next_run_at = ?
			WHERE hash = ?`,
			string(state), cloudResultPath, copySourcePath, contentPath, completedAt, removedAt, nextRunAt, submission.Download.Hash,
		); err != nil {
			t.Fatalf("assign %s state: %v", state, err)
		}
		if state == domain.StateDeleted {
			if _, err := store.db.ExecContext(ctx, `UPDATE downloads SET save_path = '' WHERE hash = ?`, submission.Download.Hash); err != nil {
				t.Fatalf("corrupt ordinary deleted save path: %v", err)
			}
		}
	}

	all, err := store.ListAllDownloads(ctx)
	if err != nil {
		t.Fatalf("ListAllDownloads() error = %v", err)
	}
	if len(all) != len(states) {
		t.Fatalf("ListAllDownloads() returned %d rows, want %d", len(all), len(states))
	}
	byHash := make(map[string]domain.Download, len(all))
	for _, download := range all {
		byHash[download.Hash] = download
	}
	for index, state := range states {
		hash := strings.Repeat(string(seeds[index]), 40)
		download, ok := byHash[hash]
		if !ok || download.State != state || download.Name != "download-"+string(seeds[index]) {
			t.Errorf("ListAllDownloads() missing mapped %s row: ok=%t download=%+v", state, ok, download)
		}
	}

	visible, err := store.ListDownloads(ctx, nil)
	if err != nil {
		t.Fatalf("ListDownloads() error = %v", err)
	}
	if len(visible) != len(states)-2 {
		t.Fatalf("ListDownloads() returned %d rows, want %d visible rows", len(visible), len(states)-2)
	}
}

func TestListAllDownloadsRejectsMalformedRow(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("e", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE downloads SET delete_files_requested = 2 WHERE hash = ?`, submission.Download.Hash); err != nil {
		t.Fatalf("corrupt download row: %v", err)
	}
	if _, err := store.ListAllDownloads(ctx); err == nil {
		t.Fatal("ListAllDownloads() error = nil, want malformed row error")
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
			submission.Download.CloudResultPath = "/cloud/downloads/download-" + test.seed
			submission.Download.CopySourcePath = submission.Download.CloudResultPath
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

func TestRetryProblemPersistenceAndClear(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("a", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := store.ClaimDue(ctx, "retrying", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	retried := claim.Download
	retried.LastError = domain.ProblemText(domain.ProblemCloudCopyNotReady)
	retried.LastErrorCode = string(domain.ProblemCloudCopyNotReady)
	retried.AttemptCount = 3
	nextRun := now.Add(4 * time.Minute)
	retried.NextRunAt = &nextRun
	retried.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *claim, retried); err != nil {
		t.Fatalf("CommitClaim(retry bookkeeping): %v", err)
	}
	stored, err := store.GetDownload(ctx, retried.Hash)
	if err != nil || stored.LastErrorCode != string(domain.ProblemCloudCopyNotReady) ||
		stored.LastError != domain.ProblemText(domain.ProblemCloudCopyNotReady) ||
		stored.AttemptCount != 3 || stored.NextRunAt == nil || !stored.NextRunAt.Equal(nextRun) {
		t.Fatalf("retry problem did not survive SQLite round trip: (%+v, %v)", stored, err)
	}

	failureClaim, err := store.ClaimDue(ctx, "fail", nextRun, time.Minute)
	if err != nil || failureClaim == nil {
		t.Fatalf("ClaimDue(fail) = (%+v, %v)", failureClaim, err)
	}
	failed := failureClaim.Download
	failed.State = domain.StateFailed
	failed.LastError = domain.ProblemText(domain.ProblemCloudCopyNotReadyTimeout)
	failed.LastErrorCode = string(domain.ProblemCloudCopyNotReadyTimeout)
	failed.NextRunAt = nil
	failed.UpdatedAt = now.Add(2 * time.Minute)
	if err := store.CommitClaim(ctx, *failureClaim, failed); err != nil {
		t.Fatalf("CommitClaim(failed): %v", err)
	}
	if err := store.Retry(ctx, failed.Hash, domain.StateAccepted, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("Retry(): %v", err)
	}
	retried, err = store.GetDownload(ctx, failed.Hash)
	if err != nil || retried.LastError != "" || retried.LastErrorCode != "" || retried.AttemptCount != 0 || retried.NextRunAt == nil {
		t.Fatalf("Retry did not clear code and text: (%+v, %v)", retried, err)
	}
}

func TestRetryDestinationConflictInvalidatesDerivedCopyTarget(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		code        domain.ProblemCode
		target      domain.State
		wantDerived bool
	}{
		{"copy destination conflict", domain.ProblemDestinationConflict, domain.StateSubmittingCopy, false},
		{"other copy failure", domain.ProblemCopyTaskFailed, domain.StateSubmittingCopy, true},
		{"non-copy retry", domain.ProblemDestinationConflict, domain.StateAccepted, true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repository := testStore(t)
			submission := testSubmission(string(rune('b'+index)), now)
			multiFile := false
			submission.Download.SourceKind = domain.SourceTorrent
			submission.Download.IsMultiFile = &multiFile
			submission.Download.TotalSize = 42
			submission.Files = []domain.DownloadFile{{
				DownloadHash: submission.Download.Hash,
				Index:        0,
				RelativePath: "episode.mkv",
				Size:         42,
			}}
			if _, inserted, err := repository.CreateSubmission(ctx, submission); err != nil || !inserted {
				t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
			}
			claim, err := repository.ClaimDue(ctx, "fail", now, time.Minute)
			if err != nil || claim == nil {
				t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
			}
			failed := claim.Download
			failed.State = domain.StateFailed
			failed.CloudResultPath = "/cloud/downloads/shared-season"
			failed.CopySourcePath = failed.CloudResultPath
			failed.DestinationName = "shared-season"
			failed.LastUpstreamStatus = domain.UpstreamOfflineFinished
			failed.LastError = domain.ProblemText(test.code)
			failed.LastErrorCode = string(test.code)
			failed.NextRunAt = nil
			failed.UpdatedAt = now.Add(time.Minute)
			if err := repository.CommitClaim(ctx, *claim, failed); err != nil {
				t.Fatalf("CommitClaim(failed): %v", err)
			}

			if err := repository.Retry(ctx, failed.Hash, test.target, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("Retry(): %v", err)
			}
			retried, err := repository.GetDownload(ctx, failed.Hash)
			if err != nil {
				t.Fatalf("GetDownload(): %v", err)
			}
			if retried.State != test.target || retried.CloudResultPath != failed.CloudResultPath ||
				retried.LastError != "" || retried.LastErrorCode != "" {
				t.Fatalf("retried download lost durable state: %+v", retried)
			}
			if gotDerived := retried.CopySourcePath != "" || retried.DestinationName != ""; gotDerived != test.wantDerived {
				t.Fatalf("derived copy target present = %t, want %t: %+v", gotDerived, test.wantDerived, retried)
			}
			manifest, err := repository.ListDownloadFiles(ctx, failed.Hash)
			if err != nil || len(manifest) != 1 || manifest[0].RelativePath != "episode.mkv" || manifest[0].Size != 42 {
				t.Fatalf("manifest after retry = (%+v, %v)", manifest, err)
			}
		})
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

func TestCommitClaimAllowsDirectOfflineToCopyTransition(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	submission := testSubmission("f", now)
	if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}

	acceptedClaim, err := store.ClaimDue(ctx, "offline-submit", now, time.Minute)
	if err != nil || acceptedClaim == nil {
		t.Fatalf("ClaimDue(accepted): (%+v, %v), want a claim", acceptedClaim, err)
	}
	offline := acceptedClaim.Download
	offline.State = domain.StateSubmittingOffline
	offline.UpdatedAt = now.Add(time.Second)
	if err := store.CommitClaim(ctx, *acceptedClaim, offline); err != nil {
		t.Fatalf("CommitClaim(offline): %v", err)
	}

	offlineClaim, err := store.ClaimDue(ctx, "copy-submit", now.Add(2*time.Second), time.Minute)
	if err != nil || offlineClaim == nil || offlineClaim.State != domain.StateSubmittingOffline {
		t.Fatalf("ClaimDue(offline): (%+v, %v), want submitting-offline claim", offlineClaim, err)
	}
	next := offlineClaim.Download
	next.State = domain.StateSubmittingCopy
	next.CloudResultPath = "/cloud/downloads/result"
	next.OfflineProgress = 1
	next.QbitProgress = 0.9
	next.UpdatedAt = now.Add(3 * time.Second)
	if err := store.CommitClaim(ctx, *offlineClaim, next); err != nil {
		t.Fatalf("CommitClaim(copy): %v", err)
	}

	stored, err := store.GetDownload(ctx, next.Hash)
	if err != nil {
		t.Fatalf("GetDownload(): %v", err)
	}
	if stored.State != domain.StateSubmittingCopy || stored.OfflineProgress != 1 || stored.QbitProgress != 0.9 {
		t.Fatalf("direct offline-to-copy commit = %+v, want copy state with offline=1 and qbit=0.9", stored)
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
func TestReservedLogicalSavePathGuardsAreSymmetric(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		savePath string
		category bool
		update   bool
	}{
		{name: "download save root insert", savePath: "/downloads/.cd211"},
		{name: "download quarantine insert", savePath: "/downloads/.cd211/.quarantine"},
		{name: "download category insert", savePath: "/downloads/.cd211/category"},
		{name: "download save root update", savePath: "/downloads/.cd211", update: true},
		{name: "category save root insert", savePath: "/downloads/.cd211", category: true},
		{name: "category quarantine insert", savePath: "/downloads/.cd211/.quarantine", category: true},
		{name: "category download insert", savePath: "/downloads/.cd211/download", category: true},
		{name: "category save root update", savePath: "/downloads/.cd211", category: true, update: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			if test.category {
				category := domain.Category{Name: "reserved", CloudPath: "/cloud", SavePath: test.savePath, Enabled: true, CreatedAt: now, UpdatedAt: now}
				if test.update {
					category.SavePath = "/downloads"
					if _, err := store.UpsertCategory(ctx, category); err != nil {
						t.Fatalf("seed category: %v", err)
					}
					category.SavePath = test.savePath
					category.UpdatedAt = now.Add(time.Second)
				}
				if _, err := store.UpsertCategory(ctx, category); !errors.Is(err, ErrDestinationConflict) {
					t.Fatalf("UpsertCategory() error = %v, want ErrDestinationConflict", err)
				}
				return
			}

			submission := testSubmission("7", now)
			if test.update {
				if _, inserted, err := store.CreateSubmission(ctx, submission); err != nil || !inserted {
					t.Fatalf("seed submission: inserted=%t err=%v", inserted, err)
				}
				if _, err := store.db.ExecContext(ctx, `UPDATE downloads SET save_path = ? WHERE hash = ?`, test.savePath, submission.Download.Hash); err == nil {
					t.Fatal("raw save_path update accepted a reserved component")
				}
				return
			}
			submission.Download.SavePath = test.savePath
			if _, inserted, err := store.CreateSubmission(ctx, submission); !errors.Is(err, ErrDestinationConflict) || inserted {
				t.Fatalf("CreateSubmission() = inserted=%t err=%v, want ErrDestinationConflict", inserted, err)
			}
		})
	}
}

func TestReservedLogicalSavePathGuardsAllowNearNamesAndGeneratedWorkspaces(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	submission := testSubmission("8", now)
	submission.Download.SavePath = "/downloads/.cd211-backup"
	submission.Download.WorkspacePath = "/downloads/.cd211-backup/.cd211/" + submission.Download.Hash
	created, inserted, err := store.CreateSubmission(ctx, submission)
	if err != nil || !inserted || created.WorkspacePath != submission.Download.WorkspacePath {
		t.Fatalf("CreateSubmission() = (%+v, %t, %v), want near-name logical root with generated workspace", created, inserted, err)
	}

	category := domain.Category{
		Name: "near", CloudPath: "/cloud/near", SavePath: "/downloads/.cd211-backup/category",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.UpsertCategory(ctx, category); err != nil {
		t.Fatalf("UpsertCategory() near-name path: %v", err)
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
