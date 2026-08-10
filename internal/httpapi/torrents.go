package httpapi

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

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
	switch {
	case len(urls) > 0:
		if len(urls) != 1 {
			badRequest(w)
			return
		}
		_, _, err = h.service.SubmitMagnet(r.Context(), urls[0], category, stopped)
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
		_, _, err = h.service.SubmitTorrent(r.Context(), data, category, stopped)
	default:
		badRequest(w)
		return
	}
	if err != nil {
		if errors.Is(err, submission.ErrInvalidSource) || errors.Is(err, submission.ErrCategoryInvalid) {
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
	if values, present := r.URL.Query()["category"]; present {
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
		result = append(result, torrentInfo{Hash: projected.Hash, Name: projected.Name, Size: projected.Size, Progress: projected.Progress, ETA: projected.ETA, State: projected.State, Category: projected.Category, SavePath: projected.SavePath, ContentPath: projected.ContentPath, Ratio: projected.Ratio, RatioLimit: projected.RatioLimit, SeedingTime: projected.SeedingTime, SeedingTimeLimit: projected.SeedingTimeLimit, InactiveSeedingTimeLimit: projected.InactiveSeedingTimeLimit, LastActivity: projected.LastActivity})
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
	result := make([]torrentFile, 0, len(storedFiles))
	for _, file := range storedFiles {
		result = append(result, torrentFile{Index: file.Index, Name: file.RelativePath, Size: file.Size, Progress: projected.Progress, Priority: 1, IsSeed: false})
	}
	writeJSON(w, http.StatusOK, result)
}

func canonicalHashQuery(r *http.Request) (string, bool) {
	values, present := r.URL.Query()["hash"]
	if !present || len(values) != 1 {
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
