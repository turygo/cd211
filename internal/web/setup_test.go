package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
)

type setupCompleteCall struct {
	passwordHash string
	values       map[string]string
	now          time.Time
}

type recordingSetupStore struct {
	mu        sync.Mutex
	completed []setupCompleteCall
	err       error
}

func (s *recordingSetupStore) CompleteSetup(ctx context.Context, passwordHash string, values map[string]string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, setupCompleteCall{passwordHash: passwordHash, values: values, now: now})
	return s.err
}

type recordingComplete struct {
	mu      sync.Mutex
	configs []settings.Config
	err     error
	log     *[]string // optional shared order log
}

func (c *recordingComplete) apply(ctx context.Context, cfg settings.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.log != nil {
		*c.log = append(*c.log, "apply")
	}
	c.configs = append(c.configs, cfg)
	return c.err
}

type recordingSettingsStore struct {
	mu         sync.Mutex
	values     map[string]string
	log        *[]string
	listErr    error
	replaceErr error
}

func (s *recordingSettingsStore) ListSettings(ctx context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		*s.log = append(*s.log, "list")
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return values, nil
}

func (s *recordingSettingsStore) ReplaceSettingsAndCategories(ctx context.Context, values map[string]string, categories []domain.Category, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		*s.log = append(*s.log, "replace")
	}
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.values = make(map[string]string, len(values))
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

type fakeTester struct {
	authErr  error
	checkErr error
	statErr  error
	statPath string
	closed   int
	dial     *fakeDial
}

func (t *fakeTester) Authenticate(ctx context.Context) error { return t.authErr }

func (t *fakeTester) Check(ctx context.Context) error { return t.checkErr }

func (t *fakeTester) StatDirectory(ctx context.Context, fullPath string) error {
	t.statPath = fullPath
	return t.statErr
}

func (t *fakeTester) ListDirectories(ctx context.Context, fullPath string) ([]clouddrive.Directory, error) {
	t.dial.mu.Lock()
	defer t.dial.mu.Unlock()
	if t.dial.listErr != nil {
		return nil, t.dial.listErr
	}
	return slices.Clone(t.dial.directories), nil
}

func (t *fakeTester) CreateDirectory(ctx context.Context, parentPath, name string) (clouddrive.Directory, error) {
	t.dial.mu.Lock()
	defer t.dial.mu.Unlock()
	t.dial.createCalls = append(t.dial.createCalls, fakeDirectoryCreateCall{parentPath: parentPath, name: name})
	if t.dial.createErr != nil {
		return clouddrive.Directory{}, t.dial.createErr
	}
	return clouddrive.Directory{Name: name, Path: path.Join(parentPath, name)}, nil
}

func (t *fakeTester) Close() error { t.closed++; return nil }

type fakeDialCall struct {
	address, username, password string
	timeout                     time.Duration
	insecure                    bool
}

type fakeDirectoryCreateCall struct {
	parentPath string
	name       string
}

type fakeDial struct {
	mu          sync.Mutex
	err         error
	authErr     error
	checkErr    error
	statErr     error
	listErr     error
	createErr   error
	directories []clouddrive.Directory
	calls       []fakeDialCall
	createCalls []fakeDirectoryCreateCall
}

func (d *fakeDial) dial(address, username, password string, timeout time.Duration, insecure bool) (CloudTester, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, fakeDialCall{address: address, username: username, password: password, timeout: timeout, insecure: insecure})
	if d.err != nil {
		return nil, d.err
	}
	return &fakeTester{authErr: d.authErr, checkErr: d.checkErr, statErr: d.statErr, dial: d}, nil
}

func (d *fakeDial) setAuthErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authErr = err
}

func (d *fakeDial) setCheckErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checkErr = err
}

type setupFixture struct {
	t            *testing.T
	clock        *fixedClock
	sessions     *session.Store
	sessionStore *store.Store
	setupStore   *recordingSetupStore
	complete     *recordingComplete
	dial         *fakeDial
	handler      http.Handler
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	clock := &fixedClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	// Sessions live in their own real SQLite store; the wizard configuration
	// keeps using the recording fake.
	sessionStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "setup-sessions.db"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := sessionStore.Close(); err != nil {
			t.Errorf("session store close: %v", err)
		}
	})
	sessions, err := session.New(sessionStore, clock, &sequenceReader{}, time.Hour, 30*time.Minute, 32)
	if err != nil {
		t.Fatalf("session.New(): %v", err)
	}
	setupStore := &recordingSetupStore{}
	complete := &recordingComplete{}
	dial := &fakeDial{}
	handler, err := NewSetup(SetupConfig{Store: setupStore, Sessions: sessions, Clock: clock, Dial: dial.dial, Complete: complete.apply})
	if err != nil {
		t.Fatalf("NewSetup(): %v", err)
	}
	return &setupFixture{t: t, clock: clock, sessions: sessions, sessionStore: sessionStore, setupStore: setupStore, complete: complete, dial: dial, handler: handler}
}

