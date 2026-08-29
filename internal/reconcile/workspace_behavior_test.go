package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
)

func TestIsolatedWorkspacesKeepSameNamedCopiesIndependent(t *testing.T) {
	localRoot := t.TempDir()
	verifier, err := fsafe.New(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := t.TempDir()
	savePath := filepath.Join(localRoot, "downloads")
	if err := os.MkdirAll(savePath, 0o770); err != nil {
		t.Fatal(err)
	}
	hashA := "0123456789abcdef0123456789abcdef01234567"
	hashB := "fedcba9876543210fedcba9876543210fedcba98"
	dataA, dataB := []byte("copy A bytes"), []byte("copy B has different bytes")
	for _, fixture := range []struct {
		hash string
		data []byte
	}{
		{hashA, dataA}, {hashB, dataB},
	} {
		sourceDir := filepath.Join(remoteRoot, fixture.hash)
		if err := os.MkdirAll(sourceDir, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDir, "same.mkv"), fixture.data, 0o660); err != nil {
			t.Fatal(err)
		}
	}

	cloud := &fakeCloud{}
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		data, err := os.ReadFile(spec.SourcePath)
		if err != nil {
			return clouddrive.CopyTask{}, err
		}
		if err := os.WriteFile(filepath.Join(spec.DestinationPath, filepath.Base(spec.SourcePath)), data, 0o660); err != nil {
			return clouddrive.CopyTask{}, err
		}
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyCompleted, Progress: 1}, nil
	}
	repo := &fakeRepository{overrides: []domain.FileOverride{{FileIndex: 0, RelativePath: "renamed.mkv", Priority: 1}}}
	clock := &fakeClock{now: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	scheduler := testScheduler(t, clock, repo, cloud, verifier)

	makeDownload := func(hash string) domain.Download {
		workspace, err := fsafe.WorkspacePath(savePath, hash)
		if err != nil {
			t.Fatal(err)
		}
		return domain.Download{
			Hash: hash, Name: "same-name", SourceKind: domain.SourceTorrent,
			SubmissionURI: "magnet:?xt=urn:btih:" + hash, CloudFolder: "/cloud",
			SavePath: savePath, WorkspacePath: workspace,
			CloudResultPath: filepath.Join(remoteRoot, hash, "same.mkv"),
			CopySourcePath:  filepath.Join(remoteRoot, hash, "same.mkv"), DestinationName: "renamed.mkv",
			IsMultiFile: new(bool), State: domain.StateSubmittingCopy,
			LastUpstreamStatus: destinationClear, PhaseStartedAt: clock.now,
		}
	}

	completed := make([]domain.Download, 0, 2)
	for _, fixture := range []struct {
		hash string
		data []byte
	}{
		{hashA, dataA}, {hashB, dataB},
	} {
		repo.files = []domain.DownloadFile{{DownloadHash: fixture.hash, Index: 0, RelativePath: "same.mkv", Size: int64(len(fixture.data))}}
		download := makeDownload(fixture.hash)
		download.TotalSize = int64(len(fixture.data))
		download = step(t, scheduler, repo, download)
		download = step(t, scheduler, repo, download)
		if download.State != domain.StateCompleted {
			t.Fatalf("hash %s state = %s, want COMPLETED", fixture.hash, download.State)
		}
		if download.ContentPath != filepath.Join(download.WorkspacePath, "renamed.mkv") {
			t.Fatalf("hash %s content path = %q, want workspace content", fixture.hash, download.ContentPath)
		}
		got, err := os.ReadFile(download.ContentPath)
		if err != nil || string(got) != string(fixture.data) {
			t.Fatalf("hash %s bytes = %q, %v", fixture.hash, got, err)
		}
		completed = append(completed, download)
	}
	if completed[0].ContentPath == completed[1].ContentPath {
		t.Fatal("same-name hashes share a content path")
	}

	deleted := completed[0]
	deleted.State = domain.StateDeleteRequested
	deleted.DeleteFilesRequested = true
	deleted.NextRunAt = &clock.now
	step(t, scheduler, repo, deleted)
	if _, err := os.Stat(completed[0].WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("deleted workspace still exists: %v", err)
	}
	remaining := completed[1]
	content, err := verifier.Verify(remaining.WorkspacePath, fsafe.ExpectedContent{
		CandidateName: "renamed.mkv", Files: []fsafe.ExpectedFile{{RelativePath: "renamed.mkv", Size: int64(len(dataB))}},
	})
	if err != nil {
		t.Fatalf("remaining workspace verification failed: %v", err)
	}
	if got, err := os.ReadFile(content.Path); err != nil || string(got) != string(dataB) {
		t.Fatalf("remaining bytes = %q, %v", got, err)
	}
}

