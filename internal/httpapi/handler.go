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
	GetDownload(context.Context, string) (domain.Download, error)
	ListDownloads(context.Context, *string) ([]domain.Download, error)
	ListDownloadFiles(context.Context, string) ([]domain.DownloadFile, error)
	SetCategory(context.Context, string, string, time.Time) error
	Start(context.Context, string, time.Time) error
	RequestDelete(context.Context, []string, bool, time.Time) error
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
	sessions   *session.Store
	clock      Clock
	waker      Waker
	filesystem filesystem
	service    *submission.Service
}

// New creates the authenticated qBittorrent-compatible HTTP handler. service
// is the shared submission boundary also consumed by the native API; it owns
// torrents/add parsing, category lookup, and persistence.
func New(config Config, credentials Credentials, repo repository, sessions *session.Store, clock Clock, waker Waker, files filesystem, service *submission.Service) (http.Handler, error) {
	if isNil(credentials) || isNil(repo) || sessions == nil || isNil(clock) || isNil(waker) || isNil(files) || isNil(service) {
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

	h := &handler{config: config, creds: credentials, repo: repo, sessions: sessions, clock: clock, waker: waker, filesystem: files, service: service}
	mux := http.NewServeMux()
	routes := map[string]string{
		apiPrefix + "auth/login":              http.MethodPost,
		apiPrefix + "auth/logout":             http.MethodPost,
		apiPrefix + "app/webapiVersion":       http.MethodGet,
		apiPrefix + "app/version":             http.MethodGet,
		apiPrefix + "app/preferences":         http.MethodGet,
		apiPrefix + "torrents/categories":     http.MethodGet,
		apiPrefix + "torrents/createCategory": http.MethodPost,
		apiPrefix + "torrents/setCategory":    http.MethodPost,
		apiPrefix + "torrents/add":            http.MethodPost,
		apiPrefix + "torrents/info":           http.MethodGet,
		apiPrefix + "torrents/properties":     http.MethodGet,
		apiPrefix + "torrents/files":          http.MethodGet,
		apiPrefix + "torrents/delete":         http.MethodPost,
		apiPrefix + "torrents/setForceStart":  http.MethodPost,
		apiPrefix + "torrents/setShareLimits": http.MethodPost,
		apiPrefix + "torrents/topPrio":        http.MethodPost,
	}
	mux.Handle("POST "+apiPrefix+"auth/login", http.HandlerFunc(h.login))
	mux.Handle("POST "+apiPrefix+"auth/logout", h.auth(h.logout))
	mux.Handle("GET "+apiPrefix+"app/webapiVersion", h.auth(h.webAPIVersion))
	mux.Handle("GET "+apiPrefix+"app/version", h.auth(h.version))
	mux.Handle("GET "+apiPrefix+"app/preferences", h.auth(h.preferences))
	mux.Handle("GET "+apiPrefix+"torrents/categories", h.auth(h.categories))
	mux.Handle("POST "+apiPrefix+"torrents/createCategory", h.auth(h.createCategory))
	mux.Handle("POST "+apiPrefix+"torrents/setCategory", h.auth(h.setCategory))
	mux.Handle("POST "+apiPrefix+"torrents/add", h.auth(h.addTorrent))
	mux.Handle("GET "+apiPrefix+"torrents/info", h.auth(h.info))
	mux.Handle("GET "+apiPrefix+"torrents/properties", h.auth(h.properties))
	mux.Handle("GET "+apiPrefix+"torrents/files", h.auth(h.files))
	mux.Handle("POST "+apiPrefix+"torrents/delete", h.auth(h.deleteTorrents))
	mux.Handle("POST "+apiPrefix+"torrents/setForceStart", h.auth(h.setForceStart))
	mux.Handle("POST "+apiPrefix+"torrents/setShareLimits", h.auth(h.emptyFormPost))
	mux.Handle("POST "+apiPrefix+"torrents/topPrio", h.auth(h.emptyFormPost))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, found := routes[r.URL.Path]
		if !found {
			notFound(w)
			return
		}
		if r.Method != method {
			plain(w, http.StatusMethodNotAllowed, "Method Not Allowed\n")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
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
