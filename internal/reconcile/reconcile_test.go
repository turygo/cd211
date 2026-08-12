package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/clouddrive/pb"
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

type fakeCloud struct {
	find           func(context.Context, string) (*pb.CloudDriveFile, error)
	ensureOffline  func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error)
	inspectOffline func(context.Context, string, string) (clouddrive.OfflineTask, bool, error)
	cancelOffline  func(context.Context, string, string) error
	ensureCopy     func(context.Context, clouddrive.CopySpec) (clouddrive.CopyTask, error)
	inspectCopy    func(context.Context, string, string) (clouddrive.CopyTask, bool, error)
	cancelCopy     func(context.Context, string, string) error
}

func (c fakeCloud) FindFile(ctx context.Context, path string) (*pb.CloudDriveFile, error) {
	return c.find(ctx, path)
}
func (c fakeCloud) EnsureOffline(ctx context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
	return c.ensureOffline(ctx, spec)
}
func (c fakeCloud) InspectOffline(ctx context.Context, folder, hash string) (clouddrive.OfflineTask, bool, error) {
	return c.inspectOffline(ctx, folder, hash)
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
	verify func(string, fsafe.ExpectedContent) (string, error)
	size   int64
	delete func(string, string) error
}

func (f fakeFilesystem) Verify(save string, expected fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	path, err := f.verify(save, expected)
	return fsafe.VerifiedContent{Path: path, Size: f.size}, err
}
func (f fakeFilesystem) Delete(content, save string) error { return f.delete(content, save) }

