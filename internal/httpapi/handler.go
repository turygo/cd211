// Package httpapi implements the supported qBittorrent WebAPI 2.11 profile.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"reflect"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/qbtkey"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
	"github.com/turygo/cd211/internal/torrentmeta"
)

const (
	apiPrefix       = "/api/v2/"
	formLimit int64 = 64 << 10
)

// repository is the durable API surface required by the WebAPI. Submission
// persistence and category lookup for torrents/add live in the shared
// submission.Service injected at construction.
type repository interface {
	UpsertCategory(context.Context, domain.Category) (domain.Category, error)
	ListCategories(context.Context) ([]domain.Category, error)
	GetCategory(context.Context, string) (domain.Category, error)
	GetDownload(context.Context, string) (domain.Download, error)
	ListDownloads(context.Context, *string) ([]domain.Download, error)
	ListDownloadFiles(context.Context, string) ([]domain.DownloadFile, error)
	ListDownloadFileOverrides(context.Context, string) ([]domain.FileOverride, error)
	SetCategory(context.Context, string, string, time.Time) error
	Start(context.Context, string, time.Time) error
	Pause(context.Context, string, time.Time) error
	Retry(context.Context, string, domain.State, time.Time) error
	RequestDelete(context.Context, []string, bool, time.Time) error
	UpdateTags(context.Context, []string, string, time.Time) error
	AddTags(context.Context, []string, string, time.Time) error
	SetAutoTMM(context.Context, []string, bool, time.Time) error
	StartMany(context.Context, []string, time.Time) error
	SetSavePath(context.Context, string, string, int64, time.Time) error
	SetSavePaths(context.Context, []string, string, time.Time) error
	SetFileOverride(context.Context, string, int64, string, int64, time.Time) error
	SetFilePriorities(context.Context, string, []int64, int64, time.Time) error
	ListSettings(context.Context) (map[string]string, error)
	ReplaceSettings(context.Context, map[string]string, time.Time) error
	UpdateQBTPreferences(context.Context, *string, *bool, time.Time) error
	ListTags(context.Context) ([]string, error)
	CreateTags(context.Context, string, time.Time) error
	DeleteTags(context.Context, string, time.Time) error
	RemoveTags(context.Context, []string, string, time.Time) error
	RemoveCategories(context.Context, []string, time.Time) error
	RenameDownload(context.Context, string, string, time.Time) error
	RenameFolder(context.Context, string, string, string, time.Time) error
	Reverify(context.Context, string, time.Time) error
}

// qbtRepository is intentionally kept as a named interface: route handlers
// depend only on the durable operations they expose, not on *store.Store.

// QBTAPIKeyRepository is the narrow qBittorrent API key lookup boundary used
// by the HTTP authentication middleware.
type QBTAPIKeyRepository interface {
	GetQBTAPIKey(context.Context) (qbtkey.Key, error)
}

// Clock provides the current time to handlers.
type Clock interface {
	Now() time.Time
}

// Waker schedules processing after a durable mutation commits.
type Waker interface {
	Wake()
}

type filesystem interface {
	Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error)
	ResolveSaveRoot(string) (string, bool, error)
	PrepareSaveRoot(string) (string, error)
	ListDirectory(string, string) ([]string, error)
}

// Config provides the fixed API path boundaries and submission bounds.
type Config struct {
	CloudRoot       string
	LocalRoot       string
	TorrentLimits   torrentmeta.Limits
	MaxRequestBytes int64
}

// Credentials verifies operator credentials shared with the Web UI.
type Credentials interface {
	Verify(ctx context.Context, username, password string) (bool, error)
}

type handler struct {
	config     Config
	creds      Credentials
	repo       repository
	qbtkeys    QBTAPIKeyRepository
	sessions   *session.Store
	clock      Clock
	waker      Waker
	filesystem filesystem
	service    *submission.Service
}

// Handler serves the qBittorrent-compatible API. LoginHandler exposes only
// the protocol login endpoint so composition can mount it outside the
// protected prefix boundary.
type Handler struct {
	inner *handler
	serve http.Handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serve.ServeHTTP(w, r)
}

func (h *Handler) LoginHandler() http.Handler {
	return http.HandlerFunc(h.inner.login)
}

