package httpapi

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
)

type contractClock struct{ now time.Time }

func (c contractClock) Now() time.Time { return c.now }

type contractWaker struct{ wakes int }

func (w *contractWaker) Wake() { w.wakes++ }

type contractFilesystem struct {
	root         string
	prepareCalls int
	content      string
	size         int64
	prepareErr   error
	err          error
}

func (f *contractFilesystem) Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	return fsafe.VerifiedContent{Path: f.content, Size: f.size}, f.err
}
func (f *contractFilesystem) ResolveSaveRoot(savePath string) (string, bool, error) {
	relative, err := filepath.Rel(f.root, savePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false, fs.ErrInvalid
	}
	return savePath, false, nil
}
func (f *contractFilesystem) PrepareSaveRoot(savePath string) (string, error) {
	f.prepareCalls++
	if f.prepareErr != nil {
		return "", f.prepareErr
	}
	resolved, _, err := f.ResolveSaveRoot(savePath)
	return resolved, err
}

func TestRealStoreSubmissionChain(t *testing.T) {
	t.Parallel()
	clock := contractClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	repository, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	sessions, err := session.New(clock, bytes.NewReader(bytes.Repeat([]byte{1}, 128)), time.Hour, 4)
	if err != nil {
		t.Fatal(err)
	}
	waker := &contractWaker{}
	api, err := New(Config{
		CloudRoot: "/cloud", LocalRoot: "/local",
		TorrentLimits:   torrentmeta.Limits{MaxInputBytes: 1 << 20, MaxInfoBytes: 1 << 18, MaxFiles: 16, MaxNameBytes: 255, MaxPathBytes: 1024, MaxComponentBytes: 255, MaxTrackerCount: 16, MaxTrackerBytes: 1024, MaxTotalSize: 1 << 30},
		MaxRequestBytes: (1 << 20) + (64 << 10),
	}, stubCredentials{username: "user", password: "password"}, repository, sessions, clock, waker, &contractFilesystem{root: "/local", err: fs.ErrNotExist})
	if err != nil {
		t.Fatal(err)
	}

	login := doForm(t, api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"password"}}, nil)
	if login.Code != http.StatusOK || login.Body.String() != "Ok." {
		t.Fatalf("login = %d %q", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "SID" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	cookie := cookies[0]

	if response := doRequest(t, api, http.MethodGet, "/api/v2/app/webapiVersion", nil, cookie); response.Code != http.StatusOK || response.Body.String() != "2.11.0" {
		t.Fatalf("webapiVersion = %d %q", response.Code, response.Body.String())
	}
	created := doForm(t, api, http.MethodPost, "/api/v2/torrents/createCategory", url.Values{"category": {"TV"}}, cookie)
	if created.Code != http.StatusOK || created.Body.Len() != 0 {
		t.Fatalf("create category = %d %q", created.Code, created.Body.String())
	}

	hash := "0123456789abcdef0123456789abcdef01234567"
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Example"
	added := doForm(t, api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {magnet}, "category": {"tv"}}, cookie)
	if added.Code != http.StatusOK || added.Body.Len() != 0 || waker.wakes != 1 {
		t.Fatalf("add = %d %q wakes=%d", added.Code, added.Body.String(), waker.wakes)
	}
	properties := doRequest(t, api, http.MethodGet, "/api/v2/torrents/properties?hash="+hash, nil, cookie)
	if properties.Code != http.StatusOK || !strings.Contains(properties.Body.String(), `"hash":"`+hash+`"`) {
		t.Fatalf("properties = %d %q", properties.Code, properties.Body.String())
	}
	info := doRequest(t, api, http.MethodGet, "/api/v2/torrents/info?category=tv", nil, cookie)
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"state":"queuedDL"`) {
		t.Fatalf("info = %d %q", info.Code, info.Body.String())
	}
	changed := doForm(t, api, http.MethodPost, "/api/v2/torrents/setCategory", url.Values{"hashes": {hash}, "category": {""}}, cookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("setCategory = %d %q", changed.Code, changed.Body.String())
	}
	deleted := doForm(t, api, http.MethodPost, "/api/v2/torrents/delete", url.Values{"hashes": {hash}, "deleteFiles": {"false"}}, cookie)
	if deleted.Code != http.StatusOK || waker.wakes != 2 {
		t.Fatalf("delete = %d %q wakes=%d", deleted.Code, deleted.Body.String(), waker.wakes)
	}
	if response := doRequest(t, api, http.MethodGet, "/api/v2/torrents/info", nil, cookie); response.Code != http.StatusOK || response.Body.String() != "[]\n" {
		t.Fatalf("hidden info = %d %q", response.Code, response.Body.String())
	}
}

func doForm(t *testing.T, handler http.Handler, method, target string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func doRequest(t *testing.T, handler http.Handler, method, target string, body *strings.Reader, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = strings.NewReader("")
	}
	request := httptest.NewRequest(method, target, body)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type contractHarness struct {
	api        http.Handler
	repository *store.Store
	clock      contractClock
	waker      *contractWaker
	filesystem *contractFilesystem
	limits     torrentmeta.Limits
}

func newContractHarness(t *testing.T) *contractHarness {
	t.Helper()
	clock := contractClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	repository, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	sessions, err := session.New(clock, bytes.NewReader(bytes.Repeat([]byte{2}, 128)), time.Hour, 8)
	if err != nil {
		t.Fatal(err)
	}
	limits := torrentmeta.Limits{MaxInputBytes: 1 << 20, MaxInfoBytes: 1 << 18, MaxFiles: 16, MaxNameBytes: 255, MaxPathBytes: 1024, MaxComponentBytes: 255, MaxTrackerCount: 16, MaxTrackerBytes: 1024, MaxTotalSize: 1 << 30}
	waker := &contractWaker{}
	filesystem := &contractFilesystem{root: "/local", err: fs.ErrNotExist}
	api, err := New(Config{
		CloudRoot: "/cloud", LocalRoot: "/local",
		TorrentLimits: limits, MaxRequestBytes: int64(limits.MaxInputBytes) + (64 << 10),
	}, stubCredentials{username: "user", password: "password"}, repository, sessions, clock, waker, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	return &contractHarness{api: api, repository: repository, clock: clock, waker: waker, filesystem: filesystem, limits: limits}
}

type stubCredentials struct {
	username string
	password string
}

func (s stubCredentials) Verify(_ context.Context, username, password string) (bool, error) {
	return username == s.username && password == s.password, nil
}

func (h *contractHarness) login(t *testing.T) *http.Cookie {
	t.Helper()
	response := doForm(t, h.api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"password"}}, nil)
	if response.Code != http.StatusOK || response.Body.String() != "Ok." {
		t.Fatalf("login = %d %q", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	return cookies[0]
}

func TestReAddRetainsVerifiedLocalContent(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	now := harness.clock.now
	if _, err := harness.repository.UpsertCategory(context.Background(), domain.Category{
		Name: "movies", CloudPath: "/cloud/movies", SavePath: "/local/movies", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	hash := "0123456789abcdef0123456789abcdef01234567"
	values := url.Values{
		"urls":     {"magnet:?xt=urn:btih:" + hash + "&dn=Example.Release"},
		"category": {"movies"},
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", values, cookie); response.Code != http.StatusOK {
		t.Fatalf("initial add = %d %q", response.Code, response.Body.String())
	}

	contentPath := "/local/movies/Example.Release"
	states := []domain.State{
		domain.StateSubmittingOffline,
		domain.StateWaitingOffline,
		domain.StateSubmittingCopy,
		domain.StateWaitingCopy,
		domain.StateVerifyingLocal,
		domain.StateCompleted,
	}
	for index, state := range states {
		at := now.Add(time.Duration(index+1) * time.Second)
		claim, err := harness.repository.ClaimDue(context.Background(), "contract", at, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("claim %s = %+v, %v", state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.PhaseStartedAt = at
		next.UpdatedAt = at
		nextRun := at.Add(time.Second)
		next.NextRunAt = &nextRun
		switch state {
		case domain.StateWaitingOffline:
			next.CloudTaskName = "Example.Release"
			next.LastUpstreamStatus = "offline:DOWNLOADING"
		case domain.StateSubmittingCopy:
			next.CloudTaskName = "Example.Release"
			next.CloudSourcePath = "/cloud/movies/Example.Release"
			next.OfflineProgress = 1
			next.LastUpstreamStatus = "offline:FINISHED"
		case domain.StateWaitingCopy:
			next.LastUpstreamStatus = domain.UpstreamCopyPending
			next.DestinationName = next.Name
		case domain.StateVerifyingLocal:
			multiFile := false
			next.IsMultiFile = &multiFile
			next.ContentPath = contentPath
			next.CopyProgress = 1
			next.QbitProgress = 0.99
			next.LastUpstreamStatus = domain.UpstreamCopyCompleted
		case domain.StateCompleted:
			completedAt := at
			next.CompletedAt = &completedAt
			next.NextRunAt = nil
			next.QbitProgress = 1
		}
		if err := harness.repository.CommitClaim(context.Background(), *claim, next); err != nil {
			t.Fatalf("commit %s: %v", state, err)
		}
	}
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	claim, err := harness.repository.ClaimDue(context.Background(), "contract", now.Add(11*time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim deletion = %+v, %v", claim, err)
	}
	deleted := claim.Download
	deleted.State = domain.StateDeleted
	deleted.NextRunAt = nil
	deleted.UpdatedAt = now.Add(11 * time.Second)
	if err := harness.repository.CommitClaim(context.Background(), *claim, deleted); err != nil {
		t.Fatal(err)
	}

	harness.filesystem.content = contentPath
	harness.filesystem.err = nil
	reAddValues := url.Values{
		"urls":     {"magnet:?xt=urn:btih:" + hash},
		"category": {"movies"},
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", reAddValues, cookie); response.Code != http.StatusOK {
		t.Fatalf("re-add = %d %q", response.Code, response.Body.String())
	}
	revived, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if revived.State != domain.StateVerifyingLocal || revived.ContentPath != contentPath ||
		revived.CloudSourcePath != "/cloud/movies/Example.Release" || revived.LastUpstreamStatus != domain.UpstreamRetainedContent {
		t.Fatalf("revived download lost retained evidence: %+v", revived)
	}

	claim, err = harness.repository.ClaimDue(context.Background(), "contract", now.Add(12*time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim revived verification = %+v, %v", claim, err)
	}
	completed := claim.Download
	completed.DestinationName = completed.Name
	completed.State = domain.StateCompleted
	completedAt := now.Add(12 * time.Second)
	completed.CompletedAt = &completedAt
	completed.QbitProgress = 1
	completed.NextRunAt = nil
	completed.UpdatedAt = completedAt
	if err := harness.repository.CommitClaim(context.Background(), *claim, completed); err != nil {
		t.Fatalf("complete revived download: %v", err)
	}
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	claim, err = harness.repository.ClaimDue(context.Background(), "contract", now.Add(14*time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim second deletion = %+v, %v", claim, err)
	}
	deleted = claim.Download
	deleted.State = domain.StateDeleted
	deleted.NextRunAt = nil
	deleted.UpdatedAt = now.Add(14 * time.Second)
	if err := harness.repository.CommitClaim(context.Background(), *claim, deleted); err != nil {
		t.Fatal(err)
	}

	harness.clock.now = now.Add(15 * time.Second)
	stoppedValues := url.Values{
		"urls":     {"magnet:?xt=urn:btih:" + hash},
		"category": {"movies"},
		"stopped":  {"true"},
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", stoppedValues, cookie); response.Code != http.StatusOK {
		t.Fatalf("stopped re-add = %d %q", response.Code, response.Body.String())
	}
	stopped, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil || stopped.State != domain.StateStopped || stopped.ContentPath != contentPath ||
		stopped.LastUpstreamStatus != domain.UpstreamRetainedContent {
		t.Fatalf("stopped revival lost retained evidence: %+v, %v", stopped, err)
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/setForceStart", url.Values{
		"hashes": {hash}, "value": {"1"},
	}, cookie); response.Code != http.StatusOK {
		t.Fatalf("start stopped revival = %d %q", response.Code, response.Body.String())
	}
	started, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil || started.State != domain.StateVerifyingLocal || started.ContentPath != contentPath ||
		started.LastUpstreamStatus != domain.UpstreamRetainedContent {
		t.Fatalf("started revival did not resume local verification: %+v, %v", started, err)
	}
}

func TestLoginCookieIsSecureOnHTTPS(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	body := strings.NewReader(url.Values{"username": {"user"}, "password": {"password"}}.Encode())
	request := httptest.NewRequest(http.MethodPost, "https://cd211.test/api/v2/auth/login", body)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	harness.api.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("HTTPS login cookie = status:%d cookies:%+v", response.Code, cookies)
	}
}

func TestLoginBansRepeatedFailures(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	for attempt := 1; attempt <= 5; attempt++ {
		response := doForm(t, harness.api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"wrong"}}, nil)
		want := http.StatusOK
		if attempt == 5 {
			want = http.StatusForbidden
		}
		if response.Code != want || response.Body.String() != "Fails." {
			t.Fatalf("login failure %d = %d %q, want %d Fails.", attempt, response.Code, response.Body.String(), want)
		}
	}
	response := doForm(t, harness.api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"password"}}, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("valid credentials bypassed login ban: %d", response.Code)
	}
}

func TestFailedDeletionRemainsQueryableAsError(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	now := harness.clock.now
	if _, err := harness.repository.UpsertCategory(context.Background(), domain.Category{
		Name: "movies", CloudPath: "/cloud/movies", SavePath: "/local/movies", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	hash := "1123456789abcdef0123456789abcdef01234567"
	add := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{
		"urls": {"magnet:?xt=urn:btih:" + hash + "&dn=Cleanup.Failure"}, "category": {"movies"},
	}, cookie)
	if add.Code != http.StatusOK {
		t.Fatalf("add = %d %q", add.Code, add.Body.String())
	}
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	claim, err := harness.repository.ClaimDue(context.Background(), "cleanup", now.Add(time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue(cleanup) = (%+v, %v)", claim, err)
	}
	failed := claim.Download
	failed.LastError = "local deletion failed"
	failed.NextRunAt = nil
	failed.UpdatedAt = now.Add(2 * time.Minute)
	if err := harness.repository.CommitClaim(context.Background(), *claim, failed); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info", nil, cookie)
	var torrents []map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &torrents) != nil ||
		len(torrents) != 1 || torrents[0]["hash"] != hash || torrents[0]["state"] != "error" {
		t.Fatalf("failed deletion info = %d %s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedMutationsRejectCrossOriginBrowsers(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)

	requestCategory := func(name, origin string) *httptest.ResponseRecorder {
		t.Helper()
		values := url.Values{"category": {name}}
		request := httptest.NewRequest(http.MethodPost, "http://cd211.example/api/v2/torrents/createCategory", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		harness.api.ServeHTTP(response, request)
		return response
	}

	if response := requestCategory("cross-site", "http://attacker.example"); response.Code != http.StatusForbidden || response.Body.String() != "Forbidden\n" {
		t.Fatalf("cross-origin mutation = %d %q", response.Code, response.Body.String())
	}
	if _, err := harness.repository.GetCategory(context.Background(), "cross-site"); err == nil {
		t.Fatal("cross-origin mutation was persisted")
	}
	if response := requestCategory("same-origin", "http://cd211.example"); response.Code != http.StatusOK {
		t.Fatalf("same-origin mutation = %d %q", response.Code, response.Body.String())
	}
	if _, err := harness.repository.GetCategory(context.Background(), "same-origin"); err != nil {
		t.Fatalf("same-origin mutation was not persisted: %v", err)
	}
}

func TestCategoryReservationConflictPrecedesFilesystemPreparation(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	hash := strings.Repeat("e", 40)
	add := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{
		"urls": {"magnet:?xt=urn:btih:" + hash + "&dn=blocked"},
	}, cookie)
	if add.Code != http.StatusOK {
		t.Fatalf("add = %d %q", add.Code, add.Body.String())
	}
	claim, err := harness.repository.ClaimDue(context.Background(), "reservation", harness.clock.now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	reserved := claim.Download
	reserved.DestinationName = reserved.Name
	reserved.UpdatedAt = harness.clock.now.Add(time.Second)
	if err := harness.repository.CommitClaim(context.Background(), *claim, reserved); err != nil {
		t.Fatalf("CommitClaim(reservation): %v", err)
	}
	harness.filesystem.prepareCalls = 0
	response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/createCategory", url.Values{"category": {"blocked"}}, cookie)
	if response.Code != http.StatusBadRequest || response.Body.String() != "Bad Request\n" {
		t.Fatalf("conflicting category = %d %q", response.Code, response.Body.String())
	}
	if harness.filesystem.prepareCalls != 0 {
		t.Fatalf("filesystem preparation calls = %d, want 0", harness.filesystem.prepareCalls)
	}
}

func TestCategoryPreparationFailureKeepsDisabledReservation(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	harness.filesystem.prepareErr = fs.ErrPermission
	response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/createCategory", url.Values{"category": {"pending"}}, cookie)
	if response.Code != http.StatusBadRequest || response.Body.String() != "Bad Request\n" {
		t.Fatalf("failed preparation = %d %q", response.Code, response.Body.String())
	}
	category, err := harness.repository.GetCategory(context.Background(), "pending")
	if err != nil || category.Enabled || category.CloudPath != "/cloud/pending" || category.SavePath != "/local/pending" {
		t.Fatalf("disabled reservation = (%+v, %v)", category, err)
	}
}

func TestAuthenticationAppCategoriesAndRoutingContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/app/version", nil, nil); response.Code != http.StatusForbidden || response.Body.String() != "Forbidden\n" {
		t.Fatalf("unauthenticated version = %d %q", response.Code, response.Body.String())
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"wrong"}}, nil); response.Code != http.StatusOK || response.Body.String() != "Fails." {
		t.Fatalf("failed login = %d %q", response.Code, response.Body.String())
	}
	cookie := harness.login(t)
	if !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login cookie = %#v", cookie)
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/app/version", nil, cookie); response.Code != http.StatusOK || response.Body.String() != "v5.0.0-cd211" {
		t.Fatalf("version = %d %q", response.Code, response.Body.String())
	}
	preferences := doRequest(t, harness.api, http.MethodGet, "/api/v2/app/preferences", nil, cookie)
	var values map[string]any
	if preferences.Code != http.StatusOK || json.Unmarshal(preferences.Body.Bytes(), &values) != nil || values["save_path"] != "/local" || values["dht"] != true || values["queueing_enabled"] != false || values["max_ratio"] != float64(-1) {
		t.Fatalf("preferences = %d %q", preferences.Code, preferences.Body.String())
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/categories", nil, cookie); response.Code != http.StatusOK || response.Body.String() != "{}\n" {
		t.Fatalf("empty categories = %d %q", response.Code, response.Body.String())
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/createCategory", url.Values{"category": {"TV"}}, cookie); response.Code != http.StatusOK {
		t.Fatalf("create category = %d %q", response.Code, response.Body.String())
	}
	categories := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/categories", nil, cookie)
	if categories.Code != http.StatusOK || !strings.Contains(categories.Body.String(), `"tv":{"name":"tv","savePath":"/local/tv"}`) {
		t.Fatalf("categories = %d %q", categories.Code, categories.Body.String())
	}
	for _, values := range []url.Values{
		{"category": {"root"}, "savePath": {"/local"}},
		{"category": {"escape"}, "savePath": {"/outside"}},
	} {
		if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/createCategory", values, cookie); response.Code != http.StatusBadRequest || response.Body.String() != "Bad Request\n" {
			t.Fatalf("invalid category path = %d %q", response.Code, response.Body.String())
		}
	}
	for target, method := range map[string]string{
		"/api/v2/auth/logout":             http.MethodGet,
		"/api/v2/app/webapiVersion":       http.MethodPost,
		"/api/v2/app/version":             http.MethodPost,
		"/api/v2/app/preferences":         http.MethodPost,
		"/api/v2/torrents/categories":     http.MethodPost,
		"/api/v2/torrents/createCategory": http.MethodGet,
		"/api/v2/torrents/setCategory":    http.MethodGet,
		"/api/v2/torrents/add":            http.MethodGet,
		"/api/v2/torrents/info":           http.MethodPost,
		"/api/v2/torrents/properties":     http.MethodPost,
		"/api/v2/torrents/files":          http.MethodPost,
		"/api/v2/torrents/delete":         http.MethodGet,
		"/api/v2/torrents/setForceStart":  http.MethodGet,
		"/api/v2/torrents/setShareLimits": http.MethodGet,
		"/api/v2/torrents/topPrio":        http.MethodGet,
	} {
		if response := doRequest(t, harness.api, method, target, nil, nil); response.Code != http.StatusMethodNotAllowed || response.Body.String() != "Method Not Allowed\n" {
			t.Fatalf("wrong method %s %s = %d %q", method, target, response.Code, response.Body.String())
		}
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/auth/login", nil, nil); response.Code != http.StatusMethodNotAllowed || response.Body.String() != "Method Not Allowed\n" {
		t.Fatalf("wrong method = %d %q", response.Code, response.Body.String())
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/not-supported", nil, nil); response.Code != http.StatusNotFound || response.Body.String() != "Not Found\n" {
		t.Fatalf("unknown route = %d %q", response.Code, response.Body.String())
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/auth/logout", url.Values{}, cookie); response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("logout = %d %q", response.Code, response.Body.String())
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/app/version", nil, cookie); response.Code != http.StatusForbidden {
		t.Fatalf("revoked session = %d %q", response.Code, response.Body.String())
	}
}

func TestBase32MagnetDuplicateAndRedactionContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		t.Fatal(err)
	}
	base32Hash := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes)
	magnet := "magnet:?xt=urn:btih:" + base32Hash + "&dn=Base32&tr=https%3A%2F%2Ftracker.invalid%2Fannounce%3Fpasskey%3Dsecret"
	added := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {magnet}}, cookie)
	if added.Code != http.StatusOK || harness.waker.wakes != 1 {
		t.Fatalf("base32 add = %d %q wakes=%d", added.Code, added.Body.String(), harness.waker.wakes)
	}
	duplicate := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {magnet}}, cookie)
	if duplicate.Code != http.StatusOK || harness.waker.wakes != 1 {
		t.Fatalf("duplicate add = %d %q wakes=%d", duplicate.Code, duplicate.Body.String(), harness.waker.wakes)
	}
	for _, response := range []*httptest.ResponseRecorder{
		doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/properties?hash="+hash, nil, cookie),
		doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info", nil, cookie),
	} {
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "passkey") || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("redacted response = %d %q", response.Code, response.Body.String())
		}
	}
}

func TestMultipartTorrentFilesAndStoppedForceStartContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	torrent := []byte("d4:infod6:lengthi3e4:name4:demo12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	metadata, err := torrentmeta.ParseTorrent(torrent, harness.limits)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("torrents", "demo.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(torrent); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	harness.api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("multipart add = %d %q", response.Code, response.Body.String())
	}
	files := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/files?hash="+metadata.Hash, nil, cookie)
	if files.Code != http.StatusOK || !strings.Contains(files.Body.String(), `"index":0`) || !strings.Contains(files.Body.String(), `"name":"demo"`) {
		t.Fatalf("torrent files = %d %q", files.Code, files.Body.String())
	}
	hash := "fedcba9876543210fedcba9876543210fedcba98"
	stopped := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {"magnet:?xt=urn:btih:" + hash + "&dn=Stopped"}, "stopped": {"TRUE"}}, cookie)
	if stopped.Code != http.StatusOK {
		t.Fatalf("stopped add = %d %q", stopped.Code, stopped.Body.String())
	}
	before := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info?category=", nil, cookie)
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"hash":"`+hash+`"`) || !strings.Contains(before.Body.String(), `"state":"stoppedDL"`) {
		t.Fatalf("stopped projection = %d %q", before.Code, before.Body.String())
	}
	forced := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/setForceStart", url.Values{"hashes": {hash}, "value": {"1"}}, cookie)
	if forced.Code != http.StatusOK {
		t.Fatalf("force start = %d %q", forced.Code, forced.Body.String())
	}
	after := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info?category=", nil, cookie)
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"state":"queuedDL"`) {
		t.Fatalf("force-start projection = %d %q", after.Code, after.Body.String())
	}
}

func TestMutationFilteringDeleteIntentAndCompletedProjectionContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	hash := "1111111111111111111111111111111111111111"
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {"magnet:?xt=urn:btih:" + hash + "&dn=Complete"}}, cookie); response.Code != http.StatusOK {
		t.Fatalf("add = %d %q", response.Code, response.Body.String())
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/setCategory", url.Values{"hashes": {hash}, "category": {"archive"}}, cookie); response.Code != http.StatusOK {
		t.Fatalf("set category = %d %q", response.Code, response.Body.String())
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info?category=archive", nil, cookie); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"hash":"`+hash+`"`) {
		t.Fatalf("category filter = %d %q", response.Code, response.Body.String())
	}
	advanceToCompleted(t, harness.repository, harness.clock.now, hash)
	completed := doRequest(t, harness.api, http.MethodGet, "/api/v2/torrents/info?category=archive", nil, cookie)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"state":"stoppedUP"`) || !strings.Contains(completed.Body.String(), `"content_path":"/local/Complete"`) {
		t.Fatalf("completed projection = %d %q", completed.Code, completed.Body.String())
	}
	deleted := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/delete", url.Values{"hashes": {hash}, "deleteFiles": {"true"}}, cookie)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete = %d %q", deleted.Code, deleted.Body.String())
	}
	download, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil || download.State != domain.StateDeleteRequested || !download.DeleteFilesRequested {
		t.Fatalf("delete intent = (%+v, %v)", download, err)
	}
}

func TestRejectedInputsAndCompatibilityContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	cookie := harness.login(t)
	for _, values := range []url.Values{
		{"urls": {"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "magnet:?xt=urn:btih:1111111111111111111111111111111111111111"}},
		{"urls": {"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}, "category": {"missing"}},
		{"urls": {strings.Repeat("x", (1<<20)+(64<<10))}},
	} {
		if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", values, cookie); response.Code != http.StatusBadRequest || response.Body.String() != "Bad Request\n" {
			t.Fatalf("rejected add = %d %q", response.Code, response.Body.String())
		}
	}
	now := harness.clock.now
	if _, err := harness.repository.UpsertCategory(context.Background(), domain.Category{Name: "disabled", CloudPath: "/cloud/DISABLED", SavePath: "/local/disabled", Enabled: false, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if response := doForm(t, harness.api, http.MethodPost, "/api/v2/torrents/add", url.Values{"urls": {"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}, "category": {"disabled"}}, cookie); response.Code != http.StatusBadRequest {
		t.Fatalf("disabled category = %d %q", response.Code, response.Body.String())
	}
	for _, target := range []string{"/api/v2/torrents/setShareLimits", "/api/v2/torrents/topPrio"} {
		if response := doForm(t, harness.api, http.MethodPost, target, url.Values{"ignored": {"value"}}, cookie); response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("compatibility %s = %d %q", target, response.Code, response.Body.String())
		}
	}
}

func advanceToCompleted(t *testing.T, repository *store.Store, now time.Time, hash string) {
	t.Helper()
	states := []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal, domain.StateCompleted}
	for _, state := range states {
		claim, err := repository.ClaimDue(context.Background(), "contract-worker", now, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("claim for %s = (%+v, %v)", state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.UpdatedAt = now
		next.PhaseStartedAt = now
		next.NextRunAt = &now
		if state == domain.StateSubmittingCopy || state == domain.StateWaitingCopy || state == domain.StateVerifyingLocal || state == domain.StateCompleted {
			next.CloudSourcePath = "/cloud/Complete"
		}
		if state == domain.StateCompleted {
			next.NextRunAt = nil
			next.ContentPath = "/local/Complete"
			next.CompletedAt = &now
		}
		if err := repository.CommitClaim(context.Background(), *claim, next); err != nil {
			t.Fatalf("commit %s: %v", state, err)
		}
	}
}
