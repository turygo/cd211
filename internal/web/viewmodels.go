package web

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

type PageMeta struct {
	Title     string
	ActiveNav string
	CSRFToken string
}

type LoginView struct {
	Title string
	Error string
}

type DownloadsView struct {
	PageMeta
	Rows             []DownloadRow
	Views            []ViewOption
	Categories       []CategoryOption
	SelectedView     string
	SelectedCategory string
	CloudStatus      string
	CloudStatusClass string
}

type ViewOption struct {
	Value  string
	Label  string
	Active bool
}

type CategoryOption struct {
	Name     string
	Selected bool
}

type DownloadRow struct {
	Hash           string
	HashPrefix     string
	Name           string
	Category       string
	InternalState  string
	ProjectedState string
	Projected      string
	Offline        string
	Copy           string
	Age            string
	Error          string
	CloudSource    string
	ContentPath    string
	Route          RouteView
}

type RouteView struct {
	Stages   []RouteStage
	Verified bool
	State    string
}

type RouteStage struct {
	Label    string
	Progress int
	Status   string
	Class    string
}

type DetailView struct {
	PageMeta
	Hash            string
	Name            string
	Category        string
	CloudFolder     string
	SavePath        string
	CloudSourcePath string
	ContentPath     string
	InternalState   string
	ProjectedState  string
	Projected       string
	Offline         string
	Copy            string
	CreatedAt       string
	UpdatedAt       string
	PhaseStartedAt  string
	CompletedAt     string
	NextRunAt       string
	AttemptCount    int64
	Error           string
	Route           RouteView
	Files           []FileView
	CanStart        bool
	CanRetry        bool
	CanCancel       bool
	CanRemove       bool
}

type FileView struct {
	Index int64
	Path  string
	Size  string
}

type CategoriesView struct {
	PageMeta
	Rows []CategoryRow
}

type CategoryRow struct {
	Name      string
	CloudPath string
	SavePath  string
	Enabled   bool
	CreatedAt string
	UpdatedAt string
}

func buildDownloadsView(downloads []domain.Download, categories []domain.Category, selectedView, selectedCategory, csrfToken string, now time.Time, cloudOnline bool) (DownloadsView, error) {
	page := DownloadsView{
		PageMeta:         PageMeta{Title: "Transfer manifest", ActiveNav: "downloads", CSRFToken: csrfToken},
		SelectedView:     selectedView,
		SelectedCategory: selectedCategory,
		Views: []ViewOption{
			{Value: "active", Label: "Active", Active: selectedView == "active"},
			{Value: "completed", Label: "Completed", Active: selectedView == "completed"},
			{Value: "failed", Label: "Failed", Active: selectedView == "failed"},
			{Value: "cancelled", Label: "Cancelled", Active: selectedView == "cancelled"},
			{Value: "all", Label: "All records", Active: selectedView == "all"},
		},
	}
	if cloudOnline {
		page.CloudStatus = "CloudDrive2 online"
		page.CloudStatusClass = "is-online"
	} else {
		page.CloudStatus = "CloudDrive2 unavailable"
		page.CloudStatusClass = "is-unavailable"
	}
	for _, category := range categories {
		page.Categories = append(page.Categories, CategoryOption{Name: category.Name, Selected: category.Name == selectedCategory})
	}
	for _, download := range downloads {
		if download.UpdatedAt.IsZero() {
			return DownloadsView{}, errors.New("download updated time is zero")
		}
		projection, err := domain.Project(download)
		if err != nil {
			return DownloadsView{}, err
		}
		if !downloadMatchesView(download, selectedView) {
			continue
		}
		row, err := buildDownloadRow(download, projection, now)
		if err != nil {
			return DownloadsView{}, err
		}
		page.Rows = append(page.Rows, row)
	}
	return page, nil
}

func buildDownloadRow(download domain.Download, projection domain.Projection, now time.Time) (DownloadRow, error) {
	if len(download.Hash) < 8 {
		return DownloadRow{}, errors.New("download hash is too short")
	}
	return DownloadRow{
		Hash:           download.Hash,
		HashPrefix:     download.Hash[:8],
		Name:           download.Name,
		Category:       displayCategory(download.Category),
		InternalState:  string(download.State),
		ProjectedState: projection.State,
		Projected:      percent(projection.Progress),
		Offline:        percent(download.OfflineProgress),
		Copy:           percent(download.CopyProgress),
		Age:            displayAge(now, download.UpdatedAt),
		Error:          safeError(download),
		CloudSource:    displayPath(download.CloudSourcePath),
		ContentPath:    displayPath(download.ContentPath),
		Route:          buildRoute(download),
	}, nil
}