func TestWorkspaceCleanupAndLegacyFailedCutoverOrdering(t *testing.T) {
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	cloud, files := defaults()
	var events []string
	cloud.cancelCopy = func(_ context.Context, _, destination string) error {
		events = append(events, "cancel:"+destination)
		return nil
	}
	files.deleteWorkspace = func(savePath, hash string) error {
		events = append(events, "workspace:"+filepath.Join(savePath, hash))
		return nil
	}
	files.delete = func(content, savePath string) error {
		events = append(events, "legacy:"+filepath.Join(savePath, filepath.Base(content)))
		return nil
	}
	var prepared []string
	files.prepareWorkspace = func(savePath, hash string) (string, error) {
		prepared = append(prepared, filepath.Join(savePath, hash))
		return fsafe.WorkspacePath(savePath, hash)
	}
	repo := &fakeRepository{}
	scheduler := testScheduler(t, &fakeClock{now: now}, repo, cloud, files)
	hash := "0123456789abcdef0123456789abcdef01234567"
	workspace, err := fsafe.WorkspacePath("/downloads", hash)
	if err != nil {
		t.Fatal(err)
	}
	isolated := domain.Download{Hash: hash, SavePath: "/downloads", WorkspacePath: workspace, CopySourcePath: "/cloud/source", DestinationName: "same.mkv", State: domain.StateCancelRequested, PhaseStartedAt: now, PauseRequested: true}
	isolated = step(t, scheduler, repo, isolated)
	if isolated.State != domain.StateStopped || len(events) != 2 || events[0] != "cancel:"+workspace || events[1] != "workspace:"+filepath.Join("/downloads", hash) {
		t.Fatalf("isolated cleanup events/state = %v / %+v", events, isolated)
	}

	events = nil
	legacy := domain.Download{
		Hash: hash, Name: "same-name", SourceKind: domain.SourceMagnet,
		SubmissionURI: "magnet:?xt=urn:btih:" + hash, CloudFolder: "/cloud",
		SavePath: "/downloads", CloudResultPath: "/cloud/source",
		CopySourcePath: "/cloud/source", DestinationName: "same.mkv",
		LastUpstreamStatus: domain.UpstreamCopyFailed, PhaseStartedAt: now,
	}
	repository, _ := retryFailedCopy(t, now, legacy)
	scheduler = testScheduler(t, &fakeClock{now: now}, repository, cloud, files)
	legacy = storeStep(t, scheduler, repository, hash)
	if legacy.WorkspacePath != "" || len(events) != 1 || events[0] != "cancel:/downloads" {
		t.Fatalf("legacy cancel phase = %+v / %v", legacy, events)
	}
	legacy = storeStep(t, scheduler, repository, hash)
	wantWorkspace := filepath.Join("/downloads", ".cd211", hash)
	if legacy.WorkspacePath != wantWorkspace || legacy.LastUpstreamStatus != domain.UpstreamOfflineFinished || len(events) != 2 || events[1] != "legacy:/downloads/same.mkv" {
		t.Fatalf("legacy cutover = %+v / %v", legacy, events)
	}
	if len(prepared) != 0 {
		t.Fatalf("workspace prepared before durable cutover: %v", prepared)
	}
}
