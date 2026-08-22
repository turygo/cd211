package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/store"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

type sequenceReader struct {
	mu   sync.Mutex
	next byte
}

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.next++
	for index := range buffer {
		buffer[index] = reader.next + byte(index)
	}
	return len(buffer), nil
}

type countingWaker struct{ count int }

func (waker *countingWaker) Wake() { waker.count++ }

type controlledCloudStatus struct {
	err         error
	waitForDone bool
	calls       int
}

func (status *controlledCloudStatus) Check(ctx context.Context) error {
	status.calls++
	if status.waitForDone {
		<-ctx.Done()
		return ctx.Err()
	}
	return status.err
}

type recordingRepository struct {
	*store.Store
	actionError  error
	retryTargets []domain.State
	deleteFlags  []bool
}

func (repo *recordingRepository) Start(ctx context.Context, hash string, now time.Time) error {
	if repo.actionError != nil {
		return repo.actionError
	}
	return repo.Store.Start(ctx, hash, now)
}

func (repo *recordingRepository) Pause(ctx context.Context, hash string, now time.Time) error {
	if repo.actionError != nil {
		return repo.actionError
	}
	return repo.Store.Pause(ctx, hash, now)
}

func (repo *recordingRepository) Retry(ctx context.Context, hash string, target domain.State, now time.Time) error {
	repo.retryTargets = append(repo.retryTargets, target)
	if repo.actionError != nil {
		return repo.actionError
	}
	return repo.Store.Retry(ctx, hash, target, now)
}

func (repo *recordingRepository) Cancel(ctx context.Context, hash string, now time.Time) error {
	if repo.actionError != nil {
		return repo.actionError
	}
	return repo.Store.Cancel(ctx, hash, now)
}

func (repo *recordingRepository) RequestDelete(ctx context.Context, hashes []string, deleteFiles bool, now time.Time) error {
	repo.deleteFlags = append(repo.deleteFlags, deleteFiles)
	if repo.actionError != nil {
		return repo.actionError
	}
	return repo.Store.RequestDelete(ctx, hashes, deleteFiles, now)
}

type webFixture struct {
	t          *testing.T
	clock      *fixedClock
	store      *store.Store
	dbPath     string
	repo       *recordingRepository
	sessions   *session.Store
	waker      *countingWaker
	cloud      *controlledCloudStatus
	filesystem Filesystem
	creds      *creds.Manager
	webhooks   *fakeWebhookStore
	handler    http.Handler
	localRoot  string
	sid        string
	csrf       string
}

func newWebFixture(t *testing.T) *webFixture {
	return newWebFixtureWithSettings(t, SettingsDeps{})
}

func newWebFixtureWithSettings(t *testing.T, settingsDeps SettingsDeps) *webFixture {
	t.Helper()
	return newWebFixtureAt(t, filepath.Join(t.TempDir(), "web.db"), settingsDeps)
}

func newWebFixtureAt(t *testing.T, dbPath string, settingsDeps SettingsDeps) *webFixture {
	t.Helper()
	clock := &fixedClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	database, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Store.Close(): %v", err)
		}
	})
	sessions, err := session.New(database, clock, &sequenceReader{}, time.Hour, 30*time.Minute, 32)
	if err != nil {
		t.Fatalf("session.New(): %v", err)
	}
	sid, current, err := sessions.Create(context.Background())
	if err != nil {
		t.Fatalf("sessions.Create(): %v", err)
	}
	repo := &recordingRepository{Store: database}
	waker := &countingWaker{}
	cloud := &controlledCloudStatus{}
	localRoot := t.TempDir()
	filesystem, err := fsafe.New(localRoot)
	if err != nil {
		t.Fatalf("fsafe.New(): %v", err)
	}
	localRoot = filesystem.LocalRoot()
	credentials, err := creds.New(database)
	if err != nil {
		t.Fatalf("creds.New(): %v", err)
	}
	// The default password is gone; seed the classic test password so login
	// and password-change flows behave as before.
	initialHash, err := creds.HashPassword("adminadmin")
	if err != nil {
		t.Fatalf("creds.HashPassword(): %v", err)
	}
	if err := database.SetOperatorPasswordHash(context.Background(), initialHash, clock.now); err != nil {
		t.Fatalf("SetOperatorPasswordHash(): %v", err)
	}
	if settingsDeps.Store == nil {
		settingsDeps.Store = database
	}
	if settingsDeps.Tokens == nil {
		settingsDeps.Tokens = database
	}
	if settingsDeps.QBTKeys == nil {
		settingsDeps.QBTKeys = database
	}
	webhooks := newFakeWebhookStore()
	handler, err := New(Config{CloudRoot: "/cloud", LocalRoot: localRoot}, credentials, repo, sessions, clock, waker, cloud, filesystem, settingsDeps, webhooks)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return &webFixture{t: t, clock: clock, store: database, dbPath: dbPath, repo: repo, sessions: sessions, waker: waker, cloud: cloud, filesystem: filesystem, creds: credentials, webhooks: webhooks, handler: handler, localRoot: localRoot, sid: sid, csrf: current.CSRFToken}
}

