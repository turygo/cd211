package domain

import (
	"strings"
	"testing"
	"time"
)

func TestProjectEveryState(t *testing.T) {
	completedAt := projectionTime()
	tests := []struct {
		state       State
		qbitState   string
		progress    float64
		eta         int64
		contentPath string
		projectable bool
	}{
		{StateAccepted, "queuedDL", 0, unknownETA, "", true},
		{StateStopped, "stoppedDL", 0, unknownETA, "", true},
		{StateSubmittingOffline, "metaDL", 0, unknownETA, "", true},
		{StateWaitingOffline, "downloading", 0.90 * 0.40, unknownETA, "", true},
		{StateSubmittingCopy, "moving", 0.90, unknownETA, "", true},
		{StateWaitingCopy, "moving", 0.90 + 0.09*0.60, unknownETA, "", true},
		{StateVerifyingLocal, "moving", 0.99, unknownETA, "", true},
		{StateCompleted, "stoppedUP", 1, 0, "/downloads/release", true},
		{StateFailed, "error", 0.25, unknownETA, "", true},
		{StateCancelRequested, "stoppedDL", 0.25, unknownETA, "", true},
		{StateCancelled, "stoppedDL", 0.25, unknownETA, "", true},
		{StateDeleteRequested, "", 0, 0, "", false},
		{StateDeleted, "", 0, 0, "", false},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			download := validDownload(test.state)
			if test.state == StateCompleted {
				download.ContentPath = test.contentPath
				download.CompletedAt = &completedAt
			}

			projection, err := Project(download)
			if !test.projectable {
				if err == nil {
					t.Fatal("Project() error = nil, want hidden-state error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if projection.State != test.qbitState || projection.Progress != test.progress || projection.ETA != test.eta {
				t.Errorf("projection state/progress/ETA = %q/%v/%d, want %q/%v/%d", projection.State, projection.Progress, projection.ETA, test.qbitState, test.progress, test.eta)
			}
			if projection.ContentPath != test.contentPath {
				t.Errorf("ContentPath = %q, want %q", projection.ContentPath, test.contentPath)
			}
			if projection.Hash != download.Hash || projection.Name != download.Name || projection.Size != download.TotalSize || projection.Category != download.Category {
				t.Error("projection did not preserve torrent identity fields")
			}
			if projection.SavePath != "/downloads/" {
				t.Errorf("SavePath = %q, want one trailing separator", projection.SavePath)
			}
			if projection.Ratio != 0 || projection.RatioLimit != -1 || projection.SeedingTime != 0 || projection.SeedingTimeLimit != -1 || projection.InactiveSeedingTimeLimit != -1 {
				t.Error("projection has incorrect unsupported ratio or seeding values")
			}
			if projection.LastActivity != download.UpdatedAt.Unix() {
				t.Errorf("LastActivity = %d, want %d", projection.LastActivity, download.UpdatedAt.Unix())
			}
		})
	}
}

func TestProjectFailedCleanupAsError(t *testing.T) {
	for _, state := range []State{StateCancelRequested, StateDeleteRequested} {
		t.Run(string(state), func(t *testing.T) {
			download := validDownload(state)
			download.LastError = "cleanup failed"
			projection, err := Project(download)
			if err != nil {
				t.Fatal(err)
			}
			if projection.State != "error" || projection.Progress != 0.25 {
				t.Fatalf("failed cleanup projection = %+v", projection)
			}
		})
	}
}

func TestValidateDownloadCompletionAndPathInvariants(t *testing.T) {
	completedAt := projectionTime()
	tests := []struct {
		name   string
		mutate func(*Download)
	}{
		{
			name: "completed requires content path",
			mutate: func(download *Download) {
				download.State = StateCompleted
				download.CompletedAt = &completedAt
			},
		},
		{
			name: "completed requires completed at",
			mutate: func(download *Download) {
				download.State = StateCompleted
				download.ContentPath = "/downloads/release"
			},
		},
		{
			name: "non completed rejects completed at",
			mutate: func(download *Download) {
				download.CompletedAt = &completedAt
			},
		},
		{
			name: "content path differs from save path after cleaning",
			mutate: func(download *Download) {
				download.ContentPath = "/downloads/./"
			},
		},
		{
			name: "copy states require copy source path",
			mutate: func(download *Download) {
				download.State = StateWaitingCopy
				download.CopySourcePath = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			download := validDownload(StateAccepted)
			test.mutate(&download)
			if err := ValidateDownload(download); err == nil {
				t.Fatal("ValidateDownload() error = nil, want invariant error")
			}
		})
	}
}

