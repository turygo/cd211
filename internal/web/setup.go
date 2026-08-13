package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
)

// setupTestTimeout bounds every CloudDrive2 RPC dialed by the setup wizard and
// the settings page.
const setupTestTimeout = 15 * time.Second

// Default timeout values prefilled on the wizard review step.
const (
	defaultOfflineTimeout = 24 * time.Hour
	defaultCopyTimeout    = 72 * time.Hour
	defaultVerifyTimeout  = 10 * time.Minute
)

// CloudTester is the reachable surface a connectivity test needs from a
// CloudDrive2 connection: sign in, report readiness, and probe directories.
type CloudTester interface {
	// Authenticate exercises the configured credentials with a login
	// round-trip; wrong credentials surface as an unauthorized-kind error.
	Authenticate(ctx context.Context) error
	Check(ctx context.Context) error
	// StatDirectory returns nil iff fullPath exists and is a directory.
	StatDirectory(ctx context.Context, fullPath string) error
	ListDirectories(ctx context.Context, fullPath string) ([]clouddrive.Directory, error)
	CreateDirectory(ctx context.Context, parentPath, name string) (clouddrive.Directory, error)
	Close() error
}

// DialFunc establishes a CloudDrive2 connection for a connectivity test.
type DialFunc func(address, username, password string, timeout time.Duration, insecure bool) (CloudTester, error)

// defaultDial adapts *clouddrive.Client behind CloudTester.
func defaultDial(address, username, password string, timeout time.Duration, insecure bool) (CloudTester, error) {
	client, err := clouddrive.Dial(address, username, password, timeout, insecure)
	if err != nil {
		return nil, err
	}
	return &clouddriveTester{client: client}, nil
}

// clouddriveTester adapts *clouddrive.Client to CloudTester.
type clouddriveTester struct {
	client *clouddrive.Client
}

func (t *clouddriveTester) Check(ctx context.Context) error { return t.client.Check(ctx) }

func (t *clouddriveTester) Authenticate(ctx context.Context) error {
	return t.client.Authenticate(ctx)
}

func (t *clouddriveTester) StatDirectory(ctx context.Context, fullPath string) error {
	file, err := t.client.FindFile(ctx, fullPath)
	if err != nil {
		return err
	}
	if !file.GetIsDirectory() {
		return errNotDirectory
	}
	return nil
}

func (t *clouddriveTester) ListDirectories(ctx context.Context, fullPath string) ([]clouddrive.Directory, error) {
	return t.client.ListDirectories(ctx, fullPath)
}

func (t *clouddriveTester) CreateDirectory(ctx context.Context, parentPath, name string) (clouddrive.Directory, error) {
	return t.client.CreateDirectory(ctx, parentPath, name)
}

func (t *clouddriveTester) Close() error { return t.client.Close() }

var errNotDirectory = errors.New("path is not a directory")
var errWizardCloudNotConfigured = errors.New("wizard CloudDrive2 connection is not configured")

// SetupStore persists the completed wizard configuration.
type SetupStore interface {
	CompleteSetup(ctx context.Context, passwordHash string, values map[string]string, now time.Time) error
}

// SetupConfig wires the first-run setup wizard.
type SetupConfig struct {
	Store    SetupStore
	Sessions *session.Store
	Clock    Clock
	// Dial establishes CloudDrive2 test connections; nil uses the default
	// *clouddrive.Client adapter.
	Dial DialFunc
	// Complete swaps the persisted configuration into the running process.
	// It is invoked only after Store.CompleteSetup succeeded.
	Complete func(ctx context.Context, cfg settings.Config) error
}

