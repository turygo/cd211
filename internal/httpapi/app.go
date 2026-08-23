package httpapi

import (
	"encoding/json"
	"github.com/turygo/cd211/internal/torrentmeta"
	"net/http"
	"strings"
)

func (h *handler) webAPIVersion(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "2.11.0")
}
func (h *handler) version(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "v5.0.0-cd211")
}

func (h *handler) preferences(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.ListSettings(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	addTrackers, enabled := settings["qbt.add_trackers"], settings["qbt.add_trackers_enabled"] == "true"
	writeJSON(w, http.StatusOK, preferences{SavePath: h.config.LocalRoot, DHT: true, QueueingEnabled: false, MaxRatioEnabled: false, MaxRatio: -1, MaxSeedingTimeEnabled: false, MaxSeedingTime: -1, MaxInactiveSeedingTimeEnabled: false, MaxInactiveSeedingTime: -1, MaxRatioAct: 0, AddTrackers: addTrackers, AddTrackersEnabled: enabled})
}

func (h *handler) setPreferences(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	raw, present := exactlyOne(form["json"])
	if !present || strings.TrimSpace(raw) == "" {
		badRequest(w)
		return
	}
	var submitted map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &submitted) != nil {
		badRequest(w)
		return
	}
	current := preferences{SavePath: h.config.LocalRoot, DHT: true, QueueingEnabled: false, MaxRatioEnabled: false, MaxRatio: -1, MaxSeedingTimeEnabled: false, MaxSeedingTime: -1, MaxInactiveSeedingTimeEnabled: false, MaxInactiveSeedingTime: -1, MaxRatioAct: 0}
	settings, err := h.repo.ListSettings(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	current.AddTrackers, current.AddTrackersEnabled = settings["qbt.add_trackers"], settings["qbt.add_trackers_enabled"] == "true"
	known := map[string]bool{"save_path": true, "dht": true, "queueing_enabled": true, "max_ratio_enabled": true, "max_ratio": true, "max_seeding_time_enabled": true, "max_seeding_time": true, "max_inactive_seeding_time_enabled": true, "max_inactive_seeding_time": true, "max_ratio_act": true, "add_trackers": true, "add_trackers_enabled": true}
	for key, value := range submitted {
		if !known[key] {
			badRequest(w)
			return
		}
		if key == "add_trackers" || key == "add_trackers_enabled" {
			continue
		}
		var expected any
		switch key {
		case "save_path":
			expected = current.SavePath
		case "dht":
			expected = current.DHT
		case "queueing_enabled":
			expected = current.QueueingEnabled
		case "max_ratio_enabled":
			expected = current.MaxRatioEnabled
		case "max_ratio":
			expected = current.MaxRatio
		case "max_seeding_time_enabled":
			expected = current.MaxSeedingTimeEnabled
		case "max_seeding_time":
			expected = current.MaxSeedingTime
		case "max_inactive_seeding_time_enabled":
			expected = current.MaxInactiveSeedingTimeEnabled
		case "max_inactive_seeding_time":
			expected = current.MaxInactiveSeedingTime
		case "max_ratio_act":
			expected = current.MaxRatioAct
		}
		encoded, _ := json.Marshal(expected)
		if string(encoded) != string(value) {
			conflict(w)
			return
		}
	}
	var trackersValue *string
	var enabledValue *bool
	if value, exists := submitted["add_trackers"]; exists {
		var trackers string
		if json.Unmarshal(value, &trackers) != nil {
			badRequest(w)
			return
		}
		if strings.TrimSpace(trackers) == "" {
			empty := ""
			trackersValue = &empty
		} else {
			lines := strings.Split(trackers, "\n")
			normalized := make([]string, 0, len(lines))
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					badRequest(w)
					return
				}
				normalized = append(normalized, line)
			}
			canonical, normalizeErr := torrentmeta.NormalizeTrackers(normalized, h.config.TorrentLimits)
			if normalizeErr != nil {
				badRequest(w)
				return
			}
			canonicalValue := strings.Join(canonical, "\n")
			trackersValue = &canonicalValue
		}
	}
	if value, exists := submitted["add_trackers_enabled"]; exists {
		var enabled bool
		if json.Unmarshal(value, &enabled) != nil {
			badRequest(w)
			return
		}
		enabledValue = &enabled
	}
	if trackersValue != nil || enabledValue != nil {
		if err := h.repo.UpdateQBTPreferences(r.Context(), trackersValue, enabledValue, h.now()); err != nil {
			internalError(w)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
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
