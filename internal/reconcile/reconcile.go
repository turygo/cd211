// Package reconcile advances durable download workflow rows one leased step at a time.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const destinationClear = "local:destination_clear"
const cleanupCancelled = domain.UpstreamCleanupCancelled
const leaseCommitMargin = 15 * time.Second

// Repository is the durable lease and CAS boundary used by Scheduler.
type Repository interface {
	ClaimDue(context.Context, string, time.Time, time.Duration) (*store.Claim, error)
	CommitClaim(context.Context, store.Claim, domain.Download) error
	ListDownloadFiles(context.Context, string) ([]domain.DownloadFile, error)
	NextDue(context.Context, time.Time) (*time.Time, error)
}

// CloudDrive is the narrow remote operation surface used by Scheduler.
type CloudDrive interface {
	EnsureOffline(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error)
	InspectOffline(context.Context, string, string) (clouddrive.OfflineTask, bool, error)
	InspectContent(context.Context, string) (clouddrive.Content, error)
	CancelOffline(context.Context, string, string) error
	EnsureCopy(context.Context, clouddrive.CopySpec) (clouddrive.CopyTask, error)
	InspectCopy(context.Context, string, string) (clouddrive.CopyTask, bool, error)
	CancelCopy(context.Context, string, string) error
}

// Filesystem verifies and deletes local torrent content under its configured root.
type Filesystem interface {
	Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error)
	VerifyUnknownType(string, string) (fsafe.UnknownContent, error)
	Delete(string, string) error
}

// Config controls leasing, polling, and phase deadlines.
type Config struct {
	Owner                                      string
	LeaseDuration, PollInterval                time.Duration
	OfflineTimeout, CopyTimeout, VerifyTimeout time.Duration
	WorkerCount                                int
}

// Scheduler owns a crash-safe workflow reconciler.
type Scheduler struct {
	config Config
	repo   Repository
	cloud  CloudDrive
	files  Filesystem
	clock  Clock
	logger *slog.Logger
	wake   chan struct{}
}