func (f *setupFixture) get(target, sid string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if sid != "" {
		request.AddCookie(&http.Cookie{Name: "CD211_SESSION", Value: sid})
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *setupFixture) post(target, sid, csrf string, values url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	if values == nil {
		values = make(url.Values)
	}
	if csrf != "" {
		values.Set("csrf_token", csrf)
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sid != "" {
		request.AddCookie(&http.Cookie{Name: "CD211_SESSION", Value: sid})
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

// advancePassword completes step 1 and returns the wizard session id and CSRF
// token it established.
func (f *setupFixture) advancePassword() (string, string) {
	f.t.Helper()
	response := f.post("/setup/password", "", "", url.Values{"password": {"correct horse"}, "confirm_password": {"correct horse"}})
	requireStatus(f.t, response, http.StatusSeeOther)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "CD211_SESSION" {
		f.t.Fatalf("password step cookies = %+v, want SID", cookies)
	}
	current, _, err := f.sessions.Get(context.Background(), cookies[0].Value, session.AudienceWeb)
	if err != nil {
		f.t.Fatalf("wizard session %q lookup: %v", cookies[0].Value, err)
	}
	if cookies[0].MaxAge <= 0 || !cookies[0].Expires.Equal(current.ExpiresAt) {
		f.t.Errorf("password step cookie = MaxAge:%d Expires:%v, want positive MaxAge and Expires %v", cookies[0].MaxAge, cookies[0].Expires, current.ExpiresAt)
	}
	return cookies[0].Value, current.CSRFToken
}

// advanceToReview walks steps 1-3 with successful tests and returns the wizard
// session id and CSRF token at the review step.
func (f *setupFixture) advanceToReview() (string, string) {
	f.t.Helper()
	sid, csrf := f.advancePassword()
	response := f.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"},
		"password": {"cd2-secret"}, "insecure": {"true"},
	})
	requireStatus(f.t, response, http.StatusSeeOther)
	response = f.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"continue"}, "cloud_root": {"/cloud"}, "local_root": {f.t.TempDir()},
	})
	requireStatus(f.t, response, http.StatusSeeOther)
	return sid, csrf
}

func TestSetupWizardHappyPath(t *testing.T) {
	fixture := newSetupFixture(t)

	start := fixture.get("/setup", "")
	requireStatus(t, start, http.StatusOK)
	requireContains(t, start.Body.String(), `action="/setup/password"`, "password", "confirm_password")
	// The setup page loads exactly its own motion module, never the download
	// live module, and carries the rendered step for direction detection.
	requireContains(t, start.Body.String(), `/static/setup-motion.js?v=1`, `data-setup-step="1"`)
	requireAbsent(t, start.Body.String(), "downloads-live")

	sid, csrf := fixture.advancePassword()

	cd2Page := fixture.get("/setup", sid)
	requireStatus(t, cd2Page, http.StatusOK)
	requireContains(t, cd2Page.Body.String(), `action="/setup/cd2/test"`, "address", "username")

	// The Test button re-renders step 2 with the result notice.
	tested := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"test"}, "address": {"cd2.example:443"}, "username": {"operator"},
		"password": {"cd2-secret"}, "insecure": {"true"},
	})
	requireStatus(t, tested, http.StatusOK)
	requireContains(t, tested.Body.String(), "All checks passed.")

	// Continue re-runs the test and advances to the paths step.
	continued := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"},
		"password": {"cd2-secret"}, "insecure": {"true"},
	})
	requireStatus(t, continued, http.StatusSeeOther)
	pathsPage := fixture.get("/setup", sid)
	requireStatus(t, pathsPage, http.StatusOK)
	requireContains(t, pathsPage.Body.String(), `action="/setup/paths/test"`, "cloud_root", "local_root",
		"Files pass through two locations", "115 offline download root", "Shared staging root")

	localRoot := t.TempDir()
	pathsTested := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"test"}, "cloud_root": {"/cloud"}, "local_root": {localRoot},
	})
	requireStatus(t, pathsTested, http.StatusOK)
	requireContains(t, pathsTested.Body.String(), "All checks passed.")

	pathsContinued := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"continue"}, "cloud_root": {"/cloud"}, "local_root": {localRoot},
	})
	requireStatus(t, pathsContinued, http.StatusSeeOther)

	// The review step masks the CloudDrive2 password and prefills the
	// documented timeout defaults.
	review := fixture.get("/setup", sid)
	requireStatus(t, review, http.StatusOK)
	requireContains(t, review.Body.String(), `action="/setup/finish"`, "24h", "72h", "10m", "••••••••",
		"115 offline download root", "Shared staging root", "Finish setup and configure categories")
	requireAbsent(t, review.Body.String(), "cd2-secret")

	finished := fixture.post("/setup/finish", sid, csrf, url.Values{
		"timeout_offline": {"30h"}, "timeout_copy": {"80h"}, "timeout_verify": {"15m"},
	})
	requireStatus(t, finished, http.StatusSeeOther)
	if location := finished.Header().Get("Location"); location != "/categories?onboarding=1" {
		t.Errorf("finish Location = %q, want /categories?onboarding=1", location)
	}

	fixture.setupStore.mu.Lock()
	completed := fixture.setupStore.completed
	fixture.setupStore.mu.Unlock()
	if len(completed) != 1 {
		t.Fatalf("CompleteSetup calls = %d, want 1", len(completed))
	}
	call := completed[0]
	if call.passwordHash == "" || strings.Contains(call.passwordHash, "correct horse") {
		t.Errorf("password hash = %q, want opaque non-plaintext hash", call.passwordHash)
	}
	wantValues := map[string]string{
		settings.KeyCD2Address:     "cd2.example:443",
		settings.KeyCD2Username:    "operator",
		settings.KeyCD2Password:    "cd2-secret",
		settings.KeyCD2Insecure:    "true",
		settings.KeyCloudRoot:      "/cloud",
		settings.KeyLocalRoot:      localRoot,
		settings.KeyOfflineTimeout: "30h0m0s",
		settings.KeyCopyTimeout:    "80h0m0s",
		settings.KeyVerifyTimeout:  "15m0s",
	}
	for key, want := range wantValues {
		if call.values[key] != want {
			t.Errorf("values[%s] = %q, want %q", key, call.values[key], want)
		}
	}
	if call.now != fixture.clock.now {
		t.Errorf("CompleteSetup now = %v, want %v", call.now, fixture.clock.now)
	}

	fixture.complete.mu.Lock()
	configs := fixture.complete.configs
	fixture.complete.mu.Unlock()
	if len(configs) != 1 {
		t.Fatalf("Complete calls = %d, want 1", len(configs))
	}
	cfg := configs[0]
	if cfg.CD2Address != "cd2.example:443" || cfg.CD2Username != "operator" || cfg.CD2Password != "cd2-secret" ||
		!cfg.CD2Insecure || cfg.CloudRoot != "/cloud" || cfg.LocalRoot != localRoot {
		t.Errorf("Complete config = %+v, want parsed wizard values", cfg)
	}
	if cfg.OfflineTimeout != 30*time.Hour || cfg.CopyTimeout != 80*time.Hour || cfg.VerifyTimeout != 15*time.Minute {
		t.Errorf("Complete timeouts = %v/%v/%v, want 30h/80h/15m", cfg.OfflineTimeout, cfg.CopyTimeout, cfg.VerifyTimeout)
	}

	// The dial saw the CD2 credentials and a bounded test timeout.
	fixture.dial.mu.Lock()
	calls := fixture.dial.calls
	fixture.dial.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("dial calls = %d, want at least 2", len(calls))
	}
	last := calls[len(calls)-1]
	if last.address != "cd2.example:443" || last.username != "operator" || last.password != "cd2-secret" || !last.insecure {
		t.Errorf("finish dial = %+v, want stored CD2 credentials", last)
	}
	if last.timeout != setupTestTimeout {
		t.Errorf("finish dial timeout = %v, want %v", last.timeout, setupTestTimeout)
	}
}

