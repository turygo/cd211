package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
)

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	durations []time.Duration
	timer     *fakeTimer
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.durations = append(c.durations, d)
	if c.timer == nil {
		c.timer = &fakeTimer{ch: make(chan time.Time)}
	}
	return c.timer
}

func (c *fakeClock) timerDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durations...)
}

type fakeTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool {
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

type fakeRepository struct {
	claim      *store.Claim
	commits    []domain.Download
	commitErr  error
	commitErrs []error
	next       *time.Time
	err        error
	filesErr   error
	files      []domain.DownloadFile
	overrides  []domain.FileOverride
}

func (r *fakeRepository) ListDownloadFileOverrides(context.Context, string) ([]domain.FileOverride, error) {
	return r.overrides, r.err
}

func (r *fakeRepository) ClaimDue(context.Context, string, time.Time, time.Duration) (*store.Claim, error) {
	claim := r.claim
	r.claim = nil
	return claim, r.err
}
func (r *fakeRepository) CommitClaim(_ context.Context, _ store.Claim, d domain.Download) error {
	r.commits = append(r.commits, d)
	if len(r.commitErrs) != 0 {
		err := r.commitErrs[0]
		r.commitErrs = r.commitErrs[1:]
		return err
	}
	return r.commitErr
}
func (r *fakeRepository) NextDue(context.Context, time.Time) (*time.Time, error) {
	return r.next, r.err
}
func (r *fakeRepository) ListDownloadFiles(context.Context, string) ([]domain.DownloadFile, error) {
	if r.filesErr != nil {
		return r.files, r.filesErr
	}
	return r.files, r.err
}

type fakeCloud struct {
	ensureOffline  func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error)
	inspectOffline func(context.Context, string, string) (clouddrive.OfflineTask, bool, error)
	inspectContent func(context.Context, string) (clouddrive.Content, error)
	cancelOffline  func(context.Context, string, string) error
	ensureCopy     func(context.Context, clouddrive.CopySpec) (clouddrive.CopyTask, error)
	inspectCopy    func(context.Context, string, string) (clouddrive.CopyTask, bool, error)
	cancelCopy     func(context.Context, string, string) error
}

func (c fakeCloud) EnsureOffline(ctx context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
	return c.ensureOffline(ctx, spec)
}
func (c fakeCloud) InspectOffline(ctx context.Context, folder, hash string) (clouddrive.OfflineTask, bool, error) {
	return c.inspectOffline(ctx, folder, hash)
}
func (c fakeCloud) InspectContent(ctx context.Context, source string) (clouddrive.Content, error) {
	if c.inspectContent == nil {
		return clouddrive.Content{Path: source, Kind: clouddrive.ContentDirectory}, nil
	}
	return c.inspectContent(ctx, source)
}
func (c fakeCloud) CancelOffline(ctx context.Context, folder, hash string) error {
	return c.cancelOffline(ctx, folder, hash)
}
func (c fakeCloud) EnsureCopy(ctx context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
	return c.ensureCopy(ctx, spec)
}
func (c fakeCloud) InspectCopy(ctx context.Context, source, destination string) (clouddrive.CopyTask, bool, error) {
	return c.inspectCopy(ctx, source, destination)
}
func (c fakeCloud) CancelCopy(ctx context.Context, source, destination string) error {
	return c.cancelCopy(ctx, source, destination)
}

type fakeFilesystem struct {
	verify            func(string, fsafe.ExpectedContent) (string, error)
	verifyUnknownType func(string, string) (fsafe.UnknownContent, error)
	size              int64
	delete            func(string, string) error
	plans             []fsafe.FilePlan
}

func (f *fakeFilesystem) ApplyFilePlan(_ string, _ string, plans []fsafe.FilePlan) error {
	f.plans = append([]fsafe.FilePlan(nil), plans...)
	return nil
}
func (f *fakeFilesystem) Verify(save string, expected fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	path, err := f.verify(save, expected)
	return fsafe.VerifiedContent{Path: path, Size: f.size}, err
}

func (f *fakeFilesystem) VerifyUnknownType(save, name string) (fsafe.UnknownContent, error) {
	if f.verifyUnknownType == nil {
		return fsafe.UnknownContent{}, fs.ErrNotExist
	}
	return f.verifyUnknownType(save, name)
}

func (f *fakeFilesystem) Delete(content, save string) error {
	if f.delete == nil {
		return nil
	}
	return f.delete(content, save)
}

func baseDownload(state domain.State, now time.Time) domain.Download {
	return domain.Download{Hash: "0123456789012345678901234567890123456789", Name: "payload", SourceKind: domain.SourceMagnet, SubmissionURI: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789", CloudFolder: "/cloud", SavePath: "/downloads", CloudResultPath: "/cloud/payload", State: state, PhaseStartedAt: now}
}

func testScheduler(t *testing.T, clock *fakeClock, repo *fakeRepository, cloud *fakeCloud, files *fakeFilesystem) *Scheduler {
	t.Helper()
	s, err := New(Config{Owner: "worker", LeaseDuration: time.Minute, PollInterval: 10 * time.Second, OfflineTimeout: time.Hour, CopyTimeout: time.Hour, VerifyTimeout: time.Hour, WorkerCount: 1}, repo, cloud, files, clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewValidatesConfigAndTypedNilDependencies(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	repo := &fakeRepository{}
	cloud, files := defaults()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(Config{}, repo, cloud, files, clock, logger); err == nil {
		t.Fatal("New accepted invalid config")
	}
	valid := Config{
		Owner: "worker", LeaseDuration: time.Minute, PollInterval: time.Second,
		OfflineTimeout: time.Hour, CopyTimeout: time.Hour, VerifyTimeout: time.Hour, WorkerCount: 1,
	}
	valid.LeaseDuration = leaseCommitMargin
	if _, err := New(valid, repo, cloud, files, clock, logger); err == nil {
		t.Fatal("New accepted a lease without commit margin")
	}
	valid.LeaseDuration = time.Minute
	var nilRepo *fakeRepository
	if _, err := New(valid, nilRepo, cloud, files, clock, logger); err == nil {
		t.Fatal("New accepted typed nil repository")
	}
}

func TestStructuredLogRedactsProtectedSource(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	scheduler := &Scheduler{
		clock:  &fakeClock{now: now},
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
	}
	download := baseDownload(domain.StateSubmittingOffline, now)
	download.SubmissionURI = "magnet:?xt=urn:btih:private&tr=https://tracker.example/passkey-secret"
	download.LastError = "CloudDrive2 is unreachable."
	download.LastErrorCode = string(domain.ProblemCloudUnreachable)
	download.AttemptCount = 2
	scheduler.log(download, "ensure_offline", now.Add(-time.Second), "committed")

	entry := output.String()
	for _, required := range []string{`"hash":"01234567"`, `"state":"SUBMITTING_OFFLINE"`, `"operation":"ensure_offline"`, `"attempt":2`, `"problem":"cloud_unreachable"`, `"error":"CloudDrive2 is unreachable."`} {
		if !strings.Contains(entry, required) {
			t.Fatalf("log entry missing %s: %s", required, entry)
		}
	}
	if strings.Contains(entry, "passkey-secret") || strings.Contains(entry, download.SubmissionURI) {
		t.Fatalf("log entry exposed protected source: %s", entry)
	}
}

func defaults() (*fakeCloud, *fakeFilesystem) {
	cloud := fakeCloud{
		ensureOffline: func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
			return clouddrive.OfflineTask{InfoHash: spec.Hash, State: clouddrive.OfflineInit}, nil
		},
		inspectOffline: func(context.Context, string, string) (clouddrive.OfflineTask, bool, error) {
			return clouddrive.OfflineTask{}, false, nil
		},
		cancelOffline: func(context.Context, string, string) error { return nil },
		ensureCopy: func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
			return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyPending}, nil
		},
		inspectCopy: func(context.Context, string, string) (clouddrive.CopyTask, bool, error) {
			return clouddrive.CopyTask{}, false, nil
		},
		cancelCopy: func(context.Context, string, string) error { return nil },
	}
	files := fakeFilesystem{verify: func(string, fsafe.ExpectedContent) (string, error) { return "", fs.ErrNotExist }, delete: func(string, string) error { return nil }}
	return &cloud, &files
}

