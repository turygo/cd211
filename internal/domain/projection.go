package domain

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const unknownETA int64 = 8_640_000

// Projection is the qBittorrent-compatible view of a download.
type Projection struct {
	Hash                     string
	Name                     string
	Size                     int64
	Completed                int64
	Progress                 float64
	ETA                      int64
	State                    string
	Category                 string
	Tags                     string
	SavePath                 string
	ContentPath              string
	Ratio                    float64
	RatioLimit               float64
	SeedingTime              int64
	SeedingTimeLimit         int64
	InactiveSeedingTimeLimit int64
	LastActivity             int64
}

// ValidateDownload verifies the invariants required before persisting a download.
func ValidateDownload(download Download) error {
	if !validHash(download.Hash) {
		return errors.New("hash is invalid")
	}
	if !safeComponent(download.Name) {
		return errors.New("name is invalid")
	}
	if download.SubmissionURI == "" {
		return errors.New("submission URI is required")
	}
	if !download.SourceKind.Valid() {
		return errors.New("source kind is invalid")
	}
	if !download.State.Valid() {
		return errors.New("state is invalid")
	}
	if download.Category != "" && !safeComponent(download.Category) {
		return errors.New("category is invalid")
	}
	if !absolutePath(download.CloudFolder) {
		return errors.New("cloud folder must be an absolute path")
	}
	if !absolutePath(download.SavePath) {
		return errors.New("save path must be an absolute path")
	}
	if download.DestinationName != "" && !safeComponent(download.DestinationName) {
		return errors.New("destination name is invalid")
	}
	if download.CloudResultPath != "" && !absolutePath(download.CloudResultPath) {
		return errors.New("cloud result path must be an absolute path")
	}
	if download.CopySourcePath != "" && !absolutePath(download.CopySourcePath) {
		return errors.New("copy source path must be an absolute path")
	}
	if download.ContentPath != "" && !absolutePath(download.ContentPath) {
		return errors.New("content path must be an absolute path")
	}
	if download.ContentPath != "" && filepath.Clean(download.ContentPath) == filepath.Clean(download.SavePath) {
		return errors.New("content path must differ from save path")
	}
	if !validTaskName(download.CloudTaskName) {
		return errors.New("cloud task name is invalid")
	}
	if download.TotalSize < 0 {
		return errors.New("total size must not be negative")
	}
	if download.AttemptCount < 0 {
		return errors.New("attempt count must not be negative")
	}
	if !safeProblemCode(download.LastErrorCode) {
		return errors.New("last error code is invalid")
	}
	if download.RowVersion < 0 {
		return errors.New("row version must not be negative")
	}
	if !progress(download.OfflineProgress) {
		return errors.New("offline progress is invalid")
	}
	if !progress(download.CopyProgress) {
		return errors.New("copy progress is invalid")
	}
	if !progress(download.QbitProgress) {
		return errors.New("qbit progress is invalid")
	}
	if download.CreatedAt.IsZero() {
		return errors.New("created at is required")
	}
	if download.UpdatedAt.IsZero() {
		return errors.New("updated at is required")
	}
	if download.PhaseStartedAt.IsZero() {
		return errors.New("phase started at is required")
	}
	if (download.LeaseOwner == "") != (download.LeaseUntil == nil) {
		return errors.New("lease owner and expiry must be paired")
	}
	if requiresCloudResult(download.State) && download.CloudResultPath == "" {
		return errors.New("cloud result path is required for state")
	}
	if requiresCopySource(download.State) && download.CopySourcePath == "" {
		return errors.New("copy source path is required for state")
	}
	if download.State == StateCompleted {
		if download.ContentPath == "" {
			return errors.New("content path is required for completed state")
		}
		if download.CompletedAt == nil {
			return errors.New("completed at is required for completed state")
		}
	} else if download.State == StateDeleteRequested || download.State == StateDeleted {
		if download.CompletedAt != nil && download.ContentPath == "" {
			return errors.New("completed deletion requires content path")
		}
	} else if download.CompletedAt != nil {
		return errors.New("completed at is only allowed for completed or deletion state")
	}
	if len(download.Tags) > 64<<10 || strings.ContainsFunc(download.Tags, unicode.IsControl) {
		return errors.New("tags are invalid")
	}
	return nil
}

