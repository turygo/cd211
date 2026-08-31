package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/torrentmeta"
)

func (h *handler) webAPIVersion(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "2.11.0")
}
func (h *handler) version(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "v5.0.0-cd211")
}

func (h *handler) buildInfo(w http.ResponseWriter, _ *http.Request) {
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"qt": "", "libtorrent": "", "boost": "", "openssl": "", "zlib": "",
		"bitness": strconv.IntSize, "platform": platform,
	})
}

func (h *handler) defaultSavePath(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, h.config.LocalRoot)
}

func (h *handler) getDirectoryContent(w http.ResponseWriter, r *http.Request) {
	params, ok := qbtParams(w, r)
	if !ok {
		return
	}
	dirPath, present := exactlyOne(params["dirPath"])
	if !present || strings.TrimSpace(dirPath) == "" || strings.HasPrefix(dirPath, ":") {
		badRequest(w)
		return
	}
	mode := "all"
	if values, exists := params["mode"]; exists {
		var valid bool
		mode, valid = exactlyOne(values)
		if !valid {
			badRequest(w)
			return
		}
	}
	paths, err := h.filesystem.ListDirectory(dirPath, mode)
	if err != nil {
		switch {
		case errors.Is(err, fsafe.ErrUnsafePath), errors.Is(err, fsafe.ErrInvalidVisibility):
			badRequest(w)
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			notFound(w)
		default:
			internalError(w)
		}
		return
	}
	writeJSON(w, http.StatusOK, paths)
}