// New validates and constructs a Scheduler.
func New(config Config, repo Repository, cloud CloudDrive, files Filesystem, clock Clock, logger *slog.Logger) (*Scheduler, error) {
	if strings.TrimSpace(config.Owner) == "" || config.LeaseDuration <= leaseCommitMargin || config.PollInterval <= 0 || config.OfflineTimeout <= 0 || config.CopyTimeout <= 0 || config.VerifyTimeout <= 0 || config.WorkerCount <= 0 {
		return nil, errors.New("invalid reconciler config")
	}
	if nilDependency(repo) || nilDependency(cloud) || nilDependency(files) || nilDependency(clock) || logger == nil {
		return nil, errors.New("nil reconciler dependency")
	}
	return &Scheduler{config: config, repo: repo, cloud: cloud, files: files, clock: clock, logger: logger, wake: make(chan struct{}, 1)}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Wake asks a sleeping worker to recompute the earliest durable due time.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Step claims and advances at most one durable row. Remote and filesystem work
// happens outside the repository transaction represented by the later CAS commit.
func (s *Scheduler) Step(ctx context.Context) (claimed bool, err error) {
	now := s.clock.Now()
	claim, err := s.repo.ClaimDue(ctx, s.config.Owner, now, s.config.LeaseDuration)
	if err != nil || claim == nil {
		return false, err
	}
	started := s.clock.Now()
	download := claim.Download
	if !download.State.Claimable() || download.PhaseStartedAt.IsZero() {
		s.log(download, "transition", started, "invariant")
		return true, fmt.Errorf("reconciler invariant: unclaimable state or zero phase start")
	}

	operationContext, cancelOperation := context.WithTimeout(ctx, s.config.LeaseDuration-leaseCommitMargin)
	op, decisionErr := s.decide(operationContext, &download)
	operationErr := operationContext.Err()
	cancelOperation()
	if ctx.Err() != nil {
		s.log(download, op, started, "cancelled")
		return true, ctx.Err()
	}
	if errors.Is(operationErr, context.DeadlineExceeded) {
		download = claim.Download
		s.retry(&download, domain.ProblemWorkflowOperationTimeout)
		decisionErr = nil
	}
	download.UpdatedAt = s.clock.Now()
	if decisionErr != nil {
		s.log(download, op, started, "invariant")
		return true, decisionErr
	}
	if err := s.repo.CommitClaim(ctx, *claim, download); err != nil {
		switch {
		case errors.Is(err, store.ErrClaimLost):
			s.log(download, op, started, "claim_lost")
			return true, nil
		case errors.Is(err, store.ErrDestinationConflict):
			download.DestinationName = ""
			s.fail(&download, domain.ProblemDestinationConflict)
			download.UpdatedAt = s.clock.Now()
			if failErr := s.repo.CommitClaim(ctx, *claim, download); failErr != nil {
				if errors.Is(failErr, store.ErrClaimLost) {
					s.log(download, op, started, "claim_lost")
					return true, nil
				}
				s.log(download, op, started, "commit_error")
				return true, failErr
			}
			s.log(download, op, started, "committed")
			return true, nil
		default:
			s.log(download, op, started, "commit_error")
			return true, err
		}
	}
	s.log(download, op, started, "committed")
	return true, nil
}

func (s *Scheduler) decide(ctx context.Context, d *domain.Download) (string, error) {
	now := s.clock.Now()
	// Copy-source resolution and destination reservation are independent
	// durable steps. Remote copy is not touched until both have committed.
	if d.State == domain.StateSubmittingCopy {
		if s.timedOut(d, now, s.config.CopyTimeout) {
			s.fail(d, s.deadlineProblem(d))
			return "resolve_copy_source", nil
		}
		if d.CloudResultPath == "" {
			s.fail(d, domain.ProblemCloudContentLayoutInvalid)
			return "resolve_copy_source", nil
		}
		if d.CopySourcePath == "" {
			source, err := s.resolveCopySource(ctx, *d)
			if err != nil {
				if errors.Is(err, errCloudContentLayout) {
					s.fail(d, domain.ProblemCloudContentLayoutInvalid)
					return "resolve_copy_source", nil
				}
				var cloudErr *clouddrive.Error
				if errors.As(err, &cloudErr) {
					return "inspect_content", s.cloudFailure(d, err, "inspect_content")
				}
				return "resolve_copy_source", err
			}
			d.CopySourcePath = source
			d.AttemptCount, d.LastError, d.LastErrorCode = 0, "", ""
			d.NextRunAt = new(now)
			return "resolve_copy_source", nil
		}
		if d.DestinationName == "" {
			name := path.Base(path.Clean(d.CopySourcePath))
			if !safeName(name) {
				s.fail(d, domain.ProblemCloudContentLayoutInvalid)
				return "reserve_destination", nil
			}
			d.DestinationName = name
			d.AttemptCount, d.LastError, d.LastErrorCode = 0, "", ""
			d.NextRunAt = new(now)
			return "reserve_destination", nil
		}
	}
	switch d.State {
	case domain.StateAccepted:
		s.advance(d, domain.StateSubmittingOffline, now, now)
		return "transition", nil
	case domain.StateSubmittingOffline:
		if s.timedOut(d, now, s.config.OfflineTimeout) {
			s.fail(d, domain.ProblemOfflineTimeout)
			return "ensure_offline", nil
		}
		if d.LastUpstreamStatus == domain.UpstreamOfflineError {
			if err := s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash); err != nil {
				return "retry_cancel_offline", s.cloudFailure(d, err, "cancel_offline")
			}
			d.OfflineProgress, d.QbitProgress = 0, 0
			d.LastUpstreamStatus, d.LastError, d.LastErrorCode, d.AttemptCount = "", "", "", 0
			d.NextRunAt = new(now)
			return "retry_cancel_offline", nil
		}
		if d.OfflineStartedAt == nil {
			started := s.clock.Now()
			d.OfflineStartedAt = &started
		}
		task, err := s.cloud.EnsureOffline(ctx, clouddrive.OfflineSpec{SubmissionURI: d.SubmissionURI, CloudFolder: d.CloudFolder, Hash: d.Hash})
		if err != nil {
			return "ensure_offline", s.cloudFailure(d, err, "ensure_offline")
		}
		if err := validateOfflineTask(d, task); err != nil {
			s.fail(d, domain.ProblemCloudResponseInvalid)
			return "ensure_offline", nil
		}
		s.recordOffline(d, task)
		switch task.State {
		case clouddrive.OfflineInit, clouddrive.OfflineDownloading:
			s.advance(d, domain.StateWaitingOffline, now, now.Add(s.config.PollInterval))
		case clouddrive.OfflineFinished:
			d.OfflineProgress, d.QbitProgress = 1, 0.9
			s.advance(d, domain.StateSubmittingCopy, now, now)
		case clouddrive.OfflineError:
			s.fail(d, domain.ProblemOfflineDownloadFailed)
		}
		return "ensure_offline", nil
	case domain.StateWaitingOffline:
		if s.timedOut(d, now, s.config.OfflineTimeout) {
			s.fail(d, domain.ProblemOfflineTimeout)
			return "inspect_offline", nil
		}
		task, found, err := s.cloud.InspectOffline(ctx, d.CloudFolder, d.Hash)
		if err != nil {
			return "inspect_offline", s.cloudFailure(d, err, "inspect_offline")
		}
		if !found {
			s.poll(d, now)
			return "inspect_offline", nil
		}
		if err := validateOfflineTask(d, task); err != nil {
			s.fail(d, domain.ProblemCloudResponseInvalid)
			return "inspect_offline", nil
		}
		s.recordOffline(d, task)
		switch task.State {
		case clouddrive.OfflineInit, clouddrive.OfflineDownloading:
			s.poll(d, now)
		case clouddrive.OfflineFinished:
			if task.Name == "" || task.SourcePath == "" {
				s.fail(d, domain.ProblemCloudResponseInvalid)
				return "inspect_offline", nil
			}
			d.OfflineProgress, d.QbitProgress = 1, 0.9
			s.advance(d, domain.StateSubmittingCopy, now, now)
		case clouddrive.OfflineError:
			s.fail(d, domain.ProblemOfflineDownloadFailed)
		default:
			s.fail(d, domain.ProblemCloudResponseInvalid)
		}
		return "inspect_offline", nil
	case domain.StateSubmittingCopy:
		if s.timedOut(d, now, s.config.CopyTimeout) {
			s.fail(d, s.deadlineProblem(d))
			return "ensure_copy", nil
		}
		if d.LastUpstreamStatus == domain.UpstreamCopyFailed {
			if err := s.cloud.CancelCopy(ctx, d.CopySourcePath, d.SavePath); err != nil {
				return "retry_cancel_copy", s.cloudFailure(d, err, "cancel_copy")
			}
			d.CopyProgress, d.QbitProgress = 0, 0.9
			markCleanupCancelled(d)
			d.LastError, d.LastErrorCode, d.AttemptCount = "", "", 0
			d.NextRunAt = new(now)
			return "retry_cancel_copy", nil
		}
		if d.LastUpstreamStatus == cleanupCancelled+"|"+domain.UpstreamCopyFailed {
			if d.DestinationName == "" {
				s.fail(d, domain.ProblemInternalWorkflowError)
				return "retry_delete_copy", nil
			}
			contentPath := filepath.Join(d.SavePath, d.DestinationName)
			if err := s.files.Delete(contentPath, d.SavePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.fail(d, domain.ProblemLocalDeleteFailed)
				d.AttemptCount++
				return "retry_delete_copy", nil
			}
			d.CopyProgress, d.QbitProgress = 0, 0.9
			d.LastUpstreamStatus, d.LastError, d.LastErrorCode, d.AttemptCount = domain.UpstreamOfflineFinished, "", "", 0
			d.NextRunAt = new(now)
			return "retry_delete_copy", nil
		}
		if d.LastUpstreamStatus != destinationClear {
			if code := s.preflightDestination(d); code != "" {
				s.fail(d, code)
				return "preflight_local", nil
			}
			d.LastUpstreamStatus, d.LastError, d.LastErrorCode, d.AttemptCount = destinationClear, "", "", 0
			d.NextRunAt = new(now)
			return "preflight_local", nil
		}
		task, err := s.cloud.EnsureCopy(ctx, clouddrive.CopySpec{SourcePath: d.CopySourcePath, DestinationPath: d.SavePath})
		if err != nil {
			return "ensure_copy", s.cloudFailure(d, err, "ensure_copy")
		}
		if err := validateCopyTask(task, d.CopySourcePath, d.SavePath); err != nil {
			s.fail(d, domain.ProblemCloudResponseInvalid)
			return "ensure_copy", nil
		}
		d.CopyProgress, d.QbitProgress = task.Progress, copyProgress(task.Progress)
		d.LastUpstreamStatus = "copy:" + string(task.State)
		switch task.State {
		case clouddrive.CopyPending, clouddrive.CopyScanning, clouddrive.CopyScanned:
			s.advance(d, domain.StateWaitingCopy, now, now.Add(s.config.PollInterval))
		case clouddrive.CopyCompleted:
			recordCopyCompleted(d, now)
			s.advance(d, domain.StateVerifyingLocal, now, now)
		case clouddrive.CopyFailed:
			s.fail(d, domain.ProblemCopyTaskFailed)
		}
		return "ensure_copy", nil
	case domain.StateWaitingCopy:
		if s.timedOut(d, now, s.config.CopyTimeout) {
			s.fail(d, s.deadlineProblem(d))
			return "inspect_copy", nil
		}
		task, found, err := s.cloud.InspectCopy(ctx, d.CopySourcePath, d.SavePath)
		if err != nil {
			return "inspect_copy", s.cloudFailure(d, err, "inspect_copy")
		}
		if !found {
			verifyErr := s.verifyAndRecord(ctx, d)
			if verifyErr == nil {
				recordCopyCompleted(d, now)
				s.advance(d, domain.StateVerifyingLocal, now, now)
				return "verify_local", nil
			}
			if isManifestRepositoryError(verifyErr) {
				return "verify_local", verifyErr
			}
			if errors.Is(verifyErr, fs.ErrNotExist) {
				s.poll(d, now)
				return "verify_local", nil
			}
			s.fail(d, domain.ProblemLocalVerificationFailed)
			return "verify_local", nil
		}
		if err := validateCopyTask(task, d.CopySourcePath, d.SavePath); err != nil {
			s.fail(d, domain.ProblemCloudResponseInvalid)
			return "inspect_copy", nil
		}
		d.CopyProgress, d.QbitProgress = task.Progress, copyProgress(task.Progress)
		d.LastUpstreamStatus = "copy:" + string(task.State)
		switch task.State {
		case clouddrive.CopyPending, clouddrive.CopyScanning, clouddrive.CopyScanned:
			s.poll(d, now)
		case clouddrive.CopyCompleted:
			recordCopyCompleted(d, now)
			s.advance(d, domain.StateVerifyingLocal, now, now)
		case clouddrive.CopyFailed:
			s.fail(d, domain.ProblemCopyTaskFailed)
		default:
			s.fail(d, domain.ProblemCloudResponseInvalid)
		}
		return "inspect_copy", nil
	case domain.StateVerifyingLocal:
		if s.timedOut(d, now, s.config.VerifyTimeout) {
			s.fail(d, domain.ProblemLocalVerificationTimeout)
			return "verify_local", nil
		}
		verifyErr := s.verifyAndRecord(ctx, d)
		if verifyErr == nil {
			// CloudDrive2 reports a directory as zero bytes and a magnet carries
			// no metadata, so the staged tree is what Sonarr and Radarr are told.
			d.OfflineProgress, d.CopyProgress, d.QbitProgress = 1, 1, 1
			d.State, d.LastError, d.LastErrorCode, d.AttemptCount = domain.StateCompleted, "", "", 0
			d.CompletedAt, d.NextRunAt = new(now), nil
			return "verify_local", nil
		}
		if isManifestRepositoryError(verifyErr) {
			return "verify_local", verifyErr
		}
		if errors.Is(verifyErr, fs.ErrNotExist) {
			s.poll(d, now)
			return "verify_local", nil
		}
		s.fail(d, domain.ProblemLocalVerificationFailed)
		return "verify_local", nil
	case domain.StateCancelRequested:
		return s.cancel(ctx, d, now)
	case domain.StateDeleteRequested:
		return s.delete(ctx, d, now)
	default:
		return "transition", fmt.Errorf("reconciler invariant: impossible claimed state")
	}
}