// New creates the qBittorrent-compatible HTTP handler.
func New(config Config, credentials Credentials, repo repository, sessions *session.Store, clock Clock, waker Waker, files filesystem, service *submission.Service, qbtkeys QBTAPIKeyRepository) (*Handler, error) {
	if isNil(credentials) || isNil(repo) || isNil(qbtkeys) || sessions == nil || isNil(clock) || isNil(waker) || isNil(files) || isNil(service) {
		return nil, errors.New("httpapi dependency is nil")
	}
	if !validCloudRoot(config.CloudRoot) {
		return nil, errors.New("cloud root must be an absolute clean POSIX path")
	}
	if !filepath.IsAbs(config.LocalRoot) || filepath.Clean(config.LocalRoot) != config.LocalRoot {
		return nil, errors.New("local root must be an absolute clean host path")
	}
	if !validLimits(config.TorrentLimits) {
		return nil, errors.New("torrent limits are invalid")
	}
	if config.MaxRequestBytes < int64(config.TorrentLimits.MaxInputBytes)+(64<<10) {
		return nil, errors.New("request limit is too small")
	}

	h := &handler{config: config, creds: credentials, repo: repo, qbtkeys: qbtkeys, sessions: sessions, clock: clock, waker: waker, filesystem: files, service: service}
	mux := http.NewServeMux()
	routes := map[string]string{}
	handlers := map[string]http.Handler{}
	addRoute := func(name, method string, fn http.HandlerFunc) {
		route := apiPrefix + name
		routes[route], handlers[route] = method, http.HandlerFunc(fn)
	}
	routes[apiPrefix+"auth/login"] = http.MethodPost
	handlers[apiPrefix+"auth/login"] = http.HandlerFunc(h.login)
	addRoute("auth/logout", http.MethodPost, h.logout)

	for _, name := range []string{"app/buildInfo", "app/defaultSavePath", "app/getDirectoryContent",
		"app/networkInterfaceAddressList", "app/networkInterfaceList", "app/preferences",
		"app/version", "app/webapiVersion", "log/main", "log/peers", "rss/items",
		"rss/matchingArticles", "rss/rules", "search/plugins", "search/results",
		"search/status", "sync/maindata", "sync/torrentPeers", "torrents/SSLParameters",
		"torrents/categories", "torrents/count", "torrents/downloadLimit",
		"torrents/files", "torrents/info", "torrents/pieceHashes", "torrents/pieceStates",
		"torrents/properties", "torrents/tags", "torrents/trackers", "torrents/uploadLimit",
		"torrents/webseeds", "transfer/downloadLimit", "transfer/info",
		"transfer/speedLimitsMode", "transfer/uploadLimit"} {
		var fn http.HandlerFunc
		switch name {
		case "app/buildInfo":
			fn = h.buildInfo
		case "app/defaultSavePath":
			fn = h.defaultSavePath
		case "app/getDirectoryContent":
			fn = h.getDirectoryContent
		case "app/networkInterfaceAddressList":
			fn = h.networkInterfaceAddressList
		case "app/networkInterfaceList":
			fn = h.networkInterfaceList
		case "app/preferences":
			fn = h.preferences
		case "app/version":
			fn = h.version
		case "app/webapiVersion":
			fn = h.webAPIVersion
		case "log/main":
			fn = h.logMain
		case "log/peers":
			fn = h.logPeers
		case "rss/items":
			fn = h.rssItems
		case "rss/matchingArticles":
			fn = h.rssMatchingArticles
		case "rss/rules":
			fn = h.rssRules
		case "search/plugins":
			fn = h.searchPlugins
		case "search/results":
			fn = h.searchResults
		case "search/status":
			fn = h.searchStatus
		case "sync/maindata":
			fn = h.syncMainData
		case "sync/torrentPeers":
			fn = h.syncTorrentPeers
		case "torrents/SSLParameters":
			fn = h.sslParameters
		case "torrents/categories":
			fn = h.categories
		case "torrents/count":
			fn = h.torrentCount
		case "torrents/downloadLimit":
			fn = h.downloadLimit
		case "torrents/files":
			fn = h.files
		case "torrents/info":
			fn = h.info
		case "torrents/pieceHashes":
			fn = h.pieceHashes
		case "torrents/pieceStates":
			fn = h.pieceStates
		case "torrents/properties":
			fn = h.properties
		case "torrents/tags":
			fn = h.tags
		case "torrents/trackers":
			fn = h.trackers
		case "torrents/uploadLimit":
			fn = h.uploadLimit
		case "torrents/webseeds":
			fn = h.webseeds
		case "transfer/downloadLimit":
			fn = h.transferDownloadLimit
		case "transfer/info":
			fn = h.transferInfo
		case "transfer/speedLimitsMode":
			fn = h.transferSpeedLimitsMode
		case "transfer/uploadLimit":
			fn = h.transferUploadLimit
		}
		addRoute(name, "read", fn)
	}
	for _, name := range []string{
		"app/sendTestEmail", "app/setPreferences", "app/shutdown",
		"rss/addFeed", "rss/addFolder", "rss/markAsRead", "rss/moveItem", "rss/refreshItem",
		"rss/removeItem", "rss/removeRule", "rss/renameRule", "rss/setFeedURL", "rss/setRule",
		"search/delete", "search/downloadTorrent", "search/enablePlugin", "search/installPlugin",
		"search/start", "search/stop", "search/uninstallPlugin", "search/updatePlugins",
		"transfer/banPeers", "transfer/setDownloadLimit", "transfer/setSpeedLimitsMode",
		"transfer/setUploadLimit", "transfer/toggleSpeedLimitsMode",
		"torrents/add", "torrents/addPeers", "torrents/addTags", "torrents/addTrackers",
		"torrents/bottomPrio", "torrents/createCategory", "torrents/createTags",
		"torrents/decreasePrio", "torrents/delete", "torrents/deleteTags",
		"torrents/editCategory", "torrents/editTracker", "torrents/export", "torrents/filePrio",
		"torrents/increasePrio", "torrents/reannounce", "torrents/recheck",
		"torrents/removeCategories", "torrents/removeTags", "torrents/removeTrackers",
		"torrents/rename", "torrents/renameFile", "torrents/renameFolder",
		"torrents/setAutoManagement", "torrents/setCategory", "torrents/setDownloadLimit",
		"torrents/setDownloadPath", "torrents/setForceStart", "torrents/setLocation",
		"torrents/setSSLParameters", "torrents/setSavePath", "torrents/setShareLimits",
		"torrents/setSuperSeeding", "torrents/setUploadLimit", "torrents/start",
		"torrents/stop", "torrents/toggleFirstLastPiecePrio", "torrents/toggleSequentialDownload",
		"torrents/topPrio",
	} {
		var fn http.HandlerFunc = h.emptyFormPost
		switch name {
		case "app/setPreferences":
			fn = h.setPreferences
		case "app/sendTestEmail", "app/shutdown":
			fn = h.emptyFormPost
		case "rss/addFeed":
			fn = h.rssAddFeed
		case "rss/addFolder":
			fn = h.rssAddFolder
		case "rss/markAsRead":
			fn = h.rssMarkAsRead
		case "rss/moveItem":
			fn = h.rssMoveItem
		case "rss/refreshItem":
			fn = h.rssRefreshItem
		case "rss/removeItem":
			fn = h.rssRemoveItem
		case "rss/removeRule":
			fn = h.rssRemoveRule
		case "rss/renameRule":
			fn = h.rssRenameRule
		case "rss/setFeedURL":
			fn = h.rssSetFeedURL
		case "rss/setRule":
			fn = h.rssSetRule
		case "search/delete":
			fn = h.searchDelete
		case "search/downloadTorrent":
			fn = h.searchDownloadTorrent
		case "search/enablePlugin":
			fn = h.searchEnablePlugin
		case "search/installPlugin":
			fn = h.searchInstallPlugin
		case "search/start":
			fn = h.searchStart
		case "search/stop":
			fn = h.searchStop
		case "search/uninstallPlugin":
			fn = h.searchUninstallPlugin
		case "search/updatePlugins":
			fn = h.searchUpdatePlugins
		case "transfer/banPeers":
			fn = h.transferBanPeers
		case "transfer/setDownloadLimit":
			fn = h.transferSetDownloadLimit
		case "transfer/setSpeedLimitsMode":
			fn = h.transferSetSpeedLimitsMode
		case "transfer/setUploadLimit":
			fn = h.transferSetUploadLimit
		case "transfer/toggleSpeedLimitsMode":
			fn = h.transferToggleSpeedLimitsMode
		case "torrents/add":
			fn = h.addTorrent
		case "torrents/addPeers":
			fn = h.addPeers
		case "torrents/addTags":
			fn = h.addTags
		case "torrents/addTrackers":
			fn = h.addTrackers
		case "torrents/bottomPrio":
			fn = h.bottomPriority
		case "torrents/createCategory":
			fn = h.createCategory
		case "torrents/createTags":
			fn = h.createTags
		case "torrents/decreasePrio":
			fn = h.decreasePriority
		case "torrents/delete":
			fn = h.deleteTorrents
		case "torrents/deleteTags":
			fn = h.deleteTags
		case "torrents/editCategory":
			fn = h.editCategory
		case "torrents/editTracker":
			fn = h.editTracker
		case "torrents/export":
			fn = h.exportTorrent
		case "torrents/filePrio":
			fn = h.filePriority
		case "torrents/increasePrio":
			fn = h.increasePriority
		case "torrents/reannounce":
			fn = h.reannounce
		case "torrents/recheck":
			fn = h.recheck
		case "torrents/removeCategories":
			fn = h.removeCategories
		case "torrents/removeTags":
			fn = h.removeTags
		case "torrents/removeTrackers":
			fn = h.removeTrackers
		case "torrents/rename":
			fn = h.renameTorrent
		case "torrents/renameFile":
			fn = h.renameFile
		case "torrents/renameFolder":
			fn = h.renameFolder
		case "torrents/setAutoManagement":
			fn = h.setAutoManagement
		case "torrents/setCategory":
			fn = h.setCategory
		case "torrents/setDownloadLimit":
			fn = h.setDownloadLimit
		case "torrents/setDownloadPath":
			fn = h.setDownloadPath
		case "torrents/setForceStart":
			fn = h.setForceStart
		case "torrents/setLocation":
			fn = h.setLocation
		case "torrents/setSSLParameters":
			fn = h.setSSLParameters
		case "torrents/setSavePath":
			fn = h.setSavePath
		case "torrents/setShareLimits":
			fn = h.setShareLimits
		case "torrents/setSuperSeeding":
			fn = h.setSuperSeeding
		case "torrents/setUploadLimit":
			fn = h.setUploadLimit
		case "torrents/start":
			fn = h.start
		case "torrents/stop":
			fn = h.stop
		case "torrents/toggleFirstLastPiecePrio":
			fn = h.toggleFirstLastPiecePriority
		case "torrents/toggleSequentialDownload":
			fn = h.toggleSequentialDownload
		case "torrents/topPrio":
			fn = h.topPriority
		}
		addRoute(name, http.MethodPost, fn)
	}
	for route, fn := range handlers {
		method := routes[route]
		if method == "read" {
			mux.Handle("GET "+route, fn)
			mux.Handle("POST "+route, fn)
		} else {
			mux.Handle(method+" "+route, fn)
		}
	}
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, found := routes[r.URL.Path]
		if !found {
			notFound(w)
			return
		}
		if method == "read" {
			if r.Method != http.MethodGet && r.Method != http.MethodPost {
				plain(w, http.StatusMethodNotAllowed, "Method Not Allowed\n")
				return
			}
		} else if r.Method != method {
			plain(w, http.StatusMethodNotAllowed, "Method Not Allowed\n")
			return
		}
		mux.ServeHTTP(w, r)
	})
	protected := h.authBoundary(dispatch)
	return &Handler{inner: h, serve: protected}, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	valueOf := reflect.ValueOf(value)
	switch valueOf.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return valueOf.IsNil()
	default:
		return false
	}
}

