package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
)

// addTorrent decodes the qBittorrent WebAPI add form and delegates the whole
// submission pipeline (metadata parsing, category lookup, destination paths,
// retained-content revival, persistence, wake-up) to the shared
// submission.Service. The response contract is unchanged: 200 with an empty
// body for accepted submissions, Bad Request for any invalid input.
func (h *handler) addTorrent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxRequestBytes)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/x-www-form-urlencoded" && mediaType != "multipart/form-data") {
		badRequest(w)
		return
	}
	if mediaType == "application/x-www-form-urlencoded" {
		if err := r.ParseForm(); err != nil {
			badRequest(w)
			return
		}
	} else if err := r.ParseMultipartForm(int64(h.config.TorrentLimits.MaxInputBytes)); err != nil {
		badRequest(w)
		return
	} else {
		defer r.MultipartForm.RemoveAll()
	}

	stopped, valid := stoppedSubmission(r.PostForm)
	if !valid {
		badRequest(w)
		return
	}
	category, valid := addCategory(r.PostForm)
	if !valid {
		badRequest(w)
		return
	}

	urls := r.PostForm["urls"]
	var files map[string][]*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File
	}
	if len(r.PostForm["torrents"]) != 0 {
		badRequest(w)
		return
	}
	if len(urls) > 0 && len(files["torrents"]) > 0 {
		badRequest(w)
		return
	}
	if values, present := r.PostForm["rename"]; present {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(values[0], "/\\\x00") {
			badRequest(w)
			return
		}
	}
	if values, present := r.PostForm["tags"]; present {
		if len(values) != 1 {
			badRequest(w)
			return
		}
		if _, valid := normalizeTags(values[0], false); !valid {
			badRequest(w)
			return
		}
	}
	if values, present := r.PostForm["autoTMM"]; present {
		if len(values) != 1 {
			badRequest(w)
			return
		}
		if _, valid := requiredBool(r.PostForm, "autoTMM"); !valid {
			badRequest(w)
			return
		}
	}
	if values, present := r.PostForm["savepath"]; present {
		if len(values) != 1 || !filepath.IsAbs(values[0]) || filepath.Clean(values[0]) != values[0] {
			badRequest(w)
			return
		}
	}
	options := submission.Options{}
	if values, present := r.PostForm["rename"]; present {
		options.RenameSet = true
		options.Rename = values[0]
	}
	if values, present := r.PostForm["tags"]; present {
		options.TagsSet = true
		options.Tags = values[0]
	}
	if _, present := r.PostForm["autoTMM"]; present {
		enabled, _ := requiredBool(r.PostForm, "autoTMM")
		if enabled {
			conflict(w)
			return
		}
		options.AutoTMMSet = true
	}
	if values, present := r.PostForm["savepath"]; present {
		options.SavePathSet = true
		options.SavePath = values[0]
	}
	switch {
	case len(urls) > 0:
		if len(urls) != 1 {
			badRequest(w)
			return
		}
		source, parseErr := url.ParseRequestURI(urls[0])
		if parseErr != nil {
			badRequest(w)
			return
		}
		switch strings.ToLower(source.Scheme) {
		case "magnet":
			_, _, err = h.service.SubmitMagnetWithOptions(r.Context(), urls[0], category, stopped, options)
		case "http", "https":
			var cookie string
			if values, present := r.PostForm["cookie"]; present {
				if len(values) != 1 {
					badRequest(w)
					return
				}
				cookie = values[0]
			}
			data, fetchErr := fetchTorrent(r.Context(), source, cookie, h.config.TorrentLimits.MaxInputBytes)
			if fetchErr != nil {
				badRequest(w)
				return
			}
			_, _, err = h.service.SubmitTorrentWithOptions(r.Context(), data, category, stopped, options)
		default:
			badRequest(w)
			return
		}
	case len(files["torrents"]) > 0:
		if mediaType != "multipart/form-data" || len(files["torrents"]) != 1 {
			badRequest(w)
			return
		}
		data, readErr := readUpload(files["torrents"][0])
		if readErr != nil {
			badRequest(w)
			return
		}
		_, _, err = h.service.SubmitTorrentWithOptions(r.Context(), data, category, stopped, options)
	default:
		badRequest(w)
		return
	}
	if err != nil {
		if errors.Is(err, submission.ErrInvalidSource) || errors.Is(err, submission.ErrCategoryInvalid) || errors.Is(err, submission.ErrInvalidOptions) {
			badRequest(w)
			return
		}
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusOK)
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
	stopped, valid := optionalBool(form["stopped"])
	if !valid {
		return false, false
	}
	paused, valid := optionalBool(form["paused"])
	if !valid {
		return false, false
	}
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
	var category *string
	values, parseOK := readRequestValues(r, "category")
	if !parseOK {
		badRequest(w)
		return
	}
	if len(values) > 0 {
		if len(values) != 1 {
			badRequest(w)
			return
		}
		value := values[0]
		category = &value
	}
	downloads, err := h.repo.ListDownloads(r.Context(), category)
	if err != nil {
		internalError(w)
		return
	}
	result := make([]torrentInfo, 0, len(downloads))
	for _, download := range downloads {
		projected, err := domain.Project(download)
		if err != nil {
			internalError(w)
			return
		}
		result = append(result, torrentInfo{Hash: projected.Hash, Name: projected.Name, Size: projected.Size, Completed: projected.Completed, Progress: projected.Progress, ETA: projected.ETA, State: projected.State, Category: projected.Category, Tags: projected.Tags, SavePath: projected.SavePath, ContentPath: projected.ContentPath, Ratio: projected.Ratio, RatioLimit: projected.RatioLimit, SeedingTime: projected.SeedingTime, SeedingTimeLimit: projected.SeedingTimeLimit, InactiveSeedingTimeLimit: projected.InactiveSeedingTimeLimit, LastActivity: projected.LastActivity})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) properties(w http.ResponseWriter, r *http.Request) {
	hash, ok := canonicalHashQuery(r)
	if !ok {
		badRequest(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil || !download.State.Visible() {
		if errors.Is(err, store.ErrNotFound) || err == nil {
			notFound(w)
		} else {
			internalError(w)
		}
		return
	}
	projected, err := domain.Project(download)
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, torrentProperties{Hash: projected.Hash, SavePath: projected.SavePath, SeedingTime: 0})
}

func (h *handler) files(w http.ResponseWriter, r *http.Request) {
	hash, ok := canonicalHashQuery(r)
	if !ok {
		badRequest(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil || !download.State.Visible() {
		if errors.Is(err, store.ErrNotFound) || err == nil {
			notFound(w)
		} else {
			internalError(w)
		}
		return
	}
	projected, err := domain.Project(download)
	if err != nil {
		internalError(w)
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
	result := make([]torrentFile, 0, len(storedFiles))
	for _, file := range storedFiles {
		name, priority := file.RelativePath, int64(1)
		if override, exists := overrideByIndex[file.Index]; exists {
			name, priority = override.RelativePath, override.Priority
		}
		result = append(result, torrentFile{Index: file.Index, Name: name, Size: file.Size, Progress: projected.Progress, Priority: priority, IsSeed: false})
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
	hashes, valid := hashesField(form, "hashes")
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
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil || !download.State.Visible() {
			if errors.Is(err, store.ErrNotFound) || err == nil {
				notFound(w)
			} else {
				internalError(w)
			}
			return
		}
	}
	now := h.now()
	for _, hash := range hashes {
		if err := h.repo.SetCategory(r.Context(), hash, category, now); err != nil {
			repositoryError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) deleteTorrents(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := hashesField(form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	deleteFiles, valid := requiredBool(form, "deleteFiles")
	if !valid {
		badRequest(w)
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
	hashes, valid := hashesField(form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	value, valid := requiredBool(form, "value")
	if !valid {
		badRequest(w)
		return
	}
	if !value {
		w.WriteHeader(http.StatusOK)
		return
	}
	for _, hash := range hashes {
		download, err := h.repo.GetDownload(r.Context(), hash)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				notFound(w)
			} else {
				internalError(w)
			}
			return
		}
		if download.State != domain.StateStopped && download.State != domain.StateAccepted {
			conflict(w)
			return
		}
	}
	now := h.now()
	for _, hash := range hashes {
		if err := h.repo.Start(r.Context(), hash, now); err != nil {
			repositoryError(w, err)
			return
		}
	}
	h.waker.Wake()
	w.WriteHeader(http.StatusOK)
}

func hashesField(form map[string][]string, name string) ([]string, bool) {
	raw, present := exactlyOne(form[name])
	if !present {
		return nil, false
	}
	return canonicalHashes(raw)
}

func canonicalHashes(raw string) ([]string, bool) {
	parts := strings.Split(raw, "|")
	if len(parts) == 0 {
		return nil, false
	}
	unique := make(map[string]struct{}, len(parts))
	hashes := make([]string, 0, len(parts))
	for _, part := range parts {
		hash, valid := canonicalHash(part)
		if !valid {
			return nil, false
		}
		if _, exists := unique[hash]; !exists {
			unique[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
	}
	return hashes, len(hashes) > 0
}

func requiredBool(form map[string][]string, name string) (bool, bool) {
	value, present := exactlyOne(form[name])
	if !present {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
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
	hashes, valid := hashesField(form, "hashes")
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
	if err := h.repo.StartMany(r.Context(), hashes, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	h.waker.Wake()
	w.WriteHeader(http.StatusOK)
}

func (h *handler) addTags(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := hashesField(form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	raw, present := exactlyOne(form["tags"])
	if !present {
		badRequest(w)
		return
	}
	tags, valid := normalizeTags(raw, true)
	if !valid {
		badRequest(w)
		return
	}
	if err := h.repo.AddTags(r.Context(), hashes, tags, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) setAutoManagement(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := hashesField(form, "hashes")
	if !valid {
		badRequest(w)
		return
	}
	enabled, valid := requiredBool(form, "enable")
	if !valid {
		badRequest(w)
		return
	}
	if enabled {
		conflict(w)
		return
	}
	if err := h.repo.SetAutoTMM(r.Context(), hashes, false, h.now()); err != nil {
		repositoryError(w, err)
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
	hash, valid := canonicalHash(first(form["id"]))
	if !valid {
		badRequest(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	if download.State != domain.StateStopped && download.State != domain.StateAccepted {
		conflict(w)
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
	if err := h.repo.SetSavePath(r.Context(), hash, resolved, download.RowVersion, h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) setLocation(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	hashes, valid := hashesField(form, "hashes")
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
	effective := make(map[int64]string)
	priorities := make(map[int64]int64)
	for _, file := range files {
		effective[file.Index] = file.RelativePath
		priorities[file.Index] = 1
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
	if err := h.repo.SetFileOverride(r.Context(), hash, target, newPath, priorities[target], h.now()); err != nil {
		repositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
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
