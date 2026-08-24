package reconcile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
)

func TestVerifyAndRecordUsesRenamedSelectedManifest(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	files := &fakeFilesystem{size: 2, verify: func(_ string, expected fsafe.ExpectedContent) (string, error) {
		if len(expected.Files) != 1 || expected.Files[0].RelativePath != "renamed.txt" || expected.Files[0].Size != 2 {
			t.Fatalf("expected manifest = %#v", expected.Files)
		}
		return "/downloads/task", nil
	}}
	repo := &fakeRepository{files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "old.txt", Size: 2}, {DownloadHash: hash, Index: 1, RelativePath: "excluded.txt", Size: 3}}, overrides: []domain.FileOverride{{DownloadHash: hash, FileIndex: 0, RelativePath: "renamed.txt", Priority: 1}, {DownloadHash: hash, FileIndex: 1, RelativePath: "excluded.txt", Priority: 0}}}
	scheduler := &Scheduler{repo: repo, files: files}
	multi := true
	download := domain.Download{Hash: hash, SourceKind: domain.SourceTorrent, SavePath: "/downloads", DestinationName: "task", IsMultiFile: &multi, TotalSize: 5, State: domain.StateWaitingCopy, PhaseStartedAt: time.Now()}
	if err := scheduler.verifyAndRecord(context.Background(), &download); err != nil {
		t.Fatal(err)
	}
	if len(files.plans) != 2 || files.plans[0].EffectivePath != "renamed.txt" || files.plans[1].Priority != 0 {
		t.Fatalf("plans = %#v", files.plans)
	}
}

func TestVerifyAndRecordMovesAndVerifiesSingleFileAtEffectivePath(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	localRoot := t.TempDir()
	savePath := filepath.Join(localRoot, "anime")
	if err := os.Mkdir(savePath, 0o770); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(savePath, "original.mkv")
	if err := os.WriteFile(originalPath, []byte("episode"), 0o660); err != nil {
		t.Fatal(err)
	}
	verifier, err := fsafe.New(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	effectivePath := filepath.Join("Season 01", "episode.mkv")
	repo := &fakeRepository{
		files:     []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "original.mkv", Size: 7}},
		overrides: []domain.FileOverride{{DownloadHash: hash, FileIndex: 0, RelativePath: effectivePath, Priority: 1}},
	}
	scheduler := &Scheduler{repo: repo, files: verifier}
	multi := false
	download := domain.Download{
		Hash: hash, SourceKind: domain.SourceTorrent, SavePath: savePath,
		DestinationName: "original.mkv", IsMultiFile: &multi, TotalSize: 7,
		State: domain.StateVerifyingLocal, PhaseStartedAt: time.Now(),
	}

	if err := scheduler.verifyAndRecord(context.Background(), &download); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(savePath, effectivePath)
	wantPath, err = filepath.EvalSymlinks(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if download.ContentPath != wantPath || download.TotalSize != 7 {
		t.Fatalf("verified download = %+v, want content path %q and size 7", download, wantPath)
	}
	if _, err := os.Stat(originalPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("original path still exists or cannot be inspected: %v", err)
	}
	if data, err := os.ReadFile(wantPath); err != nil || string(data) != "episode" {
		t.Fatalf("effective content = %q, %v", data, err)
	}
}

func TestVerifyAndRecordKeepsUnrenamedSingleFileBehavior(t *testing.T) {
	hash := "fedcba9876543210fedcba9876543210fedcba98"
	localRoot := t.TempDir()
	savePath := filepath.Join(localRoot, "anime")
	if err := os.Mkdir(savePath, 0o770); err != nil {
		t.Fatal(err)
	}
	contentPath := filepath.Join(savePath, "episode.mkv")
	if err := os.WriteFile(contentPath, []byte("episode"), 0o660); err != nil {
		t.Fatal(err)
	}
	verifier, err := fsafe.New(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "episode.mkv", Size: 7}}}
	scheduler := &Scheduler{repo: repo, files: verifier}
	multi := false
	download := domain.Download{
		Hash: hash, SourceKind: domain.SourceTorrent, SavePath: savePath,
		DestinationName: "episode.mkv", IsMultiFile: &multi, TotalSize: 7,
		State: domain.StateVerifyingLocal, PhaseStartedAt: time.Now(),
	}

	if err := scheduler.verifyAndRecord(context.Background(), &download); err != nil {
		t.Fatal(err)
	}
	contentPath, err = filepath.EvalSymlinks(contentPath)
	if err != nil {
		t.Fatal(err)
	}
	if download.ContentPath != contentPath || download.TotalSize != 7 {
		t.Fatalf("verified download = %+v, want content path %q and size 7", download, contentPath)
	}
}

func TestVerifyAndRecordKeepsMultiFileRootWithEffectivePaths(t *testing.T) {
	hash := "1123456789abcdef0123456789abcdef01234567"
	localRoot := t.TempDir()
	savePath := filepath.Join(localRoot, "anime")
	torrentRoot := filepath.Join(savePath, "show")
	if err := os.MkdirAll(torrentRoot, 0o770); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(torrentRoot, "original.mkv")
	if err := os.WriteFile(originalPath, []byte("episode"), 0o660); err != nil {
		t.Fatal(err)
	}
	verifier, err := fsafe.New(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	effectivePath := filepath.Join("Season 01", "episode.mkv")
	repo := &fakeRepository{
		files:     []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "original.mkv", Size: 7}},
		overrides: []domain.FileOverride{{DownloadHash: hash, FileIndex: 0, RelativePath: effectivePath, Priority: 1}},
	}
	scheduler := &Scheduler{repo: repo, files: verifier}
	multi := true
	download := domain.Download{
		Hash: hash, SourceKind: domain.SourceTorrent, SavePath: savePath,
		DestinationName: "show", IsMultiFile: &multi, TotalSize: 7,
		State: domain.StateVerifyingLocal, PhaseStartedAt: time.Now(),
	}

	if err := scheduler.verifyAndRecord(context.Background(), &download); err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(torrentRoot)
	if err != nil {
		t.Fatal(err)
	}
	if download.ContentPath != wantRoot || download.TotalSize != 7 {
		t.Fatalf("verified download = %+v, want content root %q and size 7", download, wantRoot)
	}
	if _, err := os.Stat(originalPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("original path still exists or cannot be inspected: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(torrentRoot, effectivePath)); err != nil || string(data) != "episode" {
		t.Fatalf("effective content = %q, %v", data, err)
	}
}