func (s *Scheduler) cancel(ctx context.Context, d *domain.Download, now time.Time) (string, error) {
	if d.PauseRequested {
		return s.pause(ctx, d, now)
	}
	var err error
	op := "cancel_offline"
	if d.CopySourcePath != "" {
		op, err = "cancel_copy", s.cloud.CancelCopy(ctx, d.CopySourcePath, d.SavePath)
	} else {
		err = s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash)
	}
	if err != nil && !notFound(err) {
		return op, s.cleanupFailure(d, err)
	}
	markCleanupCancelled(d)
	d.State, d.LastError, d.LastErrorCode, d.AttemptCount, d.NextRunAt = domain.StateCancelled, "", "", 0, nil
	return op, nil
}

func (s *Scheduler) pause(ctx context.Context, d *domain.Download, now time.Time) (string, error) {
	if d.ContentPath != "" || d.LastUpstreamStatus == domain.UpstreamCopyCompleted {
		d.State, d.LastError, d.LastErrorCode, d.AttemptCount, d.NextRunAt = domain.StateStopped, "", "", 0, nil
		return "pause_verified_copy", nil
	}
	if d.CopySourcePath == "" {
		if err := s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash); err != nil && !notFound(err) {
			return "pause_offline", s.cleanupFailure(d, err)
		}
		d.OfflineProgress, d.CopyProgress, d.QbitProgress = 0, 0, 0
		d.LastUpstreamStatus = ""
		d.State, d.LastError, d.LastErrorCode, d.AttemptCount, d.NextRunAt = domain.StateStopped, "", "", 0, nil
		return "pause_offline", nil
	}
	if err := s.cloud.CancelCopy(ctx, d.CopySourcePath, d.SavePath); err != nil && !notFound(err) {
		return "pause_copy", s.cleanupFailure(d, err)
	}
	if d.DestinationName == "" {
		if hasCopyEvidence(d.LastUpstreamStatus) {
			s.fail(d, domain.ProblemInternalWorkflowError)
			return "pause_copy", nil
		}
	} else {
		if err := s.files.Delete(filepath.Join(d.SavePath, d.DestinationName), d.SavePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.cleanupProblem(d, domain.ProblemLocalDeleteFailed)
			return "pause_copy", nil
		}
	}
	d.CopyProgress, d.QbitProgress = 0, 0.9
	d.LastUpstreamStatus = domain.UpstreamOfflineFinished
	d.State, d.LastError, d.LastErrorCode, d.AttemptCount, d.NextRunAt = domain.StateStopped, "", "", 0, nil
	return "pause_copy", nil
}