func buildDetailView(download domain.Download, files []domain.DownloadFile, csrfToken string) (DetailView, error) {
	projection, err := domain.Project(download)
	if err != nil {
		return DetailView{}, err
	}
	page := DetailView{
		PageMeta:        PageMeta{Title: download.Name, ActiveNav: "downloads", CSRFToken: csrfToken},
		Hash:            download.Hash,
		Name:            download.Name,
		Category:        displayCategory(download.Category),
		CloudFolder:     download.CloudFolder,
		SavePath:        download.SavePath,
		CloudSourcePath: displayPath(download.CloudSourcePath),
		ContentPath:     displayPath(download.ContentPath),
		InternalState:   string(download.State),
		ProjectedState:  projection.State,
		Projected:       percent(projection.Progress),
		Offline:         percent(download.OfflineProgress),
		Copy:            percent(download.CopyProgress),
		CreatedAt:       displayTime(download.CreatedAt),
		UpdatedAt:       displayTime(download.UpdatedAt),
		PhaseStartedAt:  displayTime(download.PhaseStartedAt),
		CompletedAt:     displayOptionalTime(download.CompletedAt),
		NextRunAt:       displayOptionalTime(download.NextRunAt),
		AttemptCount:    download.AttemptCount,
		Error:           safeError(download),
		Route:           buildRoute(download),
		CanStart:        download.State == domain.StateStopped,
		CanRetry:        canRetry(download),
		CanCancel:       canCancel(download.State),
		CanRemove:       download.State.Visible(),
	}
	for _, file := range files {
		page.Files = append(page.Files, FileView{Index: file.Index, Path: file.RelativePath, Size: byteSize(file.Size)})
	}
	return page, nil
}

func buildCategoriesView(categories []domain.Category, csrfToken string) CategoriesView {
	page := CategoriesView{PageMeta: PageMeta{Title: "Path registry", ActiveNav: "categories", CSRFToken: csrfToken}}
	for _, category := range categories {
		page.Rows = append(page.Rows, CategoryRow{
			Name: category.Name, CloudPath: category.CloudPath, SavePath: category.SavePath, Enabled: category.Enabled,
			CreatedAt: displayTime(category.CreatedAt), UpdatedAt: displayTime(category.UpdatedAt),
		})
	}
	return page
}

func buildRoute(download domain.Download) RouteView {
	offline := routePercent(download.OfflineProgress)
	copyProgress := routePercent(download.CopyProgress)
	stages := []RouteStage{
		{Label: "115 OFFLINE", Progress: offline, Status: fmt.Sprintf("%d%%", offline), Class: "is-idle"},
		{Label: "NAS COPY", Progress: copyProgress, Status: fmt.Sprintf("%d%%", copyProgress), Class: "is-idle"},
		{Label: "LOCAL VERIFY", Progress: 0, Status: "Pending", Class: "is-idle"},
	}
	switch download.State {
	case domain.StateAccepted, domain.StateSubmittingOffline, domain.StateWaitingOffline:
		stages[0].Class = "is-current"
		stages[0].Status += " · Active"
		stages[1].Status = "Queued"
	case domain.StateSubmittingCopy, domain.StateWaitingCopy:
		stages[0].Class = "is-passed"
		stages[0].Status = "Source ready"
		stages[1].Class = "is-current"
		stages[1].Status += " · Active"
	case domain.StateVerifyingLocal:
		stages[0].Class = "is-passed"
		stages[0].Status = "Source ready"
		stages[1].Class = "is-passed"
		stages[1].Status = "Copy present"
		stages[2].Class = "is-current"
		stages[2].Status = "Checking"
	case domain.StateCompleted:
		stages[0].Class = "is-passed"
		stages[0].Progress = 100
		stages[0].Status = "Source ready"
		stages[1].Class = "is-passed"
		stages[1].Progress = 100
		stages[1].Status = "Copy present"
		stages[2].Class = "is-verified"
		stages[2].Progress = 100
		stages[2].Status = "Verified"
	default:
		stages[2].Class = "is-halted"
		stages[2].Status = string(download.State)
	}
	return RouteView{Stages: stages, Verified: download.State == domain.StateCompleted, State: string(download.State)}
}