func step(t *testing.T, s *Scheduler, repo *fakeRepository, d domain.Download) domain.Download {
	t.Helper()
	repo.claim = &store.Claim{Download: d, Owner: "worker", State: d.State, Version: d.RowVersion}
	claimed, err := s.Step(context.Background())
	if err != nil || !claimed {
		t.Fatalf("Step() = %v, %v", claimed, err)
	}
	if len(repo.commits) == 0 {
		t.Fatal("missing durable commit")
	}
	return repo.commits[len(repo.commits)-1]
}

func TestStepBoundsExternalOperationBeforeLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	calls := 0
	cloud.ensureOffline = func(ctx context.Context, _ clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		calls++
		<-ctx.Done()
		return clouddrive.OfflineTask{}, ctx.Err()
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)
	scheduler.config.LeaseDuration = leaseCommitMargin + 20*time.Millisecond
	download := baseDownload(domain.StateSubmittingOffline, now)
	committed := step(t, scheduler, repo, download)
	if calls != 1 || committed.State != domain.StateSubmittingOffline || committed.AttemptCount != 1 ||
		committed.NextRunAt == nil || committed.LastErrorCode != string(domain.ProblemWorkflowOperationTimeout) {
		t.Fatalf("bounded operation result = calls:%d download:%+v", calls, committed)
	}
}

func TestCleanupRejectionRetainsCleanupIntent(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.cancelCopy = func(context.Context, string, string) error {
		return &clouddrive.Error{Operation: "cancel_copy", Kind: clouddrive.ErrorRejected}
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)
	download := baseDownload(domain.StateCancelRequested, now)
	download.CopySourcePath = "/cloud/payload"
	committed := step(t, scheduler, repo, download)
	if committed.State != domain.StateCancelRequested || committed.NextRunAt != nil ||
		committed.LastErrorCode != string(domain.ProblemCloudRequestRejected) ||
		committed.LastError != domain.ProblemText(domain.ProblemCloudRequestRejected) {
		t.Fatalf("cleanup rejection discarded intent: %+v", committed)
	}
}

func TestDeleteCancelsOnceThenDeletesDerivedLocalPath(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cancelCalls := 0
	cloud.cancelCopy = func(context.Context, string, string) error {
		cancelCalls++
		return nil
	}
	var deletedContent, deletedSave string
	files.delete = func(content, save string) error {
		deletedContent, deletedSave = content, save
		return errors.New("busy")
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)
	download := baseDownload(domain.StateDeleteRequested, now)
	download.CopySourcePath = "/cloud/payload/payload.mkv"
	download.DestinationName = "resolved.mkv"
	download.ContentPath = ""
	download.DeleteFilesRequested = true
	download.LastUpstreamStatus = domain.UpstreamCopyPending

	afterCancellation := step(t, scheduler, repo, download)
	afterDelete := step(t, scheduler, repo, afterCancellation)
	if cancelCalls != 1 {
		t.Fatalf("cancel copy calls = %d, want 1", cancelCalls)
	}
	if deletedContent != "/downloads/resolved.mkv" || deletedSave != "/downloads" {
		t.Fatalf("derived deletion path = %q under %q", deletedContent, deletedSave)
	}
	if afterDelete.State != domain.StateDeleteRequested || afterDelete.LastErrorCode != string(domain.ProblemLocalDeleteFailed) || afterDelete.NextRunAt != nil {
		t.Fatalf("failed local deletion lost cleanup intent: %+v", afterDelete)
	}
}

func TestRecordOfflineTrustsMetadataOnlyAfterFinish(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scheduler := &Scheduler{}
	download := baseDownload(domain.StateWaitingOffline, now)
	download.CloudResultPath = ""
	scheduler.recordOffline(&download, clouddrive.OfflineTask{
		Name: "untrusted", SourcePath: "/outside/untrusted", State: clouddrive.OfflineDownloading, Progress: 0.5,
	})
	if download.Name != "payload" || download.CloudTaskName != "" || download.CloudResultPath != "" {
		t.Fatalf("unfinished offline task overwrote durable identity: %+v", download)
	}
	scheduler.recordOffline(&download, clouddrive.OfflineTask{
		Name: "payload", SourcePath: "/cloud/payload", State: clouddrive.OfflineFinished, Progress: 1,
	})
	if download.CloudTaskName != "payload" || download.CloudResultPath != "/cloud/payload" {
		t.Fatalf("finished offline task evidence was not retained: %+v", download)
	}
}
func torrentDownload(now time.Time, multi bool, total int64) domain.Download {
	download := baseDownload(domain.StateSubmittingCopy, now)
	download.SourceKind = domain.SourceTorrent
	download.IsMultiFile = &multi
	download.TotalSize = total
	return download
}

