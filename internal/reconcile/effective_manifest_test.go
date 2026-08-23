package reconcile

import (
	"context"
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
