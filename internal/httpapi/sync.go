package httpapi

import (
	"net/http"
	"strconv"

	"github.com/turygo/cd211/internal/domain"
)

func nextRID(raw string) int {
	rid, err := strconv.Atoi(raw)
	if err != nil || rid < 0 {
		return 1
	}
	rid = (rid % 1000000) + 1
	return rid
}

func (h *handler) syncMainData(w http.ResponseWriter, r *http.Request) {
	params, ok := qbtParams(w, r)
	if !ok {
		return
	}
	rid := 1
	if values := params["rid"]; len(values) == 1 {
		rid = nextRID(values[0])
	} else if len(values) > 1 {
		badRequest(w)
		return
	}
	downloads, err := h.repo.ListDownloads(r.Context(), nil)
	if err != nil {
		internalError(w)
		return
	}
	torrents := make(map[string]torrentInfo, len(downloads))
	for _, download := range downloads {
		if !download.State.Visible() {
			continue
		}
		projected, projectErr := domain.Project(download)
		if projectErr != nil {
			internalError(w)
			return
		}
		torrents[projected.Hash] = torrentInfo{Hash: projected.Hash, Name: projected.Name, Size: projected.Size, Completed: projected.Completed, Progress: projected.Progress, ETA: projected.ETA, State: projected.State, Category: projected.Category, Tags: projected.Tags, SavePath: projected.SavePath, ContentPath: projected.ContentPath, Ratio: projected.Ratio, RatioLimit: projected.RatioLimit, SeedingTime: projected.SeedingTime, SeedingTimeLimit: projected.SeedingTimeLimit, InactiveSeedingTimeLimit: projected.InactiveSeedingTimeLimit, LastActivity: projected.LastActivity}
	}
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	categoryMap := make(map[string]categoryView, len(categories))
	for _, category := range categories {
		categoryMap[category.Name] = categoryView{Name: category.Name, SavePath: category.SavePath}
	}
	tags, err := h.repo.ListTags(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rid": rid, "full_update": true, "torrents": torrents, "categories": categoryMap, "tags": tags,
		"server_state": map[string]any{"alltime_dl": int64(0), "alltime_ul": int64(0), "average_time": int64(0), "connection_status": "disconnected", "dht_nodes": int64(0), "dl_info_data": int64(0), "dl_info_speed": int64(0), "dl_rate_limit": int64(-1), "up_info_data": int64(0), "up_info_speed": int64(0), "up_rate_limit": int64(-1)},
	})
}

func (h *handler) syncTorrentPeers(w http.ResponseWriter, r *http.Request) {
	params, ok := qbtParams(w, r)
	if !ok {
		return
	}
	if values := params["rid"]; len(values) > 1 {
		badRequest(w)
		return
	}
	hashValues, present := params["hash"]
	if !present || len(hashValues) != 1 {
		notFound(w)
		return
	}
	hash, valid := canonicalHash(hashValues[0])
	if !valid {
		notFound(w)
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	if !download.State.Visible() {
		notFound(w)
		return
	}
	rid := 1
	if len(params["rid"]) == 1 {
		rid = nextRID(params["rid"][0])
	}
	writeJSON(w, http.StatusOK, map[string]any{"rid": rid, "full_update": true, "peers": map[string]any{}, "show_flags": false})
}