func TestResolveCopySourceLayouts(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name       string
		download   domain.Download
		files      []domain.DownloadFile
		objects    map[string]clouddrive.Content
		want       string
		wantLayout bool
	}{
		{
			name:     "direct single file",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "payload.mkv", Size: 42,
			}},
			objects: map[string]clouddrive.Content{"/cloud/payload": {Path: "/cloud/payload", Kind: clouddrive.ContentFile, Size: 42}},
			want:    "/cloud/payload",
		},
		{
			name:     "directory wrapped single file",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "payload.mkv", Size: 42,
			}},
			objects: map[string]clouddrive.Content{
				"/cloud/payload":             {Path: "/cloud/payload", Kind: clouddrive.ContentDirectory},
				"/cloud/payload/payload.mkv": {Path: "/cloud/payload/payload.mkv", Kind: clouddrive.ContentFile, Size: 42},
			},
			want: "/cloud/payload/payload.mkv",
		},
		{
			name:     "single-entry multi-file directory",
			download: torrentDownload(now, true, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "episode.mkv", Size: 42,
			}},
			objects: map[string]clouddrive.Content{
				"/cloud/payload":             {Path: "/cloud/payload", Kind: clouddrive.ContentDirectory},
				"/cloud/payload/episode.mkv": {Path: "/cloud/payload/episode.mkv", Kind: clouddrive.ContentFile, Size: 42},
			},
			want: "/cloud/payload/episode.mkv",
		},
		{
			name:     "multi-file directory",
			download: torrentDownload(now, true, 84),
			files: []domain.DownloadFile{
				{DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "one.bin", Size: 42},
				{DownloadHash: "0123456789012345678901234567890123456789", Index: 1, RelativePath: "two.bin", Size: 42},
			},
			objects: map[string]clouddrive.Content{"/cloud/payload": {Path: "/cloud/payload", Kind: clouddrive.ContentDirectory}},
			want:    "/cloud/payload",
		},
		{
			name:     "single size mismatch",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "payload.mkv", Size: 42,
			}},
			objects:    map[string]clouddrive.Content{"/cloud/payload": {Path: "/cloud/payload", Kind: clouddrive.ContentFile, Size: 41}},
			wantLayout: true,
		},
		{
			name:     "single child wrong kind",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "payload.mkv", Size: 42,
			}},
			objects: map[string]clouddrive.Content{
				"/cloud/payload":             {Path: "/cloud/payload", Kind: clouddrive.ContentDirectory},
				"/cloud/payload/payload.mkv": {Path: "/cloud/payload/payload.mkv", Kind: clouddrive.ContentOther},
			},
			wantLayout: true,
		},
		{
			name:     "manifest path escape",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "../payload.mkv", Size: 42,
			}},
			wantLayout: true,
		},
		{
			name:     "control manifest path",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "bad\x01.mkv", Size: 42,
			}},
			wantLayout: true,
		},
		{
			name:     "invalid utf8 manifest path",
			download: torrentDownload(now, false, 42),
			files: []domain.DownloadFile{{
				DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: string([]byte{0xff}), Size: 42,
			}},
			wantLayout: true,
		},
		{
			name:     "duplicate manifest path",
			download: torrentDownload(now, true, 84),
			files: []domain.DownloadFile{
				{DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "same.bin", Size: 42},
				{DownloadHash: "0123456789012345678901234567890123456789", Index: 1, RelativePath: "same.bin", Size: 42},
			},
			wantLayout: true,
		},
		{
			name:     "magnet opaque result",
			download: baseDownload(domain.StateSubmittingCopy, now),
			want:     "/cloud/payload",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{files: test.files}
			cloud, files := defaults()
			cloud.inspectContent = func(_ context.Context, source string) (clouddrive.Content, error) {
				if content, ok := test.objects[source]; ok {
					return content, nil
				}
				return clouddrive.Content{}, errors.New("unexpected content path")
			}
			scheduler := testScheduler(t, &fakeClock{now: now}, repo, cloud, files)
			got, err := scheduler.resolveCopySource(context.Background(), test.download)
			if test.wantLayout {
				if !errors.Is(err, errCloudContentLayout) {
					t.Fatalf("resolveCopySource() error = %v, want layout error", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveCopySource() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}
func TestCopyMutationWaitsForDurableResolutionAndReservation(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{files: []domain.DownloadFile{{
		DownloadHash: "0123456789012345678901234567890123456789", Index: 0, RelativePath: "payload", Size: 42,
	}}}
	cloud, files := defaults()
	inspectCalls, copyCalls := 0, 0
	cloud.inspectContent = func(_ context.Context, source string) (clouddrive.Content, error) {
		inspectCalls++
		return clouddrive.Content{Path: source, Kind: clouddrive.ContentFile, Size: 42}, nil
	}
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		copyCalls++
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyPending}, nil
	}
	download := torrentDownload(now, false, 42)
	scheduler := testScheduler(t, clock, repo, cloud, files)
	download = step(t, scheduler, repo, download)
	if inspectCalls != 1 || copyCalls != 0 || download.CopySourcePath != "/cloud/payload" {
		t.Fatalf("source resolution = %+v, inspect=%d copy=%d", download, inspectCalls, copyCalls)
	}
	download = step(t, scheduler, repo, download)
	if copyCalls != 0 || download.DestinationName != "payload" {
		t.Fatalf("destination reservation = %+v, copy=%d", download, copyCalls)
	}
	download = step(t, scheduler, repo, download)
	if copyCalls != 0 || download.LastUpstreamStatus != destinationClear {
		t.Fatalf("preflight reservation = %+v, copy=%d", download, copyCalls)
	}
	download = step(t, scheduler, repo, download)
	if copyCalls != 1 || download.State != domain.StateWaitingCopy {
		t.Fatalf("copy submission = %+v, copy=%d", download, copyCalls)
	}
}

func TestRetryReResolvesHistoricalSharedDirectoryToTorrentFile(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	hash := "0123456789012345678901234567890123456789"
	repo := &fakeRepository{files: []domain.DownloadFile{{
		DownloadHash: hash,
		Index:        0,
		RelativePath: "episode-15.mkv",
		Size:         42,
	}}}
	cloud, files := defaults()
	cloud.inspectContent = func(_ context.Context, source string) (clouddrive.Content, error) {
		switch source {
		case "/cloud/shared-season":
			return clouddrive.Content{Path: source, Kind: clouddrive.ContentDirectory}, nil
		case "/cloud/shared-season/episode-15.mkv":
			return clouddrive.Content{Path: source, Kind: clouddrive.ContentFile, Size: 42}, nil
		default:
			return clouddrive.Content{}, errors.New("unexpected content path")
		}
	}
	var copied clouddrive.CopySpec
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		copied = spec
		return clouddrive.CopyTask{
			SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyPending,
		}, nil
	}
	multiFile := true
	download := domain.Download{
		Hash: hash, Name: "Episode 15", SourceKind: domain.SourceTorrent,
		SubmissionURI: "magnet:?xt=urn:btih:" + hash,
		CloudFolder:   "/cloud", SavePath: "/downloads/anime/show/Season 2",
		CloudResultPath: "/cloud/shared-season", IsMultiFile: &multiFile, TotalSize: 42,
		State: domain.StateSubmittingCopy, LastUpstreamStatus: domain.UpstreamOfflineFinished,
		PhaseStartedAt: now,
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)

	download = step(t, scheduler, repo, download)
	if download.CopySourcePath != "/cloud/shared-season/episode-15.mkv" || download.DestinationName != "" {
		t.Fatalf("resolved copy source = %+v", download)
	}
	download = step(t, scheduler, repo, download)
	if download.DestinationName != "episode-15.mkv" {
		t.Fatalf("reserved destination = %+v", download)
	}
	download = step(t, scheduler, repo, download)
	if download.LastUpstreamStatus != destinationClear {
		t.Fatalf("preflight status = %q", download.LastUpstreamStatus)
	}
	download = step(t, scheduler, repo, download)
	if copied.SourcePath != "/cloud/shared-season/episode-15.mkv" ||
		copied.DestinationPath != "/downloads/anime/show/Season 2" ||
		download.State != domain.StateWaitingCopy {
		t.Fatalf("copy submission = (%+v, %+v)", copied, download)
	}
}