func (h *handler) networkInterfaceAddressList(w http.ResponseWriter, r *http.Request) {
	params, ok := qbtParams(w, r)
	if !ok {
		return
	}
	values, exists := params["iface"]
	if !exists || len(values) != 1 {
		badRequest(w)
		return
	}
	writeJSON(w, http.StatusOK, []string{})
}
func (h *handler) networkInterfaceList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *handler) preferences(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.ListSettings(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	addTrackers := settings["qbt.add_trackers"]
	enabled := settings["qbt.add_trackers_enabled"] == "true"
	// Keep every qB 5.0 preference key present. Values without a durable CD
	// equivalent are stable neutral defaults rather than invented state.
	data := map[string]any{
		"locale": "", "performance_warning": false, "file_log_enabled": false, "file_log_path": "", "file_log_backup_enabled": false, "file_log_max_size": 0, "file_log_delete_old": false, "file_log_age": 0, "file_log_age_type": 0, "delete_torrent_content_files": false,
		"torrent_content_layout": "Original", "add_to_top_of_queue": false, "add_stopped_enabled": false, "torrent_stop_condition": "None", "merge_trackers": false, "auto_delete_mode": 0, "preallocate_all": false, "incomplete_files_ext": false, "use_unwanted_folder": false,
		"auto_tmm_enabled": false, "torrent_changed_tmm_enabled": false, "save_path_changed_tmm_enabled": false, "category_changed_tmm_enabled": false, "use_subcategories": false, "save_path": h.config.LocalRoot, "temp_path_enabled": false, "temp_path": "", "use_category_paths_in_manual_mode": false, "export_dir": "", "export_dir_fin": "", "scan_dirs": map[string]any{},
		"excluded_file_names_enabled": false, "excluded_file_names": "", "mail_notification_enabled": false, "mail_notification_sender": "", "mail_notification_email": "", "mail_notification_smtp": "", "mail_notification_ssl_enabled": false, "mail_notification_auth_enabled": false, "mail_notification_username": "", "mail_notification_password": "", "autorun_on_torrent_added_enabled": false, "autorun_on_torrent_added_program": "", "autorun_enabled": false, "autorun_program": "",
		"listen_port": 0, "ssl_enabled": false, "ssl_listen_port": 0, "random_port": false, "upnp": false, "max_connec": 0, "max_connec_per_torrent": 0, "max_uploads": 0, "max_uploads_per_torrent": 0,
		"i2p_enabled": false, "i2p_address": "", "i2p_port": 0, "i2p_mixed_mode": false, "i2p_inbound_quantity": 0, "i2p_outbound_quantity": 0, "i2p_inbound_length": 0, "i2p_outbound_length": 0,
		"proxy_type": 0, "proxy_ip": "", "proxy_port": 0, "proxy_auth_enabled": false, "proxy_username": "", "proxy_password": "", "proxy_hostname_lookup": false, "proxy_bittorrent": false, "proxy_peer_connections": false, "proxy_rss": false, "proxy_misc": false,
		"ip_filter_enabled": false, "ip_filter_path": "", "ip_filter_trackers": false, "banned_IPs": "", "dl_limit": -1, "up_limit": -1, "alt_dl_limit": -1, "alt_up_limit": -1, "bittorrent_protocol": 0, "limit_utp_rate": false, "limit_tcp_overhead": false, "limit_lan_peers": false, "scheduler_enabled": false, "schedule_from_hour": 0, "schedule_from_min": 0, "schedule_to_hour": 0, "schedule_to_min": 0, "scheduler_days": 0,
		"dht": true, "pex": false, "lsd": false, "encryption": 0, "anonymous_mode": false, "max_active_checking_torrents": 0, "queueing_enabled": false, "max_active_downloads": 0, "max_active_torrents": 0, "max_active_uploads": 0, "dont_count_slow_torrents": false, "slow_torrent_dl_rate_threshold": 0, "slow_torrent_ul_rate_threshold": 0, "slow_torrent_inactive_timer": 0, "max_ratio_enabled": false, "max_ratio": -1.0, "max_seeding_time_enabled": false, "max_seeding_time": -1, "max_inactive_seeding_time_enabled": false, "max_inactive_seeding_time": -1, "max_ratio_act": 0,
		"web_ui_domain_list": "", "web_ui_address": "", "web_ui_port": 0, "web_ui_upnp": false, "use_https": false, "web_ui_https_cert_path": "", "web_ui_https_key_path": "", "web_ui_username": "", "bypass_local_auth": false, "bypass_auth_subnet_whitelist_enabled": false, "bypass_auth_subnet_whitelist": "", "web_ui_max_auth_fail_count": 0, "web_ui_ban_duration": 0, "web_ui_session_timeout": 0, "alternative_webui_enabled": false, "alternative_webui_path": "", "web_ui_clickjacking_protection_enabled": false, "web_ui_csrf_protection_enabled": false, "web_ui_secure_cookie_enabled": false, "web_ui_host_header_validation_enabled": false, "web_ui_use_custom_http_headers_enabled": false, "web_ui_custom_http_headers": "", "web_ui_reverse_proxy_enabled": false, "web_ui_reverse_proxies_list": "", "dyndns_enabled": false, "dyndns_service": 0, "dyndns_username": "", "dyndns_password": "", "dyndns_domain": "",
		"rss_refresh_interval": 0, "rss_fetch_delay": 0, "rss_max_articles_per_feed": 0, "rss_processing_enabled": false, "rss_auto_downloading_enabled": false, "rss_download_repack_proper_episodes": false, "rss_smart_episode_filters": "", "resume_data_storage_type": "", "torrent_content_remove_option": "", "memory_working_set_limit": 0, "current_network_interface": "", "current_interface_name": "", "current_interface_address": "", "save_resume_data_interval": 0, "torrent_file_size_limit": 0, "recheck_completed_torrents": false, "app_instance_name": "", "refresh_interval": 0, "resolve_peer_countries": false, "reannounce_when_address_changed": false,
		"bdecode_depth_limit": 0, "bdecode_token_limit": 0, "async_io_threads": 0, "hashing_threads": 0, "file_pool_size": 0, "checking_memory_use": 0, "disk_cache": 0, "disk_cache_ttl": 0, "disk_queue_size": 0, "disk_io_type": 0, "disk_io_read_mode": 0, "disk_io_write_mode": 0, "enable_coalesce_read_write": false, "enable_piece_extent_affinity": false, "enable_upload_suggestions": false, "send_buffer_watermark": 0, "send_buffer_low_watermark": 0, "send_buffer_watermark_factor": 0, "connection_speed": 0, "socket_send_buffer_size": 0, "socket_receive_buffer_size": 0, "socket_backlog_size": 0, "outgoing_ports_min": 0, "outgoing_ports_max": 0, "upnp_lease_duration": 0, "peer_tos": 0, "utp_tcp_mixed_mode": 0, "idn_support_enabled": false, "enable_multi_connections_from_same_ip": false, "validate_https_tracker_certificate": false, "ssrf_mitigation": false, "block_peers_on_privileged_ports": false, "enable_embedded_tracker": false, "embedded_tracker_port": 0, "embedded_tracker_port_forwarding": false, "mark_of_the_web": false, "python_executable_path": "", "upload_slots_behavior": 0, "upload_choking_algorithm": 0, "announce_to_all_trackers": false, "announce_to_all_tiers": false, "announce_ip": "", "max_concurrent_http_announces": 0, "stop_tracker_timeout": 0, "peer_turnover": 0, "peer_turnover_cutoff": 0, "peer_turnover_interval": 0, "request_queue_size": 0, "dht_bootstrap_nodes": "",
		"add_trackers": addTrackers, "add_trackers_enabled": enabled,
	}
	writeJSON(w, http.StatusOK, data)
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
	_ = json.NewEncoder(w).Encode(value)
}