func (fixture *webFixture) request(method, target string, form url.Values, authenticated bool) *httptest.ResponseRecorder {
	fixture.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, target, body)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if authenticated {
		request.AddCookie(&http.Cookie{Name: "SID", Value: fixture.sid})
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func (fixture *webFixture) post(target string, values url.Values) *httptest.ResponseRecorder {
	fixture.t.Helper()
	if values == nil {
		values = make(url.Values)
	}
	values.Set("csrf_token", fixture.csrf)
	return fixture.request(http.MethodPost, target, values, true)
}

func (fixture *webFixture) seedDownload(seed string, target domain.State, configure func(*domain.Download)) domain.Download {
	fixture.t.Helper()
	hash := seed
	if len(hash) != 40 {
		hash = strings.Repeat(seed, 40)
	}
	now := fixture.clock.now.Add(-10 * time.Minute)
	download := domain.Download{
		Hash: hash, Name: "release-" + seed, SourceKind: domain.SourceMagnet,
		SubmissionURI: "magnet:?xt=urn:btih:" + hash + "&tr=https://tracker.invalid/secret-token",
		Category:      "movies", CloudFolder: "/cloud/movies/release-" + seed,
		SavePath: filepath.Join(fixture.localRoot, "movies"), TotalSize: 3072,
		OfflineProgress: 0.35, CopyProgress: 0.42, QbitProgress: 0.27,
		PhaseStartedAt: now, AttemptCount: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	switch target {
	case domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal, domain.StateCompleted:
		download.CloudSourcePath = "/cloud/movies/release-" + seed
	}
	if target == domain.StateCompleted {
		download.ContentPath = filepath.Join(fixture.localRoot, "movies", "release-"+seed)
		download.OfflineProgress = 1
		download.CopyProgress = 1
		download.QbitProgress = 1
	}
	if configure != nil {
		configure(&download)
	}

	var transitions []domain.State
	switch target {
	case domain.StateStopped, domain.StateCancelRequested, domain.StateCancelled, domain.StateDeleteRequested:
		download.State = domain.StateStopped
	case domain.StateVerifyingLocal, domain.StateCompleted:
		download.State = domain.StateVerifyingLocal
	default:
		download.State = domain.StateAccepted
	}
	switch target {
	case domain.StateSubmittingOffline:
		transitions = []domain.State{domain.StateSubmittingOffline}
	case domain.StateWaitingOffline:
		transitions = []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline}
	case domain.StateSubmittingCopy:
		transitions = []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy}
	case domain.StateWaitingCopy:
		transitions = []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy, domain.StateWaitingCopy}
	case domain.StateFailed:
		transitions = []domain.State{domain.StateFailed}
	case domain.StateCompleted:
		transitions = []domain.State{domain.StateCompleted}
	}
	if len(transitions) > 0 {
		nextRun := fixture.clock.now
		download.NextRunAt = &nextRun
	}
	submission := domain.Submission{Download: download, Files: []domain.DownloadFile{
		{DownloadHash: hash, Index: 0, RelativePath: "disc/video.mkv", Size: 2048},
		{DownloadHash: hash, Index: 1, RelativePath: "disc/subtitle.srt", Size: 1024},
	}}
	created, inserted, err := fixture.store.CreateSubmission(context.Background(), submission)
	if err != nil || !inserted {
		fixture.t.Fatalf("CreateSubmission(%s): inserted=%t err=%v", target, inserted, err)
	}
	if target == domain.StateDeleteRequested {
		if err := fixture.store.RequestDelete(context.Background(), []string{hash}, false, fixture.clock.now); err != nil {
			fixture.t.Fatalf("RequestDelete(seed): %v", err)
		}
		stored, err := fixture.store.GetDownload(context.Background(), hash)
		if err != nil {
			fixture.t.Fatalf("GetDownload(seed delete): %v", err)
		}
		return stored
	}
	if target == domain.StateCancelRequested || target == domain.StateCancelled {
		if err := fixture.store.Cancel(context.Background(), hash, fixture.clock.now); err != nil {
			fixture.t.Fatalf("Cancel(seed): %v", err)
		}
		if target == domain.StateCancelRequested {
			transitions = []domain.State{domain.StateCancelRequested}
		} else {
			transitions = []domain.State{domain.StateCancelled}
		}
	}
	for index, state := range transitions {
		claim, err := fixture.store.ClaimDue(context.Background(), "seed-"+seed, fixture.clock.now.Add(time.Duration(index)*time.Second), time.Minute)
		if err != nil || claim == nil || claim.Download.Hash != hash {
			fixture.t.Fatalf("ClaimDue(seed %s -> %s): claim=%+v err=%v", seed, state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.UpdatedAt = fixture.clock.now.Add(time.Duration(index+1) * time.Second)
		next.PhaseStartedAt = next.UpdatedAt
		if index == len(transitions)-1 {
			next.NextRunAt = nil
		} else {
			nextRun := fixture.clock.now.Add(time.Duration(index+1) * time.Second)
			next.NextRunAt = &nextRun
		}
		if state == domain.StateCompleted {
			completedAt := next.UpdatedAt
			next.CompletedAt = &completedAt
		}
		if err := fixture.store.CommitClaim(context.Background(), *claim, next); err != nil {
			fixture.t.Fatalf("CommitClaim(seed %s -> %s): %v", seed, state, err)
		}
	}
	stored, err := fixture.store.GetDownload(context.Background(), created.Hash)
	if err != nil {
		fixture.t.Fatalf("GetDownload(seed): %v", err)
	}
	return stored
}

// seedProblem advances a download through the given transitions, committing a
// durable structured problem on the final one. It mirrors seedDownload but
// keeps control of the final NextRunAt, which the retrying-warning
// presentation depends on.
func (fixture *webFixture) seedProblem(seed string, states []domain.State, code domain.ProblemCode, nextRun *time.Time) domain.Download {
	fixture.t.Helper()
	hash := strings.Repeat(seed, 40)
	now := fixture.clock.now
	download := domain.Download{
		Hash: hash, Name: "release-" + seed, SourceKind: domain.SourceMagnet,
		SubmissionURI: "magnet:?xt=urn:btih:" + hash,
		Category:      "movies", CloudFolder: "/cloud/movies/release-" + seed,
		SavePath:        filepath.Join(fixture.localRoot, "movies"),
		CloudSourcePath: "/cloud/movies/release-" + seed,
		OfflineProgress: 1, CopyProgress: 0.9, QbitProgress: 0.9,
		LastUpstreamStatus: domain.UpstreamOfflineFinished,
		State:              domain.StateAccepted, PhaseStartedAt: now, NextRunAt: &now,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	created, inserted, err := fixture.store.CreateSubmission(context.Background(), domain.Submission{Download: download})
	if err != nil || !inserted {
		fixture.t.Fatalf("CreateSubmission(%s): inserted=%t err=%v", seed, inserted, err)
	}
	for index, state := range states {
		due := now.Add(time.Duration(index) * time.Second)
		claim, err := fixture.store.ClaimDue(context.Background(), "seed-"+seed, due, time.Minute)
		if err != nil || claim == nil {
			fixture.t.Fatalf("ClaimDue(%s -> %s): claim=%+v err=%v", seed, state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.UpdatedAt = due
		next.PhaseStartedAt = due
		if index == len(states)-1 {
			next.LastErrorCode = string(code)
			next.LastError = domain.ProblemText(code)
			next.AttemptCount = 1
			next.NextRunAt = nextRun
		} else {
			next.NextRunAt = &due
		}
		if err := fixture.store.CommitClaim(context.Background(), *claim, next); err != nil {
			fixture.t.Fatalf("CommitClaim(%s -> %s): %v", seed, state, err)
		}
	}
	stored, err := fixture.store.GetDownload(context.Background(), created.Hash)
	if err != nil {
		fixture.t.Fatalf("GetDownload(%s): %v", seed, err)
	}
	return stored
}

func (fixture *webFixture) seedCategory(name string, enabled bool) domain.Category {
	fixture.t.Helper()
	created := fixture.clock.now.Add(-24 * time.Hour)
	category, err := fixture.store.UpsertCategory(context.Background(), domain.Category{
		Name: name, CloudPath: "/cloud/" + name, SavePath: filepath.Join(fixture.localRoot, name),
		Enabled: enabled, CreatedAt: created, UpdatedAt: created,
	})
	if err != nil {
		fixture.t.Fatalf("UpsertCategory(): %v", err)
	}
	return category
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", response.Code, status, response.Body.String())
	}
}

func requireContains(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Errorf("response does not contain %q", value)
		}
	}
}

func requireAbsent(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Errorf("response unexpectedly contains protected value %q", value)
		}
	}
}

func TestAuthenticationRedirectLoginAndLogout(t *testing.T) {
	fixture := newWebFixture(t)

	redirect := fixture.request(http.MethodGet, "/", nil, false)
	requireStatus(t, redirect, http.StatusSeeOther)
	if location := redirect.Header().Get("Location"); location != "/login" {
		t.Errorf("redirect Location = %q, want /login", location)
	}

	authenticatedLogin := fixture.request(http.MethodGet, "/login", nil, true)
	requireStatus(t, authenticatedLogin, http.StatusSeeOther)
	if location := authenticatedLogin.Header().Get("Location"); location != "/" {
		t.Errorf("authenticated login Location = %q, want /", location)
	}

	invalid := fixture.request(http.MethodPost, "/login", url.Values{"username": {"operator"}, "password": {"wrong secret"}}, false)
	requireStatus(t, invalid, http.StatusUnauthorized)
	requireContains(t, invalid.Body.String(), "The username or password did not match.")
	requireAbsent(t, invalid.Body.String(), "wrong secret")

	valid := fixture.request(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"adminadmin"}}, false)
	requireStatus(t, valid, http.StatusSeeOther)
	cookies := valid.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "SID" || cookies[0].Path != "/" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("login cookies = %+v, want secure SID contract", cookies)
	}
	createdSession, _, err := fixture.sessions.Get(context.Background(), cookies[0].Value)
	if err != nil {
		t.Fatalf("sessions.Get(): %v", err)
	}
	if cookies[0].MaxAge <= 0 || !cookies[0].Expires.Equal(createdSession.ExpiresAt) {
		t.Errorf("login cookie persistence = MaxAge:%d Expires:%v, want positive MaxAge and Expires %v", cookies[0].MaxAge, cookies[0].Expires, createdSession.ExpiresAt)
	}

	secureRequest := httptest.NewRequest(http.MethodPost, "https://cd211.test/login", strings.NewReader(url.Values{"username": {"admin"}, "password": {"adminadmin"}}.Encode()))
	secureRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secureLogin := httptest.NewRecorder()
	fixture.handler.ServeHTTP(secureLogin, secureRequest)
	secureCookies := secureLogin.Result().Cookies()
	if secureLogin.Code != http.StatusSeeOther || len(secureCookies) != 1 || !secureCookies[0].Secure {
		t.Fatalf("HTTPS login cookie = status:%d cookies:%+v", secureLogin.Code, secureCookies)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{"csrf_token": {createdSession.CSRFToken}}.Encode()))
	logoutRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutRequest.AddCookie(cookies[0])
	logout := httptest.NewRecorder()
	fixture.handler.ServeHTTP(logout, logoutRequest)
	requireStatus(t, logout, http.StatusSeeOther)
	if _, _, err := fixture.sessions.Get(context.Background(), cookies[0].Value); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("session after logout = %v, want ErrNotFound", err)
	}
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != "SID" || cleared[0].MaxAge != -1 {
		t.Errorf("logout cookies = %+v, want expired SID", cleared)
	}
}

