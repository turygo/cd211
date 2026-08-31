package httpapi

import (
	"net/http"
	"strconv"
)

func (h *handler) searchPlugins(w http.ResponseWriter, r *http.Request) {
	if _, ok := qbtParams(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}
func (h *handler) searchResults(w http.ResponseWriter, r *http.Request) {
	if !validSyntheticSearchID(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "Stopped", "results": []any{}, "total": 0})
}
func validSyntheticSearchID(w http.ResponseWriter, r *http.Request) bool {
	params, ok := requireQBTParams(w, r, "id")
	if !ok {
		return false
	}
	if len(params["id"]) != 1 {
		badRequest(w)
		return false
	}
	id, err := strconv.Atoi(params["id"][0])
	if err != nil || id != 0 {
		notFound(w)
		return false
	}
	return true
}
func (h *handler) searchStatus(w http.ResponseWriter, r *http.Request) {
	if !validSyntheticSearchID(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}
func (h *handler) searchDelete(w http.ResponseWriter, r *http.Request) {
	if validSyntheticSearchID(w, r) {
		w.WriteHeader(http.StatusOK)
	}
}
func (h *handler) searchDownloadTorrent(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "torrentUrl", "pluginName")
}
func (h *handler) searchEnablePlugin(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "names", "enable")
}
func (h *handler) searchInstallPlugin(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "sources")
}
func (h *handler) searchStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireQBTParams(w, r, "pattern", "category", "plugins"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"id": 0})
}
func (h *handler) searchStop(w http.ResponseWriter, r *http.Request) {
	if validSyntheticSearchID(w, r) {
		w.WriteHeader(http.StatusOK)
	}
}
func (h *handler) searchUninstallPlugin(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "names")
}
func (h *handler) searchUpdatePlugins(w http.ResponseWriter, r *http.Request) { qbtNoop(w, r) }