func TestDestinationConflictRetryPersistsResolutionAndCopy(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repository, err := store.Open(ctx, t.TempDir()+"/store.db")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	hash := "0123456789abcdef0123456789abcdef01234567"
	multiFile := true
	nextRun := now
	submission := domain.Submission{
		Download: domain.Download{
			Hash: hash, Name: "Episode 15", SourceKind: domain.SourceTorrent,
			SubmissionURI: "magnet:?xt=urn:btih:" + hash,
			CloudFolder:   "/cloud", SavePath: "/downloads/anime/show/Season 2",
			IsMultiFile: &multiFile, TotalSize: 42, State: domain.StateAccepted,
			PhaseStartedAt: now, NextRunAt: &nextRun, CreatedAt: now, UpdatedAt: now,
		},
		Files: []domain.DownloadFile{{
			DownloadHash: hash, Index: 0, RelativePath: "episode-15.mkv", Size: 42,
		}},
	}
	if _, inserted, err := repository.CreateSubmission(ctx, submission); err != nil || !inserted {
		t.Fatalf("CreateSubmission(): inserted=%t err=%v", inserted, err)
	}
	claim, err := repository.ClaimDue(ctx, "seed", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(seed) = (%+v, %v)", claim, err)
	}
	failed := claim.Download
	failed.State = domain.StateFailed
	failed.CloudResultPath = "/cloud/shared-season"
	failed.CopySourcePath = failed.CloudResultPath
	failed.DestinationName = "shared-season"
	failed.LastUpstreamStatus = domain.UpstreamCopyPending
	failed.LastError = domain.ProblemText(domain.ProblemDestinationConflict)
	failed.LastErrorCode = string(domain.ProblemDestinationConflict)
	failed.NextRunAt = nil
	failed.UpdatedAt = now.Add(time.Minute)
	if err := repository.CommitClaim(ctx, *claim, failed); err != nil {
		t.Fatalf("CommitClaim(failed): %v", err)
	}
	if err := repository.Retry(ctx, hash, domain.StateSubmittingCopy, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("Retry(): %v", err)
	}

	cloud, files := defaults()
	cloud.inspectContent = func(_ context.Context, source string) (clouddrive.Content, error) {
		switch source {
		case "/cloud/shared-season":
			return clouddrive.Content{Path: source, Kind: clouddrive.ContentDirectory}, nil
		case "/cloud/shared-season/episode-15.mkv":
			return clouddrive.Content{Path: source, Kind: clouddrive.ContentFile, Size: 42}, nil
		default:
			return clouddrive.Content{}, errors.New("unexpected content path")
		}
	}
	var copied clouddrive.CopySpec
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		copied = spec
		return clouddrive.CopyTask{
			SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath,
			State: clouddrive.CopyPending,
		}, nil
	}
	cloud.inspectCopy = func(_ context.Context, source, destination string) (clouddrive.CopyTask, bool, error) {
		return clouddrive.CopyTask{
			SourcePath: source, DestinationPath: destination, State: clouddrive.CopyCompleted, Progress: 1,
		}, true, nil
	}
	files.size = 42
	files.verify = func(save string, expected fsafe.ExpectedContent) (string, error) {
		if len(expected.Files) == 0 {
			return "", fs.ErrNotExist
		}
		if expected.MultiFile || expected.CandidateName != "episode-15.mkv" ||
			len(expected.Files) != 1 || expected.Files[0].RelativePath != "episode-15.mkv" || expected.Files[0].Size != 42 {
			return "", errors.New("unexpected local verification layout")
		}
		return filepath.Join(save, expected.CandidateName), nil
	}
	clock := &fakeClock{now: now.Add(2 * time.Minute)}
	scheduler, err := New(
		Config{
			Owner: "worker", LeaseDuration: time.Minute, PollInterval: 10 * time.Second,
			OfflineTimeout: time.Hour, CopyTimeout: time.Hour, VerifyTimeout: time.Hour, WorkerCount: 1,
		},
		repository, cloud, files, clock,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	for step := range 6 {
		claimed, err := scheduler.Step(ctx)
		if err != nil || !claimed {
			t.Fatalf("Step(%d) = (%t, %v)", step, claimed, err)
		}
		clock.now = clock.now.Add(10 * time.Second)
	}
	stored, err := repository.GetDownload(ctx, hash)
	if err != nil {
		t.Fatalf("GetDownload(): %v", err)
	}
	if stored.State != domain.StateCompleted ||
		stored.CopySourcePath != "/cloud/shared-season/episode-15.mkv" ||
		stored.DestinationName != "episode-15.mkv" ||
		stored.ContentPath != "/downloads/anime/show/Season 2/episode-15.mkv" ||
		copied.SourcePath != stored.CopySourcePath || copied.DestinationPath != stored.SavePath {
		t.Fatalf("persisted copy workflow = (%+v, %+v)", stored, copied)
	}
}

func TestRetryAfterLocalVerificationFailureRemovesStaleCopy(t *testing.T) {
	now := time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC)
	hash := "febc97e973498b93f1aa7c0767d730044917dea9"
	sourceName := "[Dynamis One] Grand Blue Season 3 - 05 (ABEMA 1920x1080 AVC AAC MKV) [0A0DE095].mkv"
	cloudResult := "/115open/云下载/ani-rss/" + sourceName
	copySource := cloudResult + "/" + sourceName
	savePath := "/downloads/anime/碧蓝之海/Season 3"
	destinationName := sourceName
	repo := &fakeRepository{files: []domain.DownloadFile{{
		DownloadHash: hash, Index: 0, RelativePath: sourceName, Size: 42,
	}}}
	cloud, files := defaults()
	ensureCalls, cancelCalls, verifyCalls := 0, 0, 0
	deleted := false
	cloud.cancelCopy = func(context.Context, string, string) error {
		cancelCalls++
		return nil
	}
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		ensureCalls++
		return clouddrive.CopyTask{
			SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath,
			State: clouddrive.CopyCompleted, Progress: 1,
		}, nil
	}
	files.verify = func(save string, expected fsafe.ExpectedContent) (string, error) {
		verifyCalls++
		if !deleted {
			return "", errors.New("fsafe: single-file manifest does not match candidate")
		}
		if verifyCalls == 1 {
			return "", fs.ErrNotExist
		}
		return filepath.Join(save, expected.CandidateName), nil
	}
	files.delete = func(content, save string) error {
		if content != filepath.Join(savePath, destinationName) || save != savePath {
			t.Fatalf("Delete(%q, %q), want stale candidate", content, save)
		}
		deleted = true
		return nil
	}
	download := domain.Download{
		Hash: hash, Name: "[Kirara Fantasia] 碧蓝之海 S03E05", SourceKind: domain.SourceTorrent,
		CloudFolder: "/115open/云下载/ani-rss", SavePath: savePath,
		CloudResultPath: cloudResult, CopySourcePath: copySource, DestinationName: destinationName,
		IsMultiFile: new(bool), TotalSize: 42, State: domain.StateSubmittingCopy,
		LastUpstreamStatus: domain.UpstreamCopyCompleted, PhaseStartedAt: now,
	}
	scheduler := testScheduler(t, &fakeClock{now: now}, repo, cloud, files)
	for index := range 5 {
		download = step(t, scheduler, repo, download)
		if index < 4 {
			download.NextRunAt = nil
		}
	}
	if !deleted || cancelCalls != 1 || ensureCalls != 1 || verifyCalls != 2 || download.State != domain.StateCompleted {
		t.Fatalf("retry recovery = deleted:%t cancel:%d ensure:%d verify:%d download:%+v", deleted, cancelCalls, ensureCalls, verifyCalls, download)
	}
}

func TestRecordOfflineFillsUnknownTotalSizeAndPreservesKnown(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scheduler := &Scheduler{}

	unknown := baseDownload(domain.StateWaitingOffline, now)
	scheduler.recordOffline(&unknown, clouddrive.OfflineTask{
		Name: "payload", SourcePath: "/cloud/payload", State: clouddrive.OfflineDownloading, Progress: 0.5, Size: 4096,
	})
	if unknown.TotalSize != 4096 || unknown.OfflineProgress != 0.5 || unknown.QbitProgress != 0.45 {
		t.Fatalf("unknown durable size not filled with progress preserved: %+v", unknown)
	}

	known := baseDownload(domain.StateWaitingOffline, now)
	known.TotalSize = 2048
	scheduler.recordOffline(&known, clouddrive.OfflineTask{
		Name: "payload", SourcePath: "/cloud/payload", State: clouddrive.OfflineDownloading, Progress: 0.5, Size: 4096,
	})
	if known.TotalSize != 2048 {
		t.Fatalf("known durable size was overwritten: %+v", known)
	}

	zeroTask := baseDownload(domain.StateWaitingOffline, now)
	scheduler.recordOffline(&zeroTask, clouddrive.OfflineTask{
		Name: "payload", SourcePath: "/cloud/payload", State: clouddrive.OfflineDownloading, Progress: 0.5, Size: 0,
	})
	if zeroTask.TotalSize != 0 {
		t.Fatalf("zero offline size was treated as known: %+v", zeroTask)
	}
}
func TestRecordOfflineDoesNotOverwriteTorrentManifestMetadata(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scheduler := &Scheduler{}
	download := baseDownload(domain.StateWaitingOffline, now)
	download.SourceKind = domain.SourceTorrent
	multi := false
	download.IsMultiFile = &multi
	download.TotalSize = 3
	scheduler.recordOffline(&download, clouddrive.OfflineTask{
		Name: "offline-name", SourcePath: "/cloud/offline-name", State: clouddrive.OfflineFinished, Progress: 1, Size: 4096,
	})
	if download.Name != "payload" || download.TotalSize != 3 || download.CloudResultPath != "/cloud/offline-name" {
		t.Fatalf("torrent metadata changed from offline result: %+v", download)
	}
}

