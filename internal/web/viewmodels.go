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
	Hash             string
	HashPrefix       string
	Name             string
	Category         string
	InternalState    string
	StateLabel       string
	StateFullLabel   string
	ProjectedState   string
	Projected        string
	Offline          string
	Copy             string
	Age              string
	Duration         string
	Route            RouteView
	Error            string
	RetryScheduledAt string
	ErrorIsWarning   bool
	CloudResultPath  string
	CopySourcePath   string
	CanPause         bool
	CanResume        bool
	CanRetry         bool
	CanRemove        bool
	// RowVersion is the durable domain.Download.RowVersion. The live-update
	// client keys rows by Hash and replaces a row only when RowVersion rises.
	RowVersion int64
	// Terminal mirrors download.State.Terminal() so the client can classify a
	// terminal confirmation without duplicating state logic.
	Terminal bool
	// CSRFToken, ReturnPath, and Str carry the page context required by the
	// self-contained "download-row" fragment (same markup on the full page and
	// in the updates JSON).
	CSRFToken  string
	ReturnPath string
	Str        *Strings
}

type RouteView struct {
	Stages    []RouteStage
	State     string
	ListLabel string
	ListTitle string
}

type RouteStage struct {
	Label  string
	Status string
	Class  string
}

type DetailView struct {
	PageMeta
	Hash             string
	Name             string
	Category         string
	CloudFolder      string
	SavePath         string
	CloudResultPath  string
	CopySourcePath   string
	ContentPath      string
	InternalState    string
	StateLabel       string
	ProjectedState   string
	Projected        string
	Offline          string
	Copy             string
	CreatedAt        string
	UpdatedAt        string
	PhaseStartedAt   string
	TotalDuration    string
	CompletedAt      string
	NextRunAt        string
	RetryScheduledAt string
	AttemptCount     int64
	Error            string
	ErrorIsWarning   bool
	Route            RouteView
	Files            []FileView
	CanStart         bool
	CanRetry         bool
	CanPause         bool
	CanRemove        bool
	// RowVersion mirrors the durable domain.Download.RowVersion and Terminal
	// mirrors download.State.Terminal(); the detail live region carries both
	// so the client never regresses to an older snapshot.
	RowVersion int64
	Terminal   bool
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
	QBTAPIKey  QBTAPIKeyView
}

// APITokenView renders the Automation API token on every authenticated
// Settings page visit. Secret is empty only for legacy rows migrated from the
// digest-only schema.
type APITokenView struct {
	Configured  bool
	Secret      string
	GeneratedAt string
	RowVersion  int64
}

// QBTAPIKeyView renders the qBittorrent API key on every authenticated
// Settings page visit. Secret is empty only for legacy rows migrated from the
// digest-only schema.
type QBTAPIKeyView struct {
	Configured  bool
	Secret      string
	GeneratedAt string
	RowVersion  int64
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
	page.Path = downloadListURL(options, page.PageNumber)
	for index := range page.Rows {
		page.Rows[index].CSRFToken = csrfToken
		page.Rows[index].ReturnPath = page.Path
	}
	return page, nil
}
func buildDownloadRow(download domain.Download, projection domain.Projection, now time.Time, str *Strings) (DownloadRow, error) {
	if len(download.Hash) < 8 {
		return DownloadRow{}, errors.New("download hash is too short")
	}
	message, warning := problemError(download, str)
	return DownloadRow{
		Hash:             download.Hash,
		HashPrefix:       download.Hash[:8],
		Name:             download.Name,
		Category:         displayCategory(download.Category, str),
		InternalState:    string(download.State),
		StateLabel:       displayDownloadState(download, str.CompactStates),
		StateFullLabel:   displayDownloadState(download, str.States),
		ProjectedState:   projection.State,
		Projected:        percent(projection.Progress),
		Offline:          percent(download.OfflineProgress),
		Copy:             percent(download.CopyProgress),
		Age:              displayAge(now, download.UpdatedAt, str),
		Duration:         displayDownloadDuration(download, now, str),
		Route:            buildRoute(download, str),
		Error:            message,
		RetryScheduledAt: retryScheduledAt(download, str),
		ErrorIsWarning:   warning,
		CloudResultPath:  displayPath(download.CloudResultPath, str),
		CopySourcePath:   displayPath(download.CopySourcePath, str),
		CanPause:         canPause(download),
		CanResume:        download.State == domain.StateStopped,
		CanRetry:         canRetry(download),
		CanRemove:        download.State.Visible(),
		RowVersion:       download.RowVersion,
		Terminal:         download.State.Terminal(),
		Str:              str,
	}, nil
}