func validCloudRoot(root string) bool {
	return path.IsAbs(root) && path.Clean(root) == root
}

func validLimits(limits torrentmeta.Limits) bool {
	return limits.MaxInputBytes > 0 && limits.MaxInfoBytes > 0 && limits.MaxFiles > 0 &&
		limits.MaxNameBytes > 0 && limits.MaxPathBytes > 0 && limits.MaxComponentBytes > 0 &&
		limits.MaxTrackerCount > 0 && limits.MaxTrackerBytes > 0 && limits.MaxTotalSize > 0
}

func (h *handler) now() time.Time { return h.clock.Now().UTC() }

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
func plainExact(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func badRequest(w http.ResponseWriter) { plain(w, http.StatusBadRequest, "Bad Request\n") }
func forbidden(w http.ResponseWriter)  { plain(w, http.StatusForbidden, "Forbidden\n") }
func notFound(w http.ResponseWriter)   { plain(w, http.StatusNotFound, "Not Found\n") }
func conflict(w http.ResponseWriter)   { plain(w, http.StatusConflict, "Conflict\n") }
func internalError(w http.ResponseWriter) {
	plain(w, http.StatusInternalServerError, "Internal Server Error\n")
}

func repositoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if errors.Is(err, store.ErrInvalidTransition) {
		conflict(w)
		return
	}
	internalError(w)
}