func TestSetupDirectoryPickers(t *testing.T) {
	fixture := newSetupFixture(t)
	fixture.dial.directories = []clouddrive.Directory{
		{Name: "Downloads", Path: "/115/Downloads"},
		{Name: "Media", Path: "/115/Media"},
	}
	sid, csrf := fixture.advancePassword()
	advanced := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"},
		"password": {"cd2-secret"},
	})
	requireStatus(t, advanced, http.StatusSeeOther)

	page := fixture.get("/setup", sid)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(),
		"data-directory-picker", `readonly`, "/setup/cloud-directories", "/setup/local-directories",
		"data-directory-up disabled", "data-directory-name", "data-directory-create disabled", "data-directory-select disabled",
	)

	listed := fixture.get("/setup/cloud-directories?path=%2F115", sid)
	requireStatus(t, listed, http.StatusOK)
	if contentType := listed.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want JSON", contentType)
	}
	var listResponse cloudDirectoryResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResponse.Path != "/115" || !slices.Equal(listResponse.Directories, fixture.dial.directories) {
		t.Errorf("list response = %+v", listResponse)
	}

	created := fixture.post("/setup/cloud-directories", sid, csrf, url.Values{
		"parent": {"/115"}, "name": {"CD211"},
	})
	requireStatus(t, created, http.StatusCreated)
	var createResponse cloudDirectoryResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResponse.Directory == nil || *createResponse.Directory != (clouddrive.Directory{Name: "CD211", Path: "/115/CD211"}) {
		t.Errorf("create response = %+v", createResponse)
	}
	fixture.dial.mu.Lock()
	createCalls := slices.Clone(fixture.dial.createCalls)
	fixture.dial.mu.Unlock()
	if !slices.Equal(createCalls, []fakeDirectoryCreateCall{{parentPath: "/115", name: "CD211"}}) {
		t.Errorf("create calls = %+v", createCalls)
	}

	localParent := t.TempDir()
	existingPath := path.Join(localParent, "Existing")
	if err := os.Mkdir(existingPath, 0o755); err != nil {
		t.Fatalf("create existing local directory: %v", err)
	}
	if err := os.Mkdir(path.Join(localParent, ".hidden"), 0o755); err != nil {
		t.Fatalf("create hidden local directory: %v", err)
	}
	linkPath := path.Join(localParent, "Linked")
	if err := os.Symlink(existingPath, linkPath); err != nil {
		t.Fatalf("create local directory symlink: %v", err)
	}
	localListed := fixture.get("/setup/local-directories?path="+url.QueryEscape(localParent), sid)
	requireStatus(t, localListed, http.StatusOK)
	var localListResponse cloudDirectoryResponse
	if err := json.Unmarshal(localListed.Body.Bytes(), &localListResponse); err != nil {
		t.Fatalf("decode local list response: %v", err)
	}
	wantLocalDirectories := []clouddrive.Directory{
		{Name: "Existing", Path: existingPath},
		{Name: "Linked", Path: linkPath},
	}
	if localListResponse.Path != localParent || !slices.Equal(localListResponse.Directories, wantLocalDirectories) {
		t.Errorf("local list response = %+v, want path %q and %+v", localListResponse, localParent, wantLocalDirectories)
	}

	localCreated := fixture.post("/setup/local-directories", sid, csrf, url.Values{
		"parent": {localParent}, "name": {"CD211"},
	})
	requireStatus(t, localCreated, http.StatusCreated)
	createdPath := path.Join(localParent, "CD211")
	info, err := os.Stat(createdPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("created local directory stat = (%v, %v)", info, err)
	}

	for _, target := range []string{"/setup/cloud-directories?path=%2F", "/setup/local-directories?path=%2F"} {
		unauthorized := fixture.get(target, "")
		requireStatus(t, unauthorized, http.StatusUnauthorized)
	}
}