// Project maps a valid, visible download to its qBittorrent-compatible projection.
func Project(download Download) (Projection, error) {
	if err := ValidateDownload(download); err != nil {
		return Projection{}, err
	}
	cleanupFailure := download.State == StateDeleteRequested && download.LastError != ""
	if !download.State.Visible() && !cleanupFailure {
		return Projection{}, fmt.Errorf("state %s is not projectable", download.State)
	}

	projection := Projection{
		Hash:                     download.Hash,
		Name:                     download.Name,
		Size:                     download.TotalSize,
		ETA:                      unknownETA,
		Category:                 download.Category,
		SavePath:                 trailingSeparator(download.SavePath),
		Ratio:                    0,
		RatioLimit:               -1,
		SeedingTime:              0,
		SeedingTimeLimit:         -1,
		InactiveSeedingTimeLimit: -1,
		LastActivity:             download.UpdatedAt.Unix(),
	}

	switch download.State {
	case StateAccepted:
		projection.State = "queuedDL"
	case StateStopped:
		projection.State = "stoppedDL"
	case StateSubmittingOffline:
		projection.State = "metaDL"
	case StateWaitingOffline:
		projection.State = "downloading"
		projection.Progress = roundedProgress(0.90 * download.OfflineProgress)
	case StateSubmittingCopy:
		projection.State = "moving"
		projection.Progress = 0.90
	case StateWaitingCopy:
		projection.State = "moving"
		projection.Progress = roundedProgress(0.90 + 0.09*download.CopyProgress)
	case StateVerifyingLocal:
		projection.State = "moving"
		projection.Progress = 0.99
	case StateCompleted:
		projection.State = "stoppedUP"
		projection.Progress = 1
		projection.ETA = 0
		projection.ContentPath = download.ContentPath
	case StateFailed:
		projection.State = "error"
		projection.Progress = roundedProgress(download.QbitProgress)
	case StateCancelRequested:
		if download.LastError != "" {
			projection.State = "error"
		} else {
			projection.State = "stoppedDL"
		}
		projection.Progress = roundedProgress(download.QbitProgress)
	case StateCancelled:
		projection.State = "stoppedDL"
		projection.Progress = roundedProgress(download.QbitProgress)
	case StateDeleteRequested:
		projection.State = "error"
		projection.Progress = roundedProgress(download.QbitProgress)
	default:
		return Projection{}, fmt.Errorf("state %s is not projectable", download.State)
	}

	projection.Completed = completedBytes(projection.Size, projection.Progress)
	projection.Tags = download.Tags
	return projection, nil
}
func completedBytes(size int64, progress float64) int64 {
	if size <= 0 {
		return 0
	}
	progress = clamp(progress)
	completed := int64(float64(size) * progress)
	if completed < 0 {
		return 0
	}
	if completed > size {
		return size
	}
	return completed
}

func validHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	for _, character := range hash {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func safeComponent(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || filepath.IsAbs(value) {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validTaskName(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func absolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path)
}

func progress(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func requiresCloudResult(state State) bool {
	switch state {
	case StateSubmittingCopy, StateWaitingCopy, StateVerifyingLocal, StateCompleted:
		return true
	default:
		return false
	}
}

func requiresCopySource(state State) bool {
	switch state {
	case StateWaitingCopy, StateVerifyingLocal, StateCompleted:
		return true
	default:
		return false
	}
}

func roundedProgress(value float64) float64 {
	return math.Round(clamp(value)*1_000_000) / 1_000_000
}

func clamp(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func trailingSeparator(path string) string {
	cleaned := filepath.Clean(path)
	separator := string(filepath.Separator)
	if strings.HasSuffix(cleaned, separator) {
		return cleaned
	}
	return cleaned + separator
}