// NewSetup constructs the first-run setup wizard. It is served only while the
// system is unconfigured; it routes exact full paths and leaves everything
// else (including "/") to the surrounding mux.
func NewSetup(cfg SetupConfig) (http.Handler, error) {
	if isNil(cfg.Store) || cfg.Sessions == nil || isNil(cfg.Clock) {
		return nil, errors.New("setup dependency is nil")
	}
	dial := cfg.Dial
	if dial == nil {
		dial = defaultDial
	}
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, errors.New("parse web templates")
	}
	h := &setupHandler{
		store:     cfg.Store,
		sessions:  cfg.Sessions,
		clock:     cfg.Clock,
		dial:      dial,
		complete:  cfg.Complete,
		templates: templates,
		state:     &wizardState{step: 1},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /setup", http.HandlerFunc(h.setupPage))
	mux.Handle("POST /setup/password", http.HandlerFunc(h.setupPassword))
	mux.Handle("POST /setup/cd2/test", http.HandlerFunc(h.setupCD2Test))
	mux.Handle("GET /setup/cloud-directories", http.HandlerFunc(h.setupCloudDirectories))
	mux.Handle("POST /setup/cloud-directories", http.HandlerFunc(h.setupCloudDirectoryCreate))
	mux.Handle("GET /setup/local-directories", http.HandlerFunc(h.setupLocalDirectories))
	mux.Handle("POST /setup/local-directories", http.HandlerFunc(h.setupLocalDirectoryCreate))
	mux.Handle("POST /setup/paths/test", http.HandlerFunc(h.setupPathsTest))
	mux.Handle("POST /setup/finish", http.HandlerFunc(h.setupFinish))
	mux.Handle("GET /lang", http.HandlerFunc(setLang))
	mux.Handle("GET /static/app.css", http.HandlerFunc(staticCSS))
	mux.Handle("GET /static/theme-init.js", http.HandlerFunc(staticThemeInitJS))
	mux.Handle("GET /static/app.js", http.HandlerFunc(staticJS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, found := setupRouteMethod(r.URL.Path, r.Method)
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

func setupRouteMethod(requestPath, requestMethod string) (string, bool) {
	switch requestPath {
	case "/setup", "/lang", "/static/app.css", "/static/app.js", "/static/theme-init.js":
		return http.MethodGet, true
	case "/setup/cloud-directories", "/setup/local-directories":
		if requestMethod == http.MethodGet {
			return http.MethodGet, true
		}
		return http.MethodPost, true
	case "/setup/password", "/setup/cd2/test", "/setup/paths/test", "/setup/finish":
		return http.MethodPost, true
	}
	return "", false
}

// wizardState is the in-progress wizard configuration. The operator password
// is retained exclusively as its PBKDF2 hash; the CloudDrive2 password is
// kept in plaintext because the persisted settings require it.
type wizardState struct {
	step         int
	sid          string
	passwordHash string

	cd2Address  string
	cd2Username string
	cd2Password string
	cd2Insecure bool

	cloudRoot string
	localRoot string

	offlineTimeout time.Duration
	copyTimeout    time.Duration
	verifyTimeout  time.Duration
}

type setupHandler struct {
	store     SetupStore
	sessions  *session.Store
	clock     Clock
	dial      DialFunc
	complete  func(context.Context, settings.Config) error
	templates *template.Template

	mu    sync.Mutex
	state *wizardState
}

func (h *setupHandler) setupPage(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	step := h.state.step
	h.mu.Unlock()
	csrf := ""
	if step > 1 {
		current, ok := h.wizardSession(r)
		if !ok {
			// Missing or expired wizard session: start over at the password
			// step rather than admitting a partially authenticated flow.
			step = 1
		} else {
			csrf = current.CSRFToken
		}
	}
	view := h.buildSetupView(r, step, csrf, "", "")
	view.Path = "/setup"
	h.renderSetup(w, http.StatusOK, view)
}

// wizardSession reports whether the request carries the session created by
// the password step.
func (h *setupHandler) wizardSession(r *http.Request) (session.Session, bool) {
	cookie, err := r.Cookie("SID")
	if err != nil || cookie.Value == "" {
		return session.Session{}, false
	}
	h.mu.Lock()
	sid := h.state.sid
	h.mu.Unlock()
	if cookie.Value != sid {
		return session.Session{}, false
	}
	return h.sessions.Get(cookie.Value)
}

// requireWizardPOST gates a later wizard step behind the wizard session and
// the same CSRF proof the authenticated routes use. The form is parsed here
// so handlers can read exact values afterwards.
func (h *setupHandler) requireWizardPOST(w http.ResponseWriter, r *http.Request) (session.Session, bool) {
	current, ok := h.wizardSession(r)
	if !ok {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return session.Session{}, false
	}
	if !browserOriginAllowed(r) {
		plain(w, http.StatusForbidden, "Forbidden\n")
		return session.Session{}, false
	}
	form, parsed := parseURLEncodedForm(w, r)
	if !parsed {
		return session.Session{}, false
	}
	token, exact := exactlyOne(form["csrf_token"])
	if !exact {
		plain(w, http.StatusForbidden, "Forbidden\n")
		return session.Session{}, false
	}
	tokenDigest := sha256.Sum256([]byte(token))
	expectedDigest := sha256.Sum256([]byte(current.CSRFToken))
	if subtle.ConstantTimeCompare(tokenDigest[:], expectedDigest[:]) != 1 {
		plain(w, http.StatusForbidden, "Forbidden\n")
		return session.Session{}, false
	}
	return current, true
}

func (h *setupHandler) setupPassword(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r)
	if !ok {
		return
	}
	password, passwordOK := exactlyOne(form["password"])
	confirm, confirmOK := exactlyOne(form["confirm_password"])
	if !passwordOK || !confirmOK {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	if len(password) < minPasswordLength {
		h.renderSetupStep(w, r, 1, http.StatusBadRequest, str.PasswordTooShort, "")
		return
	}
	if password != confirm {
		h.renderSetupStep(w, r, 1, http.StatusBadRequest, str.PasswordMismatch, "")
		return
	}
	hash, err := creds.HashPassword(password)
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	sid, _, err := h.sessions.Create()
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	h.mu.Lock()
	previousSID := h.state.sid
	h.state.sid = sid
	h.state.passwordHash = hash
	h.state.step = 2
	h.mu.Unlock()
	if previousSID != "" {
		h.sessions.Revoke(previousSID)
	}
	http.SetCookie(w, sidCookie(sid, false, r.TLS != nil))
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (h *setupHandler) setupCD2Test(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireWizardPOST(w, r); !ok {
		return
	}
	action, actionOK := exactPostValue(r, "action")
	if !actionOK || (action != "test" && action != "continue") {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	address, addressOK := exactPostValue(r, "address")
	username, usernameOK := exactPostValue(r, "username")
	password, passwordOK := exactPostValue(r, "password")
	if !addressOK || !usernameOK || !passwordOK {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	address = strings.TrimSpace(address)
	username = strings.TrimSpace(username)
	switch {
	case address == "":
		h.renderSetupStep(w, r, 2, http.StatusBadRequest, str.AddressRequired, "")
		return
	case settings.ValidateAddress(settings.KeyCD2Address, address, false) != nil:
		h.renderSetupStep(w, r, 2, http.StatusBadRequest, str.AddressInvalid, "")
		return
	case username == "":
		h.renderSetupStep(w, r, 2, http.StatusBadRequest, str.UsernameRequired, "")
		return
	}
	insecure := r.PostForm.Get("insecure") == "true"
	if password == "" {
		// An empty password keeps the value stored by an earlier step, so a
		// browser that does not re-submit the field can still continue.
		h.mu.Lock()
		password = h.state.cd2Password
		h.mu.Unlock()
		if password == "" {
			h.renderSetupStep(w, r, 2, http.StatusBadRequest, str.CD2PasswordRequired, "")
			return
		}
	}
	tester, err := h.dial(address, username, password, setupTestTimeout, insecure)
	if err != nil {
		h.renderSetupStep(w, r, 2, http.StatusOK, testFailureMessage(classifyTestError(err), str, address), "")
		return
	}
	defer func() { _ = tester.Close() }()
	if err := tester.Authenticate(r.Context()); err != nil {
		h.renderSetupStep(w, r, 2, http.StatusOK, testFailureMessage(classifyTestError(err), str, address), "")
		return
	}
	if err := tester.Check(r.Context()); err != nil {
		h.renderSetupStep(w, r, 2, http.StatusOK, testFailureMessage(classifyTestError(err), str, address), "")
		return
	}
	h.mu.Lock()
	h.state.cd2Address = address
	h.state.cd2Username = username
	h.state.cd2Password = password
	h.state.cd2Insecure = insecure
	if action == "continue" {
		h.state.step = 3
	}
	h.mu.Unlock()
	if action == "continue" {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	h.renderSetupStep(w, r, 2, http.StatusOK, "", str.TestPassed)
}

type cloudDirectoryResponse struct {
	Path        string                 `json:"path,omitempty"`
	Directories []clouddrive.Directory `json:"directories"`
	Directory   *clouddrive.Directory  `json:"directory,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

func (h *setupHandler) setupCloudDirectories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.wizardSession(r); !ok {
		writeSetupJSON(w, http.StatusUnauthorized, cloudDirectoryResponse{Error: tr(requestLang(r)).SetupSessionExpired})
		return
	}
	parent, exact := exactlyOne(r.URL.Query()["path"])
	parent = path.Clean(strings.TrimSpace(parent))
	str := tr(requestLang(r))
	if !exact || !validCloudRoot(parent) {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.CloudDirectoryPathInvalid})
		return
	}
	tester, err := h.dialWizardCloud()
	if errors.Is(err, errWizardCloudNotConfigured) {
		writeSetupJSON(w, http.StatusConflict, cloudDirectoryResponse{Error: str.CloudDirectoryConnectionRequired})
		return
	}
	if err != nil {
		writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: cloudDirectoryFailureMessage(err, str, str.CloudDirectoryListFailed)})
		return
	}
	defer func() { _ = tester.Close() }()
	directories, err := tester.ListDirectories(r.Context(), parent)
	if err != nil {
		writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: cloudDirectoryFailureMessage(err, str, str.CloudDirectoryListFailed)})
		return
	}
	writeSetupJSON(w, http.StatusOK, cloudDirectoryResponse{Path: parent, Directories: directories})
}

func (h *setupHandler) setupCloudDirectoryCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireWizardPOST(w, r); !ok {
		return
	}
	parent, parentOK := exactPostValue(r, "parent")
	name, nameOK := exactPostValue(r, "name")
	parent = path.Clean(strings.TrimSpace(parent))
	name = strings.TrimSpace(name)
	str := tr(requestLang(r))
	if !parentOK || !validCloudRoot(parent) {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.CloudDirectoryPathInvalid})
		return
	}
	if !nameOK || name == "" || name == "." || name == ".." || path.Base(name) != name {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.CloudDirectoryNameInvalid})
		return
	}
	tester, err := h.dialWizardCloud()
	if errors.Is(err, errWizardCloudNotConfigured) {
		writeSetupJSON(w, http.StatusConflict, cloudDirectoryResponse{Error: str.CloudDirectoryConnectionRequired})
		return
	}
	if err != nil {
		writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: cloudDirectoryFailureMessage(err, str, str.CloudDirectoryCreateFailed)})
		return
	}
	defer func() { _ = tester.Close() }()
	directory, err := tester.CreateDirectory(r.Context(), parent, name)
	if err != nil {
		writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: cloudDirectoryFailureMessage(err, str, str.CloudDirectoryCreateFailed)})
		return
	}
	writeSetupJSON(w, http.StatusCreated, cloudDirectoryResponse{Directory: &directory})
}

func (h *setupHandler) setupLocalDirectories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.wizardSession(r); !ok {
		writeSetupJSON(w, http.StatusUnauthorized, cloudDirectoryResponse{Error: tr(requestLang(r)).SetupSessionExpired})
		return
	}
	if !h.wizardAtLeast(3) {
		writeSetupJSON(w, http.StatusConflict, cloudDirectoryResponse{Error: tr(requestLang(r)).CloudDirectoryConnectionRequired})
		return
	}
	parent, exact := exactlyOne(r.URL.Query()["path"])
	parent = filepath.Clean(strings.TrimSpace(parent))
	str := tr(requestLang(r))
	if !exact || !validLocalRoot(parent) {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.LocalDirectoryPathInvalid})
		return
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: str.LocalDirectoryListFailed})
		return
	}
	directories := make([]clouddrive.Directory, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(parent, name)
		isDirectory := entry.IsDir()
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(fullPath)
			isDirectory = statErr == nil && info.IsDir()
		}
		if isDirectory {
			directories = append(directories, clouddrive.Directory{Name: name, Path: fullPath})
		}
	}
	writeSetupJSON(w, http.StatusOK, cloudDirectoryResponse{Path: parent, Directories: directories})
}

func (h *setupHandler) setupLocalDirectoryCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireWizardPOST(w, r); !ok {
		return
	}
	if !h.wizardAtLeast(3) {
		writeSetupJSON(w, http.StatusConflict, cloudDirectoryResponse{Error: tr(requestLang(r)).CloudDirectoryConnectionRequired})
		return
	}
	parent, parentOK := exactPostValue(r, "parent")
	name, nameOK := exactPostValue(r, "name")
	parent = filepath.Clean(strings.TrimSpace(parent))
	name = strings.TrimSpace(name)
	str := tr(requestLang(r))
	if !parentOK || !validLocalRoot(parent) {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.LocalDirectoryPathInvalid})
		return
	}
	if !nameOK || name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		writeSetupJSON(w, http.StatusBadRequest, cloudDirectoryResponse{Error: str.CloudDirectoryNameInvalid})
		return
	}
	fullPath := filepath.Join(parent, name)
	if err := os.Mkdir(fullPath, 0o755); err != nil {
		if info, statErr := os.Stat(fullPath); statErr != nil || !info.IsDir() {
			writeSetupJSON(w, http.StatusBadGateway, cloudDirectoryResponse{Error: str.LocalDirectoryCreateFailed})
			return
		}
	}
	directory := clouddrive.Directory{Name: name, Path: fullPath}
	writeSetupJSON(w, http.StatusCreated, cloudDirectoryResponse{Directory: &directory})
}

func (h *setupHandler) wizardAtLeast(step int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state.step >= step
}

func (h *setupHandler) dialWizardCloud() (CloudTester, error) {
	h.mu.Lock()
	state := *h.state
	h.mu.Unlock()
	if state.step < 3 || state.cd2Address == "" || state.cd2Username == "" || state.cd2Password == "" {
		return nil, errWizardCloudNotConfigured
	}
	return h.dial(state.cd2Address, state.cd2Username, state.cd2Password, setupTestTimeout, state.cd2Insecure)
}

func cloudDirectoryFailureMessage(err error, str *Strings, fallback string) string {
	if classifyTestError(err) == failureAuth {
		return str.TestAuth
	}
	return fallback
}

func writeSetupJSON(w http.ResponseWriter, status int, response cloudDirectoryResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *setupHandler) setupPathsTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireWizardPOST(w, r); !ok {
		return
	}
	action, actionOK := exactPostValue(r, "action")
	if !actionOK || (action != "test" && action != "continue") {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	cloudRoot, cloudOK := exactPostValue(r, "cloud_root")
	localRoot, localOK := exactPostValue(r, "local_root")
	if !cloudOK || !localOK {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	str := tr(requestLang(r))
	cloudRoot = strings.TrimSpace(cloudRoot)
	localRoot = strings.TrimSpace(localRoot)
	if !validCloudRoot(cloudRoot) {
		h.renderSetupStep(w, r, 3, http.StatusBadRequest, str.CloudRootInvalid, "")
		return
	}
	if !validLocalRoot(localRoot) {
		h.renderSetupStep(w, r, 3, http.StatusBadRequest, str.LocalRootInvalid, "")
		return
	}
	h.mu.Lock()
	address, username, password := h.state.cd2Address, h.state.cd2Username, h.state.cd2Password
	insecure := h.state.cd2Insecure
	h.mu.Unlock()
	if address == "" {
		h.renderSetupStep(w, r, 3, http.StatusOK, str.TestOther, "")
		return
	}
	tester, err := h.dial(address, username, password, setupTestTimeout, insecure)
	if err != nil {
		h.renderSetupStep(w, r, 3, http.StatusOK, testFailureMessage(classifyTestError(err), str, address), "")
		return
	}
	defer func() { _ = tester.Close() }()
	if err := tester.StatDirectory(r.Context(), cloudRoot); err != nil {
		h.renderSetupStep(w, r, 3, http.StatusOK, cloudRootFailureMessage(err, str), "")
		return
	}
	if err := probeLocalRoot(localRoot); err != nil {
		h.renderSetupStep(w, r, 3, http.StatusOK, str.LocalRootNotWritable, "")
		return
	}
	h.mu.Lock()
	h.state.cloudRoot = cloudRoot
	h.state.localRoot = localRoot
	if action == "continue" {
		h.state.step = 4
	}
	h.mu.Unlock()
	if action == "continue" {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	h.renderSetupStep(w, r, 3, http.StatusOK, "", str.TestPassed)
}

func (h *setupHandler) setupFinish(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireWizardPOST(w, r); !ok {
		return
	}
	str := tr(requestLang(r))
	offlineRaw, offlineOK := exactPostValue(r, "timeout_offline")
	copyRaw, copyOK := exactPostValue(r, "timeout_copy")
	verifyRaw, verifyOK := exactPostValue(r, "timeout_verify")
	if !offlineOK || !copyOK || !verifyOK {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	offline, copyTimeout, verify, err := parseTimeouts(offlineRaw, copyRaw, verifyRaw)
	if err != nil {
		h.renderSetupStep(w, r, 4, http.StatusBadRequest, str.TimeoutInvalid, "")
		return
	}
	h.mu.Lock()
	state := *h.state
	h.state.offlineTimeout = offline
	h.state.copyTimeout = copyTimeout
	h.state.verifyTimeout = verify
	h.mu.Unlock()
	cfg := settings.Config{
		CD2Address:     state.cd2Address,
		CD2Username:    state.cd2Username,
		CD2Password:    state.cd2Password,
		CD2Insecure:    state.cd2Insecure,
		CloudRoot:      state.cloudRoot,
		LocalRoot:      state.localRoot,
		OfflineTimeout: offline,
		CopyTimeout:    copyTimeout,
		VerifyTimeout:  verify,
	}
	// Every check runs again server-side before anything is persisted.
	if err := runFullTest(r.Context(), h.dial, cfg, str); err != nil {
		h.renderSetupStep(w, r, 4, http.StatusOK, err.Error(), "")
		return
	}
	err = h.store.CompleteSetup(r.Context(), state.passwordHash, settings.Values(cfg), h.clock.Now().UTC())
	if errors.Is(err, store.ErrSetupCompleted) {
		view := h.buildSetupView(r, 4, "", "", "")
		view.AlreadyConfigured = true
		view.Path = "/setup"
		h.renderSetup(w, http.StatusOK, view)
		return
	}
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	if h.complete != nil {
		if err := h.complete(r.Context(), cfg); err != nil {
			// The configuration is durably persisted; only activation failed.
			h.renderSetupStep(w, r, 4, http.StatusOK, str.ActivationFailed, "")
			return
		}
	}
	http.Redirect(w, r, "/categories?onboarding=1", http.StatusSeeOther)
}

// buildSetupView snapshots the wizard state for rendering. Values not yet
// collected (or not yet overridden) fall back to the documented defaults.
func (h *setupHandler) buildSetupView(r *http.Request, step int, csrf, errorText, successText string) SetupView {
	h.mu.Lock()
	state := *h.state
	h.mu.Unlock()
	values := SetupFormValues{
		CD2Address:     state.cd2Address,
		CD2Username:    state.cd2Username,
		CD2Insecure:    state.cd2Insecure,
		CloudRoot:      state.cloudRoot,
		LocalRoot:      state.localRoot,
		MaskedPassword: strings.Repeat("•", 8),
	}
	values.OfflineTimeout = state.offlineTimeout.String()
	values.CopyTimeout = state.copyTimeout.String()
	values.VerifyTimeout = state.verifyTimeout.String()
	if state.offlineTimeout <= 0 {
		values.OfflineTimeout = defaultOfflineTimeout.String()
	}
	if state.copyTimeout <= 0 {
		values.CopyTimeout = defaultCopyTimeout.String()
	}
	if state.verifyTimeout <= 0 {
		values.VerifyTimeout = defaultVerifyTimeout.String()
	}
	return SetupView{
		PageMeta: pageMeta(tr(requestLang(r)).SetupTitle, "", csrf, requestLang(r)),
		Step:     step,
		Error:    errorText,
		Success:  successText,
		Values:   values,
	}
}

func (h *setupHandler) renderSetupStep(w http.ResponseWriter, r *http.Request, step, status int, errorText, successText string) {
	csrf := ""
	if step > 1 {
		if current, ok := h.wizardSession(r); ok {
			csrf = current.CSRFToken
		}
	}
	view := h.buildSetupView(r, step, csrf, errorText, successText)
	view.Path = "/setup"
	h.renderSetup(w, status, view)
}

func (h *setupHandler) renderSetup(w http.ResponseWriter, status int, view SetupView) {
	var output bytes.Buffer
	if err := h.templates.ExecuteTemplate(&output, "setup", view); err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = output.WriteTo(w)
}

// SetupView renders the wizard card. Step 1..4 selects the form; setting
// AlreadyConfigured replaces the whole card with the completion notice.
type SetupView struct {
	PageMeta
	Step              int
	AlreadyConfigured bool
	Error             string
	Success           string
	Values            SetupFormValues
}

// SetupFormValues carries the prefilled wizard form fields.
type SetupFormValues struct {
	CD2Address  string
	CD2Username string
	CD2Insecure bool
	CloudRoot   string
	LocalRoot   string
	// MaskedPassword is the display-only CloudDrive2 password shown on the
	// review step; the real value lives in the wizard state.
	MaskedPassword string
	OfflineTimeout string
	CopyTimeout    string
	VerifyTimeout  string
}

// runFullTest runs the complete connectivity suite a save or finish performs
// server-side: CloudDrive2 sign-in and readiness, cloud root directory
// existence, and a write probe of the local root. The returned error carries
// the user-facing (translated) message.
func runFullTest(ctx context.Context, dial DialFunc, cfg settings.Config, str *Strings) error {
	tester, err := dial(cfg.CD2Address, cfg.CD2Username, cfg.CD2Password, setupTestTimeout, cfg.CD2Insecure)
	if err != nil {
		return errors.New(testFailureMessage(classifyTestError(err), str, cfg.CD2Address))
	}
	defer func() { _ = tester.Close() }()
	if err := tester.Authenticate(ctx); err != nil {
		return errors.New(testFailureMessage(classifyTestError(err), str, cfg.CD2Address))
	}
	if err := tester.Check(ctx); err != nil {
		return errors.New(testFailureMessage(classifyTestError(err), str, cfg.CD2Address))
	}
	if err := tester.StatDirectory(ctx, cfg.CloudRoot); err != nil {
		return errors.New(cloudRootFailureMessage(err, str))
	}
	if err := probeLocalRoot(cfg.LocalRoot); err != nil {
		return errors.New(str.LocalRootNotWritable)
	}
	return nil
}

func cloudRootFailureMessage(err error, str *Strings) string {
	if errors.Is(err, errNotDirectory) {
		return str.CloudRootNotDir
	}
	if classifyTestError(err) == failureAuth {
		return str.TestAuth
	}
	return str.CloudRootUnverified
}

// probeLocalRoot validates that the local root exists and is writable by
// creating and removing an exclusive probe file inside it.
func probeLocalRoot(localRoot string) error {
	verifier, err := fsafe.New(localRoot)
	if err != nil {
		return err
	}
	probe := filepath.Join(verifier.LocalRoot(), ".cd211-write-probe-"+probeSuffix())
	file, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(probe)
		return err
	}
	_ = os.Remove(probe)
	return nil
}

func probeSuffix() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// parseTimeouts validates the three workflow timeouts. An empty field falls
// back to its documented default.
func parseTimeouts(offline, copyTimeout, verify string) (time.Duration, time.Duration, time.Duration, error) {
	values := []string{offline, copyTimeout, verify}
	defaults := []time.Duration{defaultOfflineTimeout, defaultCopyTimeout, defaultVerifyTimeout}
	parsed := make([]time.Duration, 3)
	for i := range values {
		value := strings.TrimSpace(values[i])
		if value == "" {
			parsed[i] = defaults[i]
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return 0, 0, 0, fmt.Errorf("invalid timeout %q", value)
		}
		parsed[i] = duration
	}
	return parsed[0], parsed[1], parsed[2], nil
}

func validCloudRoot(root string) bool {
	return path.IsAbs(root) && path.Clean(root) == root
}

func validLocalRoot(root string) bool {
	return filepath.IsAbs(root) && filepath.Clean(root) == root
}

// testFailureKind classifies a CloudDrive2 connectivity failure into the
// user-facing categories the wizard and settings page explain.
type testFailureKind int

const (
	failureOther testFailureKind = iota
	failureUnreachable
	failureTLS
	failureAuth
)

// classifyTestError maps a CloudDrive2 error kind plus any TLS markers in the
// wrapped cause to a user-facing failure category.
func classifyTestError(err error) testFailureKind {
	var cloudErr *clouddrive.Error
	if !errors.As(err, &cloudErr) {
		if tlsFailure(err) {
			return failureTLS
		}
		return failureOther
	}
	switch cloudErr.Kind {
	case clouddrive.ErrorUnauthorized:
		return failureAuth
	case clouddrive.ErrorTemporary:
		if tlsFailure(err) {
			return failureTLS
		}
		return failureUnreachable
	default:
		return failureOther
	}
}

func tlsFailure(err error) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		message := strings.ToLower(current.Error())
		if strings.Contains(message, "tls") ||
			strings.Contains(message, "handshake") ||
			strings.Contains(message, "server preface") ||
			strings.Contains(message, "certificate") ||
			strings.Contains(message, "x509") {
			return true
		}
	}
	return false
}

func testFailureMessage(kind testFailureKind, str *Strings, address string) string {
	switch kind {
	case failureUnreachable:
		return fmt.Sprintf(str.TestUnreachable, address)
	case failureTLS:
		return str.TestTLS
	case failureAuth:
		return str.TestAuth
	default:
		return str.TestOther
	}
}