func buildDetailView(download domain.Download, files []domain.DownloadFile, csrfToken string, lang Lang, now time.Time) (DetailView, error) {
	projection, err := domain.Project(download)
	if err != nil {
		return DetailView{}, err
	}
	str := tr(lang)
	message, warning := problemError(download, str)
	page := DetailView{
		PageMeta:         pageMeta(download.Name, "downloads", csrfToken, lang),
		Hash:             download.Hash,
		Name:             download.Name,
		Category:         displayCategory(download.Category, str),
		CloudFolder:      download.CloudFolder,
		SavePath:         download.SavePath,
		CloudResultPath:  displayPath(download.CloudResultPath, str),
		CopySourcePath:   displayPath(download.CopySourcePath, str),
		ContentPath:      displayPath(download.ContentPath, str),
		InternalState:    string(download.State),
		StateLabel:       displayDownloadState(download, str.States),
		ProjectedState:   projection.State,
		Projected:        percent(projection.Progress),
		Offline:          percent(download.OfflineProgress),
		Copy:             percent(download.CopyProgress),
		CreatedAt:        displayTime(download.CreatedAt, str),
		UpdatedAt:        displayTime(download.UpdatedAt, str),
		PhaseStartedAt:   displayTime(download.PhaseStartedAt, str),
		TotalDuration:    displayDownloadDuration(download, now, str),
		CompletedAt:      displayOptionalTime(download.CompletedAt, str.NotCompleted, str),
		NextRunAt:        displayOptionalTime(download.NextRunAt, str.NotScheduled, str),
		RetryScheduledAt: retryScheduledAt(download, str),
		AttemptCount:     download.AttemptCount,
		Error:            message,
		ErrorIsWarning:   warning,
		Route:            buildRoute(download, str),
		CanStart:         download.State == domain.StateStopped,
		CanRetry:         canRetry(download),
		CanPause:         canPause(download),
		CanRemove:        download.State.Visible(),
		RowVersion:       download.RowVersion,
		Terminal:         download.State.Terminal(),
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
	taskLabel := displayDownloadState(download, str.CompactStates)
	taskTitle := displayDownloadState(download, str.States)
	stages := []RouteStage{
		{Label: str.Stage115, Status: str.StatusPending, Class: "is-unselected"},
		{Label: str.StageCopy, Status: str.StatusPending, Class: "is-unselected"},
		{Label: str.StageVerify, Status: str.StatusPending, Class: "is-unselected"},
	}
	listLabel, listTitle := taskLabel, taskTitle
	switch download.State {
	case domain.StateAccepted, domain.StateSubmittingOffline, domain.StateWaitingOffline:
		stages[0].Class = "is-current"
		stages[1].Class = "is-pending"
		stages[2].Class = "is-pending"
		stages[0].Status = fmt.Sprintf("%d%%", offline)
		listLabel = fmt.Sprintf("%s · %d%%", str.Stage115, offline)
		listTitle = fmt.Sprintf("%s · %d%% · %s", str.Stage115, offline, taskTitle)
	case domain.StateSubmittingCopy, domain.StateWaitingCopy:
		stages[0].Class = "is-passed"
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-current"
		stages[2].Class = "is-pending"
		stages[1].Status = fmt.Sprintf("%d%%", copyProgress)
		listLabel = fmt.Sprintf("%s · %d%%", str.StageCopy, copyProgress)
		listTitle = fmt.Sprintf("%s · %d%% · %s", str.StageCopy, copyProgress, taskTitle)
	case domain.StateVerifyingLocal:
		stages[0].Class = "is-passed"
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-passed"
		stages[1].Status = str.StatusCopyPresent
		stages[2].Class = "is-current"
		stages[2].Status = str.StatusChecking
		listLabel = fmt.Sprintf("%s · %s", str.StageVerify, str.StatusChecking)
		listTitle = fmt.Sprintf("%s · %s · %s", str.StageVerify, str.StatusChecking, taskTitle)
	case domain.StateCompleted:
		stages[0].Class = "is-passed"
		stages[0].Status = str.StatusSourceReady
		stages[1].Class = "is-passed"
		stages[1].Status = str.StatusCopyPresent
		stages[2].Class = "is-passed"
		stages[2].Status = str.StatusVerified
	}
	return RouteView{Stages: stages, State: string(download.State), ListLabel: listLabel, ListTitle: listTitle}
}

func displayDownloadState(download domain.Download, labels StateLabels) string {
	if download.State == domain.StateCancelRequested && download.PauseRequested {
		return labels.Pausing
	}
	return displayState(download.State, labels)
}

func displayState(state domain.State, labels StateLabels) string {
	switch state {
	case domain.StateAccepted:
		return labels.Accepted
	case domain.StateStopped:
		return labels.Stopped
	case domain.StateSubmittingOffline:
		return labels.SubmittingOffline
	case domain.StateWaitingOffline:
		return labels.WaitingOffline
	case domain.StateSubmittingCopy:
		return labels.SubmittingCopy
	case domain.StateWaitingCopy:
		return labels.WaitingCopy
	case domain.StateVerifyingLocal:
		return labels.VerifyingLocal
	case domain.StateCompleted:
		return labels.Completed
	case domain.StateFailed:
		return labels.Failed
	case domain.StateCancelRequested:
		return labels.CancelRequested
	case domain.StateCancelled:
		return labels.Cancelled
	case domain.StateDeleteRequested:
		return labels.DeleteRequested
	case domain.StateDeleted:
		return labels.Deleted
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
	case download.State == domain.StateFailed &&
		(download.LastErrorCode == string(domain.ProblemDestinationConflict) ||
			download.LastErrorCode == string(domain.ProblemDestinationCollision)):
		return domain.StateSubmittingCopy
	case download.State == domain.StateFailed &&
		(download.LastErrorCode == string(domain.ProblemLocalVerificationFailed) ||
			download.LastErrorCode == string(domain.ProblemLocalDeleteFailed)):
		return domain.StateSubmittingCopy
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
	case status == domain.UpstreamOfflineFinished || download.CloudResultPath != "":
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

// problemError renders the operator-facing problem for a download. Known
// durable problem codes are localized; a known problem on a non-terminal
// download with a scheduled retry is a warning whose text states that CD211
// retries automatically and shows the next retry time. Legacy or unknown
// codes fall back to the sanitized stored text, preserving the message and
// severity of rows written before problem codes existed.
func problemError(download domain.Download, str *Strings) (message string, warning bool) {
	code := domain.ProblemCode(download.LastErrorCode)
	if !code.Valid() || code == domain.ProblemLegacy {
		errorText := domain.SanitizeDownloadError(download)
		if errorText == "" {
			return "", false
		}
		if errorText == domain.RedactedErrorText {
			return str.RedactedError, false
		}
		return errorText, false
	}
	message = str.Problems[code]
	if message == "" {
		message = domain.ProblemText(code)
	}
	if activeWorkflowState(download.State) && download.NextRunAt != nil {
		warning = true
	}
	return message, warning
}

func retryScheduledAt(download domain.Download, str *Strings) string {
	if activeWorkflowState(download.State) && download.NextRunAt != nil {
		return displayTime(*download.NextRunAt, str)
	}
	return ""
}

// activeWorkflowState reports whether the row is an active download workflow
// phase whose problem is retried automatically by the reconciler. Cleanup
// intent states (CANCEL_REQUESTED / DELETE_REQUESTED) keep their blocked-error
// presentation and Retry control even when they carry scheduled retry
// bookkeeping.
func activeWorkflowState(state domain.State) bool {
	switch state {
	case domain.StateAccepted, domain.StateSubmittingOffline, domain.StateWaitingOffline,
		domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal:
		return true
	default:
		return false
	}
}

func displayAge(now, updated time.Time, str *Strings) string {
	age := now.UTC().Sub(updated.UTC())
	if age < 0 {
		age = 0
	}
	seconds := int64(age / time.Second)
	switch {
	case seconds < 60:
		return fmt.Sprintf(str.AgeSecondFormat, seconds)
	case seconds < 60*60:
		return fmt.Sprintf(str.AgeMinuteFormat, seconds/60)
	case seconds < 24*60*60:
		return fmt.Sprintf(str.AgeHourFormat, seconds/(60*60))
	default:
		return fmt.Sprintf(str.AgeDayFormat, seconds/(24*60*60))
	}
}

func displayDownloadDuration(download domain.Download, now time.Time, str *Strings) string {
	if download.OfflineStartedAt == nil {
		return str.DurationUnavailable
	}
	end := now.UTC()
	if download.CopyCompletedAt != nil {
		end = download.CopyCompletedAt.UTC()
	}
	duration := end.Sub(download.OfflineStartedAt.UTC())
	if duration < 0 {
		duration = 0
	}
	seconds := int64(duration / time.Second)
	parts := make([]string, 0, 4)
	days, seconds := seconds/(24*60*60), seconds%(24*60*60)
	hours, seconds := seconds/(60*60), seconds%(60*60)
	minutes, seconds := seconds/60, seconds%60
	if days > 0 {
		parts = append(parts, fmt.Sprintf(str.DurationDayFormat, days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf(str.DurationHourFormat, hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf(str.DurationMinuteFormat, minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf(str.DurationSecondFormat, seconds))
	}
	return strings.Join(parts, " ")
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
