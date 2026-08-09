// Package web implements the server-rendered CD211 operator interface.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
)

const (
	formLimit          int64 = 64 << 10
	cloudStatusTimeout       = 500 * time.Millisecond
	minPasswordLength        = 8
)

//go:embed templates/*.html static/app.css static/app.js
var assets embed.FS

// Repository is the durable surface required by the operator interface.
type Repository interface {
	ListDownloads(context.Context, *string) ([]domain.Download, error)
	GetDownload(context.Context, string) (domain.Download, error)
	ListDownloadFiles(context.Context, string) ([]domain.DownloadFile, error)
	ListCategories(context.Context) ([]domain.Category, error)
	UpsertCategory(context.Context, domain.Category) (domain.Category, error)
	Start(context.Context, string, time.Time) error
	Retry(context.Context, string, domain.State, time.Time) error
	Cancel(context.Context, string, time.Time) error
	RequestDelete(context.Context, []string, bool, time.Time) error
}

// Clock provides deterministic display ages and mutation timestamps.
type Clock interface {
	Now() time.Time
}

// Waker schedules workflow processing after a durable action commits.
type Waker interface {
	Wake()
}

// CloudStatus reports whether the CloudDrive2 dependency is reachable.
type CloudStatus interface {
	Check(context.Context) error
}

// Filesystem prepares canonical staging roots beneath the configured local root.
type Filesystem interface {
	ResolveSaveRoot(string) (string, bool, error)
	PrepareSaveRoot(string) (string, error)
}

// Config contains the fixed path boundaries.
type Config struct {
	CloudRoot string
	LocalRoot string
}

// Credentials verifies operator credentials and applies password changes.
// A failed current-password proof is reported as creds.ErrCurrentPasswordMismatch.
type Credentials interface {
	Verify(ctx context.Context, username, password string) (bool, error)
	Change(ctx context.Context, current, next string, now time.Time) error
}

// SettingsStore persists runtime settings and category path remaps.
type SettingsStore interface {
	ListSettings(ctx context.Context) (map[string]string, error)
	ReplaceSettingsAndCategories(ctx context.Context, values map[string]string, categories []domain.Category, now time.Time) error
}

// SettingsDeps wires the authenticated settings page.
type SettingsDeps struct {
	Store SettingsStore
	// Dial establishes CloudDrive2 test connections; nil uses the default
	// *clouddrive.Client adapter.
	Dial DialFunc
	// Apply swaps the persisted configuration into the running process. It is
	// invoked only after Store.ReplaceSettingsAndCategories succeeded; a nil
	// Apply persists without activating (a restart applies the settings).
	Apply func(ctx context.Context, cfg settings.Config) error
}

type handler struct {
	config      Config
	creds       Credentials
	repo        Repository
	sessions    *session.Store
	clock       Clock
	waker       Waker
	cloudStatus CloudStatus
	filesystem  Filesystem
	settings    SettingsDeps
	templates   *template.Template
}

type authContextKey struct{}

type authenticatedSession struct {
	sid     string
	session session.Session
}