func TestStepWorkflowAndMissingCopyRecovery(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.inspectOffline = func(_ context.Context, _ string, hash string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{Name: "payload", InfoHash: hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineFinished, Progress: 1}, true, nil
	}
	cloud.inspectCopy = func(context.Context, string, string) (clouddrive.CopyTask, bool, error) {
		return clouddrive.CopyTask{}, false, nil
	}
	unknownTypeCalls := 0
	files.verifyUnknownType = func(save, name string) (fsafe.UnknownContent, error) {
		unknownTypeCalls++
		if unknownTypeCalls == 1 {
			// Pre-copy collision detection: the destination is clear.
			return fsafe.UnknownContent{}, fs.ErrNotExist
		}
		// The staged tree is what counts for a magnet: kind, path, and size.
		return fsafe.UnknownContent{Path: "/downloads/payload", Size: 4096, MultiFile: false}, nil
	}
	files.size = 4096
	files.verify = func(string, fsafe.ExpectedContent) (string, error) {
		return "/downloads/payload", nil
	}
	s := testScheduler(t, clock, repo, cloud, files)

	d := step(t, s, repo, baseDownload(domain.StateAccepted, now))
	if d.State != domain.StateSubmittingOffline || d.NextRunAt == nil || !d.NextRunAt.Equal(now) {
		t.Fatalf("accepted transition = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateWaitingOffline {
		t.Fatalf("offline submit state = %s", d.State)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateSubmittingCopy || d.Name != "payload" || d.CloudResultPath != "/cloud/payload" {
		t.Fatalf("finished offline result = %#v", d)
	}
	// A magnet carries no file metadata, so no FindFile lookup happens and
	// IsMultiFile stays unknown until the verified local copy decides.
	if d.IsMultiFile != nil {
		t.Fatalf("magnet metadata was invented before local verification: %#v", d)
	}
	d = step(t, s, repo, d)
	if d.CopySourcePath != "/cloud/payload" || d.DestinationName != "" {
		t.Fatalf("copy source resolution = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.DestinationName != "payload" || d.LastUpstreamStatus != "offline:FINISHED" {
		t.Fatalf("destination reservation = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.LastUpstreamStatus != destinationClear {
		t.Fatalf("preflight status = %q", d.LastUpstreamStatus)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateWaitingCopy {
		t.Fatalf("copy submit state = %s", d.State)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateVerifyingLocal || d.IsMultiFile == nil || *d.IsMultiFile ||
		d.ContentPath != "/downloads/payload" || d.TotalSize != 4096 {
		t.Fatalf("missing task recovery = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateCompleted || d.CompletedAt == nil || d.NextRunAt != nil || d.TotalSize != 4096 {
		t.Fatalf("completion = %#v", d)
	}
}

func TestStepFailuresBackoffTimeoutCollisionAndCAS(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.ensureOffline = func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{}, &clouddrive.Error{Operation: "add_offline", Kind: clouddrive.ErrorTemporary}
	}
	s := testScheduler(t, clock, repo, cloud, files)
	d := step(t, s, repo, baseDownload(domain.StateSubmittingOffline, now))
	if d.State != domain.StateSubmittingOffline || d.AttemptCount != 1 || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(30*time.Second)) ||
		d.LastErrorCode != string(domain.ProblemCloudUnreachable) || d.LastError != domain.ProblemText(domain.ProblemCloudUnreachable) {
		t.Fatalf("transient backoff = %#v", d)
	}
	d.PhaseStartedAt = now.Add(-time.Hour)
	d = step(t, s, repo, d)
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemOfflineTimeout) {
		t.Fatalf("timeout = %#v", d)
	}

	multi := false
	collision := baseDownload(domain.StateSubmittingCopy, now)
	files.verifyUnknownType = func(string, string) (fsafe.UnknownContent, error) {
		return fsafe.UnknownContent{Path: "/downloads/payload", Size: 4096, MultiFile: false}, nil
	}
	collision.IsMultiFile, collision.LastUpstreamStatus = new(multi), "offline:FINISHED"
	collision.CopySourcePath = collision.CloudResultPath
	collision.DestinationName = collision.Name
	d = step(t, s, repo, collision)
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemDestinationCollision) {
		t.Fatalf("collision = %#v", d)
	}

	repo.commitErr = store.ErrClaimLost
	accepted := baseDownload(domain.StateAccepted, now)
	repo.claim = &store.Claim{Download: accepted}
	claimed, err := s.Step(context.Background())
	if !claimed || err != nil {
		t.Fatalf("CAS loss result = %v, %v", claimed, err)
	}
}

func TestSubmittingCopyTimeoutPrecedesSourceResolution(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	inspectCalls := 0
	cloud.inspectContent = func(context.Context, string) (clouddrive.Content, error) {
		inspectCalls++
		return clouddrive.Content{}, errors.New("must not inspect after deadline")
	}
	s := testScheduler(t, clock, repo, cloud, files)
	s.config.CopyTimeout = time.Minute
	d := baseDownload(domain.StateSubmittingCopy, now)
	d.PhaseStartedAt = now.Add(-time.Minute)
	d.LastErrorCode = string(domain.ProblemCloudCopyNotReady)
	got := step(t, s, repo, d)
	if got.State != domain.StateFailed || got.LastErrorCode != string(domain.ProblemCloudCopyNotReadyTimeout) || inspectCalls != 0 {
		t.Fatalf("copy timeout resolution = %#v, inspect calls=%d", got, inspectCalls)
	}
}

func TestManifestRepositoryFailureDoesNotBecomeLocalVerificationFailure(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{filesErr: errors.New("database unavailable")}
	cloud, files := defaults()
	multi := true
	d := baseDownload(domain.StateVerifyingLocal, now)
	d.SourceKind = domain.SourceTorrent
	d.IsMultiFile = &multi
	d.CloudTaskName = d.Name
	d.CloudResultPath = "/cloud/payload"
	d.CopySourcePath = d.CloudResultPath
	d.DestinationName = d.Name
	s := testScheduler(t, clock, repo, cloud, files)
	repo.claim = &store.Claim{Download: d, Owner: "worker", State: d.State, Version: d.RowVersion}
	claimed, err := s.Step(context.Background())
	if !claimed || !errors.Is(err, repo.filesErr) || len(repo.commits) != 0 {
		t.Fatalf("manifest repository failure = claimed:%t err:%v commits:%d", claimed, err, len(repo.commits))
	}
}

func TestCancelDeleteAndCancellationDoNotResumeWorkflow(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cancelled := 0
	cloud.cancelCopy = func(context.Context, string, string) error { cancelled++; return nil }
	deleted := 0
	files.delete = func(content, save string) error {
		deleted++
		if content != "/downloads/payload" || save != "/downloads" {
			t.Fatal("wrong delete roots")
		}
		return nil
	}
	s := testScheduler(t, clock, repo, cloud, files)
	d := baseDownload(domain.StateCancelRequested, now)
	d.CopySourcePath = "/cloud/payload"
	d = step(t, s, repo, d)
	if d.State != domain.StateCancelled || cancelled != 1 || d.NextRunAt != nil {
		t.Fatalf("cancel = %#v", d)
	}
	d.State = domain.StateDeleteRequested
	d.NextRunAt = new(now)
	cloud.cancelCopy = func(context.Context, string, string) error {
		t.Fatal("delete repeated a previously successful cancellation")
		return errors.New("unreachable")
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateDeleted {
		t.Fatalf("delete after cancellation = %#v", d)
	}
	d = baseDownload(domain.StateDeleteRequested, now)
	d.ContentPath, d.CompletedAt, d.DeleteFilesRequested = "/downloads/payload", new(now), true
	d = step(t, s, repo, d)
	if d.State != domain.StateDeleted || deleted != 1 || d.RemovedAt == nil {
		t.Fatalf("delete = %#v", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo.claim = &store.Claim{Download: baseDownload(domain.StateSubmittingOffline, now)}
	claimed, err := s.Step(ctx)
	if !claimed || !errors.Is(err, context.Canceled) || len(repo.commits) != 3 {
		t.Fatalf("cancelled parent committed or returned wrong result: %v, %v", claimed, err)
	}
}

func TestPauseStopsUpstreamWorkAndRetainsResumeEvidence(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	t.Run("offline", func(t *testing.T) {
		clock := &fakeClock{now: now}
		repo := &fakeRepository{}
		cloud, files := defaults()
		cancelled := 0
		cloud.cancelOffline = func(context.Context, string, string) error { cancelled++; return nil }
		s := testScheduler(t, clock, repo, cloud, files)
		d := baseDownload(domain.StateCancelRequested, now)
		d.CloudResultPath = ""
		d.PauseRequested = true
		d.OfflineProgress, d.QbitProgress = 0.4, 0.16
		d = step(t, s, repo, d)
		if d.State != domain.StateStopped || !d.PauseRequested || cancelled != 1 ||
			d.OfflineProgress != 0 || d.QbitProgress != 0 || d.LastUpstreamStatus != "" || d.NextRunAt != nil {
			t.Fatalf("offline pause = %#v", d)
		}
	})

	t.Run("copy", func(t *testing.T) {
		clock := &fakeClock{now: now}
		repo := &fakeRepository{}
		cloud, files := defaults()
		cancelled, deleted := 0, 0
		cloud.cancelCopy = func(context.Context, string, string) error { cancelled++; return nil }
		files.delete = func(content, save string) error {
			deleted++
			if content != "/downloads/payload" || save != "/downloads" {
				t.Fatalf("pause delete roots = %q, %q", content, save)
			}
			return nil
		}
		s := testScheduler(t, clock, repo, cloud, files)
		d := baseDownload(domain.StateCancelRequested, now)
		d.CopySourcePath = "/cloud/payload"
		d.DestinationName = "payload"
		d.PauseRequested = true
		d.LastUpstreamStatus = domain.UpstreamCopyScanning
		d.CopyProgress, d.QbitProgress = 0.5, 0.95
		d = step(t, s, repo, d)
		if d.State != domain.StateStopped || cancelled != 1 || deleted != 1 ||
			d.LastUpstreamStatus != domain.UpstreamOfflineFinished || d.CopyProgress != 0 || d.QbitProgress != 0.9 {
			t.Fatalf("copy pause = %#v", d)
		}
	})

	t.Run("copy before reservation", func(t *testing.T) {
		clock := &fakeClock{now: now}
		repo := &fakeRepository{}
		cloud, files := defaults()
		cancelled, deleted := 0, 0
		cloud.cancelCopy = func(context.Context, string, string) error { cancelled++; return nil }
		files.delete = func(string, string) error { deleted++; return nil }
		s := testScheduler(t, clock, repo, cloud, files)
		d := baseDownload(domain.StateCancelRequested, now)
		d.CopySourcePath = "/cloud/resolved.mkv"
		d.DestinationName = ""
		d.PauseRequested = true
		d.LastUpstreamStatus = domain.UpstreamCopyScanning
		d = step(t, s, repo, d)
		if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemInternalWorkflowError) || cancelled != 1 || deleted != 0 {
			t.Fatalf("pre-reservation pause guessed local path: %#v, cancel=%d delete=%d", d, cancelled, deleted)
		}
	})

	t.Run("completed copy", func(t *testing.T) {
		clock := &fakeClock{now: now}
		repo := &fakeRepository{}
		cloud, files := defaults()
		cloud.cancelCopy = func(context.Context, string, string) error {
			t.Fatal("completed copy was cancelled")
			return nil
		}
		files.delete = func(string, string) error {
			t.Fatal("completed copy was deleted")
			return nil
		}
		s := testScheduler(t, clock, repo, cloud, files)
		d := baseDownload(domain.StateCancelRequested, now)
		d.PauseRequested = true
		d.LastUpstreamStatus = domain.UpstreamCopyCompleted
		d = step(t, s, repo, d)
		if d.State != domain.StateStopped || d.LastUpstreamStatus != domain.UpstreamCopyCompleted {
			t.Fatalf("completed copy pause = %#v", d)
		}
	})
}

func TestWakeCoalescesAndWaitUsesTimerOrWake(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	s := testScheduler(t, clock, repo, cloud, files)
	s.Wake()
	s.Wake()
	if len(s.wake) != 1 {
		t.Fatalf("wake was not coalesced: %d", len(s.wake))
	}
	done := make(chan struct{})
	go func() { s.wait(context.Background(), 7*time.Minute); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wake did not release timer wait")
	}
	durations := clock.timerDurations()
	if len(durations) != 1 || durations[0] != 7*time.Minute {
		t.Fatalf("timer duration = %v", durations)
	}
}

func TestEnsureTerminalTasksAdvanceImmediately(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name      string
		download  domain.Download
		configure func(*fakeCloud)
		wantState domain.State
		wantCode  string
	}{
		{
			name:     "offline finished",
			download: baseDownload(domain.StateSubmittingOffline, now),
			configure: func(cloud *fakeCloud) {
				cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
					return clouddrive.OfflineTask{Name: "payload", InfoHash: spec.Hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineFinished, Progress: 1}, nil
				}
			},
			wantState: domain.StateSubmittingCopy,
		},
		{
			name:     "offline error",
			download: baseDownload(domain.StateSubmittingOffline, now),
			configure: func(cloud *fakeCloud) {
				cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
					return clouddrive.OfflineTask{Name: "payload", InfoHash: spec.Hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineError}, nil
				}
			},
			wantState: domain.StateFailed,
			wantCode:  string(domain.ProblemOfflineDownloadFailed),
		},
		{
			name:     "copy completed",
			download: copySubmission(now),
			configure: func(cloud *fakeCloud) {
				cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
					return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyCompleted, Progress: 1}, nil
				}
			},
			wantState: domain.StateVerifyingLocal,
		},
		{
			name:     "copy failed",
			download: copySubmission(now),
			configure: func(cloud *fakeCloud) {
				cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
					return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyFailed}, nil
				}
			},
			wantState: domain.StateFailed,
			wantCode:  string(domain.ProblemCopyTaskFailed),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{}
			cloud, files := defaults()
			test.configure(cloud)
			got := step(t, testScheduler(t, &fakeClock{now: now}, repo, cloud, files), repo, test.download)
			if got.State != test.wantState || got.LastErrorCode != test.wantCode {
				t.Fatalf("terminal ensure result = %+v", got)
			}
		})
	}
}

