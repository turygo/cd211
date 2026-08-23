// Package submission implements the shared application boundary for torrent
// submissions. Both the qBittorrent WebAPI adapter and the native automation
// API decode their own request bodies and then call the same service, so
// metadata parsing, canonical hashes/names/files, category lookup, frozen
// destination paths, retained-content revival, persistence, and reconciler
// wake-up live in exactly one place.
package submission

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
)

// Sentinel errors classify a submission outcome so HTTP adapters can map them
// without knowing the underlying cause.
var (
	// ErrInvalidSource reports a magnet or torrent that failed bounded parsing,
	// including a magnet that is not exactly one non-empty line after trim.
	ErrInvalidSource = errors.New("submission: invalid torrent source")
	// ErrCategoryInvalid reports a category name that violates the canonical
	// category rules, a category that is not configured, or a disabled one.
	ErrCategoryInvalid = errors.New("submission: category is unavailable")
)

// Repository is the durable boundary the service needs. *store.Store
// implements it.
type Repository interface {
	GetCategory(context.Context, string) (domain.Category, error)
	GetDownload(context.Context, string) (domain.Download, error)
	CreateSubmission(context.Context, domain.Submission) (domain.Download, bool, error)
}

// Filesystem verifies retained content during revival. *fsafe.Verifier
// implements it.
type Filesystem interface {
	Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error)
}

// Clock provides the current time.
type Clock interface {
	Now() time.Time
}

// Waker schedules reconciler processing after a durable submission commits.
type Waker interface {
	Wake()
}

// Config carries the frozen path boundaries and metadata bounds.
type Config struct {
	CloudRoot     string
	LocalRoot     string
	TorrentLimits torrentmeta.Limits
}

// Service owns the complete submission pipeline shared by the HTTP adapters.
type Service struct {
	repo   Repository
	files  Filesystem
	clock  Clock
	waker  Waker
	config Config
}

// New validates the service configuration. All dependencies must be non-nil;
// nil is a programming error, not a runtime fallback.
func New(config Config, repo Repository, clock Clock, waker Waker, files Filesystem) (*Service, error) {
	if isNil(repo) || isNil(clock) || isNil(waker) || isNil(files) {
		return nil, errors.New("submission dependency is nil")
	}
	if !validCloudRoot(config.CloudRoot) {
		return nil, errors.New("cloud root must be an absolute clean POSIX path")
	}
	if !filepath.IsAbs(config.LocalRoot) || filepath.Clean(config.LocalRoot) != config.LocalRoot {
		return nil, errors.New("local root must be an absolute clean host path")
	}
	if !validLimits(config.TorrentLimits) {
		return nil, errors.New("torrent limits are invalid")
	}
	return &Service{repo: repo, files: files, clock: clock, waker: waker, config: config}, nil
}

// SubmitMagnet parses and persists a magnet submission. raw is the request
// value exactly as decoded by the adapter; the service applies the one-line
// rule, bounded metadata parsing, category lookup, revival verification, and
// the stopped override. The returned download is the persisted row: a fresh
// insert or a revived DELETED row when created is true, otherwise the
// untouched existing row.
func (s *Service) SubmitMagnet(ctx context.Context, raw, category string, stopped bool) (domain.Download, bool, error) {
	category, cloudFolder, savePath, err := s.submissionPaths(ctx, category)
	if err != nil {
		return domain.Download{}, false, err
	}
	magnet, one := oneMagnetLine(raw)
	if !one {
		return domain.Download{}, false, ErrInvalidSource
	}
	result, err := torrentmeta.ParseMagnet(magnet, s.config.TorrentLimits)
	if err != nil {
		return domain.Download{}, false, ErrInvalidSource
	}
	return s.submit(ctx, result, domain.SourceMagnet, category, cloudFolder, savePath, stopped)
}

// SubmitTorrent parses and persists a torrent submission. data is the decoded
// file bytes; the filename is informational only. The same category lookup,
// revival verification, and stopped override apply as for magnets.
func (s *Service) SubmitTorrent(ctx context.Context, data []byte, category string, stopped bool) (domain.Download, bool, error) {
	category, cloudFolder, savePath, err := s.submissionPaths(ctx, category)
	if err != nil {
		return domain.Download{}, false, err
	}
	result, err := torrentmeta.ParseTorrent(data, s.config.TorrentLimits)
	if err != nil {
		return domain.Download{}, false, ErrInvalidSource
	}
	return s.submit(ctx, result, domain.SourceTorrent, category, cloudFolder, savePath, stopped)
}

// submissionPaths resolves the canonical category and its frozen destination
// paths. An empty category uses the configured roots; a missing, disabled, or
// syntactically invalid category is ErrCategoryInvalid; a lookup failure is an
// internal error.
func (s *Service) submissionPaths(ctx context.Context, rawCategory string) (category, cloudFolder, savePath string, err error) {
	category, valid := CanonicalCategory(rawCategory, true)
	if !valid {
		return "", "", "", ErrCategoryInvalid
	}
	if category == "" {
		return category, s.config.CloudRoot, s.config.LocalRoot, nil
	}
	configured, err := s.repo.GetCategory(ctx, category)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", "", ErrCategoryInvalid
	}
	if err != nil {
		return "", "", "", fmt.Errorf("lookup category: %w", err)
	}
	if !configured.Enabled {
		return "", "", "", ErrCategoryInvalid
	}
	return category, configured.CloudPath, configured.SavePath, nil
}