func (s *Scheduler) delete(ctx context.Context, d *domain.Download, now time.Time) (string, error) {
	if d.CompletedAt == nil && d.ContentPath == "" && !cleanupCompleted(d.LastUpstreamStatus) {
		var err error
		op := "cancel_offline"
		if d.CopySourcePath != "" {
			op, err = "cancel_copy", s.cloud.CancelCopy(ctx, d.CopySourcePath, d.SavePath)
		} else {
			err = s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash)
		}
		if err != nil && !notFound(err) {
			return op, s.cleanupFailure(d, err)
		}
		markCleanupCancelled(d)
		d.LastError, d.LastErrorCode, d.AttemptCount = "", "", 0
		d.NextRunAt = new(now)
		return op, nil
	}
	if d.DeleteFilesRequested {
		contentPath := d.ContentPath
		if contentPath == "" && hasCopyEvidence(d.LastUpstreamStatus) {
			if d.DestinationName == "" {
				s.fail(d, domain.ProblemInternalWorkflowError)
				return "delete_local", nil
			}
			contentPath = filepath.Join(d.SavePath, d.DestinationName)
		}
		if contentPath != "" {
			if err := s.files.Delete(contentPath, d.SavePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.cleanupProblem(d, domain.ProblemLocalDeleteFailed)
				return "delete_local", nil
			}
		}
	}
	d.State, d.LastError, d.LastErrorCode, d.AttemptCount, d.NextRunAt = domain.StateDeleted, "", "", 0, nil
	if d.RemovedAt == nil {
		d.RemovedAt = new(now)
	}
	return "delete_local", nil
}