// TestWebSessionRenewalEmitsRefreshedCookie pins the sliding-expiry contract:
// an authenticated request whose session is at/after the refresh boundary
// renews the record and answers with a refreshed persistent SID cookie, while
// a session that is not yet due emits no cookie. GET /login with a valid
// session refreshes the same way before redirecting.
func TestWebSessionRenewalEmitsRefreshedCookie(t *testing.T) {
	fixture := newWebFixture(t)

	// Before the refresh boundary no cookie is re-issued.
	quiet := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, quiet, http.StatusOK)
	if cookies := quiet.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("unexpected cookies before renewal: %+v", cookies)
	}

	// ttl=1h and refreshInterval=30m put the boundary at ExpiresAt-30m; the
	// clock past it makes the next Get renew.
	fixture.clock.now = fixture.clock.now.Add(31 * time.Minute)
	renewed := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, renewed, http.StatusOK)
	var refreshed *http.Cookie
	for _, cookie := range renewed.Result().Cookies() {
		if cookie.Name == "SID" {
			refreshed = cookie
		}
	}
	if refreshed == nil || refreshed.Value != fixture.sid {
		t.Fatalf("renewed cookies = %+v, want refreshed SID %q", renewed.Result().Cookies(), fixture.sid)
	}
	current, _, err := fixture.sessions.Get(context.Background(), fixture.sid)
	if err != nil {
		t.Fatalf("sessions.Get(): %v", err)
	}
	if refreshed.MaxAge <= 0 || !refreshed.Expires.Equal(current.ExpiresAt) {
		t.Errorf("refreshed cookie = MaxAge:%d Expires:%v, want positive MaxAge and Expires %v", refreshed.MaxAge, refreshed.Expires, current.ExpiresAt)
	}

	// GET /login with the renewed session refreshes again before redirecting.
	fixture.clock.now = fixture.clock.now.Add(31 * time.Minute)
	login := fixture.request(http.MethodGet, "/login", nil, true)
	requireStatus(t, login, http.StatusSeeOther)
	if location := login.Header().Get("Location"); location != "/" {
		t.Errorf("login Location = %q, want /", location)
	}
	loginRefreshed := login.Result().Cookies()
	if len(loginRefreshed) != 1 || loginRefreshed[0].Name != "SID" || loginRefreshed[0].MaxAge <= 0 {
		t.Errorf("login cookies = %+v, want refreshed persistent SID", loginRefreshed)
	}
}

// TestWebSessionSurvivesRestartOverSameDatabase proves the SID is durable: a
// cookie issued by one process authenticates against a fresh process (new
// session store and handler) over the same SQLite file.
func TestWebSessionSurvivesRestartOverSameDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	first := newWebFixtureAt(t, dbPath, SettingsDeps{})
	login := first.request(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"adminadmin"}}, false)
	requireStatus(t, login, http.StatusSeeOther)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "SID" {
		t.Fatalf("login cookies = %+v", cookies)
	}
	sidValue := cookies[0].Value
	if err := first.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted := newWebFixtureAt(t, dbPath, SettingsDeps{})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "SID", Value: sidValue})
	response := httptest.NewRecorder()
	restarted.handler.ServeHTTP(response, request)
	requireStatus(t, response, http.StatusOK)
}

// TestWebRepositoryErrorsReturn500 pins the error contract: repository
// failures during session lookup or revocation are HTTP 500, never a login
// redirect or a claimed logout.
func TestWebRepositoryErrorsReturn500(t *testing.T) {
	fixture := newWebFixture(t)
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	page := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, page, http.StatusInternalServerError)
	logout := fixture.post("/logout", url.Values{})
	requireStatus(t, logout, http.StatusInternalServerError)
}

func TestAuthenticatedMutationBrowserOriginPolicy(t *testing.T) {
	fixture := newWebFixture(t)
	values := url.Values{
		"csrf_token":    {fixture.csrf},
		"name":          {"blocked"},
		"cloud_subpath": {"blocked"},
		"save_subpath":  {"blocked"},
		"enabled":       {"true"},
	}
	send := func(origin string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/categories/save", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		request.AddCookie(&http.Cookie{Name: "SID", Value: fixture.sid})
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		return response
	}

	for _, origin := range []string{"http://evil.invalid", "null"} {
		requireStatus(t, send(origin), http.StatusForbidden)
		if _, err := fixture.store.GetCategory(context.Background(), "blocked"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("origin %q mutation persisted: %v", origin, err)
		}
	}

	// A browser sending its real same-origin Origin header must pass.
	matching := send("http://" + "example.com")
	requireStatus(t, matching, http.StatusSeeOther)
	if _, err := fixture.store.GetCategory(context.Background(), "blocked"); err != nil {
		t.Fatalf("same-origin mutation did not persist: %v", err)
	}
}

func TestSecurityHeadersAndStaticAssets(t *testing.T) {
	fixture := newWebFixture(t)
	login := fixture.request(http.MethodGet, "/login", nil, false)
	requireStatus(t, login, http.StatusOK)
	if got := login.Header().Get("Content-Security-Policy"); got != "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" {
		t.Errorf("CSP = %q", got)
	}
	if got := login.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q", got)
	}

	body := login.Body.String()
	themeInit := strings.Index(body, `<script src="/static/theme-init.js?v=2"></script>`)
	stylesheet := strings.Index(body, `<link rel="stylesheet" href="/static/app.css?v=15">`)
	if themeInit < 0 || stylesheet < 0 || themeInit > stylesheet {
		t.Errorf("theme initializer must load before stylesheet: theme=%d stylesheet=%d", themeInit, stylesheet)
	}
	moduleScript := strings.Index(body, `<script type="module" src="/static/app.js?v=8"></script>`)
	if moduleScript < 0 || moduleScript < stylesheet {
		t.Errorf("app module script must load after stylesheet: module=%d stylesheet=%d", moduleScript, stylesheet)
	}
	actionsMotion := strings.Index(body, `<script type="module" src="/static/actions-motion.js?v=1"></script>`)
	if actionsMotion < 0 || actionsMotion < moduleScript {
		t.Errorf("actions-motion module script must load after app module: actions=%d module=%d", actionsMotion, moduleScript)
	}

	for _, target := range []struct {
		path        string
		contentType string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/app.js", "text/javascript; charset=utf-8"},
		{"/static/motion.js", "text/javascript; charset=utf-8"},
		{"/static/actions-motion.js", "text/javascript; charset=utf-8"},
		{"/static/downloads-live.js", "text/javascript; charset=utf-8"},
		{"/static/setup-motion.js", "text/javascript; charset=utf-8"},
		{"/static/theme-init.js", "text/javascript; charset=utf-8"},
		{"/static/vendor/motion-mini.js", "text/javascript; charset=utf-8"},
	} {
		response := fixture.request(http.MethodGet, target.path, nil, false)
		requireStatus(t, response, http.StatusOK)
		if response.Header().Get("Cache-Control") != "public,max-age=3600" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers = %v", target.path, response.Header())
		}
		if got := response.Header().Get("Content-Type"); got != target.contentType {
			t.Errorf("%s Content-Type = %q, want %q", target.path, got, target.contentType)
		}
		if response.Body.Len() == 0 {
			t.Errorf("%s returned an empty asset", target.path)
		}
	}

	postStatic := fixture.request(http.MethodPost, "/static/vendor/motion-mini.js", nil, false)
	requireStatus(t, postStatic, http.StatusMethodNotAllowed)
}