func (s *Service) submit(ctx context.Context, result torrentmeta.Result, source domain.SourceKind, category, cloudFolder, savePath string, stopped bool) (domain.Download, bool, error) {
	now := s.clock.Now().UTC()
	download := domain.Download{
		Hash: result.Hash, Name: result.Name, SourceKind: source, SubmissionURI: result.Magnet,
		Category: category, CloudFolder: cloudFolder, SavePath: savePath, TotalSize: result.TotalSize,
		State: domain.StateAccepted, PhaseStartedAt: now, NextRunAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	filesToCreate := make([]domain.DownloadFile, 0, len(result.Files))
	if source == domain.SourceTorrent {
		multiFile := result.MultiFile
		download.IsMultiFile = &multiFile
		for _, file := range result.Files {
			filesToCreate = append(filesToCreate, domain.DownloadFile{DownloadHash: result.Hash, Index: file.Index, RelativePath: file.RelativePath, Size: file.Size})
		}
	}
	s.prepareRetainedContent(ctx, &download, filesToCreate)
	if stopped {
		download.State = domain.StateStopped
		download.NextRunAt = nil
	}
	created, inserted, err := s.repo.CreateSubmission(ctx, domain.Submission{Download: download, Files: filesToCreate})
	if err != nil {
		return domain.Download{}, false, fmt.Errorf("create submission: %w", err)
	}
	if inserted {
		s.waker.Wake()
	}
	return created, inserted, nil
}

// prepareRetainedContent revives a DELETED row's verified local content when a
// fresh submission matches its frozen save path. Torrent submissions verify
// their parsed manifest while preserving the new logical metadata; magnets
// retain the previous completed name and observed shape because they carry no
// manifest. Any lookup, verification, or path mismatch leaves the submission
// untouched.
func (s *Service) prepareRetainedContent(ctx context.Context, download *domain.Download, files []domain.DownloadFile) {
	existing, err := s.repo.GetDownload(ctx, download.Hash)
	if err != nil || existing.State != domain.StateDeleted || existing.DeleteFilesRequested ||
		existing.ContentPath == "" || existing.CloudResultPath == "" || existing.CopySourcePath == "" ||
		existing.IsMultiFile == nil || filepath.Clean(existing.SavePath) != filepath.Clean(download.SavePath) {
		return
	}
	expected := fsafe.ExpectedContent{}
	if existing.DestinationName != "" {
		if !safeRetainedName(existing.DestinationName) {
			return
		}
		expected.CandidateName = existing.DestinationName
	} else {
		candidate := filepath.Clean(existing.ContentPath)
		if filepath.Dir(candidate) != filepath.Clean(existing.SavePath) || !safeRetainedName(filepath.Base(candidate)) {
			return
		}
		expected.CandidateName = filepath.Base(candidate)
	}
	if download.SourceKind == domain.SourceTorrent {
		expected.MultiFile = *download.IsMultiFile
		expected.Files = make([]fsafe.ExpectedFile, 0, len(files))
		for _, file := range files {
			expected.Files = append(expected.Files, fsafe.ExpectedFile{RelativePath: file.RelativePath, Size: file.Size})
		}
	} else {
		expected.MultiFile = *existing.IsMultiFile
	}
	content, err := s.files.Verify(existing.SavePath, expected)
	if err != nil || filepath.Clean(content.Path) != filepath.Clean(existing.ContentPath) {
		return
	}
	download.DestinationName = expected.CandidateName
	if download.SourceKind == domain.SourceMagnet {
		multiFile := *existing.IsMultiFile
		download.Name = existing.Name
		download.IsMultiFile = &multiFile
		download.TotalSize = content.Size
	}
	download.CloudTaskName = existing.CloudTaskName
	download.CloudResultPath = existing.CloudResultPath
	download.CopySourcePath = existing.CopySourcePath
	download.ContentPath = content.Path
	download.State = domain.StateVerifyingLocal
	download.OfflineProgress = 1
	download.CopyProgress = 1
	download.QbitProgress = 0.99
	download.LastUpstreamStatus = domain.UpstreamRetainedContent
}

// CanonicalCategory applies the shared canonical category rules shared by the
// qBittorrent adapter and the native API: lowercase, trimmed, no separators,
// no control characters, and no dot components. allowEmpty permits the empty
// default category.
func CanonicalCategory(raw string, allowEmpty bool) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", allowEmpty
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", false
	}
	for _, char := range name {
		if char == 0 || char < 0x20 || char == 0x7f {
			return "", false
		}
	}
	return name, true
}
func safeRetainedName(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func oneMagnetLine(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	if len(lines) != 1 {
		return "", false
	}
	line := strings.TrimSpace(lines[0])
	return line, line != ""
}

func validCloudRoot(root string) bool {
	return path.IsAbs(root) && path.Clean(root) == root
}

func validLimits(limits torrentmeta.Limits) bool {
	return limits.MaxInputBytes > 0 && limits.MaxInfoBytes > 0 && limits.MaxFiles > 0 &&
		limits.MaxNameBytes > 0 && limits.MaxPathBytes > 0 && limits.MaxComponentBytes > 0 &&
		limits.MaxTrackerCount > 0 && limits.MaxTrackerBytes > 0 && limits.MaxTotalSize > 0
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	valueOf := reflect.ValueOf(value)
	switch valueOf.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return valueOf.IsNil()
	default:
		return false
	}
}