func TestSetupPasswordValidation(t *testing.T) {
	fixture := newSetupFixture(t)

	short := fixture.post("/setup/password", "", "", url.Values{"password": {"short"}, "confirm_password": {"short"}})
	requireStatus(t, short, http.StatusBadRequest)
	requireContains(t, short.Body.String(), "at least 8 characters")
	if count, err := fixture.sessions.Len(context.Background()); err != nil || count != 0 {
		t.Errorf("sessions after short password = %d (%v), want 0", count, err)
	}

	mismatch := fixture.post("/setup/password", "", "", url.Values{"password": {"correct horse"}, "confirm_password": {"correct horsey"}})
	requireStatus(t, mismatch, http.StatusBadRequest)
	requireContains(t, mismatch.Body.String(), "do not match")
	if count, err := fixture.sessions.Len(context.Background()); err != nil || count != 0 {
		t.Errorf("sessions after mismatch = %d (%v), want 0", count, err)
	}

	// The wizard is still at step 1.
	page := fixture.get("/setup", "")
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/setup/password"`)
}

func TestSetupStepGatingWithoutSession(t *testing.T) {
	fixture := newSetupFixture(t)
	for _, target := range []string{"/setup/cd2/test", "/setup/cloud-directories", "/setup/local-directories", "/setup/paths/test", "/setup/finish"} {
		response := fixture.post(target, "", "", url.Values{"action": {"test"}, "address": {"cd2.example:443"}})
		requireStatus(t, response, http.StatusSeeOther)
		if location := response.Header().Get("Location"); location != "/setup" {
			t.Errorf("%s Location = %q, want /setup", target, location)
		}
	}

	// A session that is not the wizard's own is equally rejected.
	foreign, _, err := fixture.sessions.Create(context.Background(), session.AudienceWeb)
	if err != nil {
		t.Fatalf("sessions.Create(): %v", err)
	}
	response := fixture.post("/setup/cd2/test", foreign, "", url.Values{"action": {"test"}})
	requireStatus(t, response, http.StatusSeeOther)
	if location := response.Header().Get("Location"); location != "/setup" {
		t.Errorf("foreign session Location = %q, want /setup", location)
	}

	// Losing the wizard session resets the visible flow to step 1.
	sid, _ := fixture.advancePassword()
	if err := fixture.sessions.Revoke(context.Background(), sid, session.AudienceWeb); err != nil {
		t.Fatalf("sessions.Revoke(): %v", err)
	}
	page := fixture.get("/setup", "")
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/setup/password"`)
}

// TestSetupSessionRenewalEmitsRefreshedCookie pins the wizard's sliding-expiry
// contract: authenticated setup requests renew the session and answer with a
// refreshed persistent SID cookie once the refresh boundary is reached.
func TestSetupSessionRenewalEmitsRefreshedCookie(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, _ := fixture.advancePassword()

	// Before the refresh boundary no cookie is re-issued.
	quiet := fixture.get("/setup", sid)
	requireStatus(t, quiet, http.StatusOK)
	if cookies := quiet.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("unexpected cookies before renewal: %+v", cookies)
	}

	// ttl=1h and refreshInterval=30m put the boundary at ExpiresAt-30m; the
	// clock past it makes the wizard session renew on the next request.
	fixture.clock.now = fixture.clock.now.Add(31 * time.Minute)
	renewed := fixture.get("/setup", sid)
	requireStatus(t, renewed, http.StatusOK)
	requireContains(t, renewed.Body.String(), `action="/setup/cd2/test"`)
	var refreshed *http.Cookie
	for _, cookie := range renewed.Result().Cookies() {
		if cookie.Name == "CD211_SESSION" {
			refreshed = cookie
		}
	}
	if refreshed == nil || refreshed.Value != sid {
		t.Fatalf("renewed cookies = %+v, want refreshed SID %q", renewed.Result().Cookies(), sid)
	}
	current, _, err := fixture.sessions.Get(context.Background(), sid, session.AudienceWeb)
	if err != nil {
		t.Fatalf("sessions.Get(): %v", err)
	}
	if refreshed.MaxAge <= 0 || !refreshed.Expires.Equal(current.ExpiresAt) {
		t.Errorf("refreshed cookie = MaxAge:%d Expires:%v, want positive MaxAge and Expires %v", refreshed.MaxAge, refreshed.Expires, current.ExpiresAt)
	}
}

