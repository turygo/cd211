package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
)

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
	cloudFolder, savePath, valid, pathErr := h.submissionPaths(r, category)
	if pathErr != nil {
		internalError(w)
		return
	}
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
	var result torrentmeta.Result
	var source domain.SourceKind
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
		magnet, one := oneMagnetLine(urls[0])
		if !one {
			badRequest(w)
			return
		}
		result, err = torrentmeta.ParseMagnet(magnet, h.config.TorrentLimits)
		source = domain.SourceMagnet
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
		result, err = torrentmeta.ParseTorrent(data, h.config.TorrentLimits)
		source = domain.SourceTorrent
	default:
		badRequest(w)
		return
	}
	if err != nil {
		badRequest(w)
		return
	}

	now := h.now()
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
	h.prepareRetainedContent(r.Context(), &download)
	if stopped {
		download.State = domain.StateStopped
		download.NextRunAt = nil
	}
	_, created, err := h.repo.CreateSubmission(r.Context(), domain.Submission{Download: download, Files: filesToCreate})
	if err != nil {
		internalError(w)
		return
	}
	if created {
		h.waker.Wake()
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) prepareRetainedContent(ctx context.Context, download *domain.Download) {
	existing, err := h.repo.GetDownload(ctx, download.Hash)
	if err != nil || existing.State != domain.StateDeleted || existing.DeleteFilesRequested ||
		existing.ContentPath == "" || existing.CloudSourcePath == "" || existing.IsMultiFile == nil ||
		filepath.Clean(existing.SavePath) != filepath.Clean(download.SavePath) {
		return
	}
	content, err := h.filesystem.Verify(existing.SavePath, fsafe.ExpectedContent{Name: existing.Name, MultiFile: *existing.IsMultiFile})
	if err != nil || filepath.Clean(content.Path) != filepath.Clean(existing.ContentPath) {
		return
	}
	multiFile := *existing.IsMultiFile
	download.Name = existing.Name
	download.CloudTaskName = existing.CloudTaskName
	download.CloudSourcePath = existing.CloudSourcePath
	download.ContentPath = content.Path
	download.IsMultiFile = &multiFile
	// The retained tree is already on disk, so its measured size supersedes
	// whatever the previous submission recorded.
	download.TotalSize = content.Size
	download.State = domain.StateVerifyingLocal
	download.OfflineProgress = 1
	download.CopyProgress = 1
	download.QbitProgress = 0.99
	download.LastUpstreamStatus = domain.UpstreamRetainedContent
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

func oneMagnetLine(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	if len(lines) != 1 {
		return "", false
	}
	line := strings.TrimSpace(lines[0])
	return line, line != ""
}

func (h *handler) submissionPaths(r *http.Request, category string) (string, string, bool, error) {
	if category == "" {
		return h.config.CloudRoot, h.config.LocalRoot, true, nil
	}
	configured, err := h.repo.GetCategory(r.Context(), category)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if !configured.Enabled {
		return "", "", false, nil
	}
	return configured.CloudPath, configured.SavePath, true, nil
}

func addCategory(form map[string][]string) (string, bool) {
	values, present := form["category"]
	if !present {
		return "", true
	}
	raw, one := exactlyOne(values)
	if !one {
		return "", false
	}
	return canonicalCategory(raw, true)
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
	category, valid := canonicalCategory(rawCategory, true)
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
