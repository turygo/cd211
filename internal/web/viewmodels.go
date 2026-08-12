package web

import (
	"errors"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

type PageMeta struct {
	Title     string
	ActiveNav string
	CSRFToken string
	Lang      Lang
	OtherLang Lang
	Path      string
	Str       *Strings
}

type LoginView struct {
	Title     string
	Error     string
	Lang      Lang
	OtherLang Lang
	Path      string
	Str       *Strings
}

type PasswordView struct {
	PageMeta
	Error   string
	Success bool
}

type DownloadsView struct {
	PageMeta
	Rows             []DownloadRow
	Views            []ViewOption
	Categories       []CategoryOption
	SelectedView     string
	SelectedCategory string
	Search           string
	TotalRows        int
	PageNumber       int
	TotalPages       int
	PageStart        int
	PageEnd          int
	HasPrevious      bool
	HasNext          bool
	PreviousURL      string
	NextURL          string
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
	StateLabel     string
	ProjectedState string
	Projected      string
	Offline        string
	Copy           string
	Age            string
	Error          string
	CloudSource    string
	ContentPath    string
	Route          RouteView
	CanPause       bool
	CanResume      bool
	CanRemove      bool
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
	StateLabel      string
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
	CanPause        bool
	CanRemove       bool
}

type FileView struct {
	Index int64
	Path  string
	Size  string
}

type CategoriesView struct {
	PageMeta
	Rows       []CategoryRow
	CloudRoot  string
	LocalRoot  string
	Onboarding bool
	Notice     string
}

type SettingsView struct {
	PageMeta
	Values     SettingsFormValues
	Categories []SettingsCategoryPath
	Notice     string
	Success    bool
	APIToken   APITokenView
}

// APITokenView renders the Automation API token lifecycle section of the
// Settings page. It never carries the plaintext secret or its digest; Hint
// and the timestamps are the only stored token details shown.
type APITokenView struct {
	Configured bool
	Hint       string
	CreatedAt  string
	UpdatedAt  string
	RowVersion int64
}

// APITokenSecretView renders the one-time token reveal page. It must only be
// served with Cache-Control: no-store and never linked, redirected, or cached.
type APITokenSecretView struct {
	PageMeta
	Secret string
}

// SettingsFormValues carries the prefilled settings form fields. CD2Password
// is always rendered empty; an empty submission keeps the stored password.
type SettingsFormValues struct {
	CD2Address     string
	CD2Username    string
	CD2Password    string
	CD2Insecure    bool
	CloudRoot      string
	LocalRoot      string
	OfflineTimeout string
	CopyTimeout    string
	VerifyTimeout  string
}

type SettingsCategoryPath struct {
	Name         string
	CloudSubpath string
	SaveSubpath  string
	Valid        bool
}

type CategoryRow struct {
	Name          string
	CloudSubpath  string
	SaveSubpath   string
	CloudFullPath string
	SaveFullPath  string
	Enabled       bool
	PathValid     bool
	CreatedAt     string
	UpdatedAt     string
}

func buildDownloadsView(downloads []domain.Download, categories []domain.Category, options downloadListOptions, csrfToken string, now time.Time, cloudOnline bool, lang Lang) (DownloadsView, error) {
	str := tr(lang)
	page := DownloadsView{
		PageMeta:         pageMeta(str.TitleDownloads, "downloads", csrfToken, lang),
		SelectedView:     options.View,
		SelectedCategory: options.Category,
		Search:           options.Search,
		Views: []ViewOption{
			{Value: "active", Label: str.ViewActive, Active: options.View == "active"},
			{Value: "completed", Label: str.ViewCompleted, Active: options.View == "completed"},
			{Value: "failed", Label: str.ViewFailed, Active: options.View == "failed"},
			{Value: "cancelled", Label: str.ViewCancelled, Active: options.View == "cancelled"},
			{Value: "all", Label: str.ViewAll, Active: options.View == "all"},
		},
	}
	if cloudOnline {
		page.CloudStatus = str.CloudOnline
		page.CloudStatusClass = "is-online"
	} else {
		page.CloudStatus = str.CloudUnavailable
		page.CloudStatusClass = "is-unavailable"
	}
	for _, category := range categories {
		page.Categories = append(page.Categories, CategoryOption{Name: category.Name, Selected: category.Name == options.Category})
	}
	search := strings.ToLower(options.Search)
	for _, download := range downloads {
		if download.UpdatedAt.IsZero() {
			return DownloadsView{}, errors.New("download updated time is zero")
		}
		if !downloadMatchesView(download, options.View) ||
			search != "" && !strings.Contains(strings.ToLower(download.Name), search) && !strings.Contains(download.Hash, search) {
			continue
		}
		projection, err := domain.Project(download)
		if err != nil {
			return DownloadsView{}, err
		}
		row, err := buildDownloadRow(download, projection, now, str)
		if err != nil {
			return DownloadsView{}, err
		}
		page.Rows = append(page.Rows, row)
	}
	page.TotalRows = len(page.Rows)
	page.TotalPages = max(1, (page.TotalRows+downloadPageSize-1)/downloadPageSize)
	page.PageNumber = min(options.Page, page.TotalPages)
	start := min((page.PageNumber-1)*downloadPageSize, page.TotalRows)
	end := min(start+downloadPageSize, page.TotalRows)
	if page.TotalRows > 0 {
		page.PageStart = start + 1
		page.PageEnd = end
	}
	page.Rows = page.Rows[start:end]
	page.HasPrevious = page.PageNumber > 1
	page.HasNext = page.PageNumber < page.TotalPages
	if page.HasPrevious {
		page.PreviousURL = downloadListURL(options, page.PageNumber-1)
	}
	if page.HasNext {
		page.NextURL = downloadListURL(options, page.PageNumber+1)
	}
	return page, nil
}

func buildDownloadRow(download domain.Download, projection domain.Projection, now time.Time, str *Strings) (DownloadRow, error) {
	if len(download.Hash) < 8 {
		return DownloadRow{}, errors.New("download hash is too short")
	}
	return DownloadRow{
		Hash:           download.Hash,
		HashPrefix:     download.Hash[:8],
		Name:           download.Name,
		Category:       displayCategory(download.Category, str),
		InternalState:  string(download.State),
		StateLabel:     displayDownloadState(download, str),
		ProjectedState: projection.State,
		Projected:      percent(projection.Progress),
		Offline:        percent(download.OfflineProgress),
		Copy:           percent(download.CopyProgress),
		Age:            displayAge(now, download.UpdatedAt),
		Error:          safeError(download, str),
		CloudSource:    displayPath(download.CloudSourcePath, str),
		ContentPath:    displayPath(download.ContentPath, str),
		Route:          buildRoute(download, str),
		CanPause:       canPause(download),
		CanResume:      download.State == domain.StateStopped,
		CanRemove:      download.State.Visible(),
	}, nil
}

func buildDetailView(download domain.Download, files []domain.DownloadFile, csrfToken string, lang Lang) (DetailView, error) {
	projection, err := domain.Project(download)
	if err != nil {
		return DetailView{}, err
	}
	str := tr(lang)
	page := DetailView{
		PageMeta:        pageMeta(download.Name, "downloads", csrfToken, lang),
		Hash:            download.Hash,
		Name:            download.Name,
		Category:        displayCategory(download.Category, str),
		CloudFolder:     download.CloudFolder,
		SavePath:        download.SavePath,
		CloudSourcePath: displayPath(download.CloudSourcePath, str),
		ContentPath:     displayPath(download.ContentPath, str),
		InternalState:   string(download.State),
		StateLabel:      displayDownloadState(download, str),
		ProjectedState:  projection.State,
		Projected:       percent(projection.Progress),
		Offline:         percent(download.OfflineProgress),
		Copy:            percent(download.CopyProgress),
		CreatedAt:       displayTime(download.CreatedAt, str),
		UpdatedAt:       displayTime(download.UpdatedAt, str),
		PhaseStartedAt:  displayTime(download.PhaseStartedAt, str),
		CompletedAt:     displayOptionalTime(download.CompletedAt, str.NotCompleted, str),
		NextRunAt:       displayOptionalTime(download.NextRunAt, str.NotScheduled, str),
		AttemptCount:    download.AttemptCount,
		Error:           safeError(download, str),
		Route:           buildRoute(download, str),
		CanStart:        download.State == domain.StateStopped,
		CanRetry:        canRetry(download),
		CanPause:        canPause(download),
		CanRemove:       download.State.Visible(),
	}
	for _, file := range files {
		page.Files = append(page.Files, FileView{Index: file.Index, Path: file.RelativePath, Size: byteSize(file.Size)})
	}
	return page, nil
}

func buildCategoriesView(categories []domain.Category, cloudRoot, localRoot, csrfToken string, onboarding bool, lang Lang) CategoriesView {
	str := tr(lang)
	page := CategoriesView{
		PageMeta:  pageMeta(str.TitleCategories, "categories", csrfToken, lang),
		CloudRoot: cloudRoot, LocalRoot: localRoot, Onboarding: onboarding,
	}
	for _, category := range categories {
		cloudSubpath, cloudOK := relativeCloudSubpath(cloudRoot, category.CloudPath)
		saveSubpath, saveOK := relativeLocalSubpath(localRoot, category.SavePath)
		page.Rows = append(page.Rows, CategoryRow{
			Name: category.Name, CloudSubpath: cloudSubpath, SaveSubpath: saveSubpath,
			CloudFullPath: category.CloudPath, SaveFullPath: category.SavePath,
			Enabled: category.Enabled, PathValid: cloudOK && saveOK,
			CreatedAt: displayTime(category.CreatedAt, str), UpdatedAt: displayTime(category.UpdatedAt, str),
		})
	}
	return page
}

func buildSettingsCategoryPaths(categories []domain.Category, cloudRoot, localRoot string) []SettingsCategoryPath {
	rows := make([]SettingsCategoryPath, 0, len(categories))
	for _, category := range categories {
		cloudSubpath, cloudOK := relativeCloudSubpath(cloudRoot, category.CloudPath)
		saveSubpath, saveOK := relativeLocalSubpath(localRoot, category.SavePath)
		rows = append(rows, SettingsCategoryPath{
			Name: category.Name, CloudSubpath: cloudSubpath, SaveSubpath: saveSubpath, Valid: cloudOK && saveOK,
		})
	}
	return rows
}

func relativeCloudSubpath(root, fullPath string) (string, bool) {
	if !strictCloudDescendant(root, fullPath) {
		return "", false
	}
	relative := strings.TrimPrefix(fullPath, root)
	relative = strings.TrimPrefix(relative, "/")
	return relative, relative != "" && path.Clean(relative) == relative
}

func relativeLocalSubpath(root, fullPath string) (string, bool) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(fullPath))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func pageMeta(title, activeNav, csrfToken string, lang Lang) PageMeta {
	return PageMeta{Title: title, ActiveNav: activeNav, CSRFToken: csrfToken, Lang: lang, OtherLang: otherLang(lang), Str: tr(lang)}
}