// New constructs the server-rendered operator interface.
func New(config Config, credentials Credentials, repo Repository, sessions *session.Store, clock Clock, waker Waker, cloudStatus CloudStatus, filesystem Filesystem, settings SettingsDeps) (http.Handler, error) {
	if isNil(credentials) || isNil(repo) || sessions == nil || isNil(clock) || isNil(waker) || isNil(cloudStatus) || isNil(filesystem) || isNil(settings.Store) {
		return nil, errors.New("web dependency is nil")
	}
	dial := settings.Dial
	if dial == nil {
		dial = defaultDial
	}
	settings.Dial = dial
	if !path.IsAbs(config.CloudRoot) || path.Clean(config.CloudRoot) != config.CloudRoot {
		return nil, errors.New("cloud root must be an absolute clean POSIX path")
	}
	if !filepath.IsAbs(config.LocalRoot) || filepath.Clean(config.LocalRoot) != config.LocalRoot {
		return nil, errors.New("local root must be an absolute clean host path")
	}
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, errors.New("parse web templates")
	}

	h := &handler{
		config:      config,
		creds:       credentials,
		repo:        repo,
		sessions:    sessions,
		clock:       clock,
		waker:       waker,
		cloudStatus: cloudStatus,
		filesystem:  filesystem,
		settings:    settings,
		templates:   templates,
	}
	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(h.loginPage))
	mux.Handle("POST /login", http.HandlerFunc(h.login))
	mux.Handle("GET /lang", http.HandlerFunc(setLang))
	mux.Handle("GET /static/app.css", http.HandlerFunc(staticCSS))
	mux.Handle("GET /static/app.js", http.HandlerFunc(staticJS))
	mux.Handle("GET /", h.auth(h.downloads, false))
	mux.Handle("GET /downloads/{hash}", h.auth(h.detail, false))
	mux.Handle("GET /categories", h.auth(h.categories, false))
	mux.Handle("GET /settings", h.auth(h.settingsPage, false))
	mux.Handle("GET /password", h.auth(h.passwordPage, false))
	mux.Handle("POST /password", h.auth(h.changePassword, true))
	mux.Handle("POST /logout", h.auth(h.logout, true))
	mux.Handle("POST /categories/save", h.auth(h.saveCategory, true))
	mux.Handle("POST /settings/test", h.auth(h.settingsTest, true))
	mux.Handle("POST /settings/save", h.auth(h.settingsSave, true))
	mux.Handle("POST /downloads/{hash}/start", h.auth(h.start, true))
	mux.Handle("POST /downloads/{hash}/retry", h.auth(h.retry, true))
	mux.Handle("POST /downloads/{hash}/cancel", h.auth(h.cancel, true))
	mux.Handle("POST /downloads/{hash}/remove", h.auth(h.remove, true))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, found := routeMethod(r.URL.Path, r.Method)
		if !found {
			htmlHeaders(w)
			plain(w, http.StatusNotFound, "Not Found\n")
			return
		}
		if r.Method != method {
			htmlHeaders(w)
			plain(w, http.StatusMethodNotAllowed, "Method Not Allowed\n")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("X-Content-Type-Options", "nosniff")
		} else {
			htmlHeaders(w)
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func routeMethod(requestPath, requestMethod string) (string, bool) {
	switch requestPath {
	case "/login", "/password":
		if requestMethod == http.MethodPost {
			return http.MethodPost, true
		}
		return http.MethodGet, true
	case "/", "/categories", "/settings", "/lang", "/static/app.css", "/static/app.js":
		return http.MethodGet, true
	case "/logout", "/categories/save", "/settings/test", "/settings/save":
		return http.MethodPost, true
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	if len(parts) == 2 && parts[0] == "downloads" && parts[1] != "" {
		return http.MethodGet, true
	}
	if len(parts) == 3 && parts[0] == "downloads" && parts[1] != "" {
		switch parts[2] {
		case "start", "retry", "cancel", "remove":
			return http.MethodPost, true
		}
	}
	return "", false
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func browserOriginAllowed(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, r.Host)
}

func (h *handler) auth(next http.HandlerFunc, csrf bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("SID")
		if err != nil || cookie.Value == "" {
			h.redirectLogin(w, r)
			return
		}
		current, ok := h.sessions.Get(cookie.Value)
		if !ok {
			h.redirectLogin(w, r)
			return
		}
		if csrf {
			if !browserOriginAllowed(r) {
				plain(w, http.StatusForbidden, "Forbidden\n")
				return
			}
			form, parsed := parseURLEncodedForm(w, r)
			if !parsed {
				return
			}
			token, exact := exactlyOne(form["csrf_token"])
			if !exact {
				plain(w, http.StatusForbidden, "Forbidden\n")
				return
			}
			tokenDigest := sha256.Sum256([]byte(token))
			expectedDigest := sha256.Sum256([]byte(current.CSRFToken))
			if subtle.ConstantTimeCompare(tokenDigest[:], expectedDigest[:]) != 1 {
				plain(w, http.StatusForbidden, "Forbidden\n")
				return
			}
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, authenticatedSession{sid: cookie.Value, session: current})
		next(w, r.WithContext(ctx))
	})
}

func (h *handler) redirectLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func htmlHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// "same-origin" keeps referrers internal while still letting browsers send
	// a real Origin header on same-origin form POSTs; under "no-referrer",
	// Chrome serializes that Origin as "null", which the CSRF origin check
	// must reject.
	w.Header().Set("Referrer-Policy", "same-origin")
}

func staticCSS(w http.ResponseWriter, _ *http.Request) {
	static(w, "static/app.css", "text/css; charset=utf-8")
}

func staticJS(w http.ResponseWriter, _ *http.Request) {
	static(w, "static/app.js", "text/javascript; charset=utf-8")
}