// TestSetupRepositoryErrorsReturn500 pins the wizard's error contract:
// repository failures during session lookup or revocation surface as 500
// instead of restarting the wizard or being treated as expired/missing.
func TestSetupRepositoryErrorsReturn500(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, _ := fixture.advancePassword()
	if err := fixture.sessionStore.Close(); err != nil {
		t.Fatalf("close session store: %v", err)
	}
	page := fixture.get("/setup", sid)
	requireStatus(t, page, http.StatusInternalServerError)
	step := fixture.post("/setup/cd2/test", sid, "wrong", url.Values{"action": {"test"}})
	requireStatus(t, step, http.StatusInternalServerError)
	password := fixture.post("/setup/password", "", "", url.Values{"password": {"correct horse"}, "confirm_password": {"correct horse"}})
	requireStatus(t, password, http.StatusInternalServerError)
}

func TestSetupLaterStepsRequireCSRF(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, _ := fixture.advancePassword()
	response := fixture.post("/setup/cd2/test", sid, "wrong-token", url.Values{
		"action": {"test"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, response, http.StatusForbidden)
	response = fixture.post("/setup/cloud-directories", sid, "wrong-token", url.Values{
		"parent": {"/115"}, "name": {"CD211"},
	})
	requireStatus(t, response, http.StatusForbidden)
	response = fixture.post("/setup/local-directories", sid, "wrong-token", url.Values{
		"parent": {t.TempDir()}, "name": {"CD211"},
	})
	requireStatus(t, response, http.StatusForbidden)
}

func TestSetupFinishRetestsServerSide(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, csrf := fixture.advanceToReview()

	// The connection now fails; finish must not persist or complete anything.
	fixture.dial.setCheckErr(&clouddrive.Error{Operation: "system_status", Kind: clouddrive.ErrorTemporary})
	response := fixture.post("/setup/finish", sid, csrf, url.Values{
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusOK)
	requireContains(t, response.Body.String(), "Could not reach CloudDrive2")

	fixture.setupStore.mu.Lock()
	completed := len(fixture.setupStore.completed)
	fixture.setupStore.mu.Unlock()
	if completed != 0 {
		t.Errorf("CompleteSetup calls = %d, want 0", completed)
	}
	fixture.complete.mu.Lock()
	configs := len(fixture.complete.configs)
	fixture.complete.mu.Unlock()
	if configs != 0 {
		t.Errorf("Complete calls = %d, want 0", configs)
	}
}

func TestSetupFinishAlreadyConfigured(t *testing.T) {
	fixture := newSetupFixture(t)
	fixture.setupStore.mu.Lock()
	fixture.setupStore.err = store.ErrSetupCompleted
	fixture.setupStore.mu.Unlock()
	sid, csrf := fixture.advanceToReview()

	response := fixture.post("/setup/finish", sid, csrf, url.Values{
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusOK)
	requireContains(t, response.Body.String(), "Setup has already been completed")
	fixture.complete.mu.Lock()
	configs := len(fixture.complete.configs)
	fixture.complete.mu.Unlock()
	if configs != 0 {
		t.Errorf("Complete calls = %d, want 0", configs)
	}
}

func TestSetupCD2FailureClassification(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		viaAuth bool
		want    string
	}{
		{"auth", &clouddrive.Error{Operation: "authenticate", Kind: clouddrive.ErrorUnauthorized}, true, "rejected the username or password"},
		{"unreachable", &clouddrive.Error{Operation: "system_status", Kind: clouddrive.ErrorTemporary}, false, "Could not reach CloudDrive2"},
		{"tls", errors.New("tls: first record does not look like a TLS handshake"), false, "insecure"},
		{"other", &clouddrive.Error{Operation: "find_file", Kind: clouddrive.ErrorRejected}, false, "connection test failed"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			fixture := newSetupFixture(t)
			sid, csrf := fixture.advancePassword()
			if item.viaAuth {
				fixture.dial.setAuthErr(item.err)
			} else {
				fixture.dial.setCheckErr(item.err)
			}
			response := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
				"action": {"test"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
			})
			requireStatus(t, response, http.StatusOK)
			requireContains(t, response.Body.String(), item.want)
			// A failed test re-renders step 2 and does not advance the wizard.
			requireContains(t, response.Body.String(), `action="/setup/cd2/test"`)
		})
	}
}

func TestSetupAuthFailureBlocksStepAndFinish(t *testing.T) {
	fixture := newSetupFixture(t)
	fixture.dial.setAuthErr(&clouddrive.Error{Operation: "authenticate", Kind: clouddrive.ErrorUnauthorized})
	sid, csrf := fixture.advancePassword()

	// Wrong credentials surface on step 2 even though the readiness check
	// would pass, because Authenticate performs the login round-trip.
	step2 := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"test"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"wrong-password"},
	})
	requireStatus(t, step2, http.StatusOK)
	requireContains(t, step2.Body.String(), "rejected the username or password")

	// Continue is equally blocked; the wizard stays on step 2.
	continued := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"wrong-password"},
	})
	requireStatus(t, continued, http.StatusOK)
	requireContains(t, continued.Body.String(), "rejected the username or password")

	// A forged review submission is blocked by the server-side re-test.
	finish := fixture.post("/setup/finish", sid, csrf, url.Values{
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, finish, http.StatusOK)
	requireContains(t, finish.Body.String(), "rejected the username or password")
	fixture.setupStore.mu.Lock()
	completed := len(fixture.setupStore.completed)
	fixture.setupStore.mu.Unlock()
	if completed != 0 {
		t.Errorf("CompleteSetup calls = %d, want 0", completed)
	}
}