func TestCSRFFailureDoesNotMutate(t *testing.T) {
	fixture := newWebFixture(t)
	download := fixture.seedDownload("a", domain.StateStopped, nil)
	response := fixture.request(http.MethodPost, "/downloads/"+download.Hash+"/start", url.Values{"csrf_token": {"wrong"}}, true)
	requireStatus(t, response, http.StatusForbidden)
	stored, err := fixture.store.GetDownload(context.Background(), download.Hash)
	if err != nil || stored.State != domain.StateStopped {
		t.Fatalf("download after CSRF failure = (%+v, %v)", stored, err)
	}
	if fixture.waker.count != 0 {
		t.Errorf("Wake count = %d, want 0", fixture.waker.count)
	}
}

func TestDashboardFiltersRouteEvidenceRedactionAndCloudStatus(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedCategory("movies", true)
	states := []struct {
		seed  string
		state domain.State
	}{
		{"a", domain.StateAccepted},
		{"b", domain.StateWaitingCopy},
		{"c", domain.StateVerifyingLocal},
		{"d", domain.StateCompleted},
		{"e", domain.StateFailed},
		{"f", domain.StateCancelled},
	}
	for _, item := range states {
		fixture.seedDownload(item.seed, item.state, func(download *domain.Download) {
			if item.state == domain.StateFailed {
				download.LastError = "upstream rejected " + download.SubmissionURI
			}
		})
	}

	all := fixture.request(http.MethodGet, "/?category=movies", nil, true)
	requireStatus(t, all, http.StatusOK)
	body := all.Body.String()
	requireContains(t, body, "CloudDrive2 Online", `<span aria-hidden="true">Online</span>`, "115 OFFLINE", "NAS COPY", "LOCAL VERIFY", "Protected upstream details were redacted.", `value="all" selected`)
	requireAbsent(t, body, "stage-strip", "is-verified", "stage-fill", "cell-progress", "magnet:?", "tracker.invalid", "secret-token", fixture.sid)
	for _, item := range states {
		requireContains(t, body, "release-"+item.seed, string(item.state))
	}

	active := fixture.request(http.MethodGet, "/?view=active", nil, true)
	requireStatus(t, active, http.StatusOK)
	requireContains(t, active.Body.String(), "release-a", "release-b", "release-c")
	requireAbsent(t, active.Body.String(), "release-d", "release-e", "release-f")

	for _, filter := range []struct {
		view string
		name string
	}{
		{"completed", "release-d"}, {"failed", "release-e"}, {"cancelled", "release-f"},
	} {
		response := fixture.request(http.MethodGet, "/?view="+filter.view, nil, true)
		requireStatus(t, response, http.StatusOK)
		requireContains(t, response.Body.String(), filter.name)
	}

	empty := fixture.request(http.MethodGet, "/?view=completed&category=other", nil, true)
	requireStatus(t, empty, http.StatusOK)
	requireContains(t, empty.Body.String(), "No downloads match this filter.")

	fixture.cloud.err = errors.New("private upstream detail")
	unavailable := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, unavailable, http.StatusOK)
	requireContains(t, unavailable.Body.String(), "CloudDrive2 Issue", `<span aria-hidden="true">Issue</span>`)
	requireAbsent(t, unavailable.Body.String(), "private upstream detail")
	if fixture.cloud.calls != 7 {
		t.Errorf("cloud status calls = %d, want one for each dashboard request", fixture.cloud.calls)
	}
}

func TestDashboardSearchPaginationAndInlineReturn(t *testing.T) {
	fixture := newWebFixture(t)
	var last domain.Download
	for index := 1; index <= 30; index++ {
		hash := fmt.Sprintf("%040x", index)
		last = fixture.seedDownload(hash, domain.StateStopped, func(download *domain.Download) {
			download.Name = fmt.Sprintf("alpha-release-%02d-with-a-long-title", index)
		})
	}

	first := fixture.request(http.MethodGet, "/?q=alpha", nil, true)
	requireStatus(t, first, http.StatusOK)
	firstBody := first.Body.String()
	if count := strings.Count(firstBody, `class="task-title"`); count != downloadPageSize {
		t.Fatalf("first page rows = %d, want %d", count, downloadPageSize)
	}
	requireContains(t, firstBody, "30 shown", "1–25 of 30", `title="alpha-release-01-with-a-long-title"`, `href="/?page=2&amp;q=alpha"`)

	second := fixture.request(http.MethodGet, "/?q=alpha&page=2", nil, true)
	requireStatus(t, second, http.StatusOK)
	if count := strings.Count(second.Body.String(), `class="task-title"`); count != 5 {
		t.Fatalf("second page rows = %d, want 5", count)
	}
	requireContains(t, second.Body.String(), "26–30 of 30")

	byHash := fixture.request(http.MethodGet, "/?q="+last.Hash, nil, true)
	requireStatus(t, byHash, http.StatusOK)
	if count := strings.Count(byHash.Body.String(), `class="task-title"`); count != 1 {
		t.Fatalf("hash search rows = %d, want 1", count)
	}

	resumed := fixture.post("/downloads/"+last.Hash+"/start", url.Values{"return_to": {"/?q=alpha&page=2"}})
	requireStatus(t, resumed, http.StatusSeeOther)
	if location := resumed.Header().Get("Location"); location != "/?page=2&q=alpha" {
		t.Errorf("inline action location = %q", location)
	}
}

func TestDetailSegmentedTrackSelection(t *testing.T) {
	fixture := newWebFixture(t)
	states := []struct {
		seed       string
		state      domain.State
		hasCurrent bool
		passed     int
		unselected int
	}{
		{"1", domain.StateAccepted, true, 0, 0}, {"2", domain.StateSubmittingOffline, true, 0, 0}, {"3", domain.StateWaitingOffline, true, 0, 0},
		{"4", domain.StateSubmittingCopy, true, 1, 0}, {"5", domain.StateWaitingCopy, true, 1, 0}, {"6", domain.StateVerifyingLocal, true, 2, 0},
		{"7", domain.StateCompleted, false, 3, 0}, {"8", domain.StateFailed, false, 0, 3}, {"9", domain.StateCancelRequested, false, 0, 3},
		{"a", domain.StateCancelled, false, 0, 3}, {"b", domain.StateStopped, false, 0, 3},
	}
	for _, item := range states {
		download := fixture.seedDownload(item.seed, item.state, nil)
		response := fixture.request(http.MethodGet, "/downloads/"+download.Hash, nil, true)
		requireStatus(t, response, http.StatusOK)
		body := response.Body.String()
		requireContains(t, body, "115 OFFLINE", "NAS COPY", "LOCAL VERIFY", string(item.state))
		requireAbsent(t, body, "stage-fill", "is-verified", "is-halted")
		wantCurrent := 0
		if item.hasCurrent {
			wantCurrent = 1
		}
		if current := strings.Count(body, `class="stage is-current"`); current != wantCurrent {
			t.Errorf("state %s current segment count = %d", item.state, current)
		}
		if passed := strings.Count(body, `class="stage is-passed"`); passed != item.passed {
			t.Errorf("state %s passed segment count = %d, want %d", item.state, passed, item.passed)
		}
		if unselected := strings.Count(body, `class="stage is-unselected"`); unselected != item.unselected {
			t.Errorf("state %s unselected segment count = %d, want %d", item.state, unselected, item.unselected)
		}
	}
}

