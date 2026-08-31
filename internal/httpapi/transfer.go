package httpapi

import (
	"net/http"
	"strconv"
)

func (h *handler) transferDownloadLimit(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "-1")
}
func (h *handler) transferUploadLimit(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "-1")
}
func (h *handler) transferSpeedLimitsMode(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "0")
}
func (h *handler) transferInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"dl_info_speed": int64(0), "dl_info_data": int64(0), "up_info_speed": int64(0), "up_info_data": int64(0),
		"dl_rate_limit": int64(-1), "up_rate_limit": int64(-1), "dht_nodes": int64(0), "connection_status": "disconnected",
	})
}
func (h *handler) transferBanPeers(w http.ResponseWriter, r *http.Request) { qbtNoop(w, r, "peers") }
func (h *handler) transferSetDownloadLimit(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "limit")
}
func (h *handler) transferSetUploadLimit(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "limit")
}
func (h *handler) transferSetSpeedLimitsMode(w http.ResponseWriter, r *http.Request) {
	params, ok := requireQBTParams(w, r, "mode")
	if !ok {
		return
	}
	if _, err := strconv.Atoi(params["mode"][0]); err != nil {
		badRequest(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func (h *handler) transferToggleSpeedLimitsMode(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r)
}
