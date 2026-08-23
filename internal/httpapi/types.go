package httpapi

type preferences struct {
	SavePath                      string  `json:"save_path"`
	DHT                           bool    `json:"dht"`
	QueueingEnabled               bool    `json:"queueing_enabled"`
	MaxRatioEnabled               bool    `json:"max_ratio_enabled"`
	MaxRatio                      float64 `json:"max_ratio"`
	MaxSeedingTimeEnabled         bool    `json:"max_seeding_time_enabled"`
	MaxSeedingTime                int64   `json:"max_seeding_time"`
	MaxInactiveSeedingTimeEnabled bool    `json:"max_inactive_seeding_time_enabled"`
	MaxInactiveSeedingTime        int64   `json:"max_inactive_seeding_time"`
	MaxRatioAct                   int64   `json:"max_ratio_act"`
	AddTrackers                   string  `json:"add_trackers"`
	AddTrackersEnabled            bool    `json:"add_trackers_enabled"`
}

type categoryView struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}
type torrentInfo struct {
	Hash                     string  `json:"hash"`
	Name                     string  `json:"name"`
	Size                     int64   `json:"size"`
	Completed                int64   `json:"completed"`
	Progress                 float64 `json:"progress"`
	ETA                      int64   `json:"eta"`
	State                    string  `json:"state"`
	Category                 string  `json:"category"`
	Tags                     string  `json:"tags"`
	SavePath                 string  `json:"save_path"`
	ContentPath              string  `json:"content_path"`
	Ratio                    float64 `json:"ratio"`
	RatioLimit               float64 `json:"ratio_limit"`
	SeedingTime              int64   `json:"seeding_time"`
	SeedingTimeLimit         int64   `json:"seeding_time_limit"`
	InactiveSeedingTimeLimit int64   `json:"inactive_seeding_time_limit"`
	LastActivity             int64   `json:"last_activity"`
}

type torrentProperties struct {
	Hash        string `json:"hash"`
	SavePath    string `json:"save_path"`
	SeedingTime int64  `json:"seeding_time"`
}

type torrentFile struct {
	Index    int64   `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int64   `json:"priority"`
	IsSeed   bool    `json:"is_seed"`
}