func TestDetailIsRedactedAndExposesOnlyLegalActions(t *testing.T) {
	fixture := newWebFixture(t)
	cases := []struct {
		seed    string
		state   domain.State
		present []string
		absent  []string
	}{
		{"a", domain.StateStopped, []string{"/start", "delete_files\" value=\"false", "delete_files\" value=\"true"}, []string{"/retry", "/pause"}},
		{"b", domain.StateFailed, []string{"/retry", "delete_files\" value=\"false"}, []string{"/start", "/pause"}},
		{"c", domain.StateWaitingCopy, []string{"/pause", "delete_files\" value=\"true"}, []string{"/start", "/retry", "/cancel"}},
		{"d", domain.StateCompleted, []string{"delete_files\" value=\"false", `class="stage is-passed"`}, []string{"/start", "/retry", "/pause", `class="stage is-current"`}},
	}
	for _, item := range cases {
		download := fixture.seedDownload(item.seed, item.state, func(download *domain.Download) {
			download.LastError = "source=" + download.SubmissionURI
		})
		response := fixture.request(http.MethodGet, "/downloads/"+download.Hash, nil, true)
		requireStatus(t, response, http.StatusOK)
		body := response.Body.String()
		requireContains(t, body, download.Hash, download.CloudFolder, download.SavePath, "disc/video.mkv", "disc/subtitle.srt", "Protected upstream details were redacted.", "data-confirm=")
		requireContains(t, body, item.present...)
		requireAbsent(t, body, item.absent...)
		requireAbsent(t, body, download.SubmissionURI, "tracker.invalid", "secret-token", fixture.sid)
	}
}

func TestActionsUseRealRepositoryAndWake(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		fixture := newWebFixture(t)
		download := fixture.seedDownload("a", domain.StateStopped, nil)
		response := fixture.post("/downloads/"+download.Hash+"/start", nil)
		requireStatus(t, response, http.StatusSeeOther)
		stored, _ := fixture.store.GetDownload(context.Background(), download.Hash)
		if stored.State != domain.StateAccepted || fixture.waker.count != 1 {
			t.Errorf("start state/wake = %s/%d", stored.State, fixture.waker.count)
		}
	})

	t.Run("pause", func(t *testing.T) {
		fixture := newWebFixture(t)
		download := fixture.seedDownload("b", domain.StateWaitingOffline, nil)
		response := fixture.post("/downloads/"+download.Hash+"/pause", nil)
		requireStatus(t, response, http.StatusSeeOther)
		stored, _ := fixture.store.GetDownload(context.Background(), download.Hash)
		if stored.State != domain.StateCancelRequested || !stored.PauseRequested || fixture.waker.count != 1 {
			t.Errorf("pause state/intent/wake = %s/%t/%d", stored.State, stored.PauseRequested, fixture.waker.count)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		fixture := newWebFixture(t)
		download := fixture.seedDownload("b", domain.StateWaitingOffline, nil)
		response := fixture.post("/downloads/"+download.Hash+"/cancel", nil)
		requireStatus(t, response, http.StatusSeeOther)
		stored, _ := fixture.store.GetDownload(context.Background(), download.Hash)
		if stored.State != domain.StateCancelRequested || fixture.waker.count != 1 {
			t.Errorf("cancel state/wake = %s/%d", stored.State, fixture.waker.count)
		}
	})

	retryCases := []struct {
		name      string
		seed      string
		configure func(*domain.Download)
		want      domain.State
	}{
		{"content", "c", func(download *domain.Download) {
			download.CloudSourcePath = "/cloud/c"
			download.ContentPath = filepath.Join(t.TempDir(), "content-c")
		}, domain.StateVerifyingLocal},
		{"copy completed", "d", func(download *domain.Download) { download.LastUpstreamStatus = domain.UpstreamCopyCompleted }, domain.StateVerifyingLocal},
		{"copy pending", "e", func(download *domain.Download) { download.LastUpstreamStatus = domain.UpstreamCopyPending }, domain.StateWaitingCopy},
		{"copy failed", "f", func(download *domain.Download) { download.LastUpstreamStatus = domain.UpstreamCopyFailed }, domain.StateSubmittingCopy},
		{"copy cleanup", "0", func(download *domain.Download) {
			download.LastUpstreamStatus = domain.UpstreamCleanupCancelled + "|" + domain.UpstreamCopyFailed
		}, domain.StateSubmittingCopy},
		{"offline finished", "1", func(download *domain.Download) { download.LastUpstreamStatus = domain.UpstreamOfflineFinished }, domain.StateSubmittingCopy},
		{"offline downloading", "2", func(download *domain.Download) { download.LastUpstreamStatus = domain.UpstreamOfflineDownloading }, domain.StateWaitingOffline},
		{"cloud source", "3", func(download *domain.Download) { download.CloudSourcePath = "/cloud/d" }, domain.StateSubmittingCopy},
		{"no evidence", "4", nil, domain.StateSubmittingOffline},
	}
	for _, item := range retryCases {
		t.Run("retry "+item.name, func(t *testing.T) {
			fixture := newWebFixture(t)
			download := fixture.seedDownload(item.seed, domain.StateFailed, item.configure)
			response := fixture.post("/downloads/"+download.Hash+"/retry", nil)
			requireStatus(t, response, http.StatusSeeOther)
			if len(fixture.repo.retryTargets) != 1 || fixture.repo.retryTargets[0] != item.want || fixture.waker.count != 1 {
				t.Errorf("retry target/wake = %v/%d, want %s/1", fixture.repo.retryTargets, fixture.waker.count, item.want)
			}
		})
	}

	for _, deleteFiles := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove files=%t", deleteFiles), func(t *testing.T) {
			fixture := newWebFixture(t)
			download := fixture.seedDownload("a", domain.StateCompleted, nil)
			response := fixture.post("/downloads/"+download.Hash+"/remove", url.Values{"delete_files": {fmt.Sprint(deleteFiles)}})
			requireStatus(t, response, http.StatusSeeOther)
			if response.Header().Get("Location") != "/" || len(fixture.repo.deleteFlags) != 1 || fixture.repo.deleteFlags[0] != deleteFiles || fixture.waker.count != 1 {
				t.Errorf("remove result location=%q flags=%v wake=%d", response.Header().Get("Location"), fixture.repo.deleteFlags, fixture.waker.count)
			}
			stored, err := fixture.store.GetDownload(context.Background(), download.Hash)
			if err != nil || stored.State != domain.StateDeleteRequested || stored.DeleteFilesRequested != deleteFiles {
				t.Errorf("removed record = (%+v, %v)", stored, err)
			}
		})
	}
}

func TestProblemWarningVsFailurePresentation(t *testing.T) {
	fixture := newWebFixture(t)
	nextRun := fixture.clock.now.Add(4 * time.Minute)
	retrying := fixture.seedProblem("1", []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy}, domain.ProblemCloudCopyNotReady, &nextRun)
	failed := fixture.seedDownload("2", domain.StateFailed, func(download *domain.Download) {
		download.LastErrorCode = string(domain.ProblemCloudCopyNotReadyTimeout)
		download.LastError = domain.ProblemText(domain.ProblemCloudCopyNotReadyTimeout)
	})

	// The retrying row is a warning: it states the automatic retry, the
	// scheduled next run, and the persistent-failure action, and never
	// renders internal operation terms.
	list := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, list, http.StatusOK)
	listBody := list.Body.String()
	requireContains(t, listBody, "The 115 offline download finished, but CloudDrive2 has not accepted the copy yet.", "If this persists, refresh the 115 mount and verify the cloud category and NAS staging paths.", "CD211 will retry automatically. Next retry", nextRun.UTC().Format(time.RFC3339))
	requireAbsent(t, listBody, "find_file", "add_copy", "permanent", "invalid_response", "cloud_copy_not_ready")

	detail := fixture.request(http.MethodGet, "/downloads/"+retrying.Hash, nil, true)
	requireStatus(t, detail, http.StatusOK)
	detailBody := detail.Body.String()
	requireContains(t, detailBody, "notice-warning", "Retrying automatically", "If this persists, refresh the 115 mount", "CD211 will retry automatically. Next retry")
	// A retrying active download has no Retry CTA.
	requireAbsent(t, detailBody, ">Retry</button>")

	// A terminal failure is an error with the corrective action and Retry.
	failedDetail := fixture.request(http.MethodGet, "/downloads/"+failed.Hash, nil, true)
	requireStatus(t, failedDetail, http.StatusOK)
	failedBody := failedDetail.Body.String()
	requireContains(t, failedBody, "notice-error", ">Retry</button>", "Refresh the 115 mount and verify the cloud category and NAS staging paths")
	requireAbsent(t, failedBody, "notice-warning", "cloud_copy_not_ready_timeout", "find_file", "add_copy", "permanent", "invalid_response")
}