func buildRoute(download domain.Download, str *Strings) RouteView {
	offline := routePercent(download.OfflineProgress)
	copyProgress := routePercent(download.CopyProgress)
	stages := []RouteStage{
		{Label: str.Stage115, Progress: offline, Status: fmt.Sprintf("%d%%", offline), Class: "is-idle"},
		{Label: str.StageCopy, Progress: copyProgress, Status: fmt.Sprintf("%d%%", copyProgress), Class: "is-idle"},
		{Label: str.StageVerify, Progress: 0, Status: str.StatusPending, Class: "is-idle"},
	}
	switch download.State {
	case domain.StateAccepted, domain.StateSubmittingOffline, domain.StateWaitingOffline:
		stages[0].Class = "is-current"
		stages[0].Status += str.StatusActiveSuffix
		stages[1].Status = str.StatusQueued
	case domain.StateSubmittingCopy, domain.StateWaitingCopy:
		stages[0].Class = "is-passed"
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-current"
		stages[1].Status += str.StatusActiveSuffix
	case domain.StateVerifyingLocal:
		stages[0].Class = "is-passed"
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-passed"
		stages[1].Status = str.StatusCopyPresent
		stages[2].Class = "is-current"
		stages[2].Status = str.StatusChecking
	case domain.StateCompleted:
		stages[0].Class = "is-passed"
		stages[0].Progress = 100
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-passed"
		stages[1].Progress = 100
		stages[1].Status = str.StatusCopyPresent
		stages[2].Class = "is-verified"
		stages[2].Progress = 100
		stages[2].Status = str.StatusVerified
	default:
		stages[2].Class = "is-halted"
		stages[2].Status = displayDownloadState(download, str)
	}
	return RouteView{Stages: stages, Verified: download.State == domain.StateCompleted, State: string(download.State)}
}