func cleanupCompleted(status string) bool {
	return status == cleanupCancelled || strings.HasPrefix(status, cleanupCancelled+"|")
}

func markCleanupCancelled(d *domain.Download) {
	if cleanupCompleted(d.LastUpstreamStatus) {
		return
	}
	if d.LastUpstreamStatus == "" {
		d.LastUpstreamStatus = cleanupCancelled
		return
	}
	d.LastUpstreamStatus = cleanupCancelled + "|" + d.LastUpstreamStatus
}

var errCloudContentLayout = errors.New("cloud content layout invalid")

type manifestRepositoryError struct{ err error }

func (e *manifestRepositoryError) Error() string { return e.err.Error() }
func (e *manifestRepositoryError) Unwrap() error { return e.err }

func isManifestRepositoryError(err error) bool {
	var target *manifestRepositoryError
	return errors.As(err, &target)
}

// errCloudContentLayout is returned only for durable manifest/layout contradictions.

func (s *Scheduler) resolveCopySource(ctx context.Context, d domain.Download) (string, error) {
	result := path.Clean(d.CloudResultPath)
	if !safeCloudPath(d.CloudResultPath) || result != d.CloudResultPath {
		return "", errCloudContentLayout
	}
	// Magnets have no manifest and intentionally treat the offline result as
	// an opaque copy source; local verification observes the copied shape.
	if d.SourceKind == domain.SourceMagnet || d.IsMultiFile == nil {
		return result, nil
	}
	manifest, err := s.repo.ListDownloadFiles(ctx, d.Hash)
	if err != nil {
		return "", &manifestRepositoryError{err: fmt.Errorf("list download manifest: %w", err)}
	}
	if err := validateManifest(d, manifest); err != nil {
		return "", errCloudContentLayout
	}
	content, err := s.cloud.InspectContent(ctx, result)
	if err != nil {
		return "", err
	}
	switch {
	case d.IsMultiFile == nil:
		return result, nil
	case *d.IsMultiFile:
		if content.Kind != clouddrive.ContentDirectory {
			return "", errCloudContentLayout
		}
		return result, nil
	case content.Kind == clouddrive.ContentFile:
		if content.Size != manifest[0].Size {
			return "", errCloudContentLayout
		}
		return result, nil
	case content.Kind != clouddrive.ContentDirectory:
		return "", errCloudContentLayout
	default:
		relative := filepath.ToSlash(manifest[0].RelativePath)
		child := path.Join(result, relative)
		if !cloudDescendant(result, child) {
			return "", errCloudContentLayout
		}
		childContent, childErr := s.cloud.InspectContent(ctx, child)
		if childErr != nil {
			return "", childErr
		}
		if childContent.Kind != clouddrive.ContentFile || childContent.Size != manifest[0].Size {
			return "", errCloudContentLayout
		}
		return child, nil
	}
}

func validateManifest(d domain.Download, files []domain.DownloadFile) error {
	if d.IsMultiFile == nil {
		return nil
	}
	if *d.IsMultiFile {
		if len(files) == 0 {
			return errCloudContentLayout
		}
	} else if len(files) != 1 {
		return errCloudContentLayout
	}
	var total int64
	seen := make(map[string]struct{}, len(files))
	for index, file := range files {
		if file.DownloadHash != d.Hash || file.Index != int64(index) || file.Size < 0 || file.RelativePath == "" ||
			filepath.IsAbs(file.RelativePath) || filepath.Clean(file.RelativePath) != file.RelativePath ||
			file.RelativePath == "." || file.RelativePath == ".." ||
			strings.HasPrefix(file.RelativePath, ".."+string(filepath.Separator)) ||
			strings.ContainsAny(file.RelativePath, "\\\x00") || !utf8.ValidString(file.RelativePath) ||
			strings.ContainsFunc(file.RelativePath, unicode.IsControl) {
			return errCloudContentLayout
		}
		if _, exists := seen[file.RelativePath]; exists {
			return errCloudContentLayout
		}
		seen[file.RelativePath] = struct{}{}
		if total > math.MaxInt64-file.Size {
			return errCloudContentLayout
		}
		total += file.Size
	}
	if total != d.TotalSize {
		return errCloudContentLayout
	}
	return nil
}