func TestSetupPathsValidation(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, csrf := fixture.advancePassword()
	advanced := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"},
		"password": {"cd2-secret"}, "insecure": {"true"},
	})
	requireStatus(t, advanced, http.StatusSeeOther)

	relative := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"test"}, "cloud_root": {"cloud"}, "local_root": {t.TempDir()},
	})
	requireStatus(t, relative, http.StatusBadRequest)
	requireContains(t, relative.Body.String(), "absolute clean path")

	missing := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"test"}, "cloud_root": {"/cloud"}, "local_root": {"/no/such/root"},
	})
	requireStatus(t, missing, http.StatusOK)
	requireContains(t, missing.Body.String(), "not writable or does not exist")
}

func TestSetupRoutesServeLangAndStaticPreAuth(t *testing.T) {
	fixture := newSetupFixture(t)
	lang := fixture.get("/lang?to=zh", "")
	requireStatus(t, lang, http.StatusSeeOther)
	for _, target := range []struct {
		path        string
		contentType string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/app.js", "text/javascript; charset=utf-8"},
		{"/static/motion.js", "text/javascript; charset=utf-8"},
		{"/static/actions-motion.js", "text/javascript; charset=utf-8"},
		{"/static/setup-motion.js", "text/javascript; charset=utf-8"},
		{"/static/theme-init.js", "text/javascript; charset=utf-8"},
		{"/static/vendor/motion-mini.js", "text/javascript; charset=utf-8"},
	} {
		asset := fixture.get(target.path, "")
		requireStatus(t, asset, http.StatusOK)
		if asset.Header().Get("Cache-Control") != "public,max-age=3600" || asset.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers = %v", target.path, asset.Header())
		}
		if got := asset.Header().Get("Content-Type"); got != target.contentType {
			t.Errorf("%s Content-Type = %q, want %q", target.path, got, target.contentType)
		}
		if asset.Body.Len() == 0 {
			t.Errorf("%s returned an empty asset", target.path)
		}
	}
	postStatic := fixture.post("/static/vendor/motion-mini.js", "", "", nil)
	requireStatus(t, postStatic, http.StatusMethodNotAllowed)
	setupModulePOST := fixture.post("/static/setup-motion.js", "", "", nil)
	requireStatus(t, setupModulePOST, http.StatusMethodNotAllowed)
	get := fixture.get("/setup/password", "")
	requireStatus(t, get, http.StatusMethodNotAllowed)
	missing := fixture.get("/no-such-route", "")
	requireStatus(t, missing, http.StatusNotFound)
}

func TestSetupStepURLsRestoreReachedSteps(t *testing.T) {
	fixture := newSetupFixture(t)

	// Without a wizard session every step URL falls back to step 1.
	for _, target := range []string{"/setup?step=2", "/setup?step=3", "/setup?step=9", "/setup?step=two"} {
		page := fixture.get(target, "")
		requireStatus(t, page, http.StatusOK)
		requireContains(t, page.Body.String(), `action="/setup/password"`)
	}

	sid, _ := fixture.advancePassword()

	// Malformed and ahead steps render the current step, never an error.
	for _, target := range []string{"/setup?step=abc", "/setup?step=0", "/setup?step=4"} {
		page := fixture.get(target, sid)
		requireStatus(t, page, http.StatusOK)
		requireContains(t, page.Body.String(), `action="/setup/cd2/test"`)
	}

	// Reached steps stay viewable in either direction.
	step1 := fixture.get("/setup?step=1", sid)
	requireStatus(t, step1, http.StatusOK)
	requireContains(t, step1.Body.String(), `action="/setup/password"`, `data-setup-step="1"`)
	step2 := fixture.get("/setup?step=2", sid)
	requireStatus(t, step2, http.StatusOK)
	requireContains(t, step2.Body.String(), `action="/setup/cd2/test"`, `data-setup-step="2"`)

	// A later reached step remains viewable backwards from the review.
	reviewSid, csrf := fixture.advanceToReview()
	backward := fixture.get("/setup?step=2", reviewSid)
	requireStatus(t, backward, http.StatusOK)
	requireContains(t, backward.Body.String(), `action="/setup/cd2/test"`, `data-setup-step="2"`)
	review := fixture.get("/setup?step=4", reviewSid)
	requireStatus(t, review, http.StatusOK)
	requireContains(t, review.Body.String(), `action="/setup/finish"`, `data-setup-step="4"`)

	// The wizard still advances through the normal POST/redirect flow.
	continued := fixture.post("/setup/paths/test", reviewSid, csrf, url.Values{
		"action": {"continue"}, "cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
	})
	requireStatus(t, continued, http.StatusSeeOther)
}

