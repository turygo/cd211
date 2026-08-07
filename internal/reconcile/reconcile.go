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
	"github.com/turygo/cd211/internal/clouddrive/pb"
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
	NextDue(context.Context, time.Time) (*time.Time, error)
}

// CloudDrive is the narrow remote operation surface used by Scheduler.
type CloudDrive interface {
	FindFile(context.Context, string) (*pb.CloudDriveFile, error)
	EnsureOffline(context.Context, clouddrive.OfflineSpec) (clouddrive.OfflineTask, error)
	InspectOffline(context.Context, string, string) (clouddrive.OfflineTask, bool, error)
	CancelOffline(context.Context, string, string) error
	EnsureCopy(context.Context, clouddrive.CopySpec) (clouddrive.CopyTask, error)
	InspectCopy(context.Context, string, string) (clouddrive.CopyTask, bool, error)
	CancelCopy(context.Context, string, string) error
}

// Filesystem verifies and deletes local torrent content under its configured root.
type Filesystem interface {
	Verify(string, fsafe.ExpectedContent) (string, error)
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
		s.retry(&download, "reconciler operation timeout")
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
			s.fail(&download, "destination path conflicts with another download")
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
	if d.DestinationName == "" && d.IsMultiFile != nil &&
		(d.State == domain.StateSubmittingCopy || d.State == domain.StateWaitingCopy || d.State == domain.StateVerifyingLocal) {
		d.DestinationName = d.Name
		d.AttemptCount, d.LastError = 0, ""
		d.NextRunAt = new(now)
		return "reserve_destination", nil
	}
	switch d.State {
	case domain.StateAccepted:
		s.advance(d, domain.StateSubmittingOffline, now, now)
		return "transition", nil
	case domain.StateSubmittingOffline:
		if s.timedOut(d, now, s.config.OfflineTimeout) {
			s.fail(d, "offline phase timeout")
			return "ensure_offline", nil
		}
		if d.LastUpstreamStatus == domain.UpstreamOfflineError {
			if err := s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash); err != nil {
				return "retry_cancel_offline", s.cloudFailure(d, err, "cancel_offline")
			}
			d.OfflineProgress, d.QbitProgress = 0, 0
			d.LastUpstreamStatus, d.LastError, d.AttemptCount = "", "", 0
			d.NextRunAt = new(now)
			return "retry_cancel_offline", nil
		}
		task, err := s.cloud.EnsureOffline(ctx, clouddrive.OfflineSpec{SubmissionURI: d.SubmissionURI, CloudFolder: d.CloudFolder, Hash: d.Hash})
		if err != nil {
			return "ensure_offline", s.cloudFailure(d, err, "ensure_offline")
		}
		if err := validateOfflineTask(d, task); err != nil {
			s.fail(d, "clouddrive ensure_offline: invalid_response")
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
			s.fail(d, "offline task error")
		}
		return "ensure_offline", nil
	case domain.StateWaitingOffline:
		if s.timedOut(d, now, s.config.OfflineTimeout) {
			s.fail(d, "offline phase timeout")
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
			s.fail(d, "clouddrive inspect_offline: invalid_response")
			return "inspect_offline", nil
		}
		s.recordOffline(d, task)
		switch task.State {
		case clouddrive.OfflineInit, clouddrive.OfflineDownloading:
			s.poll(d, now)
		case clouddrive.OfflineFinished:
			if task.Name == "" || task.SourcePath == "" {
				s.fail(d, "clouddrive inspect_offline: invalid_response")
				return "inspect_offline", nil
			}
			d.OfflineProgress, d.QbitProgress = 1, 0.9
			s.advance(d, domain.StateSubmittingCopy, now, now)
		case clouddrive.OfflineError:
			s.fail(d, "offline task error")
		default:
			s.fail(d, "clouddrive inspect_offline: invalid_response")
		}
		return "inspect_offline", nil
	case domain.StateSubmittingCopy:
		if s.timedOut(d, now, s.config.CopyTimeout) {
			s.fail(d, "copy phase timeout")
			return "find_file", nil
		}
		if d.LastUpstreamStatus == domain.UpstreamCopyFailed {
			if err := s.cloud.CancelCopy(ctx, d.CloudSourcePath, d.SavePath); err != nil {
				return "retry_cancel_copy", s.cloudFailure(d, err, "cancel_copy")
			}
			d.CopyProgress, d.QbitProgress = 0, 0.9
			markCleanupCancelled(d)
			d.LastError, d.AttemptCount = "", 0
			d.NextRunAt = new(now)
			return "retry_cancel_copy", nil
		}
		if d.LastUpstreamStatus == cleanupCancelled+"|"+domain.UpstreamCopyFailed {
			if d.DestinationName == "" {
				s.fail(d, "missing destination reservation")
				return "retry_delete_copy", nil
			}
			contentPath := filepath.Join(d.SavePath, d.DestinationName)
			if err := s.files.Delete(contentPath, d.SavePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				d.State = domain.StateFailed
				d.LastError = "local deletion failed"
				d.AttemptCount++
				d.NextRunAt = nil
				return "retry_delete_copy", nil
			}
			d.CopyProgress, d.QbitProgress = 0, 0.9
			d.LastUpstreamStatus, d.LastError, d.AttemptCount = domain.UpstreamOfflineFinished, "", 0
			d.NextRunAt = new(now)
			return "retry_delete_copy", nil
		}
		if d.IsMultiFile == nil {
			file, err := s.cloud.FindFile(ctx, d.CloudSourcePath)
			if err != nil {
				return "find_file", s.cloudFailure(d, err, "find_file")
			}
			multi, name, size, err := validateFile(d.CloudSourcePath, file)
			if err != nil {
				s.fail(d, "clouddrive find_file: invalid_response")
				return "find_file", nil
			}
			d.IsMultiFile, d.Name, d.TotalSize = new(multi), name, size
			d.LastError = ""
			d.AttemptCount = 0
			d.NextRunAt = new(now)
			return "find_file", nil
		}
		if d.LastUpstreamStatus != destinationClear {
			content, err := s.files.Verify(d.SavePath, expected(*d))
			if err == nil {
				_ = content
				s.fail(d, "destination collision")
				return "preflight_local", nil
			}
			if errors.Is(err, fs.ErrNotExist) {
				d.LastUpstreamStatus, d.LastError, d.AttemptCount = destinationClear, "", 0
				d.NextRunAt = new(now)
				return "preflight_local", nil
			}
			s.fail(d, "local verification failed")
			return "preflight_local", nil
		}
		task, err := s.cloud.EnsureCopy(ctx, clouddrive.CopySpec{SourcePath: d.CloudSourcePath, DestinationPath: d.SavePath})
		if err != nil {
			return "ensure_copy", s.cloudFailure(d, err, "ensure_copy")
		}
		if err := validateCopyTask(task, d.CloudSourcePath, d.SavePath); err != nil {
			s.fail(d, "clouddrive ensure_copy: invalid_response")
			return "ensure_copy", nil
		}
		d.CopyProgress, d.QbitProgress = task.Progress, copyProgress(task.Progress)
		d.LastUpstreamStatus = "copy:" + string(task.State)
		switch task.State {
		case clouddrive.CopyPending, clouddrive.CopyScanning, clouddrive.CopyScanned:
			s.advance(d, domain.StateWaitingCopy, now, now.Add(s.config.PollInterval))
		case clouddrive.CopyCompleted:
			s.advance(d, domain.StateVerifyingLocal, now, now)
		case clouddrive.CopyFailed:
			s.fail(d, "copy task failed")
		}
		return "ensure_copy", nil
	case domain.StateWaitingCopy:
		if s.timedOut(d, now, s.config.CopyTimeout) {
			s.fail(d, "copy phase timeout")
			return "inspect_copy", nil
		}
		task, found, err := s.cloud.InspectCopy(ctx, d.CloudSourcePath, d.SavePath)
		if err != nil {
			return "inspect_copy", s.cloudFailure(d, err, "inspect_copy")
		}
		if !found {
			content, verifyErr := s.verify(d)
			if verifyErr == nil {
				d.ContentPath = content
				s.advance(d, domain.StateVerifyingLocal, now, now)
				return "verify_local", nil
			}
			if errors.Is(verifyErr, fs.ErrNotExist) {
				s.poll(d, now)
				return "verify_local", nil
			}
			s.fail(d, "local verification failed")
			return "verify_local", nil
		}
		if err := validateCopyTask(task, d.CloudSourcePath, d.SavePath); err != nil {
			s.fail(d, "clouddrive inspect_copy: invalid_response")
			return "inspect_copy", nil
		}
		d.CopyProgress, d.QbitProgress = task.Progress, copyProgress(task.Progress)
		d.LastUpstreamStatus = "copy:" + string(task.State)
		switch task.State {
		case clouddrive.CopyPending, clouddrive.CopyScanning, clouddrive.CopyScanned:
			s.poll(d, now)
		case clouddrive.CopyCompleted:
			s.advance(d, domain.StateVerifyingLocal, now, now)
		case clouddrive.CopyFailed:
			s.fail(d, "copy task failed")
		default:
			s.fail(d, "clouddrive inspect_copy: invalid_response")
		}
		return "inspect_copy", nil
	case domain.StateVerifyingLocal:
		if s.timedOut(d, now, s.config.VerifyTimeout) {
			s.fail(d, "local verification timeout")
			return "verify_local", nil
		}
		content, err := s.verify(d)
		if err == nil {
			d.ContentPath = content
			d.OfflineProgress, d.CopyProgress, d.QbitProgress = 1, 1, 1
			d.State, d.LastError, d.AttemptCount = domain.StateCompleted, "", 0
			d.CompletedAt, d.NextRunAt = new(now), nil
			return "verify_local", nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			s.poll(d, now)
			return "verify_local", nil
		}
		s.fail(d, "local verification failed")
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
	var err error
	op := "cancel_offline"
	if d.CloudSourcePath != "" {
		op, err = "cancel_copy", s.cloud.CancelCopy(ctx, d.CloudSourcePath, d.SavePath)
	} else {
		err = s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash)
	}
	if err != nil && !notFound(err) {
		return op, s.cleanupFailure(d, err, op)
	}
	markCleanupCancelled(d)
	d.State, d.LastError, d.AttemptCount, d.NextRunAt = domain.StateCancelled, "", 0, nil
	return op, nil
}