func hasCopyEvidence(status string) bool {
	status = strings.TrimPrefix(status, cleanupCancelled+"|")
	switch status {
	case destinationClear,
		domain.UpstreamCopyPending,
		domain.UpstreamCopyScanning,
		domain.UpstreamCopyScanned,
		domain.UpstreamCopyCompleted,
		domain.UpstreamCopyFailed:
		return true
	default:
		return false
	}
}

// verifyAndRecord checks the staged candidate and records the verified
// metadata. A magnet without file metadata is verified by type so the staged
// tree decides file-vs-directory, size, and content path; uploaded torrents
// use their durable manifest.
func (s *Scheduler) verifyAndRecord(ctx context.Context, d *domain.Download) error {
	if d.SourceKind == domain.SourceMagnet || d.IsMultiFile == nil {
		content, err := s.files.VerifyUnknownType(d.SavePath, d.DestinationName)
		if err != nil {
			return err
		}
		multiFile := content.MultiFile
		d.IsMultiFile = &multiFile
		d.ContentPath, d.TotalSize = content.Path, content.Size
		return nil
	}
	files, err := s.repo.ListDownloadFiles(ctx, d.Hash)
	if err != nil {
		return &manifestRepositoryError{err: fmt.Errorf("list download manifest: %w", err)}
	}
	if err := validateManifest(*d, files); err != nil {
		return err
	}
	expected := fsafe.ExpectedContent{CandidateName: d.DestinationName, MultiFile: *d.IsMultiFile, Files: make([]fsafe.ExpectedFile, 0, len(files))}
	for _, file := range files {
		expected.Files = append(expected.Files, fsafe.ExpectedFile{RelativePath: file.RelativePath, Size: file.Size})
	}
	content, err := s.files.Verify(d.SavePath, expected)
	if err != nil {
		return err
	}
	d.ContentPath, d.TotalSize = content.Path, content.Size
	return nil
}

func expected(d domain.Download) fsafe.ExpectedContent {
	return fsafe.ExpectedContent{CandidateName: d.DestinationName, MultiFile: *d.IsMultiFile}
}

// preflightDestination verifies the reserved destination is clear before copy
// submission. Uploaded torrents require the strict expected candidate to be
// absent; magnets carry no metadata, so any safe existing regular file or
// directory at the destination is a collision regardless of type.
func (s *Scheduler) preflightDestination(d *domain.Download) domain.ProblemCode {
	if d.SourceKind == domain.SourceTorrent && d.IsMultiFile != nil {
		if _, err := s.files.Verify(d.SavePath, expected(*d)); err == nil {
			return domain.ProblemDestinationCollision
		} else if !errors.Is(err, fs.ErrNotExist) {
			return domain.ProblemLocalVerificationFailed
		}
		return ""
	}
	if _, err := s.files.VerifyUnknownType(d.SavePath, d.DestinationName); err == nil {
		return domain.ProblemDestinationCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return domain.ProblemLocalVerificationFailed
	}
	return ""
}

// deadlineProblem chooses the terminal copy-phase deadline code from the last
// durable retry problem, so a sustained not-ready or unreachable condition
// does not degrade to a generic timeout.
func (s *Scheduler) deadlineProblem(d *domain.Download) domain.ProblemCode {
	switch domain.ProblemCode(d.LastErrorCode) {
	case domain.ProblemCloudCopyNotReady:
		return domain.ProblemCloudCopyNotReadyTimeout
	case domain.ProblemCloudAuthenticationRequired:
		return domain.ProblemCloudAuthenticationTimeout
	case domain.ProblemCloudUnreachable:
		return domain.ProblemCloudUnreachableTimeout
	default:
		return domain.ProblemCopyTimeout
	}
}

func (s *Scheduler) timedOut(d *domain.Download, now time.Time, limit time.Duration) bool {
	return !d.PhaseStartedAt.IsZero() && now.Sub(d.PhaseStartedAt) >= limit
}

func (s *Scheduler) advance(d *domain.Download, state domain.State, now, due time.Time) {
	d.State, d.AttemptCount, d.LastError, d.LastErrorCode = state, 0, "", ""
	d.PhaseStartedAt, d.NextRunAt = now, new(due)
}

func (s *Scheduler) poll(d *domain.Download, now time.Time) {
	d.AttemptCount, d.LastError, d.LastErrorCode = 0, "", ""
	d.NextRunAt = new(now.Add(s.config.PollInterval))
}