func TestProblemLocalizationChinese(t *testing.T) {
	fixture := newWebFixture(t)
	nextRun := fixture.clock.now.Add(2 * time.Minute)
	retrying := fixture.seedProblem("3", []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy}, domain.ProblemCloudCopyNotReady, &nextRun)

	request := httptest.NewRequest(http.MethodGet, "/downloads/"+retrying.Hash, nil)
	request.AddCookie(&http.Cookie{Name: "SID", Value: fixture.sid})
	request.AddCookie(&http.Cookie{Name: langCookie, Value: string(LangZH)})
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	requireStatus(t, response, http.StatusOK)
	body := response.Body.String()
	requireContains(t, body, "自动重试中", "115 离线下载已完成，但 CloudDrive2 尚未接受复制任务", "刷新 115 挂载", "CD211 会自动重试。下次重试时间：")
	requireAbsent(t, body, "find_file", "add_copy", "permanent", "invalid_response", "cloud_copy_not_ready")
}

func TestLegacyProblemRendersStoredText(t *testing.T) {
	fixture := newWebFixture(t)
	download := fixture.seedDownload("4", domain.StateFailed, func(download *domain.Download) {
		download.LastErrorCode = string(domain.ProblemLegacy)
		download.LastError = "local deletion failed"
	})
	detail := fixture.request(http.MethodGet, "/downloads/"+download.Hash, nil, true)
	requireStatus(t, detail, http.StatusOK)
	body := detail.Body.String()
	requireContains(t, body, "notice-error", "local deletion failed")
	requireAbsent(t, body, "notice-warning", "cloud_copy_not_ready")
}

func TestCleanupFailurePresentationIsTerminalError(t *testing.T) {
	fixture := newWebFixture(t)
	// A structured cleanup failure retains CANCEL_REQUESTED with the problem
	// and scheduled retry bookkeeping; it must render as a blocked error with
	// the Retry control, never as an automatic-retry warning.
	nextRun := fixture.clock.now.Add(2 * time.Minute)
	blocked := fixture.seedProblem("c", []domain.State{domain.StateCancelRequested}, domain.ProblemCloudRequestRejected, &nextRun)

	detail := fixture.request(http.MethodGet, "/downloads/"+blocked.Hash, nil, true)
	requireStatus(t, detail, http.StatusOK)
	body := detail.Body.String()
	requireContains(t, body, "notice-error", "CloudDrive2 rejected the request. Check the configuration, then Retry.", ">Retry</button>")
	requireAbsent(t, body, "notice-warning", "Retrying automatically", "will retry automatically", "CD211 会自动重试")

	list := fixture.request(http.MethodGet, "/?view=failed", nil, true)
	requireStatus(t, list, http.StatusOK)
	requireContains(t, list.Body.String(), "CloudDrive2 rejected the request. Check the configuration, then Retry.")
	requireAbsent(t, list.Body.String(), "will retry automatically")
}

func TestFailedCleanupRemainsVisibleAndRetryable(t *testing.T) {
	fixture := newWebFixture(t)
	download := fixture.seedDownload("8", domain.StateDeleteRequested, nil)
	claim, err := fixture.store.ClaimDue(context.Background(), "cleanup", fixture.clock.now, time.Minute)
	if err != nil || claim == nil || claim.Download.Hash != download.Hash {
		t.Fatalf("ClaimDue(cleanup) = (%+v, %v)", claim, err)
	}
	failed := claim.Download
	failed.LastError = "local deletion failed"
	failed.NextRunAt = nil
	failed.UpdatedAt = fixture.clock.now.Add(time.Second)
	if err := fixture.store.CommitClaim(context.Background(), *claim, failed); err != nil {
		t.Fatal(err)
	}

	list := fixture.request(http.MethodGet, "/?view=failed", nil, true)
	requireStatus(t, list, http.StatusOK)
	requireContains(t, list.Body.String(), download.Hash[:8], "local deletion failed")
	detail := fixture.request(http.MethodGet, "/downloads/"+download.Hash, nil, true)
	requireStatus(t, detail, http.StatusOK)
	requireContains(t, detail.Body.String(), ">Retry</button>", "local deletion failed")
	retry := fixture.post("/downloads/"+download.Hash+"/retry", nil)
	requireStatus(t, retry, http.StatusSeeOther)
	retried, err := fixture.store.GetDownload(context.Background(), download.Hash)
	if err != nil || retried.State != domain.StateDeleteRequested || retried.LastError != "" || retried.NextRunAt == nil {
		t.Fatalf("retried cleanup = (%+v, %v)", retried, err)
	}
}

func TestCategoryCreateUpdateDisableAndContainment(t *testing.T) {
	fixture := newWebFixture(t)
	page := fixture.request(http.MethodGet, "/categories", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), "115 category subfolder", "Shared staging subfolder", fixture.localRoot)
	onboarding := fixture.request(http.MethodGet, "/categories?onboarding=1", nil, true)
	requireStatus(t, onboarding, http.StatusOK)
	requireContains(t, onboarding.Body.String(), "Last step: configure a Sonarr or Radarr category")

	cloudSubpath := "TV Shows"
	configuredLocalRoot := fixture.localRoot
	canonicalLocalRoot, err := filepath.EvalSymlinks(configuredLocalRoot)
	if err != nil {
		t.Fatal(err)
	}
	create := fixture.post("/categories/save", url.Values{
		"name": {"  TV Shows  "}, "cloud_subpath": {cloudSubpath}, "save_subpath": {"TV Shows"}, "enabled": {"1"},
	})
	requireStatus(t, create, http.StatusSeeOther)
	categories, err := fixture.store.ListCategories(context.Background())
	if err != nil || len(categories) != 1 || categories[0].Name != "tv shows" ||
		categories[0].CloudPath != "/cloud/TV Shows" ||
		categories[0].SavePath != filepath.Join(canonicalLocalRoot, "TV Shows") || !categories[0].Enabled {
		t.Fatalf("categories after create = (%+v, %v)", categories, err)
	}
	createdAt := categories[0].CreatedAt
	savedPage := fixture.request(http.MethodGet, "/categories", nil, true)
	requireStatus(t, savedPage, http.StatusOK)
	requireContains(t, savedPage.Body.String(), `name="cloud_subpath" type="text" value="TV Shows"`,
		`name="save_subpath" type="text" value="TV Shows"`, "/cloud/TV Shows")

	fixture.clock.now = fixture.clock.now.Add(30 * time.Minute)
	updatedCloud := "/cloud/tv-updated"
	update := fixture.post("/categories/save", url.Values{
		"name": {"tv shows"}, "cloud_subpath": {"tv-updated"}, "save_subpath": {"tv-updated"}, "enabled": {"false"},
	})
	requireStatus(t, update, http.StatusSeeOther)
	categories, err = fixture.store.ListCategories(context.Background())
	if err != nil || len(categories) != 1 || categories[0].Enabled || categories[0].CloudPath != updatedCloud || categories[0].SavePath != filepath.Join(canonicalLocalRoot, "tv-updated") || !categories[0].CreatedAt.Equal(createdAt) || !categories[0].UpdatedAt.Equal(fixture.clock.now) {
		t.Fatalf("categories after update = (%+v, %v)", categories, err)
	}

	invalidPaths := []url.Values{
		{"name": {"root-cloud"}, "cloud_subpath": {""}, "save_subpath": {"ok"}, "enabled": {"true"}},
		{"name": {"escape-cloud"}, "cloud_subpath": {"../escape"}, "save_subpath": {"ok"}, "enabled": {"true"}},
		{"name": {"root-local"}, "cloud_subpath": {"ok"}, "save_subpath": {"."}, "enabled": {"true"}},
		{"name": {"escape-local"}, "cloud_subpath": {"ok"}, "save_subpath": {"../escape"}, "enabled": {"true"}},
	}
	for _, form := range invalidPaths {
		response := fixture.post("/categories/save", form)
		requireStatus(t, response, http.StatusBadRequest)
	}
	categories, _ = fixture.store.ListCategories(context.Background())
	if len(categories) != 1 {
		t.Errorf("invalid paths mutated category registry: %+v", categories)
	}
}