func validDownloadView(view string) bool {
	switch view {
	case "active", "completed", "failed", "cancelled", "all":
		return true
	default:
		return false
	}
}

func downloadMatchesView(download domain.Download, view string) bool {
	cleanupFailure := cleanupFailed(download)
	if !download.State.Visible() && !cleanupFailure {
		return false
	}
	switch view {
	case "active":
		return download.State != domain.StateCompleted && download.State != domain.StateFailed && download.State != domain.StateCancelled && !cleanupFailure
	case "completed":
		return download.State == domain.StateCompleted
	case "failed":
		return download.State == domain.StateFailed || cleanupFailure
	case "cancelled":
		return download.State == domain.StateCancelled
	case "all":
		return true
	default:
		return false
	}
}
func cleanupFailed(download domain.Download) bool {
	return (download.State == domain.StateCancelRequested || download.State == domain.StateDeleteRequested) && download.LastError != ""
}

func canRetry(download domain.Download) bool {
	return download.State == domain.StateFailed || cleanupFailed(download)
}

func retryTarget(download domain.Download) domain.State {
	status := strings.TrimPrefix(download.LastUpstreamStatus, domain.UpstreamCleanupCancelled+"|")
	switch {
	case download.State == domain.StateCancelRequested || download.State == domain.StateDeleteRequested:
		return download.State
	case download.ContentPath != "" || status == domain.UpstreamCopyCompleted:
		return domain.StateVerifyingLocal
	case status == domain.UpstreamCopyPending ||
		status == domain.UpstreamCopyScanning ||
		status == domain.UpstreamCopyScanned:
		return domain.StateWaitingCopy
	case status == domain.UpstreamOfflineError:
		return domain.StateSubmittingOffline
	case strings.HasPrefix(status, "copy:"):
		return domain.StateSubmittingCopy
	case status == domain.UpstreamOfflineFinished || download.CloudSourcePath != "":
		return domain.StateSubmittingCopy
	case strings.HasPrefix(status, "offline:"):
		return domain.StateWaitingOffline
	default:
		return domain.StateSubmittingOffline
	}
}

func canCancel(state domain.State) bool {
	switch state {
	case domain.StateAccepted, domain.StateStopped, domain.StateSubmittingOffline, domain.StateWaitingOffline,
		domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal, domain.StateCancelRequested:
		return true
	default:
		return false
	}
}

func safeError(download domain.Download) string {
	errorText := strings.TrimSpace(download.LastError)
	if errorText == "" {
		return ""
	}
	lower := strings.ToLower(errorText)
	if (download.SubmissionURI != "" && strings.Contains(errorText, download.SubmissionURI)) ||
		strings.Contains(lower, "magnet:") || strings.Contains(lower, "tracker") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "sid=") {
		return "Protected upstream details were redacted."
	}
	return errorText
}

func displayAge(now, updated time.Time) string {
	age := now.UTC().Sub(updated.UTC())
	if age < 0 {
		age = 0
	}
	seconds := int64(age / time.Second)
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 60*60:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 24*60*60:
		return fmt.Sprintf("%dh", seconds/(60*60))
	default:
		return fmt.Sprintf("%dd", seconds/(24*60*60))
	}
}

func percent(value float64) string {
	return fmt.Sprintf("%d%%", routePercent(value))
}

func routePercent(value float64) int {
	return int(math.Round(math.Max(0, math.Min(1, value)) * 100))
}

func displayCategory(category string) string {
	if category == "" {
		return "Uncategorized"
	}
	return category
}

func displayPath(value string) string {
	if value == "" {
		return "Not recorded"
	}
	return value
}

func displayTime(value time.Time) string {
	if value.IsZero() {
		return "Not recorded"
	}
	return value.UTC().Format(time.RFC3339)
}

func displayOptionalTime(value *time.Time) string {
	if value == nil {
		return "Not scheduled"
	}
	return displayTime(*value)
}

func byteSize(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%d B", value)
}