// fail makes the download terminal with an actionable structured problem:
// the row records both the stable code and its safe default English text.
func (s *Scheduler) fail(d *domain.Download, code domain.ProblemCode) {
	d.State, d.LastError, d.LastErrorCode, d.NextRunAt = domain.StateFailed, domain.ProblemText(code), string(code), nil
}

// retry keeps the download non-terminal and schedules the persisted
// exponential backoff with the durable structured problem.
func (s *Scheduler) retry(d *domain.Download, code domain.ProblemCode) {
	d.AttemptCount++
	d.LastError = domain.ProblemText(code)
	d.LastErrorCode = string(code)
	d.NextRunAt = new(s.clock.Now().Add(backoff(d.AttemptCount)))
}

// cleanupProblem records a durable cleanup problem without changing state, so
// the original cancellation or removal intent stays visible and retryable.
func (s *Scheduler) cleanupProblem(d *domain.Download, code domain.ProblemCode) {
	d.AttemptCount++
	d.LastError = domain.ProblemText(code)
	d.LastErrorCode = string(code)
	d.NextRunAt = nil
}

// cloudFailure records the outcome of a workflow cloud operation. The durable
// problem code is derived from the factual error kind and the operation:
// temporary, auth, and (for copy submission) not-found/rejected observations
// keep the download non-terminal, while invalid input and invalid response
// fail immediately with an actionable structured problem.
func (s *Scheduler) cloudFailure(d *domain.Download, err error, operation string) error {
	code := s.cloudProblem(err, operation)
	if s.retryObservation(err, operation) {
		s.retry(d, code)
		return nil
	}
	s.fail(d, code)
	return nil
}

// cloudProblem maps a factual CloudDrive2 error kind to the stable user-facing
// problem code for the operation being performed.
func (s *Scheduler) cloudProblem(err error, operation string) domain.ProblemCode {
	var cloudErr *clouddrive.Error
	if !errors.As(err, &cloudErr) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.ProblemCloudUnreachable
		}
		return domain.ProblemCloudResponseInvalid
	}
	switch cloudErr.Kind {
	case clouddrive.ErrorTemporary:
		return domain.ProblemCloudUnreachable
	case clouddrive.ErrorUnauthorized:
		return domain.ProblemCloudAuthenticationRequired
	case clouddrive.ErrorNotFound:
		if operation == "ensure_copy" || operation == "inspect_copy" || operation == "inspect_content" {
			return domain.ProblemCloudCopyNotReady
		}
		return domain.ProblemCloudFolderUnavailable
	case clouddrive.ErrorRejected:
		if operation == "ensure_copy" || operation == "inspect_copy" || operation == "inspect_content" {
			return domain.ProblemCloudCopyNotReady
		}
		// The workflow operation is not enough: EnsureOffline performs the
		// folder ensure before AddOfflineFiles, so a rejected CreateFolder
		// must not claim the torrent source was rejected.
		switch cloudErr.Operation {
		case "create_folder":
			return domain.ProblemCloudFolderUnavailable
		case "add_offline":
			return domain.ProblemOfflineSubmissionRejected
		default:
			return domain.ProblemCloudRequestRejected
		}
	case clouddrive.ErrorInvalidInput:
		return domain.ProblemInternalWorkflowError
	case clouddrive.ErrorInvalidResponse:
		return domain.ProblemCloudResponseInvalid
	default:
		return domain.ProblemInternalWorkflowError
	}
}

// retryObservation reports whether the observation keeps the workflow
// non-terminal. Copy submission and inspection retry on temporary, auth,
// not-found, and rejected observations; offline and cleanup operations retry
// only on temporary and auth observations.
func (s *Scheduler) retryObservation(err error, operation string) bool {
	var cloudErr *clouddrive.Error
	if !errors.As(err, &cloudErr) {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}
	switch cloudErr.Kind {
	case clouddrive.ErrorTemporary, clouddrive.ErrorUnauthorized:
		return true
	case clouddrive.ErrorNotFound, clouddrive.ErrorRejected:
		return operation == "ensure_copy" || operation == "inspect_copy" || operation == "inspect_content"
	default:
		return false
	}
}

// cleanupFailure records the outcome of a cancellation or removal operation.
// The cleanup intent (CANCEL_REQUESTED / DELETE_REQUESTED) is preserved:
// temporary and auth observations schedule a retry, and other factual kinds
// record a durable cleanup problem without advancing state.
func (s *Scheduler) cleanupFailure(d *domain.Download, err error) error {
	code := s.cloudProblem(err, "cancel")
	if s.retryObservation(err, "cancel") {
		s.retry(d, code)
		return nil
	}
	s.cleanupProblem(d, code)
	return nil
}