func TestRetryRemovesFailedUpstreamTaskBeforeResubmission(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	offlineCancels, offlineSubmits := 0, 0
	cloud.cancelOffline = func(context.Context, string, string) error { offlineCancels++; return nil }
	cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		offlineSubmits++
		return clouddrive.OfflineTask{InfoHash: spec.Hash, State: clouddrive.OfflineInit}, nil
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)

	offline := baseDownload(domain.StateSubmittingOffline, now)
	offline.LastUpstreamStatus = domain.UpstreamOfflineError
	offline = step(t, scheduler, repo, offline)
	if offlineCancels != 1 || offlineSubmits != 0 || offline.LastUpstreamStatus != "" || offline.State != domain.StateSubmittingOffline {
		t.Fatalf("offline retry cleanup = %+v, cancels=%d submits=%d", offline, offlineCancels, offlineSubmits)
	}
	offline = step(t, scheduler, repo, offline)
	if offlineSubmits != 1 || offline.State != domain.StateWaitingOffline {
		t.Fatalf("offline retry resubmit = %+v, submits=%d", offline, offlineSubmits)
	}

	copyCancels, copySubmits := 0, 0
	cloud.cancelCopy = func(context.Context, string, string) error { copyCancels++; return nil }
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		copySubmits++
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyPending}, nil
	}
	copyDeletes := 0
	files.delete = func(content, save string) error {
		copyDeletes++
		if content != "/downloads/payload" || save != "/downloads" {
			t.Fatalf("copy retry delete = %q under %q", content, save)
		}
		return nil
	}
	copyRow := copySubmission(now)
	copyRow.LastUpstreamStatus = domain.UpstreamCopyFailed
	copyRow = step(t, scheduler, repo, copyRow)
	if copyCancels != 1 || copySubmits != 0 ||
		copyRow.LastUpstreamStatus != domain.UpstreamCleanupCancelled+"|"+domain.UpstreamCopyFailed {
		t.Fatalf("copy retry cancellation = %+v, cancels=%d submits=%d", copyRow, copyCancels, copySubmits)
	}
	copyRow = step(t, scheduler, repo, copyRow)
	if copyDeletes != 1 || copyRow.LastUpstreamStatus != domain.UpstreamOfflineFinished {
		t.Fatalf("copy retry partial cleanup = %+v, deletes=%d", copyRow, copyDeletes)
	}
	copyRow = step(t, scheduler, repo, copyRow)
	copyRow = step(t, scheduler, repo, copyRow)
	if copySubmits != 1 || copyRow.State != domain.StateWaitingCopy {
		t.Fatalf("copy retry resubmit = %+v, submits=%d", copyRow, copySubmits)
	}
}