func TestSetupStepRedirectTargets(t *testing.T) {
	fixture := newSetupFixture(t)
	password := fixture.post("/setup/password", "", "", url.Values{
		"password": {"correct horse"}, "confirm_password": {"correct horse"},
	})
	requireStatus(t, password, http.StatusSeeOther)
	if location := password.Header().Get("Location"); location != "/setup?step=2" {
		t.Errorf("password Location = %q, want /setup?step=2", location)
	}
	cookies := password.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "CD211_SESSION" {
		t.Fatalf("password cookies = %+v, want one SID", cookies)
	}
	sid := cookies[0].Value
	current, _, err := fixture.sessions.Get(context.Background(), sid, session.AudienceWeb)
	if err != nil {
		t.Fatalf("wizard session %q lookup: %v", sid, err)
	}
	csrf := current.CSRFToken

	cd2 := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, cd2, http.StatusSeeOther)
	if location := cd2.Header().Get("Location"); location != "/setup?step=3" {
		t.Errorf("cd2 continue Location = %q, want /setup?step=3", location)
	}

	paths := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"continue"}, "cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
	})
	requireStatus(t, paths, http.StatusSeeOther)
	if location := paths.Header().Get("Location"); location != "/setup?step=4" {
		t.Errorf("paths continue Location = %q, want /setup?step=4", location)
	}
}

func TestSetupBackwardViewNeverRegressesStep(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, csrf := fixture.advanceToReview()

	// An older step's Continue re-runs its validation but must not roll the
	// wizard state back; the redirect targets the actual current step.
	backward := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, backward, http.StatusSeeOther)
	if location := backward.Header().Get("Location"); location != "/setup?step=4" {
		t.Errorf("backward cd2 continue Location = %q, want /setup?step=4", location)
	}
	pathsBackward := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"continue"}, "cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
	})
	requireStatus(t, pathsBackward, http.StatusSeeOther)
	if location := pathsBackward.Header().Get("Location"); location != "/setup?step=4" {
		t.Errorf("backward paths continue Location = %q, want /setup?step=4", location)
	}

	page := fixture.get("/setup", sid)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/setup/finish"`)
}

func TestSetupErrorsKeepEnteredValues(t *testing.T) {
	fixture := newSetupFixture(t)
	sid, csrf := fixture.advancePassword()

	// An invalid address re-renders step 2 with the typed values intact.
	invalid := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"test"}, "address": {"not an address"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, invalid, http.StatusBadRequest)
	requireContains(t, invalid.Body.String(), `value="not an address"`, `value="operator"`)

	// A failed connectivity test preserves the same fields.
	fixture.dial.setCheckErr(&clouddrive.Error{Operation: "system_status", Kind: clouddrive.ErrorTemporary})
	failed := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"test"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, failed, http.StatusOK)
	requireContains(t, failed.Body.String(), `value="cd2.example:443"`, `value="operator"`)
	fixture.dial.setCheckErr(nil)

	// Path validation errors keep both roots typed on the re-rendered form.
	advanced := fixture.post("/setup/cd2/test", sid, csrf, url.Values{
		"action": {"continue"}, "address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
	})
	requireStatus(t, advanced, http.StatusSeeOther)
	localRoot := t.TempDir()
	relative := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"test"}, "cloud_root": {"relative"}, "local_root": {localRoot},
	})
	requireStatus(t, relative, http.StatusBadRequest)
	requireContains(t, relative.Body.String(), `value="relative"`, `value="`+localRoot+`"`)

	// A non-writable local root keeps the typed cloud root too.
	missing := fixture.post("/setup/paths/test", sid, csrf, url.Values{
		"action": {"test"}, "cloud_root": {"/cloud"}, "local_root": {"/no/such/root"},
	})
	requireStatus(t, missing, http.StatusOK)
	requireContains(t, missing.Body.String(), `value="/cloud"`)
}

func TestSettingsRequiresAuth(t *testing.T) {
	fixture := newWebFixture(t)
	page := fixture.request(http.MethodGet, "/settings", nil, false)
	requireStatus(t, page, http.StatusSeeOther)
	if location := page.Header().Get("Location"); location != "/login" {
		t.Errorf("GET /settings Location = %q, want /login", location)
	}
	test := fixture.request(http.MethodPost, "/settings/test", url.Values{"address": {"cd2.example:443"}}, false)
	requireStatus(t, test, http.StatusSeeOther)
	if location := test.Header().Get("Location"); location != "/login" {
		t.Errorf("POST /settings/test Location = %q, want /login", location)
	}
	save := fixture.request(http.MethodPost, "/settings/save", url.Values{}, false)
	requireStatus(t, save, http.StatusSeeOther)
	if location := save.Header().Get("Location"); location != "/login" {
		t.Errorf("POST /settings/save Location = %q, want /login", location)
	}
}

