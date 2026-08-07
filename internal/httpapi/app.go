package httpapi

import (
	"encoding/json"
	"net/http"
)

func (h *handler) webAPIVersion(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "2.11.0")
}

func (h *handler) version(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "v5.0.0-cd211")
}

func (h *handler) preferences(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, preferences{
		SavePath:                      h.config.LocalRoot,
		DHT:                           true,
		QueueingEnabled:               false,
		MaxRatioEnabled:               false,
		MaxRatio:                      -1,
		MaxSeedingTimeEnabled:         false,
		MaxSeedingTime:                -1,
		MaxInactiveSeedingTimeEnabled: false,
		MaxInactiveSeedingTime:        -1,
		MaxRatioAct:                   0,
	})
}

func (h *handler) emptyFormPost(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseURLEncodedForm(w, r, formLimit); !ok {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
