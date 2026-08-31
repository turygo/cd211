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
	Name                string `json:"name"`
	SavePath            string `json:"savePath"`
	DownloadPath        string `json:"downloadPath"`
	DownloadPathEnabled bool   `json:"downloadPathEnabled"`
}

type torrentInfo struct {
	AddedOn                  int64   `json:"added_on"`
	AmountLeft               int64   `json:"amount_left"`
	AutoTMM                  bool    `json:"auto_tmm"`
	Availability             float64 `json:"availability"`
	Category                 string  `json:"category"`
	Completed                int64   `json:"completed"`
	CompletionOn             int64   `json:"completion_on"`
	ContentPath              string  `json:"content_path"`
	DLSpeed                  int64   `json:"dlspeed"`
	DownloadLimit            int64   `json:"dl_limit"`
	DownloadPath             string  `json:"download_path"`
	Downloaded               int64   `json:"downloaded"`
	DownloadedSession        int64   `json:"downloaded_session"`
	ETA                      int64   `json:"eta"`
	FLPiecePrio              bool    `json:"f_l_piece_prio"`
	ForceStart               bool    `json:"force_start"`
	Hash                     string  `json:"hash"`
	InfohashV1               string  `json:"infohash_v1"`
	InfohashV2               string  `json:"infohash_v2"`
	InactiveSeedingTimeLimit int64   `json:"inactive_seeding_time_limit"`
	LastActivity             int64   `json:"last_activity"`
	MagnetURI                string  `json:"magnet_uri"`
	MaxRatio                 float64 `json:"max_ratio"`
	MaxSeedingTime           int64   `json:"max_seeding_time"`
	Name                     string  `json:"name"`
	NameLo                   string  `json:"name_l"`
	NumComplete              int64   `json:"num_complete"`
	NumIncomplete            int64   `json:"num_incomplete"`
	NumLeechs                int64   `json:"num_leechs"`
	NumSeeds                 int64   `json:"num_seeds"`
	Popularity               float64 `json:"popularity"`
	Priority                 int64   `json:"priority"`
	Private                  any     `json:"private"`
	Progress                 float64 `json:"progress"`
	Ratio                    float64 `json:"ratio"`
	RatioLimit               float64 `json:"ratio_limit"`
	SavePath                 string  `json:"save_path"`
	SeedingTime              int64   `json:"seeding_time"`
	SeedingTimeLimit         int64   `json:"seeding_time_limit"`
	SeenComplete             int64   `json:"seen_complete"`
	SeqDL                    bool    `json:"seq_dl"`
	Size                     int64   `json:"size"`
	State                    string  `json:"state"`
	SuperSeeding             bool    `json:"super_seeding"`
	Tags                     string  `json:"tags"`
	TimeActive               int64   `json:"time_active"`
	TotalSize                int64   `json:"total_size"`
	Tracker                  string  `json:"tracker"`
	TrackersCount            int64   `json:"trackers_count"`
	UploadLimit              int64   `json:"up_limit"`
	Uploaded                 int64   `json:"uploaded"`
	UploadedSession          int64   `json:"uploaded_session"`
	UPSpeed                  int64   `json:"upspeed"`
}

type torrentProperties struct {
	Hash              string  `json:"hash"`
	Name              string  `json:"name"`
	SavePath          string  `json:"save_path"`
	DownloadPath      string  `json:"download_path"`
	Comment           string  `json:"comment"`
	CreatedBy         string  `json:"created_by"`
	CreationDate      int64   `json:"creation_date"`
	AdditionDate      int64   `json:"addition_date"`
	CompletionDate    int64   `json:"completion_date"`
	LastSeen          int64   `json:"last_seen"`
	TotalSize         int64   `json:"total_size"`
	PieceSize         int64   `json:"piece_size"`
	PieceCount        int64   `json:"pieces_num"`
	PiecesHave        int64   `json:"pieces_have"`
	SeedingTime       int64   `json:"seeding_time"`
	TimeElapsed       int64   `json:"time_elapsed"`
	ETA               int64   `json:"eta"`
	ConnectCount      int64   `json:"nb_connections"`
	ConnectLimit      int64   `json:"nb_connections_limit"`
	Downloaded        int64   `json:"total_downloaded"`
	DownloadedSession int64   `json:"total_downloaded_session"`
	Uploaded          int64   `json:"total_uploaded"`
	UploadedSession   int64   `json:"total_uploaded_session"`
	DownloadSpeed     int64   `json:"dl_speed"`
	DownloadSpeedAvg  int64   `json:"dl_speed_avg"`
	UploadSpeed       int64   `json:"upload_speed"`
	UploadSpeedAvg    int64   `json:"upload_speed_avg"`
	DownloadLimit     int64   `json:"dl_limit"`
	UploadLimit       int64   `json:"up_limit"`
	Wasted            int64   `json:"total_wasted"`
	Seeds             int64   `json:"seeds"`
	SeedsTotal        int64   `json:"seeds_total"`
	Peers             int64   `json:"peers"`
	PeersTotal        int64   `json:"peers_total"`
	ShareRatio        float64 `json:"share_ratio"`
	Popularity        float64 `json:"popularity"`
	Reannounce        int64   `json:"reannounce"`
	Private           any     `json:"private"`
	IsPrivate         bool    `json:"is_private"`
	HasMetadata       bool    `json:"has_metadata"`
}

type torrentFile struct {
	Index        int64    `json:"index"`
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Progress     float64  `json:"progress"`
	Priority     int64    `json:"priority"`
	Availability float64  `json:"availability"`
	PieceRange   [2]int64 `json:"piece_range"`
	IsSeed       *bool    `json:"is_seed,omitempty"`
}
