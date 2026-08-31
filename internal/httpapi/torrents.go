package httpapi

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/logging"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
)

// addTorrent decodes the qBittorrent WebAPI add form and delegates the whole
// submission pipeline to the shared submission.Service.
func (h *handler) addTorrent(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, body, reason string) {
		logging.SetReason(r, reason)
		plainExact(w, status, body)
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxRequestBytes)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/x-www-form-urlencoded" && mediaType != "multipart/form-data") {
		fail(http.StatusBadRequest, "Bad Request", "invalid_request")
		return
	}
	if mediaType == "application/x-www-form-urlencoded" {
		if err := r.ParseForm(); err != nil {
			reason := "invalid_request"
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reason = "request_too_large"
			}
			fail(http.StatusBadRequest, "Bad Request", reason)
			return
		}
	} else if err := r.ParseMultipartForm(int64(h.config.TorrentLimits.MaxInputBytes)); err != nil {
		reason := "invalid_request"
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reason = "request_too_large"
		}
		fail(http.StatusBadRequest, "Bad Request", reason)
		return
	} else {
		defer r.MultipartForm.RemoveAll()
	}

	stopped, valid := stoppedSubmission(r.PostForm)
	if !valid {
		fail(http.StatusBadRequest, "Bad Request", "invalid_request")
		return
	}
	category, valid := addCategory(r.PostForm)
	if !valid {
		fail(http.StatusBadRequest, "Bad Request", "invalid_category")
		return
	}
	rawCategory := category
	logging.Enrich(r, map[string]any{"raw_category": rawCategory})
	category, valid = submission.CanonicalCategory(category, true)
	if !valid {
		fail(http.StatusBadRequest, "Bad Request", "invalid_category")
		return
	}
	logging.Enrich(r, map[string]any{"category": category})

	urlValues := r.PostForm["urls"]
	if len(urlValues) > 1 {
		fail(http.StatusBadRequest, "Bad Request", "invalid_request")
		return
	}
	var urls []string
	if len(urlValues) == 1 {
		for line := range strings.SplitSeq(urlValues[0], "\n") {
			if line = strings.TrimSpace(line); line != "" {
				urls = append(urls, line)
			}
		}
	}
	var torrentFiles []*multipart.FileHeader
	if r.MultipartForm != nil {
		for _, headers := range r.MultipartForm.File {
			torrentFiles = append(torrentFiles, headers...)
		}
	}
	if values, present := r.PostForm["rename"]; present {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(values[0], "/\\\x00") {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
	}
	if values, present := r.PostForm["tags"]; present {
		if len(values) != 1 {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
		if _, valid := normalizeTags(values[0], false); !valid {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
	}
	if values, present := r.PostForm["autoTMM"]; present {
		if len(values) != 1 {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
	}
	if values, present := r.PostForm["savepath"]; present {
		if len(values) != 1 {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
	}
	options := submission.Options{}
	if values, present := r.PostForm["rename"]; present {
		options.RenameSet, options.Rename = true, values[0]
	}
	if values, present := r.PostForm["tags"]; present {
		options.TagsSet, options.Tags = true, values[0]
	}
	if _, present := r.PostForm["autoTMM"]; present {
		enabled, _ := requiredBool(r.PostForm, "autoTMM")
		options.AutoTMMSet, options.AutoTMM = true, enabled
	}
	if values, present := r.PostForm["savepath"]; present {
		options.SavePathSet, options.SavePath = true, values[0]
	}

	var cookie string
	if values, present := r.PostForm["cookie"]; present {
		if len(values) != 1 {
			fail(http.StatusBadRequest, "Bad Request", "invalid_request")
			return
		}
		cookie = values[0]
	}

	successCount := 0
	failureCount := 0
	addedIDs := make([]string, 0, len(urls)+len(torrentFiles))
	record := func(download domain.Download, inserted bool, submitErr error, filename string) bool {
		if submitErr == nil {
			if inserted {
				successCount++
				addedIDs = append(addedIDs, download.Hash)
			} else {
				failureCount++
			}
			return true
		}
		if errors.Is(submitErr, submission.ErrCategoryInvalid) {
			fail(http.StatusConflict, "Conflict", "category_unavailable")
		} else if errors.Is(submitErr, submission.ErrInvalidOptions) {
			reason := "invalid_options"
			if options.SavePathSet {
				reason = "invalid_save_path"
			}
			fail(http.StatusConflict, "Conflict", reason)
		} else if errors.Is(submitErr, submission.ErrInvalidSource) {
			if filename == "" {
				failureCount++
				return true
			}
			fail(http.StatusUnsupportedMediaType, "Error: '"+logging.SanitizeFilename(filename)+"' is not a valid torrent file.", "invalid_torrent")
		} else {
			logging.Enrich(r, map[string]any{"error": submitErr.Error()})
			fail(http.StatusInternalServerError, "Internal Server Error", "internal_error")
		}
		return false
	}

	for _, rawURL := range urls {
		source, parseErr := url.ParseRequestURI(rawURL)
		if parseErr != nil {
			failureCount++
			continue
		}
		var download domain.Download
		var inserted bool
		var submitErr error
		switch strings.ToLower(source.Scheme) {
		case "magnet":
			logging.Enrich(r, logging.SanitizeMagnet(rawURL))
			download, inserted, submitErr = h.service.SubmitMagnetWithOptions(r.Context(), rawURL, category, stopped, options)
		case "http", "https":
			logging.Enrich(r, logging.SanitizeURL(rawURL))
			data, fetchErr := fetchTorrent(r.Context(), source, cookie, h.config.TorrentLimits.MaxInputBytes)
			if fetchErr != nil {
				failureCount++
				continue
			}
			download, inserted, submitErr = h.service.SubmitTorrentWithOptions(r.Context(), data, category, stopped, options)
		default:
			failureCount++
			continue
		}
		if !record(download, inserted, submitErr, "") {
			return
		}
	}

	for _, header := range torrentFiles {
		logging.Enrich(r, map[string]any{"source_kind": "torrent", "filename": logging.SanitizeFilename(header.Filename), "size": header.Size})
		data, readErr := readUpload(header)
		if readErr != nil {
			fail(http.StatusBadRequest, "Bad Request", "request_too_large")
			return
		}
		download, inserted, submitErr := h.service.SubmitTorrentWithOptions(r.Context(), data, category, stopped, options)
		if !record(download, inserted, submitErr, header.Filename) {
			return
		}
	}

	if successCount == 0 {
		logging.SetReason(r, "failed")
		plainExact(w, http.StatusOK, "Fails.")
		return
	}
	logging.Enrich(r, map[string]any{"success_count": successCount, "failure_count": failureCount})
	logging.SetReason(r, "accepted")
	plainExact(w, http.StatusOK, "Ok.")
}

func readUpload(header *multipart.FileHeader) ([]byte, error) {
	if header == nil {
		return nil, errors.New("missing upload")
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func fetchTorrent(ctx context.Context, source *url.URL, cookie string, limit int) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, err
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("torrent URL returned an unsuccessful response")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("torrent URL response is too large")
	}
	return data, nil
}

func addCategory(form map[string][]string) (string, bool) {
	values, present := form["category"]
	if !present {
		return "", true
	}
	return exactlyOne(values)
}

func stoppedSubmission(form map[string][]string) (bool, bool) {
	stopped, _ := optionalBool(form["stopped"])
	paused, _ := optionalBool(form["paused"])
	return stopped || paused, true
}

func optionalBool(values []string) (bool, bool) {
	if len(values) == 0 {
		return false, true
	}
	if len(values) != 1 {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "", "false", "0":
		return false, true
	case "true", "1":
		return true, true
	default:
		return false, false
	}
}

func (h *handler) info(w http.ResponseWriter, r *http.Request) {
	categoryValues, ok := readRequestValues(r, "category")
	if !ok || len(categoryValues) > 1 {
		badRequest(w)
		return
	}
	categoryValue := first(categoryValues)
	category, valid := submission.CanonicalCategory(categoryValue, true)
	if !valid {
		badRequest(w)
		return
	}
	var categoryFilter *string
	if len(categoryValues) == 1 {
		categoryFilter = &category
	}
	filter, ok := requestOne(r, "filter")
	if !ok {
		badRequest(w)
		return
	}
	tagValues, ok := readRequestValues(r, "tag")
	if !ok || len(tagValues) > 1 {
		badRequest(w)
		return
	}
	tag := first(tagValues)
	tagPresent := len(tagValues) == 1
	hashesRaw, ok := requestOne(r, "hashes")
	if !ok {
		badRequest(w)
		return
	}
	hashFilter := map[string]struct{}{}
	if hashesRaw != "" {
		for _, raw := range strings.Split(hashesRaw, "|") {
			if hash, valid := canonicalHash(raw); valid {
				hashFilter[hash] = struct{}{}
			}
		}
	}
	privateRaw, ok := requestOne(r, "private")
	if !ok {
		badRequest(w)
		return
	}
	privateFilter, privateValid := optionalBool([]string{privateRaw})
	sortBy, ok := requestOne(r, "sort")
	if !ok {
		badRequest(w)
		return
	}
	reverseRaw, ok := requestOne(r, "reverse")
	if !ok {
		badRequest(w)
		return
	}
	reverse, _ := optionalBool([]string{reverseRaw})
	limitRaw, ok := requestOne(r, "limit")
	if !ok {
		badRequest(w)
		return
	}
	offsetRaw, ok := requestOne(r, "offset")
	if !ok {
		badRequest(w)
		return
	}
	limit := parseQBTInt(limitRaw)
	offset := parseQBTInt(offsetRaw)
	if sortBy != "" && !validTorrentSort(sortBy) {
		badRequest(w)
		return
	}
	downloads, err := h.repo.ListDownloads(r.Context(), categoryFilter)
	if err != nil {
		internalError(w)
		return
	}
	result := make([]torrentInfo, 0, len(downloads))
	for _, download := range downloads {
		if len(hashFilter) > 0 {
			if _, exists := hashFilter[download.Hash]; !exists {
				continue
			}
		}
		projected, err := domain.Project(download)
		if err != nil {
			internalError(w)
			return
		}
		if !matchesTorrentFilter(projected, filter) || (tagPresent && !hasTag(projected.Tags, tag)) {
			continue
		}
		isPrivate := false
		if download.Private != nil {
			isPrivate = *download.Private
		}
		if privateRaw != "" && privateValid && isPrivate != privateFilter {
			continue
		}
		result = append(result, projectTorrentInfo(download, projected, isPrivate))
	}
	if sortBy != "" {
		sort.SliceStable(result, func(i, j int) bool {
			if reverse {
				return torrentInfoLess(result[j], result[i], sortBy)
			}
			return torrentInfoLess(result[i], result[j], sortBy)
		})
	}
	if offset < 0 {
		offset = len(result) + offset
	}
	if offset >= len(result) || offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = len(result)
	}
	if offset > 0 || limit < len(result) {
		end := offset + limit
		if end > len(result) {
			end = len(result)
		}
		if offset > len(result) {
			offset = len(result)
		}
		result = result[offset:end]
	}
	writeJSON(w, http.StatusOK, result)
}

func requestOne(r *http.Request, name string) (string, bool) {
	values, ok := readRequestValues(r, name)
	if !ok || len(values) > 1 {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return values[0], true
}

func parseQBTInt(value string) int {
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func hasTag(tags, wanted string) bool {
	for _, tag := range strings.Split(tags, ",") {
		if tag == wanted {
			return true
		}
	}
	return false
}

func validTorrentSort(value string) bool {
	switch value {
	case "added_on", "amount_left", "category", "completed", "completion_on", "content_path",
		"dl_limit", "dlspeed", "downloaded", "eta", "hash", "last_activity", "name",
		"num_leechs", "num_seeds", "popularity", "progress", "ratio", "save_path",
		"size", "state", "tags", "time_active", "total_size", "up_limit", "uploaded", "upspeed":
		return true
	default:
		return false
	}
}

func projectTorrentInfo(download domain.Download, projected domain.Projection, private bool) torrentInfo {
	privateValue := any(nil)
	if download.Private != nil {
		privateValue = private
	}
	return torrentInfo{
		AddedOn: download.CreatedAt.Unix(), AmountLeft: projected.Size - projected.Completed,
		AutoTMM: download.AutoTMM, Availability: -1, Category: projected.Category, Completed: projected.Completed,
		CompletionOn: unixOrZero(download.CompletedAt), ContentPath: projected.ContentPath, DLSpeed: 0,
		DownloadLimit: -1, DownloadPath: "", Downloaded: projected.Completed, DownloadedSession: 0, ETA: projected.ETA,
		FLPiecePrio: false, ForceStart: false, Hash: projected.Hash, InfohashV1: projected.Hash, InfohashV2: "",
		InactiveSeedingTimeLimit: projected.InactiveSeedingTimeLimit, LastActivity: projected.LastActivity,
		MagnetURI: "", MaxRatio: projected.RatioLimit, MaxSeedingTime: projected.SeedingTimeLimit,
		Name: projected.Name, NameLo: strings.ToLower(projected.Name), NumComplete: 0, NumIncomplete: 0, NumLeechs: 0,
		NumSeeds: 0, Popularity: -1, Priority: 0, Private: privateValue, Progress: projected.Progress, Ratio: projected.Ratio,
		RatioLimit: projected.RatioLimit, SavePath: projected.SavePath, SeedingTime: projected.SeedingTime,
		SeedingTimeLimit: projected.SeedingTimeLimit, SeenComplete: 0, SeqDL: false, Size: projected.Size,
		State: projected.State, SuperSeeding: false, Tags: projected.Tags, TimeActive: 0, TotalSize: projected.Size,
		Tracker: "", TrackersCount: 0, UploadLimit: -1, Uploaded: 0, UploadedSession: 0, UPSpeed: 0,
	}
}

func unixOrZero(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return value.Unix()
}

func torrentInfoLess(a, b torrentInfo, field string) bool {
	var less, greater bool
	switch field {
	case "added_on":
		less, greater = a.AddedOn < b.AddedOn, a.AddedOn > b.AddedOn
	case "amount_left":
		less, greater = a.AmountLeft < b.AmountLeft, a.AmountLeft > b.AmountLeft
	case "category":
		less, greater = a.Category < b.Category, a.Category > b.Category
	case "completed":
		less, greater = a.Completed < b.Completed, a.Completed > b.Completed
	case "completion_on":
		less, greater = a.CompletionOn < b.CompletionOn, a.CompletionOn > b.CompletionOn
	case "content_path":
		less, greater = a.ContentPath < b.ContentPath, a.ContentPath > b.ContentPath
	case "dl_limit":
		less, greater = a.DownloadLimit < b.DownloadLimit, a.DownloadLimit > b.DownloadLimit
	case "dlspeed":
		less, greater = a.DLSpeed < b.DLSpeed, a.DLSpeed > b.DLSpeed
	case "downloaded":
		less, greater = a.Downloaded < b.Downloaded, a.Downloaded > b.Downloaded
	case "eta":
		less, greater = a.ETA < b.ETA, a.ETA > b.ETA
	case "hash":
		less, greater = a.Hash < b.Hash, a.Hash > b.Hash
	case "last_activity":
		less, greater = a.LastActivity < b.LastActivity, a.LastActivity > b.LastActivity
	case "name":
		less, greater = a.Name < b.Name, a.Name > b.Name
	case "num_leechs":
		less, greater = a.NumLeechs < b.NumLeechs, a.NumLeechs > b.NumLeechs
	case "num_seeds":
		less, greater = a.NumSeeds < b.NumSeeds, a.NumSeeds > b.NumSeeds
	case "popularity":
		less, greater = a.Popularity < b.Popularity, a.Popularity > b.Popularity
	case "progress":
		less, greater = a.Progress < b.Progress, a.Progress > b.Progress
	case "ratio":
		less, greater = a.Ratio < b.Ratio, a.Ratio > b.Ratio
	case "save_path":
		less, greater = a.SavePath < b.SavePath, a.SavePath > b.SavePath
	case "size", "total_size":
		less, greater = a.Size < b.Size, a.Size > b.Size
	case "state":
		less, greater = a.State < b.State, a.State > b.State
	case "tags":
		less, greater = a.Tags < b.Tags, a.Tags > b.Tags
	case "time_active":
		less, greater = a.TimeActive < b.TimeActive, a.TimeActive > b.TimeActive
	case "up_limit":
		less, greater = a.UploadLimit < b.UploadLimit, a.UploadLimit > b.UploadLimit
	case "uploaded":
		less, greater = a.Uploaded < b.Uploaded, a.Uploaded > b.Uploaded
	case "upspeed":
		less, greater = a.UPSpeed < b.UPSpeed, a.UPSpeed > b.UPSpeed
	}
	if less {
		return true
	}
	if greater {
		return false
	}
	return a.Hash < b.Hash
}

func matchesTorrentFilter(projected domain.Projection, filter string) bool {
	switch filter {
	case "", "all":
		return true
	case "downloading", "running", "active":
		return projected.State == "queuedDL" || projected.State == "metaDL" || projected.State == "downloading" || projected.State == "moving"
	case "completed":
		return projected.Progress == 1
	case "stopped":
		return projected.State == "stoppedDL" || projected.State == "stoppedUP"
	case "inactive":
		return projected.State == "stoppedDL" || projected.State == "stoppedUP" || projected.State == "error"
	case "moving":
		return projected.State == "moving"
	case "errored":
		return projected.State == "error"
	case "seeding", "stalled", "stalled_uploading", "stalled_downloading", "checking":
		return false
	default:
		return true
	}
}

func (h *handler) properties(w http.ResponseWriter, r *http.Request) {
	hash, ok := canonicalHashQuery(r)
	if !ok {
		badRequest(w)
		return
	}
	download, ok := h.visibleDownload(r, hash)
	if !ok {
		notFound(w)
		return
	}
	projected, err := domain.Project(download)
	if err != nil {
		internalError(w)
		return
	}
	privateValue := any(nil)
	if download.Private != nil {
		privateValue = *download.Private
	}
	writeJSON(w, http.StatusOK, torrentProperties{
		Hash: projected.Hash, Name: projected.Name, SavePath: projected.SavePath, DownloadPath: "",
		CreationDate: download.CreatedAt.Unix(), AdditionDate: download.CreatedAt.Unix(),
		CompletionDate: unixOrZero(download.CompletedAt), LastSeen: 0, TotalSize: projected.Size,
		PieceSize: 0, PieceCount: 0, PiecesHave: 0, SeedingTime: projected.SeedingTime,
		TimeElapsed: 0, ETA: projected.ETA, ConnectCount: 0, ConnectLimit: -1,
		Downloaded: projected.Completed, DownloadedSession: 0, Uploaded: 0, UploadedSession: 0,
		DownloadSpeed: 0, DownloadSpeedAvg: -1, UploadSpeed: 0, UploadSpeedAvg: -1,
		DownloadLimit: -1, UploadLimit: -1, Wasted: 0, Seeds: 0, SeedsTotal: 0,
		Peers: 0, PeersTotal: 0, ShareRatio: projected.Ratio, Popularity: -1, Reannounce: 0,
		Private: privateValue, IsPrivate: download.Private != nil && *download.Private, HasMetadata: download.Private != nil,
	})
}

func (h *handler) visibleDownload(r *http.Request, hash string) (domain.Download, bool) {
	download, err := h.repo.GetDownload(r.Context(), hash)
	return download, err == nil && download.State.Visible()
}

func (h *handler) files(w http.ResponseWriter, r *http.Request) {
	hash, ok := canonicalHashQuery(r)
	if !ok {
		badRequest(w)
		return
	}
	download, ok := h.visibleDownload(r, hash)
	if !ok {
		notFound(w)
		return
	}
	projected, err := domain.Project(download)
	if err != nil {
		internalError(w)
		return
	}
	indexesRaw, ok := requestOne(r, "indexes")
	if !ok {
		badRequest(w)
		return
	}
	storedFiles, err := h.repo.ListDownloadFiles(r.Context(), hash)
	if err != nil {
		internalError(w)
		return
	}
	overrides, err := h.repo.ListDownloadFileOverrides(r.Context(), hash)
	if err != nil {
		internalError(w)
		return
	}
	overrideByIndex := make(map[int64]domain.FileOverride, len(overrides))
	for _, override := range overrides {
		overrideByIndex[override.FileIndex] = override
	}
	selected := make(map[int64]struct{}, len(storedFiles))
	if indexesRaw == "" {
		for _, file := range storedFiles {
			selected[file.Index] = struct{}{}
		}
	} else {
		for _, raw := range strings.Split(indexesRaw, "|") {
			index, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil || index < 0 || index >= int64(len(storedFiles)) {
				conflict(w)
				return
			}
			selected[index] = struct{}{}
		}
	}
	result := make([]torrentFile, 0, len(storedFiles))
	for _, file := range storedFiles {
		if _, exists := selected[file.Index]; !exists {
			continue
		}
		name, priority := file.RelativePath, int64(1)
		if override, exists := overrideByIndex[file.Index]; exists {
			name, priority = override.RelativePath, override.Priority
		}
		var isSeed *bool
		if file.Index == 0 {
			seeded := projected.Progress == 1
			isSeed = &seeded
		}
		result = append(result, torrentFile{Index: file.Index, Name: name, Size: file.Size, Progress: projected.Progress,
			Priority: priority, Availability: -1, PieceRange: [2]int64{0, 0}, IsSeed: isSeed})
	}
	writeJSON(w, http.StatusOK, result)
}

func readRequestValues(r *http.Request, name string) ([]string, bool) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			return nil, false
		}
		return r.PostForm[name], true
	}
	return r.URL.Query()[name], true
}

func canonicalHashQuery(r *http.Request) (string, bool) {
	values, ok := readRequestValues(r, "hash")
	if !ok || len(values) != 1 {
		return "", false
	}
	return canonicalHash(values[0])
}

func canonicalHash(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) != 40 {
		return "", false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return "", false
		}
	}
	return value, true
}
func (h *handler) setCategory(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	rawCategory, present := exactlyOne(form["category"])
	category, valid := submission.CanonicalCategory(rawCategory, true)
	if !present || !valid {
		badRequest(w)
		return
	}
	if len(hashes) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.repo.SetCategory(r.Context(), hashes[0], category, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	if len(hashes) > 1 {
		for _, hash := range hashes[1:] {
			if err := h.repo.SetCategory(r.Context(), hash, category, h.now()); err != nil {
				repositoryError(w, err)
				return
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) deleteTorrents(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	deleteFiles, valid := requiredBool(form, "deleteFiles")
	if !valid {
		badRequest(w)
		return
	}
	if len(hashes) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.repo.RequestDelete(r.Context(), hashes, deleteFiles, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	h.waker.Wake()
	w.WriteHeader(http.StatusOK)
}

func (h *handler) setForceStart(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	if _, valid := requiredBool(form, "value"); !valid {
		badRequest(w)
		return
	}
	effective := false
	now := h.now()
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			continue
		}
		if download.State == domain.StateStopped || download.State == domain.StateAccepted {
			if err := h.repo.Start(r.Context(), hash, now); err != nil {
				repositoryError(w, err)
				return
			}
			if download.State == domain.StateStopped {
				effective = true
			}
		}
	}
	if effective {
		h.waker.Wake()
	}
	w.WriteHeader(http.StatusOK)
}

func selectedHashes(ctx context.Context, repo repository, form map[string][]string, name string) ([]string, bool) {
	raw, present := exactlyOne(form[name])
	if !present {
		return nil, false
	}
	if raw == "all" {
		downloads, err := repo.ListDownloads(ctx, nil)
		if err != nil {
			return nil, false
		}
		hashes := make([]string, 0, len(downloads))
		for _, download := range downloads {
			if download.State.Visible() || (download.State == domain.StateDeleteRequested && download.LastError != "") {
				hashes = append(hashes, download.Hash)
			}
		}
		return hashes, true
	}
	seen := make(map[string]struct{})
	hashes := make([]string, 0)
	for _, part := range strings.Split(raw, "|") {
		hash, valid := canonicalHash(part)
		if !valid {
			continue
		}
		if _, exists := seen[hash]; exists {
			continue
		}
		download, err := repo.GetDownload(ctx, hash)
		if err != nil || (!download.State.Visible() && !(download.State == domain.StateDeleteRequested && download.LastError != "")) {
			continue
		}
		seen[hash] = struct{}{}
		hashes = append(hashes, hash)
	}
	return hashes, true
}

func requiredBool(form map[string][]string, name string) (bool, bool) {
	value, present := exactlyOne(form[name])
	if !present {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1":
		return true, true
	default:
		return false, true
	}
}
func normalizeTags(raw string, required bool) (string, bool) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.ContainsAny(part, ",\x00") || strings.ContainsFunc(part, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", false
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	if required && len(result) == 0 {
		return "", false
	}
	return strings.Join(result, ","), true
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	effective := false
	now := h.now()
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			continue
		}
		if download.State == domain.StateStopped || download.State == domain.StateAccepted {
			if err := h.repo.Start(r.Context(), hash, now); err != nil {
				repositoryError(w, err)
				return
			}
			effective = effective || download.State == domain.StateStopped
		}
	}
	if effective {
		h.waker.Wake()
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) addTags(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	raw, present := exactlyOne(form["tags"])
	if !present {
		badRequest(w)
		return
	}
	tags, valid := normalizeTags(raw, false)
	if !valid {
		badRequest(w)
		return
	}
	if len(hashes) > 0 && tags != "" {
		if err := h.repo.AddTags(r.Context(), hashes, tags, h.now()); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) setAutoManagement(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, valid := selectedHashes(r.Context(), h.repo, form, "hashes"); !valid {
		badRequest(w)
		return
	}
	if _, valid := requiredBool(form, "enable"); !valid {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) setSavePath(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	raw, present := exactlyOne(form["path"])
	if !present || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		badRequest(w)
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "id")
	if !valid {
		badRequest(w)
		return
	}
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			repositoryError(w, err)
			return
		}
		if download.State != domain.StateStopped && download.State != domain.StateAccepted {
			conflict(w)
			return
		}
	}
	resolved, _, err := h.filesystem.ResolveSaveRoot(raw)
	if err != nil {
		badRequest(w)
		return
	}
	if _, err := h.filesystem.PrepareSaveRoot(resolved); err != nil {
		internalError(w)
		return
	}
	if len(hashes) > 0 {
		if err := h.repo.SetSavePaths(r.Context(), hashes, resolved, h.now()); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) setLocation(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	for _, hash := range hashes {
		download, getErr := h.repo.GetDownload(r.Context(), hash)
		if getErr != nil {
			repositoryError(w, getErr)
			return
		}
		if download.State != domain.StateStopped && download.State != domain.StateAccepted {
			conflict(w)
			return
		}
	}
	raw, present := exactlyOne(form["location"])
	if !present || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		badRequest(w)
		return
	}
	resolved, _, err := h.filesystem.ResolveSaveRoot(raw)
	if err != nil {
		badRequest(w)
		return
	}
	if _, err := h.filesystem.PrepareSaveRoot(resolved); err != nil {
		internalError(w)
		return
	}
	if err := h.repo.SetSavePaths(r.Context(), hashes, resolved, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) filePriority(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hash, valid := canonicalHash(first(form["hash"]))
	if !valid {
		badRequest(w)
		return
	}
	ids, present := exactlyOne(form["id"])
	if !present {
		badRequest(w)
		return
	}
	rawPriority, present := exactlyOne(form["priority"])
	if !present {
		badRequest(w)
		return
	}
	priority, err := strconv.ParseInt(rawPriority, 10, 64)
	if err != nil || (priority != 0 && priority != 1 && priority != 6 && priority != 7) {
		badRequest(w)
		return
	}
	indexes := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, rawID := range strings.Split(ids, "|") {
		index, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil || index < 0 {
			badRequest(w)
			return
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	if len(indexes) == 0 {
		badRequest(w)
		return
	}
	if err := h.repo.SetFilePriorities(r.Context(), hash, indexes, priority, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) renameFile(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hash, valid := canonicalHash(first(form["hash"]))
	if !valid {
		badRequest(w)
		return
	}
	oldPath, oldPresent := exactlyOne(form["oldPath"])
	newPath, newPresent := exactlyOne(form["newPath"])
	if !oldPresent || !newPresent || !safeRelativePath(oldPath) || !safeRelativePath(newPath) {
		badRequest(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	files, err := h.repo.ListDownloadFiles(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	overrides, err := h.repo.ListDownloadFileOverrides(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	effective := make(map[int64]string, len(files))
	priorities := make(map[int64]int64, len(files))
	sizes := make(map[int64]int64, len(files))
	for _, file := range files {
		effective[file.Index] = file.RelativePath
		priorities[file.Index] = 1
		sizes[file.Index] = file.Size
	}
	for _, override := range overrides {
		effective[override.FileIndex] = override.RelativePath
		priorities[override.FileIndex] = override.Priority
	}
	target := int64(-1)
	for index, current := range effective {
		if current == oldPath {
			if target != -1 {
				conflict(w)
				return
			}
			target = index
		}
	}
	if target < 0 {
		notFound(w)
		return
	}
	for index, current := range effective {
		if index != target && current == newPath {
			conflict(w)
			return
		}
	}
	if download.State == domain.StateCompleted && oldPath != newPath {
		if err := h.applyCompletedFilePlan(download, oldPath, newPath, sizes[target]); err != nil {
			renamePlanError(w, err)
			return
		}
	}
	if err := h.repo.SetFileOverride(r.Context(), hash, target, newPath, priorities[target], h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) applyCompletedFilePlan(download domain.Download, oldPath, newPath string, size int64) error {
	planner, ok := h.filesystem.(interface {
		ApplyFilePlan(string, string, []fsafe.FilePlan) error
	})
	if !ok {
		return errors.New("completed rename requires file planner")
	}
	return planner.ApplyFilePlan(completedPlanRoot(download), download.Hash, []fsafe.FilePlan{{
		Index: 0, OriginalPath: oldPath, EffectivePath: newPath, Priority: 1, Size: size,
	}})
}

func completedPlanRoot(download domain.Download) string {
	root := download.WorkspacePath
	if root == "" {
		root = download.SavePath
	}
	if download.SourceKind == domain.SourceTorrent && download.IsMultiFile != nil && *download.IsMultiFile &&
		download.CopySourcePath == download.CloudResultPath {
		root = filepath.Join(root, download.DestinationName)
	}
	return filepath.Clean(root)
}

func renamePlanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		notFound(w)
	case errors.Is(err, fsafe.ErrFilePlanConflict):
		conflict(w)
	default:
		internalError(w)
	}
}

func first(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return ""
}
func safeRelativePath(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.IsAbs(value) || filepath.Clean(value) != value || strings.HasPrefix(value, ".."+string(filepath.Separator)) || strings.ContainsAny(value, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(value, string(filepath.Separator)) {
		if part == "." || part == ".." || strings.ContainsFunc(part, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
	}
	return true
}
func (h *handler) torrentCount(w http.ResponseWriter, r *http.Request) {
	downloads, err := h.repo.ListDownloads(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	plainExact(w, http.StatusOK, strconv.Itoa(len(downloads)))
}

func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.repo.ListTags(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (h *handler) sslParameters(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireVisibleHash(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ssl_certificate": "", "ssl_private_key": "", "ssl_dh_params": ""})
}

func (h *handler) pieceHashes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireVisibleHash(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []string{})
}

func (h *handler) pieceStates(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireVisibleHash(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []int{})
}

func (h *handler) trackers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireVisibleHash(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handler) webseeds(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireVisibleHash(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handler) requireVisibleHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	hash, ok := canonicalHashQuery(r)
	if !ok {
		badRequest(w)
		return "", false
	}
	if _, ok := h.visibleDownload(r, hash); !ok {
		notFound(w)
		return "", false
	}
	return hash, true
}

func (h *handler) addPeers(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	raw, present := exactlyOne(form["peers"])
	if !present {
		badRequest(w)
		return
	}
	added := 0
	for _, peer := range strings.Split(raw, "|") {
		host, port, err := net.SplitHostPort(strings.TrimSpace(peer))
		if err == nil && net.ParseIP(host) != nil {
			if value, err := strconv.Atoi(port); err == nil && value >= 0 && value <= 65535 {
				added++
			}
		}
	}
	if added == 0 {
		badRequest(w)
		return
	}
	results := make(map[string]map[string]int, len(hashes))
	for _, hash := range hashes {
		results[hash] = map[string]int{"added": added, "failed": len(strings.Split(raw, "|")) - added}
	}
	writeJSON(w, http.StatusOK, results)
}
func (h *handler) setShareLimits(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, valid := selectedHashes(r.Context(), h.repo, form, "hashes"); !valid {
		badRequest(w)
		return
	}
	for _, name := range []string{"ratioLimit", "seedingTimeLimit", "inactiveSeedingTimeLimit"} {
		if _, present := exactlyOne(form[name]); !present {
			badRequest(w)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) addTrackers(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, ok := h.requireSingleVisibleForm(w, r, form, "hash"); !ok {
		return
	}
	if _, present := exactlyOne(form["urls"]); !present {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) editTracker(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, ok := h.requireSingleVisibleForm(w, r, form, "hash"); !ok {
		return
	}
	original, originalOK := exactlyOne(form["origUrl"])
	replacement, replacementOK := exactlyOne(form["newUrl"])
	if !originalOK || !replacementOK {
		badRequest(w)
		return
	}
	if original == replacement {
		w.WriteHeader(http.StatusOK)
		return
	}
	if parsed, err := url.ParseRequestURI(replacement); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) removeTrackers(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, ok := h.requireSingleVisibleForm(w, r, form, "hash"); !ok {
		return
	}
	if _, present := exactlyOne(form["urls"]); !present {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) requireSingleVisibleForm(w http.ResponseWriter, r *http.Request, form map[string][]string, name string) (string, bool) {
	raw, present := exactlyOne(form[name])
	if !present {
		badRequest(w)
		return "", false
	}
	hash, valid := canonicalHash(raw)
	if !valid {
		notFound(w)
		return "", false
	}
	_, visible := h.visibleDownload(r, hash)
	if !visible {
		notFound(w)
		return "", false
	}
	return hash, true
}

func (h *handler) noopHashes(w http.ResponseWriter, r *http.Request, field string) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, valid := selectedHashes(r.Context(), h.repo, form, field); !valid {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) bottomPriority(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) decreasePriority(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) increasePriority(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) topPriority(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) reannounce(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) toggleFirstLastPiecePriority(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) toggleSequentialDownload(w http.ResponseWriter, r *http.Request) {
	h.noopHashes(w, r, "hashes")
}
func (h *handler) setSuperSeeding(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, valid := selectedHashes(r.Context(), h.repo, form, "hashes"); !valid {
		badRequest(w)
		return
	}
	if _, valid := requiredBool(form, "value"); !valid {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) setDownloadLimit(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, valid := selectedHashes(r.Context(), h.repo, form, "hashes"); !valid {
		badRequest(w)
		return
	}
	if _, present := exactlyOne(form["limit"]); !present {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) setUploadLimit(w http.ResponseWriter, r *http.Request) {
	h.setDownloadLimit(w, r)
}
func (h *handler) setSSLParameters(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, ok := h.requireSingleVisibleForm(w, r, form, "hash"); !ok {
		return
	}
	for _, name := range []string{"ssl_certificate", "ssl_private_key", "ssl_dh_params"} {
		if _, present := exactlyOne(form[name]); !present {
			badRequest(w)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) setDownloadPath(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	if _, present := exactlyOne(form["id"]); !present {
		badRequest(w)
		return
	}
	if _, present := exactlyOne(form["path"]); !present {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) downloadLimit(w http.ResponseWriter, r *http.Request) {
	h.limits(w, r, "hashes")
}
func (h *handler) uploadLimit(w http.ResponseWriter, r *http.Request) {
	h.limits(w, r, "hashes")
}
func (h *handler) limits(w http.ResponseWriter, r *http.Request, field string) {
	values, ok := readRequestValues(r, field)
	if !ok || len(values) != 1 {
		badRequest(w)
		return
	}
	result := map[string]int64{}
	for _, raw := range strings.Split(values[0], "|") {
		if raw != "" {
			result[raw] = -1
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) createTags(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	raw, present := exactlyOne(form["tags"])
	if !present {
		badRequest(w)
		return
	}
	tags, valid := normalizeTags(raw, false)
	if !valid {
		badRequest(w)
		return
	}
	if tags != "" {
		if err := h.repo.CreateTags(r.Context(), tags, h.now()); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) deleteTags(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	raw, present := exactlyOne(form["tags"])
	if !present {
		badRequest(w)
		return
	}
	tags, valid := normalizeTags(raw, false)
	if !valid {
		badRequest(w)
		return
	}
	if tags != "" {
		if err := h.repo.DeleteTags(r.Context(), tags, h.now()); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) removeTags(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	raw := ""
	if values, present := form["tags"]; present {
		var one bool
		raw, one = exactlyOne(values)
		if !one {
			badRequest(w)
			return
		}
	}
	tags, valid := normalizeTags(raw, false)
	if !valid {
		badRequest(w)
		return
	}
	if len(hashes) > 0 {
		if err := h.repo.RemoveTags(r.Context(), hashes, tags, h.now()); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) removeCategories(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	raw, present := exactlyOne(form["categories"])
	if !present {
		badRequest(w)
		return
	}
	categories := make([]string, 0)
	for _, value := range strings.Split(raw, "\n") {
		name, valid := submission.CanonicalCategory(value, false)
		if valid {
			categories = append(categories, name)
		}
	}
	if err := h.repo.RemoveCategories(r.Context(), categories, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) renameTorrent(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hash, ok := h.requireSingleVisibleForm(w, r, form, "hash")
	if !ok {
		return
	}
	name, present := exactlyOne(form["name"])
	if !present || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\\x00") {
		conflict(w)
		return
	}
	name = strings.Join(strings.Fields(name), " ")
	if err := h.repo.RenameDownload(r.Context(), hash, name, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) renameFolder(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hash, ok := h.requireSingleVisibleForm(w, r, form, "hash")
	if !ok {
		return
	}
	oldPath, oldOK := exactlyOne(form["oldPath"])
	newPath, newOK := exactlyOne(form["newPath"])
	if !oldOK || !newOK || !safeRelativePath(oldPath) || !safeRelativePath(newPath) {
		badRequest(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	if download.State == domain.StateCompleted {
		files, filesErr := h.repo.ListDownloadFiles(r.Context(), hash)
		if filesErr != nil {
			repositoryError(w, filesErr)
			return
		}
		overrides, overridesErr := h.repo.ListDownloadFileOverrides(r.Context(), hash)
		if overridesErr != nil {
			repositoryError(w, overridesErr)
			return
		}
		plans := completedFolderPlans(files, overrides, oldPath, newPath)
		if len(plans) > 0 {
			planner, plannerOK := h.filesystem.(interface {
				ApplyFilePlan(string, string, []fsafe.FilePlan) error
			})
			if !plannerOK {
				internalError(w)
				return
			}
			if err := planner.ApplyFilePlan(completedPlanRoot(download), download.Hash, plans); err != nil {
				renamePlanError(w, err)
				return
			}
		}
	}
	if err := h.repo.RenameFolder(r.Context(), hash, oldPath, newPath, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func completedFolderPlans(files []domain.DownloadFile, overrides []domain.FileOverride, oldPath, newPath string) []fsafe.FilePlan {
	overrideByIndex := make(map[int64]domain.FileOverride, len(overrides))
	for _, override := range overrides {
		overrideByIndex[override.FileIndex] = override
	}
	plans := make([]fsafe.FilePlan, 0)
	for _, file := range files {
		current, priority := file.RelativePath, int64(1)
		if override, exists := overrideByIndex[file.Index]; exists {
			current, priority = override.RelativePath, override.Priority
		}
		effective := current
		if current == oldPath {
			effective = newPath
		} else if strings.HasPrefix(current, oldPath+"/") {
			effective = newPath + current[len(oldPath):]
		}
		if effective != current && priority != 0 {
			plans = append(plans, fsafe.FilePlan{
				Index: file.Index, OriginalPath: current, EffectivePath: effective, Priority: 1, Size: file.Size,
			})
		}
	}
	return plans
}

func (h *handler) editCategory(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	rawName, nameOK := exactlyOne(form["category"])
	rawPath, pathOK := exactlyOne(form["savePath"])
	name, valid := submission.CanonicalCategory(rawName, false)
	if !nameOK || !pathOK || !valid || !filepath.IsAbs(rawPath) || filepath.Clean(rawPath) != rawPath {
		badRequest(w)
		return
	}
	category, err := h.repo.GetCategory(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			conflict(w)
		} else {
			internalError(w)
		}
		return
	}
	resolved, _, err := h.filesystem.ResolveSaveRoot(rawPath)
	if err != nil {
		badRequest(w)
		return
	}
	if _, err := h.filesystem.PrepareSaveRoot(resolved); err != nil {
		badRequest(w)
		return
	}
	category.SavePath, category.UpdatedAt = resolved, h.now()
	if _, err := h.repo.UpsertCategory(r.Context(), category); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) exportTorrent(w http.ResponseWriter, r *http.Request) {
	_, _ = r, w
	notFound(w)
}

func (h *handler) recheck(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	effective := false
	now := h.now()
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			continue
		}
		switch {
		case domain.CanRetry(download):
			if err := h.repo.Retry(r.Context(), hash, domain.RetryTarget(download), now); err != nil {
				repositoryError(w, err)
				return
			}
			effective = true
		case download.State == domain.StateCompleted:
			if err := h.repo.Reverify(r.Context(), hash, now); err != nil {
				repositoryError(w, err)
				return
			}
			effective = true
		}
	}
	if effective {
		h.waker.Wake()
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := selectedHashes(r.Context(), h.repo, form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	effective := false
	now := h.now()
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil || !domain.CanPause(download) {
			continue
		}
		if err := h.repo.Pause(r.Context(), hash, now); err != nil {
			repositoryError(w, err)
			return
		}
		effective = effective || download.State != domain.StateStopped
	}
	if effective {
		h.waker.Wake()
	}
	w.WriteHeader(http.StatusOK)
}