func baseDownload(state domain.State, now time.Time) domain.Download {
	return domain.Download{Hash: "0123456789012345678901234567890123456789", Name: "payload", SubmissionURI: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789", CloudFolder: "/cloud", SavePath: "/downloads", CloudSourcePath: "/cloud/payload", State: state, PhaseStartedAt: now}
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
	download.LastError = "clouddrive ensure_offline: transient"
	download.AttemptCount = 2
	scheduler.log(download, "ensure_offline", now.Add(-time.Second), "committed")

	entry := output.String()
	for _, required := range []string{`"hash":"01234567"`, `"state":"SUBMITTING_OFFLINE"`, `"operation":"ensure_offline"`, `"attempt":2`, `"error":"clouddrive ensure_offline: transient"`} {
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
		find: func(context.Context, string) (*pb.CloudDriveFile, error) {
			return &pb.CloudDriveFile{Name: "payload", FileType: pb.CloudDriveFile_File}, nil
		},
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
		committed.NextRunAt == nil || committed.LastError != "reconciler operation timeout" {
		t.Fatalf("bounded operation result = calls:%d download:%+v", calls, committed)
	}
}

func TestPermanentCancellationFailureRetainsCleanupIntent(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.cancelCopy = func(context.Context, string, string) error {
		return &clouddrive.Error{Operation: "cancel_copy", Kind: clouddrive.ErrorPermanent}
	}
	scheduler := testScheduler(t, clock, repo, cloud, files)
	download := baseDownload(domain.StateCancelRequested, now)
	committed := step(t, scheduler, repo, download)
	if committed.State != domain.StateCancelRequested || committed.NextRunAt != nil ||
		committed.LastError != "clouddrive cancel_copy: permanent" {
		t.Fatalf("permanent cancellation failure discarded intent: %+v", committed)
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
	download.ContentPath = ""
	download.DeleteFilesRequested = true
	download.LastUpstreamStatus = domain.UpstreamCopyPending

	afterCancellation := step(t, scheduler, repo, download)
	afterDelete := step(t, scheduler, repo, afterCancellation)
	if cancelCalls != 1 {
		t.Fatalf("cancel copy calls = %d, want 1", cancelCalls)
	}
	if deletedContent != "/downloads/payload" || deletedSave != "/downloads" {
		t.Fatalf("derived deletion path = %q under %q", deletedContent, deletedSave)
	}
	if afterDelete.State != domain.StateDeleteRequested || afterDelete.LastError != "local deletion failed" || afterDelete.NextRunAt != nil {
		t.Fatalf("failed local deletion lost cleanup intent: %+v", afterDelete)
	}
}

func TestRecordOfflineTrustsMetadataOnlyAfterFinish(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scheduler := &Scheduler{}
	download := baseDownload(domain.StateWaitingOffline, now)
	download.CloudSourcePath = ""
	scheduler.recordOffline(&download, clouddrive.OfflineTask{
		Name: "untrusted", SourcePath: "/outside/untrusted", State: clouddrive.OfflineDownloading, Progress: 0.5,
	})
	if download.Name != "payload" || download.CloudTaskName != "" || download.CloudSourcePath != "" {
		t.Fatalf("unfinished offline task overwrote durable identity: %+v", download)
	}
	scheduler.recordOffline(&download, clouddrive.OfflineTask{
		Name: "payload", SourcePath: "/cloud/payload", State: clouddrive.OfflineFinished, Progress: 1,
	})
	if download.CloudTaskName != "payload" || download.CloudSourcePath != "/cloud/payload" {
		t.Fatalf("finished offline task evidence was not retained: %+v", download)
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

func TestStepWorkflowAndMissingCopyRecovery(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: now}
	repo := &fakeRepository{}
	cloud, files := defaults()
	cloud.inspectOffline = func(_ context.Context, _ string, hash string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{Name: "payload", InfoHash: hash, SourcePath: "/cloud/payload", State: clouddrive.OfflineFinished, Progress: 1}, true, nil
	}
	cloud.find = func(context.Context, string) (*pb.CloudDriveFile, error) {
		return &pb.CloudDriveFile{Name: "payload", FileType: pb.CloudDriveFile_File, Size: 42}, nil
	}
	cloud.inspectCopy = func(context.Context, string, string) (clouddrive.CopyTask, bool, error) {
		return clouddrive.CopyTask{}, false, nil
	}
	verifyCalls := 0
	// CloudDrive2 claimed 42 bytes above; the staged tree is what counts.
	files.size = 4096
	files.verify = func(string, fsafe.ExpectedContent) (string, error) {
		verifyCalls++
		if verifyCalls == 1 {
			return "", fs.ErrNotExist
		}
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
	if d.State != domain.StateSubmittingCopy || d.Name != "payload" || d.CloudSourcePath != "/cloud/payload" {
		t.Fatalf("finished offline result = %#v", d)
	}
	d = step(t, s, repo, d)
	if d.IsMultiFile == nil || *d.IsMultiFile || d.TotalSize != 42 {
		t.Fatalf("file metadata = %#v", d)
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
	if d.State != domain.StateVerifyingLocal || d.ContentPath != "/downloads/payload" || d.TotalSize != 4096 {
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
		return clouddrive.OfflineTask{}, &clouddrive.Error{Operation: "add_offline", Kind: clouddrive.ErrorTransient}
	}
	s := testScheduler(t, clock, repo, cloud, files)
	d := step(t, s, repo, baseDownload(domain.StateSubmittingOffline, now))
	if d.State != domain.StateSubmittingOffline || d.AttemptCount != 1 || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("transient backoff = %#v", d)
	}
	d.PhaseStartedAt = now.Add(-time.Hour)
	d = step(t, s, repo, d)
	if d.State != domain.StateFailed || d.LastError != "offline phase timeout" {
		t.Fatalf("timeout = %#v", d)
	}

	multi := false
	collision := baseDownload(domain.StateSubmittingCopy, now)
	collision.IsMultiFile, collision.LastUpstreamStatus = new(multi), "offline:FINISHED"
	collision.DestinationName = collision.Name
	files.verify = func(string, fsafe.ExpectedContent) (string, error) { return "/downloads/payload", nil }
	d = step(t, s, repo, collision)
	if d.State != domain.StateFailed || d.LastError != "destination collision" {
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
		d.CloudSourcePath = ""
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
		d.PauseRequested = true
		d.LastUpstreamStatus = domain.UpstreamCopyScanning
		d.CopyProgress, d.QbitProgress = 0.5, 0.95
		d = step(t, s, repo, d)
		if d.State != domain.StateStopped || cancelled != 1 || deleted != 1 ||
			d.LastUpstreamStatus != domain.UpstreamOfflineFinished || d.CopyProgress != 0 || d.QbitProgress != 0.9 {
			t.Fatalf("copy pause = %#v", d)
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
		wantError string
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
			wantError: "offline task error",
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
			wantError: "copy task failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{}
			cloud, files := defaults()
			test.configure(cloud)
			got := step(t, testScheduler(t, &fakeClock{now: now}, repo, cloud, files), repo, test.download)
			if got.State != test.wantState || got.LastError != test.wantError {
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
		got.LastError != "destination path conflicts with another download" || len(repo.commits) != 2 {
		t.Fatalf("destination conflict result = %+v, copy calls=%d commits=%d", got, copyCalls, len(repo.commits))
	}
}

func copySubmission(now time.Time) domain.Download {
	multi := false
	download := baseDownload(domain.StateSubmittingCopy, now)
	download.IsMultiFile = &multi
	download.DestinationName = download.Name
	download.LastUpstreamStatus = destinationClear
	return download
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
	if d.State != domain.StateFailed || d.LastError != "offline task error" {
		t.Fatalf("offline task error = %#v", d)
	}

	multi := false
	copyRow := baseDownload(domain.StateWaitingCopy, now)
	copyRow.IsMultiFile = new(multi)
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
	if d.State != domain.StateFailed || d.LastError != "copy task failed" {
		t.Fatalf("copy task error = %#v", d)
	}

	verifyRow := baseDownload(domain.StateVerifyingLocal, now)
	verifyRow.IsMultiFile = new(multi)
	verifyRow.DestinationName = verifyRow.Name
	files.verify = func(string, fsafe.ExpectedContent) (string, error) { return "", fs.ErrNotExist }
	d = step(t, s, repo, verifyRow)
	if d.State != domain.StateVerifyingLocal || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("verify retry = %#v", d)
	}
	files.verify = func(string, fsafe.ExpectedContent) (string, error) { return "", errors.New("unsafe symlink") }
	d = step(t, s, repo, verifyRow)
	if d.State != domain.StateFailed || d.LastError != "local verification failed" {
		t.Fatalf("unsafe verification = %#v", d)
	}

	cloud.ensureOffline = func(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{}, &clouddrive.Error{Operation: "add_offline", Kind: clouddrive.ErrorPermanent}
	}
	d = step(t, s, repo, baseDownload(domain.StateSubmittingOffline, now))
	if d.State != domain.StateFailed || d.LastError != "clouddrive add_offline: permanent" || d.NextRunAt != nil {
		t.Fatalf("permanent cloud failure = %#v", d)
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
	if d.State != domain.StateDeleteRequested || d.LastError != "local deletion failed" || d.NextRunAt != nil || d.ContentPath == "" {
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