func TestDestinationReservationConflictFailsWithoutCopy(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &fakeRepository{commitErrs: []error{store.ErrDestinationConflict, nil}}
	cloud, files := defaults()
	copyCalls := 0
	cloud.ensureCopy = func(context.Context, clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		copyCalls++
		return clouddrive.CopyTask{}, nil
	}
	download := copySubmission(now)
	download.DestinationName = ""
	got := step(t, testScheduler(t, &fakeClock{now: now}, repo, cloud, files), repo, download)
	if copyCalls != 0 || got.State != domain.StateFailed || got.DestinationName != "" ||
		got.LastErrorCode != string(domain.ProblemDestinationConflict) || len(repo.commits) != 2 {
		t.Fatalf("destination conflict result = %+v, copy calls=%d commits=%d", got, copyCalls, len(repo.commits))
	}
}

func copySubmission(now time.Time) domain.Download {
	multi := false
	download := baseDownload(domain.StateSubmittingCopy, now)
	download.IsMultiFile = &multi
	download.CopySourcePath = download.CloudResultPath
	download.DestinationName = download.Name
	download.LastUpstreamStatus = destinationClear
	return download
}

// magnetCopySubmission is a magnet row in SUBMITTING_COPY whose file-vs-folder
// kind is still unknown (IsMultiFile == nil) and whose destination has been
// reserved and preflighted, so the next step submits the copy.
func magnetCopySubmission(now time.Time) domain.Download {
	download := baseDownload(domain.StateSubmittingCopy, now)
	download.CopySourcePath = download.CloudResultPath
	download.DestinationName = download.Name
	download.LastUpstreamStatus = destinationClear
	return download
}

func TestMagnetCopySubmissionRetriesOnNotReady(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	ensureCalls := 0
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		ensureCalls++
		if ensureCalls == 1 {
			return clouddrive.CopyTask{}, &clouddrive.Error{Operation: "add_copy", Kind: clouddrive.ErrorNotFound}
		}
		return clouddrive.CopyTask{}, &clouddrive.Error{Operation: "add_copy", Kind: clouddrive.ErrorRejected}
	}
	s := testScheduler(t, clock, repo, cloud, files)

	d := step(t, s, repo, magnetCopySubmission(now))
	if d.State != domain.StateSubmittingCopy || d.LastErrorCode != string(domain.ProblemCloudCopyNotReady) ||
		d.LastError != domain.ProblemText(domain.ProblemCloudCopyNotReady) ||
		d.AttemptCount != 1 || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("not-found copy submission = %#v", d)
	}
	// A rejected submission keeps the same retry semantics.
	d = step(t, s, repo, d)
	if d.State != domain.StateSubmittingCopy || d.LastErrorCode != string(domain.ProblemCloudCopyNotReady) ||
		d.AttemptCount != 2 || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("rejected copy submission = %#v", d)
	}
}

func TestMagnetCopySubmissionRetriesThenSucceeds(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	ensureCalls := 0
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		ensureCalls++
		if ensureCalls == 1 {
			return clouddrive.CopyTask{}, &clouddrive.Error{Operation: "add_copy", Kind: clouddrive.ErrorNotFound}
		}
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyPending}, nil
	}
	s := testScheduler(t, clock, repo, cloud, files)

	d := step(t, s, repo, magnetCopySubmission(now))
	if d.State != domain.StateSubmittingCopy || d.LastErrorCode != string(domain.ProblemCloudCopyNotReady) {
		t.Fatalf("first submission = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.State != domain.StateWaitingCopy || d.LastErrorCode != "" || d.LastError != "" ||
		d.AttemptCount != 0 || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("successful submission did not clear the problem: %#v", d)
	}
}

func TestCopyPhaseDeadlineMapsLastProblem(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		code domain.ProblemCode
		want string
	}{
		{"not ready", domain.ProblemCloudCopyNotReady, string(domain.ProblemCloudCopyNotReadyTimeout)},
		{"unreachable", domain.ProblemCloudUnreachable, string(domain.ProblemCloudUnreachableTimeout)},
		{"authentication", domain.ProblemCloudAuthenticationRequired, string(domain.ProblemCloudAuthenticationTimeout)},
		{"no recognized problem", "", string(domain.ProblemCopyTimeout)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{}
			cloud, files := defaults()
			s := testScheduler(t, &fakeClock{now: now}, repo, cloud, files)
			d := magnetCopySubmission(now)
			d.LastErrorCode, d.LastError = string(test.code), domain.ProblemText(test.code)
			d.PhaseStartedAt = now.Add(-time.Hour)
			got := step(t, s, repo, d)
			if got.State != domain.StateFailed || got.LastErrorCode != test.want || got.NextRunAt != nil {
				t.Fatalf("deadline result = %#v, want code %s", got, test.want)
			}
		})
	}
}