func (s *Scheduler) delete(ctx context.Context, d *domain.Download, now time.Time) (string, error) {
	if d.CompletedAt == nil && d.ContentPath == "" && !cleanupCompleted(d.LastUpstreamStatus) {
		var err error
		op := "cancel_offline"
		if d.CloudSourcePath != "" {
			op, err = "cancel_copy", s.cloud.CancelCopy(ctx, d.CloudSourcePath, d.SavePath)
		} else {
			err = s.cloud.CancelOffline(ctx, d.CloudFolder, d.Hash)
		}
		if err != nil && !notFound(err) {
			return op, s.cleanupFailure(d, err, op)
		}
		markCleanupCancelled(d)
		d.LastError, d.AttemptCount = "", 0
		d.NextRunAt = new(now)
		return op, nil
	}
	if d.DeleteFilesRequested {
		contentPath := d.ContentPath
		if contentPath == "" && hasCopyEvidence(d.LastUpstreamStatus) {
			contentPath = filepath.Join(d.SavePath, d.Name)
		}
		if contentPath != "" {
			if err := s.files.Delete(contentPath, d.SavePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				d.LastError = "local deletion failed"
				d.AttemptCount++
				d.NextRunAt = nil
				return "delete_local", nil
			}
		}
	}
	d.State, d.LastError, d.AttemptCount, d.NextRunAt = domain.StateDeleted, "", 0, nil
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

func (s *Scheduler) verify(d *domain.Download) (string, error) {
	if d.IsMultiFile == nil {
		return "", errors.New("missing file metadata")
	}
	return s.files.Verify(d.SavePath, expected(*d))
}

func expected(d domain.Download) fsafe.ExpectedContent {
	return fsafe.ExpectedContent{Name: d.Name, MultiFile: *d.IsMultiFile}
}

func (s *Scheduler) timedOut(d *domain.Download, now time.Time, limit time.Duration) bool {
	return !d.PhaseStartedAt.IsZero() && now.Sub(d.PhaseStartedAt) >= limit
}

func (s *Scheduler) advance(d *domain.Download, state domain.State, now, due time.Time) {
	d.State, d.AttemptCount, d.LastError = state, 0, ""
	d.PhaseStartedAt, d.NextRunAt = now, new(due)
}

func (s *Scheduler) poll(d *domain.Download, now time.Time) {
	d.AttemptCount, d.LastError = 0, ""
	d.NextRunAt = new(now.Add(s.config.PollInterval))
}

func (s *Scheduler) fail(d *domain.Download, message string) {
	d.State, d.LastError, d.NextRunAt = domain.StateFailed, message, nil
}

func (s *Scheduler) cloudFailure(d *domain.Download, err error, operation string) error {
	var cloudErr *clouddrive.Error
	if errors.As(err, &cloudErr) {
		if cloudErr.Kind == clouddrive.ErrorTransient || cloudErr.Kind == clouddrive.ErrorUnauthorized {
			s.retry(d, cloudErr.Error())
			return nil
		}
		s.fail(d, cloudErr.Error())
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.retry(d, "clouddrive "+operation+": transient")
		return nil
	}
	s.fail(d, "clouddrive "+operation+": invalid_response")
	return nil
}

func (s *Scheduler) cleanupFailure(d *domain.Download, err error, operation string) error {
	var cloudErr *clouddrive.Error
	if errors.As(err, &cloudErr) {
		if cloudErr.Kind == clouddrive.ErrorTransient || cloudErr.Kind == clouddrive.ErrorUnauthorized {
			s.retry(d, cloudErr.Error())
			return nil
		}
		d.AttemptCount++
		d.LastError = cloudErr.Error()
		d.NextRunAt = nil
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.retry(d, "clouddrive "+operation+": transient")
		return nil
	}
	d.AttemptCount++
	d.LastError = "clouddrive " + operation + ": invalid_response"
	d.NextRunAt = nil
	return nil
}

func (s *Scheduler) retry(d *domain.Download, message string) {
	d.AttemptCount++
	d.LastError = message
	d.NextRunAt = new(s.clock.Now().Add(backoff(d.AttemptCount)))
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
	if task.InfoHash != d.Hash || task.Progress < 0 || task.Progress > 1 || (task.Name == "" && task.SourcePath != "") || (task.Name != "" && !safeName(task.Name)) {
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
		d.CloudTaskName, d.Name, d.CloudSourcePath = task.Name, task.Name, task.SourcePath
	}
}

func validateFile(source string, file *pb.CloudDriveFile) (bool, string, int64, error) {
	if file == nil || file.Size < 0 || (file.FullPathName != "" && path.Clean(file.FullPathName) != path.Clean(source)) {
		return false, "", 0, errors.New("invalid cloud file")
	}
	base := path.Base(path.Clean(source))
	name := file.Name
	if name == "" {
		name = base
	}
	if !safeName(name) || name != base {
		return false, "", 0, errors.New("invalid cloud file name")
	}
	if file.IsDirectory || file.FileType == pb.CloudDriveFile_Directory {
		return true, name, file.Size, nil
	}
	if file.FileType == pb.CloudDriveFile_File {
		return false, name, file.Size, nil
	}
	return false, "", 0, errors.New("unsupported cloud file type")
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
	return path.IsAbs(value) && utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00") && !strings.ContainsFunc(value, unicode.IsControl)
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
	if d.LastError != "" {
		attributes = append(attributes, "error", d.LastError)
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