func static(w http.ResponseWriter, name, contentType string) {
	content, err := assets.ReadFile(name)
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public,max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "login", loginView(requestLang(r), ""))
}

// setLang stores the display-language preference and returns to the page the
// operator came from. The cookie carries no authority, so a GET is safe here.
func setLang(w http.ResponseWriter, r *http.Request) {
	lang := LangEN
	if Lang(r.URL.Query().Get("to")) == LangZH {
		lang = LangZH
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookie,
		Value:    string(lang),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	back := r.URL.Query().Get("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") || strings.ContainsAny(back, "\\\r\n") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func loginView(lang Lang, errorText string) LoginView {
	str := tr(lang)
	return LoginView{Title: str.TitleSignIn, Error: errorText, Lang: lang, OtherLang: otherLang(lang), Path: "/login", Str: str}
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r)
	if !ok {
		return
	}
	username, usernameOK := exactlyOne(form["username"])
	password, passwordOK := exactlyOne(form["password"])
	credentialsValid := usernameOK && passwordOK
	if credentialsValid {
		match, err := h.creds.Verify(r.Context(), username, password)
		if err != nil {
			plain(w, http.StatusInternalServerError, "Internal Server Error\n")
			return
		}
		credentialsValid = match
	}
	switch h.sessions.AuthorizeLogin(r.RemoteAddr, credentialsValid) {
	case session.LoginBanned:
		plain(w, http.StatusForbidden, "Forbidden\n")
		return
	case session.LoginInvalid:
		h.render(w, http.StatusUnauthorized, "login", loginView(requestLang(r), tr(requestLang(r)).LoginFailed))
		return
	}
	sid, _, err := h.sessions.Create()
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	http.SetCookie(w, sidCookie(sid, false, r.TLS != nil))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	current, ok := r.Context().Value(authContextKey{}).(authenticatedSession)
	if !ok {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	h.sessions.Revoke(current.sid)
	http.SetCookie(w, sidCookie("", true, r.TLS != nil))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func sidCookie(value string, expired, secure bool) *http.Cookie {
	cookie := &http.Cookie{Name: "SID", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure}
	if expired {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0).UTC()
	}
	return cookie
}

func (h *handler) downloads(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	viewValues, hasView := query["view"]
	if hasView && len(viewValues) != 1 {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	view := "all"
	if hasView {
		view = viewValues[0]
	}
	if !validDownloadView(view) {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	categoryValues, hasCategory := query["category"]
	if hasCategory && len(categoryValues) != 1 {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	var category *string
	selectedCategory := ""
	if hasCategory && categoryValues[0] != "" {
		selectedCategory = categoryValues[0]
		category = &selectedCategory
	}
	downloads, err := h.repo.ListDownloads(r.Context(), category)
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	cloudContext, cancel := context.WithTimeout(r.Context(), cloudStatusTimeout)
	cloudOnline := h.cloudStatus.Check(cloudContext) == nil
	cancel()
	page, err := buildDownloadsView(downloads, categories, view, selectedCategory, h.authSession(r).CSRFToken, h.clock.Now().UTC(), cloudOnline, requestLang(r))
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	page.Path = r.URL.RequestURI()
	h.render(w, http.StatusOK, "downloads", page)
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	hash, ok := canonicalHash(r.PathValue("hash"))
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	cleanupFailure := cleanupFailed(download)
	if !download.State.Visible() && !cleanupFailure {
		plain(w, http.StatusNotFound, "Not Found\n")
		return
	}
	files, err := h.repo.ListDownloadFiles(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	page, err := buildDetailView(download, files, h.authSession(r).CSRFToken, requestLang(r))
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	page.Path = r.URL.RequestURI()
	h.render(w, http.StatusOK, "detail", page)
}

func (h *handler) categories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	page := buildCategoriesView(
		categories, h.config.CloudRoot, h.config.LocalRoot, h.authSession(r).CSRFToken,
		r.URL.Query().Get("onboarding") == "1", requestLang(r),
	)
	page.Path = r.URL.RequestURI()
	h.render(w, http.StatusOK, "categories", page)
}

func (h *handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	values, err := h.settings.Store.ListSettings(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	str := tr(requestLang(r))
	notice, success := "", false
	if r.URL.Query().Get("saved") == "1" {
		notice, success = str.SettingsSaved, true
	}
	view := SettingsView{
		PageMeta:   pageMeta(str.TitleSettings, "settings", h.authSession(r).CSRFToken, requestLang(r)),
		Values:     settingsFormFromValues(values),
		Categories: buildSettingsCategoryPaths(categories, h.config.CloudRoot, h.config.LocalRoot),
		Notice:     notice, Success: success,
	}
	view.Path = "/settings"
	h.render(w, http.StatusOK, "settings", view)
}

func (h *handler) settingsTest(w http.ResponseWriter, r *http.Request) {
	form, errMsg, ok := parseSettingsForm(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	if errMsg != "" {
		h.renderSettings(w, r, http.StatusBadRequest, form, errMsg, false)
		return
	}
	cfg, err := h.mergedSettingsConfig(r, form)
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	if err := runFullTest(r.Context(), h.settings.Dial, cfg, str); err != nil {
		h.renderSettings(w, r, http.StatusOK, form, err.Error(), false)
		return
	}
	h.renderSettings(w, r, http.StatusOK, form, str.TestPassed, true)
}

func (h *handler) settingsSave(w http.ResponseWriter, r *http.Request) {
	form, errMsg, ok := parseSettingsForm(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	if errMsg != "" {
		h.renderSettings(w, r, http.StatusBadRequest, form, errMsg, false)
		return
	}
	cfg, err := h.mergedSettingsConfig(r, form)
	if err != nil {
		h.renderSettings(w, r, http.StatusBadRequest, form, str.CD2PasswordRequired, false)
		return
	}
	// Every check runs again server-side before anything is persisted.
	if err := runFullTest(r.Context(), h.settings.Dial, cfg, str); err != nil {
		h.renderSettings(w, r, http.StatusOK, form, err.Error(), false)
		return
	}
	now := h.clock.Now().UTC()
	categories, canonicalLocalRoot, err := h.remapCategories(r.Context(), cfg, now)
	if err != nil {
		h.renderSettings(w, r, http.StatusConflict, form, str.CategoryRemapFailed, false)
		return
	}
	cfg.LocalRoot = canonicalLocalRoot
	if err := h.settings.Store.ReplaceSettingsAndCategories(r.Context(), settings.Values(cfg), categories, now); err != nil {
		if errors.Is(err, store.ErrDestinationConflict) {
			h.renderSettings(w, r, http.StatusConflict, form, str.CategoryRemapFailed, false)
			return
		}
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	if h.settings.Apply != nil {
		if err := h.settings.Apply(r.Context(), cfg); err != nil {
			// The settings are durable; only activation failed.
			h.renderSettings(w, r, http.StatusOK, form, str.SettingsApplyFailed, false)
			return
		}
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// renderSettings re-renders the settings page with the submitted values and
// a notice describing the outcome of the last action.
func (h *handler) renderSettings(w http.ResponseWriter, r *http.Request, status int, form SettingsFormValues, notice string, success bool) {
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	view := SettingsView{
		PageMeta: pageMeta(tr(requestLang(r)).TitleSettings, "settings", h.authSession(r).CSRFToken, requestLang(r)),
		Values:   form, Categories: buildSettingsCategoryPaths(categories, h.config.CloudRoot, h.config.LocalRoot),
		Notice: notice, Success: success,
	}
	view.Path = "/settings"
	h.render(w, status, "settings", view)
}

func (h *handler) remapCategories(ctx context.Context, cfg settings.Config, now time.Time) ([]domain.Category, string, error) {
	verifier, err := fsafe.New(cfg.LocalRoot)
	if err != nil {
		return nil, "", err
	}
	canonicalLocalRoot := verifier.LocalRoot()
	if cfg.CloudRoot == h.config.CloudRoot && canonicalLocalRoot == h.config.LocalRoot {
		return nil, canonicalLocalRoot, nil
	}
	categories, err := h.repo.ListCategories(ctx)
	if err != nil {
		return nil, "", err
	}
	for index := range categories {
		cloudSubpath, cloudOK := relativeCloudSubpath(h.config.CloudRoot, categories[index].CloudPath)
		saveSubpath, saveOK := relativeLocalSubpath(h.config.LocalRoot, categories[index].SavePath)
		if !cloudOK || !saveOK {
			return nil, "", errors.New("category path is outside the configured root")
		}
		categories[index].CloudPath = path.Join(cfg.CloudRoot, cloudSubpath)
		savePath := filepath.Join(canonicalLocalRoot, filepath.FromSlash(saveSubpath))
		prepared, err := verifier.PrepareSaveRoot(savePath)
		if err != nil || prepared != savePath {
			return nil, "", errors.New("prepare remapped category save path")
		}
		categories[index].SavePath = prepared
		categories[index].UpdatedAt = now
	}
	return categories, canonicalLocalRoot, nil
}

// parseSettingsForm extracts and validates the settings form. An empty
// CloudDrive2 password field means "keep the stored value", so it is not an
// error here; mergedSettingsConfig resolves it.
func parseSettingsForm(r *http.Request) (SettingsFormValues, string, bool) {
	str := tr(requestLang(r))
	var form SettingsFormValues
	var ok bool
	if form.CD2Address, ok = exactPostValue(r, "address"); !ok {
		return form, "", false
	}
	if form.CD2Username, ok = exactPostValue(r, "username"); !ok {
		return form, "", false
	}
	if form.CD2Password, ok = exactPostValue(r, "password"); !ok {
		return form, "", false
	}
	if form.CloudRoot, ok = exactPostValue(r, "cloud_root"); !ok {
		return form, "", false
	}
	if form.LocalRoot, ok = exactPostValue(r, "local_root"); !ok {
		return form, "", false
	}
	if form.OfflineTimeout, ok = exactPostValue(r, "timeout_offline"); !ok {
		return form, "", false
	}
	if form.CopyTimeout, ok = exactPostValue(r, "timeout_copy"); !ok {
		return form, "", false
	}
	if form.VerifyTimeout, ok = exactPostValue(r, "timeout_verify"); !ok {
		return form, "", false
	}
	form.CD2Insecure = r.PostForm.Get("insecure") == "true"
	form.CD2Address = strings.TrimSpace(form.CD2Address)
	form.CD2Username = strings.TrimSpace(form.CD2Username)
	form.CloudRoot = strings.TrimSpace(form.CloudRoot)
	form.LocalRoot = strings.TrimSpace(form.LocalRoot)
	switch {
	case form.CD2Address == "":
		return form, str.AddressRequired, true
	case settings.ValidateAddress(settings.KeyCD2Address, form.CD2Address, false) != nil:
		return form, str.AddressInvalid, true
	case form.CD2Username == "":
		return form, str.UsernameRequired, true
	case !validCloudRoot(form.CloudRoot):
		return form, str.CloudRootInvalid, true
	case !validLocalRoot(form.LocalRoot):
		return form, str.LocalRootInvalid, true
	}
	if _, _, _, err := parseTimeouts(form.OfflineTimeout, form.CopyTimeout, form.VerifyTimeout); err != nil {
		return form, str.TimeoutInvalid, true
	}
	return form, "", true
}

// mergedSettingsConfig builds the runtime configuration from the submitted
// form, resolving an empty password field against the stored value.
func (h *handler) mergedSettingsConfig(r *http.Request, form SettingsFormValues) (settings.Config, error) {
	password := form.CD2Password
	if password == "" {
		stored, err := h.settings.Store.ListSettings(r.Context())
		if err != nil {
			return settings.Config{}, err
		}
		password = stored[settings.KeyCD2Password]
	}
	if password == "" {
		return settings.Config{}, errors.New("cd2 password is required")
	}
	offline, copyTimeout, verify, err := parseTimeouts(form.OfflineTimeout, form.CopyTimeout, form.VerifyTimeout)
	if err != nil {
		return settings.Config{}, err
	}
	return settings.Config{
		CD2Address:     form.CD2Address,
		CD2Username:    form.CD2Username,
		CD2Password:    password,
		CD2Insecure:    form.CD2Insecure,
		CloudRoot:      form.CloudRoot,
		LocalRoot:      form.LocalRoot,
		OfflineTimeout: offline,
		CopyTimeout:    copyTimeout,
		VerifyTimeout:  verify,
	}, nil
}

// settingsFormFromValues prefills the settings form from persisted values,
// falling back to the documented defaults for missing keys.
func settingsFormFromValues(values map[string]string) SettingsFormValues {
	form := SettingsFormValues{
		CD2Address:  values[settings.KeyCD2Address],
		CD2Username: values[settings.KeyCD2Username],
		CD2Insecure: values[settings.KeyCD2Insecure] == "true",
		CloudRoot:   values[settings.KeyCloudRoot],
		LocalRoot:   values[settings.KeyLocalRoot],
	}
	form.OfflineTimeout = values[settings.KeyOfflineTimeout]
	form.CopyTimeout = values[settings.KeyCopyTimeout]
	form.VerifyTimeout = values[settings.KeyVerifyTimeout]
	if form.OfflineTimeout == "" {
		form.OfflineTimeout = defaultOfflineTimeout.String()
	}
	if form.CopyTimeout == "" {
		form.CopyTimeout = defaultCopyTimeout.String()
	}
	if form.VerifyTimeout == "" {
		form.VerifyTimeout = defaultVerifyTimeout.String()
	}
	return form
}

func (h *handler) passwordPage(w http.ResponseWriter, r *http.Request) {
	h.renderPasswordPage(w, r, http.StatusOK, "", false)
}

func (h *handler) changePassword(w http.ResponseWriter, r *http.Request) {
	current, currentOK := exactPostValue(r, "current_password")
	next, nextOK := exactPostValue(r, "new_password")
	confirm, confirmOK := exactPostValue(r, "confirm_password")
	if !currentOK || !nextOK || !confirmOK {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	if len(next) < minPasswordLength {
		h.renderPasswordPage(w, r, http.StatusBadRequest, str.PasswordTooShort, false)
		return
	}
	if next != confirm {
		h.renderPasswordPage(w, r, http.StatusBadRequest, str.PasswordMismatch, false)
		return
	}
	err := h.creds.Change(r.Context(), current, next, h.clock.Now().UTC())
	if errors.Is(err, creds.ErrCurrentPasswordMismatch) {
		h.renderPasswordPage(w, r, http.StatusBadRequest, str.PasswordWrongCurrent, false)
		return
	}
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	h.renderPasswordPage(w, r, http.StatusOK, "", true)
}

func (h *handler) renderPasswordPage(w http.ResponseWriter, r *http.Request, status int, errorText string, success bool) {
	lang := requestLang(r)
	page := PasswordView{PageMeta: pageMeta(tr(lang).TitlePassword, "password", h.authSession(r).CSRFToken, lang), Error: errorText, Success: success}
	page.Path = "/password"
	h.render(w, status, "password", page)
}

func (h *handler) saveCategory(w http.ResponseWriter, r *http.Request) {
	name, nameOK := exactPostValue(r, "name")
	cloudSubpath, cloudOK := exactPostValue(r, "cloud_subpath")
	saveSubpath, saveOK := exactPostValue(r, "save_subpath")
	enabledValue, enabledOK := exactPostValue(r, "enabled")
	enabled, validEnabled := parseEnabled(enabledValue)
	name, validName := canonicalCategory(name)
	if !nameOK || !cloudOK || !saveOK || !enabledOK || !validName || !validEnabled ||
		!validCloudSubpath(cloudSubpath) || !validLocalSubpath(saveSubpath) {
		h.renderCategoriesNotice(w, r, http.StatusBadRequest, tr(requestLang(r)).CategorySubpathInvalid)
		return
	}
	cloudPath := path.Join(h.config.CloudRoot, cloudSubpath)
	if !strictCloudDescendant(h.config.CloudRoot, cloudPath) {
		h.renderCategoriesNotice(w, r, http.StatusBadRequest, tr(requestLang(r)).CategorySubpathInvalid)
		return
	}
	savePath := filepath.Join(h.config.LocalRoot, filepath.FromSlash(saveSubpath))
	resolvedSavePath, _, err := h.filesystem.ResolveSaveRoot(savePath)
	if err != nil {
		h.renderCategoriesNotice(w, r, http.StatusBadRequest, tr(requestLang(r)).CategorySubpathInvalid)
		return
	}
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	now := h.clock.Now().UTC()
	createdAt := now
	for _, category := range categories {
		if category.Name == name {
			createdAt = category.CreatedAt
			break
		}
	}
	category := domain.Category{
		Name: name, CloudPath: cloudPath, SavePath: resolvedSavePath, Enabled: false, CreatedAt: createdAt, UpdatedAt: now,
	}
	if _, err = h.repo.UpsertCategory(r.Context(), category); err != nil {
		repositoryError(w, err)
		return
	}
	preparedSavePath, err := h.filesystem.PrepareSaveRoot(resolvedSavePath)
	if err != nil || preparedSavePath != resolvedSavePath {
		h.renderCategoriesNotice(w, r, http.StatusConflict, tr(requestLang(r)).CategoryPrepareFailed)
		return
	}
	category.Enabled = enabled
	if _, err = h.repo.UpsertCategory(r.Context(), category); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/categories", http.StatusSeeOther)
}

func (h *handler) renderCategoriesNotice(w http.ResponseWriter, r *http.Request, status int, notice string) {
	categories, err := h.repo.ListCategories(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	page := buildCategoriesView(
		categories, h.config.CloudRoot, h.config.LocalRoot, h.authSession(r).CSRFToken, false, requestLang(r),
	)
	page.Notice = notice
	page.Path = "/categories"
	h.render(w, status, "categories", page)
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	h.mutateDownload(w, r, func(download domain.Download, now time.Time) error {
		if download.State != domain.StateStopped {
			return store.ErrInvalidTransition
		}
		return h.repo.Start(r.Context(), download.Hash, now)
	}, false)
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request) {
	h.mutateDownload(w, r, func(download domain.Download, now time.Time) error {
		if !canRetry(download) {
			return store.ErrInvalidTransition
		}
		return h.repo.Retry(r.Context(), download.Hash, retryTarget(download), now)
	}, false)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	h.mutateDownload(w, r, func(download domain.Download, now time.Time) error {
		if !canCancel(download.State) {
			return store.ErrInvalidTransition
		}
		return h.repo.Cancel(r.Context(), download.Hash, now)
	}, false)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	deleteFilesValue, ok := exactPostValue(r, "delete_files")
	if !ok || (deleteFilesValue != "true" && deleteFilesValue != "false") {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	deleteFiles := deleteFilesValue == "true"
	h.mutateDownload(w, r, func(download domain.Download, now time.Time) error {
		return h.repo.RequestDelete(r.Context(), []string{download.Hash}, deleteFiles, now)
	}, true)
}

func (h *handler) mutateDownload(w http.ResponseWriter, r *http.Request, mutation func(domain.Download, time.Time) error, remove bool) {
	hash, ok := canonicalHash(r.PathValue("hash"))
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	download, err := h.repo.GetDownload(r.Context(), hash)
	if err != nil {
		repositoryError(w, err)
		return
	}
	cleanupFailure := cleanupFailed(download)
	if !download.State.Visible() && !cleanupFailure {
		plain(w, http.StatusNotFound, "Not Found\n")
		return
	}
	if err := mutation(download, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	h.waker.Wake()
	if remove {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/downloads/"+download.Hash, http.StatusSeeOther)
}

func (h *handler) authSession(r *http.Request) session.Session {
	current, _ := r.Context().Value(authContextKey{}).(authenticatedSession)
	return current.session
}

func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	var output bytes.Buffer
	if err := h.templates.ExecuteTemplate(&output, name, data); err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = output.WriteTo(w)
}

func parseURLEncodedForm(w http.ResponseWriter, r *http.Request) (map[string][]string, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return nil, false
	}
	return r.PostForm, true
}

func exactlyOne(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func exactPostValue(r *http.Request, name string) (string, bool) {
	return exactlyOne(r.PostForm[name])
}

func canonicalHash(raw string) (string, bool) {
	hash := strings.ToLower(strings.TrimSpace(raw))
	if len(hash) != 40 {
		return "", false
	}
	for _, character := range hash {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", false
		}
	}
	return hash, true
}

func canonicalCategory(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || name == "." || name == ".." || path.IsAbs(name) || strings.ContainsAny(name, "/\\") {
		return "", false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return name, true
}

func parseEnabled(raw string) (bool, bool) {
	switch raw {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func strictCloudDescendant(root, candidate string) bool {
	if !path.IsAbs(candidate) || path.Clean(candidate) != candidate || candidate == root {
		return false
	}
	prefix := root
	if prefix != "/" {
		prefix += "/"
	}
	return strings.HasPrefix(candidate, prefix)
}

func validCloudSubpath(value string) bool {
	if value == "" || value == "." || value == ".." || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	return validSubpathCharacters(value)
}

func validLocalSubpath(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.IsAbs(value) {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return cleaned == value && validSubpathCharacters(value)
}

func validSubpathCharacters(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00") && !strings.ContainsFunc(value, unicode.IsControl)
}

func repositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		plain(w, http.StatusNotFound, "Not Found\n")
	case errors.Is(err, store.ErrInvalidTransition), errors.Is(err, store.ErrDestinationConflict):
		plain(w, http.StatusConflict, "Conflict\n")
	default:
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
	}
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