func TestValidateDownloadRetainsCompletionEvidenceDuringDeletion(t *testing.T) {
	completedAt := projectionTime()
	for _, state := range []State{StateDeleteRequested, StateDeleted} {
		download := validDownload(StateAccepted)
		download.State = state
		download.ContentPath = "/downloads/release"
		download.CompletedAt = &completedAt
		if err := ValidateDownload(download); err != nil {
			t.Fatalf("ValidateDownload(%s): %v", state, err)
		}
	}
}

func TestValidateDownloadRejectsInvalidDurableFieldsAndRedactsSubmissionURI(t *testing.T) {
	secretURI := "magnet:?xt=urn:btih:private-source"
	tests := []struct {
		name   string
		mutate func(*Download)
	}{
		{"submission URI", func(download *Download) { download.SubmissionURI = "" }},
		{"hash", func(download *Download) { download.Hash = strings.Repeat("A", 40) }},
		{"name", func(download *Download) { download.Name = "bad/name" }},
		{"source kind", func(download *Download) { download.SourceKind = "file" }},
		{"state", func(download *Download) { download.State = "UNKNOWN" }},
		{"category", func(download *Download) { download.Category = ".." }},
		{"cloud folder", func(download *Download) { download.CloudFolder = "relative" }},
		{"save path", func(download *Download) { download.SavePath = "relative" }},
		{"cloud result path", func(download *Download) { download.CloudResultPath = "relative" }},
		{"content path", func(download *Download) { download.ContentPath = "relative" }},
		{"cloud task name", func(download *Download) { download.CloudTaskName = "bad\nname" }},
		{"total size", func(download *Download) { download.TotalSize = -1 }},
		{"attempt count", func(download *Download) { download.AttemptCount = -1 }},
		{"row version", func(download *Download) { download.RowVersion = -1 }},
		{"offline progress", func(download *Download) { download.OfflineProgress = 1.01 }},
		{"copy progress", func(download *Download) { download.CopyProgress = -0.01 }},
		{"qbit progress", func(download *Download) { download.QbitProgress = 1.01 }},
		{"created at", func(download *Download) { download.CreatedAt = time.Time{} }},
		{"updated at", func(download *Download) { download.UpdatedAt = time.Time{} }},
		{"phase started at", func(download *Download) { download.PhaseStartedAt = time.Time{} }},
		{"lease pairing", func(download *Download) { download.LeaseOwner = "worker" }},
		{"lease until without owner", func(download *Download) {
			leaseUntil := projectionTime()
			download.LeaseUntil = &leaseUntil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			download := validDownload(StateAccepted)
			download.SubmissionURI = secretURI
			test.mutate(&download)
			err := ValidateDownload(download)
			if err == nil {
				t.Fatal("ValidateDownload() error = nil, want invariant error")
			}
			if strings.Contains(err.Error(), secretURI) {
				t.Fatalf("ValidateDownload() leaked SubmissionURI in error: %q", err)
			}
		})
	}
}

func validDownload(state State) Download {
	now := projectionTime()
	download := Download{
		Hash:            "0123456789abcdef0123456789abcdef01234567",
		Name:            "release",
		SourceKind:      SourceMagnet,
		SubmissionURI:   "magnet:?xt=urn:btih:source",
		Category:        "linux",
		CloudFolder:     "/cloud/releases",
		SavePath:        "/downloads",
		CloudTaskName:   "copy-release",
		CloudResultPath: "/cloud/releases/release",
		CopySourcePath:  "/cloud/releases/release",
		TotalSize:       42,
		State:           state,
		OfflineProgress: 0.40,
		CopyProgress:    0.60,
		QbitProgress:    0.25,
		PhaseStartedAt:  now,
		CreatedAt:       now,
		UpdatedAt:       now,
		AttemptCount:    2,
		RowVersion:      3,
	}
	return download
}

func projectionTime() time.Time {
	return time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
}
