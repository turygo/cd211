// Package domain defines dependency-free durable download concepts.
package domain

import "time"

// Persisted upstream statuses are shared by reconciliation and retry routing.
const (
	UpstreamOfflineInit        = "offline:INIT"
	UpstreamOfflineDownloading = "offline:DOWNLOADING"
	UpstreamOfflineFinished    = "offline:FINISHED"
	UpstreamOfflineError       = "offline:ERROR"
	UpstreamCopyPending        = "copy:PENDING"
	UpstreamCopyScanning       = "copy:SCANNING"
	UpstreamCopyScanned        = "copy:SCANNED"
	UpstreamCopyCompleted      = "copy:COMPLETED"
	UpstreamCopyFailed         = "copy:FAILED"
	UpstreamRetainedContent    = "revive:retained_content"
	UpstreamCleanupCancelled   = "cleanup:cancelled"
)

// Category identifies a configured destination category.
type Category struct {
	Name      string
	CloudPath string
	SavePath  string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DownloadFile is one file in a torrent submission, ordered by Index.
type DownloadFile struct {
	DownloadHash string
	Index        int64
	RelativePath string
	Size         int64
	Priority     int64
}

// FileOverride stores the durable qBittorrent-visible path and priority.
type FileOverride struct {
	DownloadHash string
	FileIndex    int64
	RelativePath string
	Priority     int64
}

// Download is the durable state of a submitted download.
type Download struct {
	Hash           string
	Name           string
	NameOverridden bool
	SourceKind     SourceKind
	SubmissionURI  string
	Category       string
	Tags           string
	AutoTMM        bool
	CloudFolder    string
	SavePath       string
	// WorkspacePath is the isolated physical workspace. Empty means this is
	// an intentional legacy shared-layout row.
	WorkspacePath        string
	DestinationName      string
	CloudTaskName        string
	CloudResultPath      string
	CopySourcePath       string
	ContentPath          string
	IsMultiFile          *bool
	TotalSize            int64
	State                State
	OfflineProgress      float64
	CopyProgress         float64
	QbitProgress         float64
	LastUpstreamStatus   string
	LastError            string
	LastErrorCode        string
	PhaseStartedAt       time.Time
	OfflineStartedAt     *time.Time
	CopyCompletedAt      *time.Time
	NextRunAt            *time.Time
	LeaseUntil           *time.Time
	LeaseOwner           string
	AttemptCount         int64
	DeleteFilesRequested bool
	PauseRequested       bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CompletedAt          *time.Time
	RemovedAt            *time.Time
	RowVersion           int64
}

// Submission groups a download and its ordered files for an atomic insert.
type Submission struct {
	Download Download
	Files    []DownloadFile
}