func TestSettingsRootChangeRemapsCategoriesButFreezesExistingDownloads(t *testing.T) {
	fixture := newWebFixture(t)
	dial := &fakeDial{}
	handler, err := New(
		Config{CloudRoot: "/cloud", LocalRoot: fixture.localRoot},
		fixture.creds, fixture.repo, fixture.sessions, fixture.clock, fixture.waker, fixture.cloud, fixture.filesystem,
		SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store, Dial: dial.dial}, fixture.webhooks,
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	fixture.handler = handler

	fixture.seedCategory("movies", true)
	oldSavePath := filepath.Join(fixture.localRoot, "movies")
	download := fixture.seedDownload("a", domain.StateAccepted, func(download *domain.Download) {
		download.Category = "movies"
		download.CloudFolder = "/cloud/movies"
		download.SavePath = oldSavePath
	})
	newLocalRoot := t.TempDir()
	response := fixture.post("/settings/save", url.Values{
		"address": {"cd2.example:443"}, "username": {"operator"}, "password": {"cd2-secret"},
		"cloud_root": {"/new-cloud"}, "local_root": {newLocalRoot},
		"timeout_offline": {"24h"}, "timeout_copy": {"72h"}, "timeout_verify": {"10m"},
	})
	requireStatus(t, response, http.StatusSeeOther)

	category, err := fixture.store.GetCategory(context.Background(), "movies")
	if err != nil {
		t.Fatal(err)
	}
	canonicalNewRoot, err := filepath.EvalSymlinks(newLocalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if category.CloudPath != "/new-cloud/movies" || category.SavePath != filepath.Join(canonicalNewRoot, "movies") {
		t.Errorf("remapped category = %+v", category)
	}
	if info, err := os.Stat(category.SavePath); err != nil || !info.IsDir() {
		t.Errorf("remapped staging directory = (%v, %v), want directory", info, err)
	}
	storedDownload, err := fixture.store.GetDownload(context.Background(), download.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if storedDownload.CloudFolder != "/cloud/movies" || storedDownload.SavePath != oldSavePath {
		t.Errorf("existing download paths changed = %+v", storedDownload)
	}
}

func TestCategoryConflictDoesNotCreateReservedDestination(t *testing.T) {
	fixture := newWebFixture(t)
	destinationPath := filepath.Join(fixture.localRoot, "blocked")
	fixture.seedDownload("e", domain.StateAccepted, func(download *domain.Download) {
		download.Name = "blocked"
		download.Category = ""
		download.CloudFolder = "/cloud"
		download.SavePath = fixture.localRoot
		download.DestinationName = "blocked"
	})

	response := fixture.post("/categories/save", url.Values{
		"name": {"blocked"}, "cloud_subpath": {"blocked"}, "save_subpath": {"blocked"}, "enabled": {"true"},
	})
	requireStatus(t, response, http.StatusConflict)
	if _, err := os.Lstat(destinationPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("conflicting category created destination path: %v", err)
	}
	if _, err := fixture.store.GetCategory(context.Background(), "blocked"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("conflicting category persisted: %v", err)
	}
}

func TestInvalidHashTransitionRepositoryErrorsMethodsAndUnknown(t *testing.T) {
	fixture := newWebFixture(t)
	completed := fixture.seedDownload("a", domain.StateCompleted, nil)

	for _, target := range []string{"/downloads/short", "/downloads/" + strings.Repeat("z", 40)} {
		response := fixture.request(http.MethodGet, target, nil, true)
		requireStatus(t, response, http.StatusBadRequest)
	}
	unknownHash := strings.Repeat("b", 40)
	requireStatus(t, fixture.request(http.MethodGet, "/downloads/"+unknownHash, nil, true), http.StatusNotFound)
	hidden := fixture.seedDownload("c", domain.StateDeleteRequested, nil)
	requireStatus(t, fixture.request(http.MethodGet, "/downloads/"+hidden.Hash, nil, true), http.StatusNotFound)
	requireStatus(t, fixture.post("/downloads/"+completed.Hash+"/start", nil), http.StatusConflict)
	if fixture.waker.count != 0 {
		t.Errorf("invalid transition woke worker %d times", fixture.waker.count)
	}

	fixture.repo.actionError = errors.New("database private failure")
	failedAction := fixture.post("/downloads/"+completed.Hash+"/remove", url.Values{"delete_files": {"false"}})
	requireStatus(t, failedAction, http.StatusInternalServerError)
	requireAbsent(t, failedAction.Body.String(), "database private failure")
	if fixture.waker.count != 0 {
		t.Errorf("failed action woke worker %d times", fixture.waker.count)
	}

	wrongMethod := fixture.request(http.MethodPost, "/", url.Values{}, false)
	requireStatus(t, wrongMethod, http.StatusMethodNotAllowed)
	unknown := fixture.request(http.MethodGet, "/not-a-route", nil, false)
	requireStatus(t, unknown, http.StatusNotFound)
	if wrongMethod.Header().Get("Location") != "" || unknown.Header().Get("Location") != "" {
		t.Error("method/unknown handling ran authentication first")
	}
	invalidFilter := fixture.request(http.MethodGet, "/?view=pending", nil, true)
	requireStatus(t, invalidFilter, http.StatusBadRequest)
}

func TestCloudStatusTimeoutIsBoundedAndPrivate(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.cloud.waitForDone = true
	started := time.Now()
	response := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, response, http.StatusOK)
	elapsed := time.Since(started)
	if elapsed < 450*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("cloud status timeout elapsed = %s, want bounded near 500ms", elapsed)
	}
	requireContains(t, response.Body.String(), "CloudDrive2 Issue")
	requireAbsent(t, response.Body.String(), context.DeadlineExceeded.Error())
}