func backoff(attempt int64) time.Duration {
	switch {
	case attempt <= 1:
		return 30 * time.Second
	case attempt == 2:
		return time.Minute
	case attempt == 3:
		return 2 * time.Minute
	case attempt == 4:
		return 4 * time.Minute
	default:
		return 5 * time.Minute
	}
}
func validateOfflineTask(d *domain.Download, task clouddrive.OfflineTask) error {
	switch task.State {
	case clouddrive.OfflineInit, clouddrive.OfflineDownloading, clouddrive.OfflineFinished, clouddrive.OfflineError:
	default:
		return errors.New("invalid offline state")
	}
	if task.InfoHash != d.Hash || task.Progress < 0 || task.Progress > 1 || task.Size < 0 || (task.Name == "" && task.SourcePath != "") || (task.Name != "" && !safeName(task.Name)) {
		return errors.New("invalid offline task")
	}
	if task.Name == "" {
		if task.State != clouddrive.OfflineInit {
			return errors.New("invalid synthetic offline task")
		}
		return nil
	}
	if !safeCloudPath(task.SourcePath) || path.Base(task.SourcePath) != task.Name || !cloudDescendant(d.CloudFolder, task.SourcePath) {
		return errors.New("invalid offline source")
	}
	return nil
}

func (s *Scheduler) recordOffline(d *domain.Download, task clouddrive.OfflineTask) {
	d.OfflineProgress, d.QbitProgress = task.Progress, offlineProgress(task.Progress)
	d.LastUpstreamStatus = "offline:" + string(task.State)
	if task.State == clouddrive.OfflineFinished {
		d.CloudTaskName, d.CloudResultPath = task.Name, task.SourcePath
		if d.SourceKind == domain.SourceMagnet {
			d.Name = task.Name
		}
	}
	if d.SourceKind == domain.SourceMagnet && d.TotalSize == 0 && task.Size > 0 {
		d.TotalSize = task.Size
	}
}

func recordCopyCompleted(d *domain.Download, at time.Time) {
	if d.CopyCompletedAt == nil {
		d.CopyCompletedAt = &at
	}
}

func validateCopyTask(task clouddrive.CopyTask, source, destination string) error {
	if task.SourcePath != path.Clean(source) || task.DestinationPath != path.Clean(destination) || task.Progress < 0 || task.Progress > 1 {
		return errors.New("invalid copy task")
	}
	switch task.State {
	case clouddrive.CopyPending, clouddrive.CopyScanning, clouddrive.CopyScanned, clouddrive.CopyCompleted, clouddrive.CopyFailed:
		return nil
	default:
		return errors.New("invalid copy state")
	}
}

func cloudDescendant(folder, source string) bool {
	folder, source = path.Clean(folder), path.Clean(source)
	if folder == "/" {
		return source != "/" && strings.HasPrefix(source, "/")
	}
	return strings.HasPrefix(source, folder+"/")
}

func safeCloudPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00") && !strings.ContainsFunc(value, unicode.IsControl)
}

func safeName(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || strings.ContainsAny(value, "/\\\x00") {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func offlineProgress(value float64) float64 { return round6(0.9 * value) }
func copyProgress(value float64) float64    { return round6(0.9 + 0.09*value) }
func round6(value float64) float64          { return math.Round(value*1_000_000) / 1_000_000 }

func notFound(err error) bool { return status.Code(errors.Unwrap(err)) == codes.NotFound }

func (s *Scheduler) log(d domain.Download, operation string, started time.Time, result string) {
	hash := d.Hash
	if len(hash) > 8 {
		hash = hash[:8]
	}
	attributes := []any{
		"hash", hash,
		"state", d.State,
		"operation", operation,
		"attempt", d.AttemptCount,
		"latency", s.clock.Now().Sub(started),
		"result", result,
	}
	if d.LastErrorCode != "" {
		attributes = append(attributes, "problem", d.LastErrorCode)
	}
	if errorText := domain.SanitizeDownloadError(d); errorText != "" {
		attributes = append(attributes, "error", errorText)
	}
	s.logger.Info("reconciled download", attributes...)
}

// Run starts the configured number of workers and returns after cancellation.
func (s *Scheduler) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	workers.Add(s.config.WorkerCount)
	for range s.config.WorkerCount {
		go func() { defer workers.Done(); s.worker(ctx) }()
	}
	workers.Wait()
	return nil
}

func (s *Scheduler) worker(ctx context.Context) {
	for {
		claimed, err := s.Step(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.logger.Warn("reconciler repository error", "operation", "step", "result", "error")
			s.wait(ctx, s.config.PollInterval)
			continue
		}
		if claimed {
			continue
		}
		now := s.clock.Now()
		due, dueErr := s.repo.NextDue(ctx, now)
		if ctx.Err() != nil {
			return
		}
		if dueErr != nil {
			s.logger.Warn("reconciler repository error", "operation", "next_due", "result", "error")
			s.wait(ctx, s.config.PollInterval)
			continue
		}
		wait := s.config.PollInterval
		if due != nil {
			wait = due.Sub(now)
			if wait < 0 {
				wait = 0
			}
		}
		s.wait(ctx, wait)
	}
}

func (s *Scheduler) wait(ctx context.Context, delay time.Duration) {
	timer := s.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-timer.C():
	}
}
