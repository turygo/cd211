package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
)

// recoveryHarness 使用真实 SQLite 和 fsafe；远端替身保留复制记录并写入真实文件。
type recoveryHarness struct {
	t         *testing.T
	database  string
	localRoot string
	repo      *store.Store
	files     *fsafe.Verifier
	cloud     *fakeCloud
	clock     *fakeClock
	scheduler *Scheduler
	tasks     map[clouddrive.CopySpec]clouddrive.CopyTask
	payload   []byte
	copies    int
	removeErr error
	staleCopy bool
}

func newRecoveryHarness(t *testing.T) *recoveryHarness {
	t.Helper()
	root := t.TempDir()
	h := &recoveryHarness{
		t: t, database: filepath.Join(root, "store.sqlite"), localRoot: filepath.Join(root, "downloads"),
		clock: &fakeClock{now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)},
		tasks: make(map[clouddrive.CopySpec]clouddrive.CopyTask), payload: []byte("verified media payload\n"),
	}
	if err := os.Mkdir(h.localRoot, 0o770); err != nil {
		t.Fatal(err)
	}
	var err error
	h.files, err = fsafe.New(h.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	h.cloud, _ = defaults()
	h.cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{Name: "payload.mkv", InfoHash: spec.Hash, SourcePath: "/cloud/payload.mkv", State: clouddrive.OfflineFinished, Progress: 1, Size: int64(len(h.payload))}, nil
	}
	h.cloud.inspectContent = func(_ context.Context, source string) (clouddrive.Content, error) {
		return clouddrive.Content{Path: source, Kind: clouddrive.ContentFile, Size: int64(len(h.payload))}, nil
	}
	h.cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		if task, found := h.tasks[spec]; found {
			return task, nil
		}
		h.copies++
		task := clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyCompleted, Progress: 1}
		h.tasks[spec] = task
		if !h.staleCopy {
			if err := os.WriteFile(filepath.Join(spec.DestinationPath, "payload.mkv"), h.payload, 0o660); err != nil {
				return clouddrive.CopyTask{}, err
			}
		}
		return task, nil
	}
	h.cloud.inspectCopy = func(_ context.Context, source, destination string) (clouddrive.CopyTask, bool, error) {
		task, found := h.tasks[clouddrive.CopySpec{SourcePath: source, DestinationPath: destination}]
		return task, found, nil
	}
	h.cloud.removeCopy = func(_ context.Context, source, destination string) error {
		if h.removeErr != nil {
			return h.removeErr
		}
		delete(h.tasks, clouddrive.CopySpec{SourcePath: source, DestinationPath: destination})
		return nil
	}
	h.reopen()
	t.Cleanup(func() {
		if err := h.repo.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

func (h *recoveryHarness) reopen() {
	h.t.Helper()
	if h.repo != nil {
		if err := h.repo.Close(); err != nil {
			h.t.Fatal(err)
		}
	}
	var err error
	h.repo, err = store.Open(context.Background(), h.database)
	if err != nil {
		h.t.Fatal(err)
	}
	h.scheduler = testScheduler(h.t, h.clock, h.repo, h.cloud, h.files)
	h.scheduler.config.VerifyTimeout = time.Minute
}

func (h *recoveryHarness) add(hash string) domain.Download {
	h.t.Helper()
	workspace, err := fsafe.WorkspacePath(h.localRoot, hash)
	if err != nil {
		h.t.Fatal(err)
	}
	now := h.clock.now
	d := domain.Download{
		Hash: hash, Name: "payload.mkv", SourceKind: domain.SourceTorrent, SubmissionURI: "magnet:?xt=urn:btih:" + hash,
		CloudFolder: "/cloud", SavePath: h.localRoot, WorkspacePath: workspace,
		IsMultiFile: new(bool), TotalSize: int64(len(h.payload)), State: domain.StateAccepted,
		PhaseStartedAt: now, NextRunAt: new(now), CreatedAt: now, UpdatedAt: now,
	}
	d, created, err := h.repo.CreateSubmission(context.Background(), domain.Submission{Download: d, Files: []domain.DownloadFile{{DownloadHash: hash, Index: 0, RelativePath: "payload.mkv", Size: int64(len(h.payload))}}})
	if err != nil || !created {
		h.t.Fatalf("CreateSubmission: created=%t error=%v", created, err)
	}
	return d
}

func (h *recoveryHarness) get(hash string) domain.Download {
	h.t.Helper()
	d, err := h.repo.GetDownload(context.Background(), hash)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *recoveryHarness) step(hash string) domain.Download {
	h.t.Helper()
	d := h.get(hash)
	if d.NextRunAt != nil && d.NextRunAt.After(h.clock.now) {
		h.clock.now = *d.NextRunAt
	}
	return storeStep(h.t, h.scheduler, h.repo, hash)
}

func (h *recoveryHarness) reach(hash string, state domain.State) domain.Download {
	h.t.Helper()
	for range 32 {
		d := h.get(hash)
		if d.State == state {
			return d
		}
		if d.NextRunAt == nil {
			h.t.Fatalf("cannot reach %s from %+v", state, d)
		}
		h.step(hash)
	}
	h.t.Fatalf("did not reach %s: %+v", state, h.get(hash))
	return domain.Download{}
}

func (h *recoveryHarness) assertContent(d domain.Download) {
	h.t.Helper()
	data, err := os.ReadFile(d.ContentPath)
	if err != nil || !bytes.Equal(data, h.payload) || d.TotalSize != int64(len(h.payload)) || d.QbitProgress != 1 {
		h.t.Fatalf("completed content: data=%q error=%v download=%+v", data, err, d)
	}
}

func TestRecoveryHarnessImmediateCompletion(t *testing.T) {
	h := newRecoveryHarness(t)
	d := h.add("0123456789012345678901234567890123456789")
	h.assertContent(h.reach(d.Hash, domain.StateCompleted))
	if h.copies != 1 {
		t.Fatalf("copies=%d, want exactly one", h.copies)
	}
}

func TestRecoveryHarnessStaleCompletionRetry(t *testing.T) {
	h := newRecoveryHarness(t)
	h.staleCopy = true
	d := h.add("0123456789012345678901234567890123456789")
	d = h.reach(d.Hash, domain.StateFailed)
	if d.LastErrorCode != string(domain.ProblemLocalVerificationTimeout) || d.ContentPath != "" {
		t.Fatalf("stale task falsely completed or misclassified: %+v", d)
	}
	if target := domain.RetryTarget(d); target != domain.StateSubmittingCopy {
		t.Fatalf("retry target=%s", target)
	}
	if err := h.repo.Retry(context.Background(), d.Hash, domain.RetryTarget(d), h.clock.now); err != nil {
		t.Fatal(err)
	}
	h.step(d.Hash)
	h.reopen()
	h.staleCopy = false
	h.assertContent(h.reach(d.Hash, domain.StateCompleted))
	if h.copies != 2 {
		t.Fatalf("copies=%d, want stale attempt plus one replacement", h.copies)
	}
}

func TestRecoveryHarnessIncompleteContentRetry(t *testing.T) {
	h := newRecoveryHarness(t)
	d := h.add("0123456789012345678901234567890123456789")
	h.reach(d.Hash, domain.StateWaitingCopy)
	if err := os.WriteFile(filepath.Join(d.WorkspacePath, "payload.mkv"), []byte("truncated"), 0o660); err != nil {
		t.Fatal(err)
	}
	d = h.reach(d.Hash, domain.StateFailed)
	if d.LastErrorCode != string(domain.ProblemLocalContentLayoutInvalid) {
		t.Fatalf("incomplete content classification: %+v", d)
	}
	if err := h.repo.Retry(context.Background(), d.Hash, domain.RetryTarget(d), h.clock.now); err != nil {
		t.Fatal(err)
	}
	h.assertContent(h.reach(d.Hash, domain.StateCompleted))
}

func TestRecoveryHarnessDeleteAndReadd(t *testing.T) {
	h := newRecoveryHarness(t)
	hash := "0123456789012345678901234567890123456789"
	h.add(hash)
	d := h.reach(hash, domain.StateCompleted)
	sentinel, err := h.files.PrepareWorkspace(h.localRoot, "abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	if err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(sentinel, "keep.bin")
	if err := os.WriteFile(sentinelPath, []byte("keep"), 0o660); err != nil {
		t.Fatal(err)
	}
	sentinelSpec := clouddrive.CopySpec{SourcePath: "/cloud/keep.bin", DestinationPath: sentinel}
	h.tasks[sentinelSpec] = clouddrive.CopyTask{SourcePath: sentinelSpec.SourcePath, DestinationPath: sentinel, State: clouddrive.CopyCompleted}
	if err := h.repo.RequestDelete(context.Background(), []string{hash}, true, h.clock.now); err != nil {
		t.Fatal(err)
	}
	h.removeErr = &clouddrive.Error{Operation: "remove_copy", Kind: clouddrive.ErrorTemporary}
	h.step(hash)
	if data, err := os.ReadFile(d.ContentPath); err != nil || !bytes.Equal(data, h.payload) {
		t.Fatalf("content deleted before remote confirmation: %v", err)
	}
	h.removeErr = nil
	h.step(hash)
	h.reopen()
	h.reach(hash, domain.StateDeleted)
	if _, err := os.Stat(d.WorkspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted workspace still present: %v", err)
	}
	if data, err := os.ReadFile(sentinelPath); err != nil || string(data) != "keep" {
		t.Fatalf("unrelated content changed: %v", err)
	}
	if _, found := h.tasks[sentinelSpec]; !found {
		t.Fatal("unrelated remote task removed")
	}
	h.add(hash)
	h.assertContent(h.reach(hash, domain.StateCompleted))
	if h.copies != 2 {
		t.Fatalf("re-add reused stale completion: copies=%d", h.copies)
	}
}

type interruptedCommit struct {
	*store.Store
	interrupted  bool
	afterCleanup bool
}

func (r *interruptedCommit) CommitClaim(ctx context.Context, claim store.Claim, next domain.Download) error {
	copyFinished := claim.State == domain.StateSubmittingCopy && next.LastUpstreamStatus == domain.UpstreamCopyCompleted
	localCleaned := claim.State == domain.StateSubmittingCopy && copyRemovalCompleted(claim.Download.LastUpstreamStatus) && next.LastUpstreamStatus == domain.UpstreamOfflineFinished
	if !r.interrupted && ((!r.afterCleanup && copyFinished) || (r.afterCleanup && localCleaned)) {
		r.interrupted = true
		return errors.New("simulated process interruption after external side effect")
	}
	return r.Store.CommitClaim(ctx, claim, next)
}

func TestRecoveryHarnessUnknownCopyResult(t *testing.T) {
	h := newRecoveryHarness(t)
	d := h.add("0123456789012345678901234567890123456789")
	h.reach(d.Hash, domain.StateSubmittingCopy)
	r := &interruptedCommit{Store: h.repo}
	h.scheduler.repo = r
	for !r.interrupted {
		claimed, err := h.scheduler.Step(context.Background())
		if !claimed {
			t.Fatalf("no work while injecting interruption: %v", err)
		}
		if err != nil && !r.interrupted {
			t.Fatal(err)
		}
	}
	h.reopen()
	h.clock.now = h.clock.now.Add(h.scheduler.config.LeaseDuration + time.Second)
	h.assertContent(h.reach(d.Hash, domain.StateCompleted))
	if h.copies != 1 {
		t.Fatalf("unknown result duplicated the copy: %d", h.copies)
	}
}

func TestRecoveryHarnessInterruptedLocalCleanup(t *testing.T) {
	h := newRecoveryHarness(t)
	h.staleCopy = true
	d := h.add("0123456789012345678901234567890123456789")
	d = h.reach(d.Hash, domain.StateFailed)
	if err := h.repo.Retry(context.Background(), d.Hash, domain.RetryTarget(d), h.clock.now); err != nil {
		t.Fatal(err)
	}
	h.step(d.Hash)
	r := &interruptedCommit{Store: h.repo, afterCleanup: true}
	h.scheduler.repo = r
	if _, err := h.scheduler.Step(context.Background()); err == nil || !r.interrupted {
		t.Fatalf("cleanup interruption was not exercised: %v", err)
	}
	if _, err := os.Stat(d.WorkspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup did not remove the owned workspace: %v", err)
	}
	h.reopen()
	h.clock.now = h.clock.now.Add(h.scheduler.config.LeaseDuration + time.Second)
	h.staleCopy = false
	h.assertContent(h.reach(d.Hash, domain.StateCompleted))
	if h.copies != 2 {
		t.Fatalf("cleanup recovery duplicated the copy: %d", h.copies)
	}
}

func TestRecoveryHarnessOfflineDeadlineAttribution(t *testing.T) {
	for _, state := range []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline} {
		for _, item := range []struct {
			name string
			err  error
			want domain.ProblemCode
		}{
			{"unreachable", &clouddrive.Error{Kind: clouddrive.ErrorTemporary}, domain.ProblemCloudUnreachableTimeout},
			{"authentication", &clouddrive.Error{Kind: clouddrive.ErrorUnauthorized}, domain.ProblemCloudAuthenticationTimeout},
			{"downloading", nil, domain.ProblemOfflineTimeout},
		} {
			t.Run(string(state)+"/"+item.name, func(t *testing.T) {
				h := newRecoveryHarness(t)
				d := h.add("0123456789012345678901234567890123456789")
				h.cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
					if state == domain.StateSubmittingOffline && item.err != nil {
						return clouddrive.OfflineTask{}, item.err
					}
					return clouddrive.OfflineTask{Name: "payload.mkv", SourcePath: "/cloud/payload.mkv", InfoHash: spec.Hash, State: clouddrive.OfflineDownloading}, nil
				}
				h.cloud.inspectOffline = func(context.Context, string, string) (clouddrive.OfflineTask, bool, error) {
					return clouddrive.OfflineTask{Name: "payload.mkv", SourcePath: "/cloud/payload.mkv", InfoHash: d.Hash, State: clouddrive.OfflineDownloading}, true, item.err
				}
				h.reach(d.Hash, state)
				h.step(d.Hash)
				h.clock.now = h.clock.now.Add(h.scheduler.config.OfflineTimeout)
				failed := h.step(d.Hash)
				if failed.State != domain.StateFailed || failed.LastErrorCode != string(item.want) {
					t.Fatalf("deadline attribution=%+v, want %s", failed, item.want)
				}
			})
		}
	}
}

func TestRecoveryHarnessRecoveredConnectionDoesNotMaskOfflineTimeout(t *testing.T) {
	h := newRecoveryHarness(t)
	d := h.add("0123456789012345678901234567890123456789")
	h.cloud.ensureOffline = func(_ context.Context, spec clouddrive.OfflineSpec) (clouddrive.OfflineTask, error) {
		return clouddrive.OfflineTask{Name: "payload.mkv", SourcePath: "/cloud/payload.mkv", InfoHash: spec.Hash, State: clouddrive.OfflineDownloading}, nil
	}
	h.cloud.inspectOffline = func(context.Context, string, string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{}, false, &clouddrive.Error{Kind: clouddrive.ErrorTemporary}
	}
	h.reach(d.Hash, domain.StateWaitingOffline)
	h.step(d.Hash)
	h.cloud.inspectOffline = func(context.Context, string, string) (clouddrive.OfflineTask, bool, error) {
		return clouddrive.OfflineTask{Name: "payload.mkv", SourcePath: "/cloud/payload.mkv", InfoHash: d.Hash, State: clouddrive.OfflineDownloading}, true, nil
	}
	h.step(d.Hash)
	h.clock.now = h.clock.now.Add(h.scheduler.config.OfflineTimeout)
	failed := h.step(d.Hash)
	if failed.State != domain.StateFailed || failed.LastErrorCode != string(domain.ProblemOfflineTimeout) {
		t.Fatalf("old connectivity error masked offline timeout: %+v", failed)
	}
}

func TestCommitFailureDiagnosticsExcludeSecrets(t *testing.T) {
	var output bytes.Buffer
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	d := baseDownload(domain.StateSubmittingCopy, now)
	d.CopySourcePath, d.DestinationName, d.LastUpstreamStatus = "/cloud/payload", "payload", destinationClear
	repo := &fakeRepository{claim: &store.Claim{Download: d, State: d.State, Version: 42}, commitErr: fmt.Errorf("secret-sql-parameter: %w", store.ErrInvalidTransition)}
	cloud, files := defaults()
	cloud.ensureCopy = func(_ context.Context, spec clouddrive.CopySpec) (clouddrive.CopyTask, error) {
		return clouddrive.CopyTask{SourcePath: spec.SourcePath, DestinationPath: spec.DestinationPath, State: clouddrive.CopyCompleted, Progress: 1}, nil
	}
	s := testScheduler(t, &fakeClock{now: now}, repo, cloud, files)
	s.logger = slog.New(slog.NewJSONHandler(&output, nil))
	if _, err := s.Step(context.Background()); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("commit error changed: %v", err)
	}
	data := output.Bytes()
	for _, expected := range [][]byte{[]byte(`"state_from":"SUBMITTING_COPY"`), []byte(`"state_to":"WAITING_COPY"`), []byte(`"claim_version":42`), []byte(`"error_kind":"invalid_transition"`)} {
		if !bytes.Contains(data, expected) {
			t.Fatalf("missing safe diagnostic %s in %s", expected, data)
		}
	}
	if bytes.Contains(data, []byte("secret-sql-parameter")) || bytes.Contains(data, []byte("/cloud/payload")) {
		t.Fatalf("diagnostic leaked details: %s", data)
	}
}