func TestMagnetFinalVerificationPersistsTypeAndSize(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		content fsafe.UnknownContent
	}{
		{"single file", fsafe.UnknownContent{Path: "/downloads/payload", Size: 4096, MultiFile: false}},
		{"directory", fsafe.UnknownContent{Path: "/downloads/payload", Size: 8192, MultiFile: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &fakeClock{now: now}
			repo := &fakeRepository{}
			cloud, files := defaults()
			files.verifyUnknownType = func(save, name string) (fsafe.UnknownContent, error) {
				return test.content, nil
			}
			s := testScheduler(t, clock, repo, cloud, files)
			d := baseDownload(domain.StateVerifyingLocal, now)
			d.DestinationName = d.Name
			got := step(t, s, repo, d)
			if got.State != domain.StateCompleted || got.IsMultiFile == nil || *got.IsMultiFile != test.content.MultiFile ||
				got.ContentPath != test.content.Path || got.TotalSize != test.content.Size || got.NextRunAt != nil {
				t.Fatalf("magnet completion = %#v, want multi=%t size=%d", got, test.content.MultiFile, test.content.Size)
			}
		})
	}
}

func TestMagnetPreflightCollisionRegardlessOfType(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	files.verifyUnknownType = func(string, string) (fsafe.UnknownContent, error) {
		return fsafe.UnknownContent{Path: "/downloads/payload", Size: 1, MultiFile: false}, nil
	}
	s := testScheduler(t, clock, repo, cloud, files)

	d := baseDownload(domain.StateSubmittingCopy, now)
	d.CopySourcePath = d.CloudResultPath
	d = step(t, s, repo, d)
	if d.DestinationName != "payload" {
		t.Fatalf("destination reservation = %#v", d)
	}
	got := step(t, s, repo, d)
	if got.State != domain.StateFailed || got.LastErrorCode != string(domain.ProblemDestinationCollision) {
		t.Fatalf("magnet preflight collision = %#v", got)
	}
}

func TestCreateFolderRejectionIsNotSourceRejection(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.ensureOffline = func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{}, &clouddrive.Error{Operation: "create_folder", Kind: clouddrive.ErrorRejected}
	}
	s := testScheduler(t, clock, repo, cloud, files)
	d := step(t, s, repo, baseDownload(domain.StateSubmittingOffline, now))
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemCloudFolderUnavailable) || d.NextRunAt != nil {
		t.Fatalf("create-folder rejection = %#v, want folder guidance not source rejection", d)
	}
}

func TestPollStatesAndPermanentFailures(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	s := testScheduler(t, clock, repo, cloud, files)

	cloud.inspectOffline = func(_ context.Context, _ string, hash string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{Name: "payload", InfoHash: hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineDownloading, Progress: 0.5}, true, nil
	}
	d := step(t, s, repo, baseDownload(domain.StateWaitingOffline, now))
	if d.State != domain.StateWaitingOffline || d.OfflineProgress != 0.5 || d.QbitProgress != 0.45 || d.AttemptCount != 0 {
		t.Fatalf("offline poll = %#v", d)
	}

	cloud.inspectOffline = func(_ context.Context, _ string, hash string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{Name: "payload", InfoHash: hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineError}, true, nil
	}
	d = step(t, s, repo, baseDownload(domain.StateWaitingOffline, now))
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemOfflineDownloadFailed) {
		t.Fatalf("offline task error = %#v", d)
	}

	multi := false
	copyRow := baseDownload(domain.StateWaitingCopy, now)
	copyRow.IsMultiFile = new(multi)
	copyRow.CopySourcePath = copyRow.CloudResultPath
	copyRow.DestinationName = copyRow.Name
	cloud.inspectCopy = func(_ context.Context, source, destination string) (clouddrive.CopyTask, bool, error) {
		return clouddrive.CopyTask{SourcePath: source, DestinationPath: destination, State: clouddrive.CopyScanned, Progress: 0.5}, true, nil
	}
	d = step(t, s, repo, copyRow)
	if d.State != domain.StateWaitingCopy || d.CopyProgress != 0.5 || d.QbitProgress != 0.945 {
		t.Fatalf("copy poll = %#v", d)
	}
	cloud.inspectCopy = func(_ context.Context, source, destination string) (clouddrive.CopyTask, bool, error) {
		return clouddrive.CopyTask{SourcePath: source, DestinationPath: destination, State: clouddrive.CopyFailed}, true, nil
	}
	d = step(t, s, repo, copyRow)
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemCopyTaskFailed) {
		t.Fatalf("copy task error = %#v", d)
	}

	verifyRow := baseDownload(domain.StateVerifyingLocal, now)
	verifyRow.IsMultiFile = new(multi)
	verifyRow.DestinationName = verifyRow.Name
	verifyRow.CopySourcePath = verifyRow.CloudResultPath
	files.verify = func(string, fsafe.ExpectedContent) (string, error) {
		return "", errors.New("manifest verification should not run for magnet")
	}
	files.verifyUnknownType = func(string, string) (fsafe.UnknownContent, error) {
		return fsafe.UnknownContent{}, fs.ErrNotExist
	}
	d = step(t, s, repo, verifyRow)
	if d.State != domain.StateVerifyingLocal || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("magnet verify retry = %#v", d)
	}
	files.verifyUnknownType = func(string, string) (fsafe.UnknownContent, error) {
		return fsafe.UnknownContent{}, errors.New("unsafe symlink")
	}
	d = step(t, s, repo, verifyRow)
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemLocalVerificationFailed) {
		t.Fatalf("unsafe verification = %#v", d)
	}

	cloud.ensureOffline = func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{}, &clouddrive.Error{Operation: "add_offline", Kind: clouddrive.ErrorRejected}
	}
	d = step(t, s, repo, baseDownload(domain.StateSubmittingOffline, now))
	if d.State != domain.StateFailed || d.LastErrorCode != string(domain.ProblemOfflineSubmissionRejected) || d.NextRunAt != nil {
		t.Fatalf("rejected cloud failure = %#v", d)
	}
}

func TestBackoffDeleteRetentionAndNextDueTimer(t *testing.T) {
	if got := []time.Duration{backoff(1), backoff(2), backoff(3), backoff(4), backoff(5), backoff(6)}; got[0] != 30*time.Second || got[1] != time.Minute || got[2] != 2*time.Minute || got[3] != 4*time.Minute || got[4] != 5*time.Minute || got[5] != 5*time.Minute {
		t.Fatalf("backoff sequence = %v", got)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	files.delete = func(string, string) error { return errors.New("unsafe removal") }
	s := testScheduler(t, clock, repo, cloud, files)
	d := baseDownload(domain.StateDeleteRequested, now)
	d.ContentPath, d.DeleteFilesRequested = "/downloads/payload", true
	d = step(t, s, repo, d)
	if d.State != domain.StateDeleteRequested || d.LastErrorCode != string(domain.ProblemLocalDeleteFailed) || d.NextRunAt != nil || d.ContentPath == "" {
		t.Fatalf("deletion safety retention = %#v", d)
	}

	due := now.Add(3 * time.Minute)
	repo.next = new(due)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.worker(ctx); close(done) }()
	deadline := time.After(time.Second)
	var durations []time.Duration
	for len(durations) == 0 {
		select {
		case <-deadline:
			t.Fatal("worker did not schedule earliest due")
		default:
			time.Sleep(time.Millisecond)
		}
		durations = clock.timerDurations()
	}
	if durations[0] != 3*time.Minute {
		t.Fatalf("earliest due delay = %s", durations[0])
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on context cancellation")
	}
}