func TestSettingsPagePrefillAndSavedFlash(t *testing.T) {
	store := &recordingSettingsStore{values: map[string]string{
		settings.KeyCD2Address:       "cd2.example:443",
		settings.KeyCD2Username:      "operator",
		settings.KeyCD2Password:      "cd2-secret",
		settings.KeyCD2Insecure:      "true",
		settings.KeyCloudRoot:        "/cloud",
		settings.KeyLocalRoot:        "/data",
		settings.KeyOfflineTimeout:   "24h",
		settings.KeyCopyTimeout:      "72h",
		settings.KeyVerifyTimeout:    "10m",
		settings.KeySetupCompletedAt: "2026-08-06T12:00:00Z",
	}}
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store})

	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/save"`, `formaction="/settings/test"`,
		`value="cd2.example:443"`, `value="operator"`, `value="/cloud"`, `value="/data"`, "Settings")
	requireAbsent(t, page.Body.String(), "cd2-secret")

	saved := fixture.request(http.MethodGet, "/settings?saved=1", nil, true)
	requireStatus(t, saved, http.StatusOK)
	requireContains(t, saved.Body.String(), "Settings saved and applied.")
}

func TestSettingsTestEndpoint(t *testing.T) {
	log := &[]string{}
	store := &recordingSettingsStore{values: map[string]string{settings.KeyCD2Password: "cd2-secret"}, log: log}
	dial := &fakeDial{}
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store, Dial: dial.dial})

	response := fixture.post("/settings/test", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {""},
		"cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusOK)
	requireContains(t, response.Body.String(), "All checks passed.")
	if slices.Contains(*log, "replace") {
		t.Errorf("settings/test persisted settings: log = %v", *log)
	}
}

func TestSettingsSaveKeepsStoredPassword(t *testing.T) {
	store := &recordingSettingsStore{values: map[string]string{settings.KeyCD2Password: "stored-secret"}}
	apply := &recordingComplete{}
	dial := &fakeDial{}
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store, Dial: dial.dial, Apply: apply.apply})

	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireAbsent(t, page.Body.String(), "stored-secret")

	response := fixture.post("/settings/save", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {""},
		"insecure": {"true"}, "cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusSeeOther)
	store.mu.Lock()
	values := store.values
	store.mu.Unlock()
	if values[settings.KeyCD2Password] != "stored-secret" {
		t.Errorf("persisted cd2.password = %q, want stored-secret", values[settings.KeyCD2Password])
	}
	apply.mu.Lock()
	configs := apply.configs
	apply.mu.Unlock()
	if len(configs) != 1 || configs[0].CD2Password != "stored-secret" {
		t.Errorf("Apply configs = %+v, want stored-secret password", configs)
	}
}

func TestSettingsSaveCallsReplaceThenApply(t *testing.T) {
	log := &[]string{}
	store := &recordingSettingsStore{values: map[string]string{settings.KeyCD2Password: "cd2-secret"}, log: log}
	apply := &recordingComplete{log: log}
	dial := &fakeDial{}
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store, Dial: dial.dial, Apply: apply.apply})

	response := fixture.post("/settings/save", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {""},
		"cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusSeeOther)
	want := []string{"list", "replace", "apply"}
	if !slices.Equal(*log, want) {
		t.Errorf("call order = %v, want %v", *log, want)
	}
}

func TestSettingsSaveTestFailureDoesNotPersist(t *testing.T) {
	store := &recordingSettingsStore{values: map[string]string{settings.KeyCD2Password: "cd2-secret"}}
	apply := &recordingComplete{}
	dial := &fakeDial{}
	dial.setCheckErr(&clouddrive.Error{Operation: "authenticate", Kind: clouddrive.ErrorUnauthorized})
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store, Dial: dial.dial, Apply: apply.apply})

	response := fixture.post("/settings/save", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {""},
		"cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusOK)
	requireContains(t, response.Body.String(), "rejected the username or password")
	store.mu.Lock()
	values := store.values
	store.mu.Unlock()
	if _, ok := values[settings.KeyCD2Address]; ok {
		t.Errorf("settings persisted despite failed test: %v", values)
	}
	apply.mu.Lock()
	configs := len(apply.configs)
	apply.mu.Unlock()
	if configs != 0 {
		t.Errorf("Apply calls = %d, want 0", configs)
	}
}

func TestSettingsSaveApplyFailureKeepsPersisted(t *testing.T) {
	store := &recordingSettingsStore{values: map[string]string{settings.KeyCD2Password: "cd2-secret"}}
	apply := &recordingComplete{err: errors.New("swap failed")}
	dial := &fakeDial{}
	fixture := newWebFixtureWithSettings(t, SettingsDeps{Store: store, Dial: dial.dial, Apply: apply.apply})

	response := fixture.post("/settings/save", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {""},
		"cloud_root": {"/cloud"}, "local_root": {t.TempDir()},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusOK)
	requireContains(t, response.Body.String(), "applying them failed")
	store.mu.Lock()
	values := store.values
	store.mu.Unlock()
	if values[settings.KeyCD2Address] != "cd2.example:443" {
		t.Errorf("persisted address = %q, want cd2.example:443", values[settings.KeyCD2Address])
	}
}