func displayDownloadState(download domain.Download, str *Strings) string {
	if download.State == domain.StateCancelRequested && download.PauseRequested {
		return str.States.Pausing
	}
	return displayState(download.State, str)
}

func displayState(state domain.State, str *Strings) string {
	switch state {
	case domain.StateAccepted:
		return str.States.Accepted
	case domain.StateStopped:
		return str.States.Stopped
	case domain.StateSubmittingOffline:
		return str.States.SubmittingOffline
	case domain.StateWaitingOffline:
		return str.States.WaitingOffline
	case domain.StateSubmittingCopy:
		return str.States.SubmittingCopy
	case domain.StateWaitingCopy:
		return str.States.WaitingCopy
	case domain.StateVerifyingLocal:
		return str.States.VerifyingLocal
	case domain.StateCompleted:
		return str.States.Completed
	case domain.StateFailed:
		return str.States.Failed
	case domain.StateCancelRequested:
		return str.States.CancelRequested
	case domain.StateCancelled:
		return str.States.Cancelled
	case domain.StateDeleteRequested:
		return str.States.DeleteRequested
	case domain.StateDeleted:
		return str.States.Deleted
	default:
		return string(state)
	}
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

func canPause(download domain.Download) bool {
	if download.PauseRequested {
		return false
	}
	switch download.State {
	case domain.StateAccepted, domain.StateSubmittingOffline, domain.StateWaitingOffline,
		domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal:
		return true
	default:
		return false
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

func safeError(download domain.Download, str *Strings) string {
	errorText := domain.SanitizeDownloadError(download)
	if errorText == "" {
		return ""
	}
	if errorText == domain.RedactedErrorText {
		return str.RedactedError
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

func displayCategory(category string, str *Strings) string {
	if category == "" {
		return str.Uncategorized
	}
	return category
}

func displayPath(value string, str *Strings) string {
	if value == "" {
		return str.NotRecorded
	}
	return value
}

func displayTime(value time.Time, str *Strings) string {
	if value.IsZero() {
		return str.NotRecorded
	}
	return value.UTC().Format(time.RFC3339)
}

func displayOptionalTime(value *time.Time, missing string, str *Strings) string {
	if value == nil {
		return missing
	}
	return displayTime(*value, str)
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