func TestConstructorValidation(t *testing.T) {
	fixture := newWebFixture(t)
	config := Config{CloudRoot: "/cloud", LocalRoot: t.TempDir()}
	cases := []struct {
		name     string
		config   Config
		repo     Repository
		clock    Clock
		waker    Waker
		cloud    CloudStatus
		settings SettingsDeps
		webhooks outbox.EndpointRepository
	}{
		{"cloud root", Config{CloudRoot: "relative", LocalRoot: t.TempDir()}, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"local root", Config{CloudRoot: "/cloud", LocalRoot: "relative"}, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"repository", config, nil, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"clock", config, fixture.repo, nil, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"waker", config, fixture.repo, fixture.clock, nil, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"cloud status", config, fixture.repo, fixture.clock, fixture.waker, nil, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks},
		{"settings store", config, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{}, fixture.webhooks},
		{"token store", config, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store}, fixture.webhooks},
		{"qbt key store", config, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store}, fixture.webhooks},
		{"webhook store", config, fixture.repo, fixture.clock, fixture.waker, fixture.cloud, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, nil},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if handler, err := New(item.config, fixture.creds, item.repo, fixture.sessions, item.clock, item.waker, item.cloud, fixture.filesystem, item.settings, item.webhooks); err == nil || handler != nil {
				t.Errorf("New() = (%v, %v), want validation error", handler, err)
			}
		})
	}
	if handler, err := New(config, fixture.creds, fixture.repo, fixture.sessions, fixture.clock, fixture.waker, fixture.cloud, nil, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks); err == nil || handler != nil {
		t.Errorf("New(nil filesystem) = (%v, %v), want validation error", handler, err)
	}
	if handler, err := New(config, nil, fixture.repo, fixture.sessions, fixture.clock, fixture.waker, fixture.cloud, fixture.filesystem, SettingsDeps{Store: fixture.store, Tokens: fixture.store, QBTKeys: fixture.store}, fixture.webhooks); err == nil || handler != nil {
		t.Errorf("New(nil credentials) = (%v, %v), want validation error", handler, err)
	}
	if handler, err := New(config, fixture.creds, fixture.repo, fixture.sessions, fixture.clock, fixture.waker, fixture.cloud, fixture.filesystem, SettingsDeps{Store: fixture.store, Tokens: fixture.store}, fixture.webhooks); err == nil || handler != nil {
		t.Errorf("New(nil qbt key store) = (%v, %v), want validation error", handler, err)
	}
}

func TestLoginBodyLimitAndExactFields(t *testing.T) {
	fixture := newWebFixture(t)
	oversized := url.Values{"username": {strings.Repeat("a", int(formLimit))}, "password": {"x"}}
	response := fixture.request(http.MethodPost, "/login", oversized, false)
	requireStatus(t, response, http.StatusBadRequest)

	encoded := "username=operator&username=other&password=correct+horse"
	request := httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(encoded))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	fixture.handler.ServeHTTP(result, request)
	requireStatus(t, result, http.StatusUnauthorized)
}

func (fixture *webFixture) requestLang(method, target string, authenticated bool, lang string) *httptest.ResponseRecorder {
	fixture.t.Helper()
	request := httptest.NewRequest(method, target, nil)
	if authenticated {
		request.AddCookie(&http.Cookie{Name: "SID", Value: fixture.sid})
	}
	if lang != "" {
		request.AddCookie(&http.Cookie{Name: langCookie, Value: lang})
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func TestLanguagePreferenceRendersChinese(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedCategory("movies", true)
	fixture.seedDownload("a", domain.StateWaitingOffline, func(download *domain.Download) {
		started := fixture.clock.now.Add(-9 * 24 * time.Hour)
		download.OfflineStartedAt = &started
	})

	login := fixture.requestLang(http.MethodGet, "/login", false, "zh")
	requireStatus(t, login, http.StatusOK)
	requireContains(t, login.Body.String(), `lang="zh"`, "用户名", "密码", "初始设置")

	downloads := fixture.requestLang(http.MethodGet, "/?view=all", true, "zh")
	requireStatus(t, downloads, http.StatusOK)
	requireContains(t, downloads.Body.String(), "115 离线下载 · 35%", "CloudDrive2 在线", "下载任务", "分类管理", "总共耗时", "9天", `data-state="WAITING_OFFLINE" aria-label="115 离线下载 · 35% · 等待离线下载" title="115 离线下载 · 35% · 等待离线下载">115 离线下载 · 35%</span>`)
	englishDownloads := fixture.requestLang(http.MethodGet, "/?view=all", true, "en")
	requireStatus(t, englishDownloads, http.StatusOK)
	requireContains(t, englishDownloads.Body.String(), `data-state="WAITING_OFFLINE" aria-label="115 OFFLINE · 35% · Waiting for offline download" title="115 OFFLINE · 35% · Waiting for offline download">115 OFFLINE · 35%</span>`)

	detail := fixture.requestLang(http.MethodGet, "/downloads/"+strings.Repeat("a", 40), true, "zh")
	requireStatus(t, detail, http.StatusOK)
	requireContains(t, detail.Body.String(), `data-state="WAITING_OFFLINE">等待离线下载</span>`)

	fallback := fixture.requestLang(http.MethodGet, "/login", false, "de")
	requireStatus(t, fallback, http.StatusOK)
	requireContains(t, fallback.Body.String(), `lang="en"`, "Username")
}

func TestSetLangCookieAndRedirectSanitization(t *testing.T) {
	fixture := newWebFixture(t)
	cases := []struct {
		target       string
		wantLang     string
		wantLocation string
	}{
		{"/lang?to=zh&back=/categories", "zh", "/categories"},
		{"/lang?to=en&back=/downloads/abc", "en", "/downloads/abc"},
		{"/lang?to=zh&back=//evil.example", "zh", "/"},
		{"/lang?to=zh&back=https://evil.example", "zh", "/"},
		{"/lang?to=unknown", "en", "/"},
	}
	for _, item := range cases {
		response := fixture.requestLang(http.MethodGet, item.target, false, "")
		requireStatus(t, response, http.StatusSeeOther)
		if location := response.Header().Get("Location"); location != item.wantLocation {
			t.Errorf("%s Location = %q, want %q", item.target, location, item.wantLocation)
		}
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != langCookie || cookies[0].Value != item.wantLang || cookies[0].Path != "/" {
			t.Errorf("%s cookies = %+v, want %s=%s", item.target, cookies, langCookie, item.wantLang)
		}
	}
}

func TestPasswordChangeFlow(t *testing.T) {
	fixture := newWebFixture(t)

	page := fixture.request(http.MethodGet, "/password", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/password"`, "current_password", "new_password", "confirm_password")

	wrongCurrent := fixture.post("/password", url.Values{
		"current_password": {"not-the-password"}, "new_password": {"horse staple 9"}, "confirm_password": {"horse staple 9"},
	})
	requireStatus(t, wrongCurrent, http.StatusBadRequest)
	requireContains(t, wrongCurrent.Body.String(), "The current password is incorrect.")

	tooShort := fixture.post("/password", url.Values{
		"current_password": {"adminadmin"}, "new_password": {"short"}, "confirm_password": {"short"},
	})
	requireStatus(t, tooShort, http.StatusBadRequest)
	requireContains(t, tooShort.Body.String(), "at least 8 characters")

	mismatch := fixture.post("/password", url.Values{
		"current_password": {"adminadmin"}, "new_password": {"horse staple 9"}, "confirm_password": {"horse staple 8"},
	})
	requireStatus(t, mismatch, http.StatusBadRequest)
	requireContains(t, mismatch.Body.String(), "do not match")

	changed := fixture.post("/password", url.Values{
		"current_password": {"adminadmin"}, "new_password": {"horse staple 9"}, "confirm_password": {"horse staple 9"},
	})
	requireStatus(t, changed, http.StatusOK)
	requireContains(t, changed.Body.String(), "Password changed.")
	requireAbsent(t, changed.Body.String(), "horse staple 9")

	oldLogin := fixture.request(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"adminadmin"}}, false)
	requireStatus(t, oldLogin, http.StatusUnauthorized)

	newLogin := fixture.request(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"horse staple 9"}}, false)
	requireStatus(t, newLogin, http.StatusSeeOther)

	// The persisted hash survives a fresh manager over the same store.
	fresh, err := creds.New(fixture.store)
	if err != nil {
		t.Fatalf("creds.New(): %v", err)
	}
	ok, err := fresh.Verify(context.Background(), "admin", "horse staple 9")
	if err != nil || !ok {
		t.Fatalf("Verify(new) = (%t, %v), want (true, nil)", ok, err)
	}
}
