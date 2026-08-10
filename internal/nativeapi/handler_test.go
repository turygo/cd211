package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
	"github.com/turygo/cd211/internal/token"
	"github.com/turygo/cd211/internal/torrentmeta"
)

const (
	invalidRequestJSON       = "{\"error\":{\"code\":\"invalid_request\",\"message\":\"Request is invalid\"}}\n"
	downloadNotFoundJSON     = "{\"error\":{\"code\":\"download_not_found\",\"message\":\"Download was not found\"}}\n"
	methodNotAllowedJSON     = "{\"error\":{\"code\":\"method_not_allowed\",\"message\":\"Method is not allowed\"}}\n"
	requestTooLargeJSON      = "{\"error\":{\"code\":\"request_too_large\",\"message\":\"Request is too large\"}}\n"
	unsupportedMediaTypeJSON = "{\"error\":{\"code\":\"unsupported_media_type\",\"message\":\"Content type is not supported\"}}\n"
	invalidSubmissionJSON    = "{\"error\":{\"code\":\"invalid_submission\",\"message\":\"Submission is invalid\"}}\n"
)

type nativeClock struct{ now time.Time }

func (c nativeClock) Now() time.Time { return c.now }

type nativeWaker struct{ wakes int }

func (w *nativeWaker) Wake() { w.wakes++ }

type nativeFilesystem struct {
	content string
	size    int64
	err     error
}

func (f *nativeFilesystem) Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	return fsafe.VerifiedContent{Path: f.content, Size: f.size}, f.err
}

type nativeHarness struct {
	handler    http.Handler
	repository *store.Store
	clock      nativeClock
	waker      *nativeWaker
	filesystem *nativeFilesystem
	limits     torrentmeta.Limits
	secret     token.Secret
	signal     *outbox.Signal
	shutdown   chan struct{}
}

func newNativeHarness(t *testing.T) *nativeHarness {
	return newNativeHarnessWith(t, nil)
}

// newNativeHarnessWith builds a harness whose handler queries through the
// store returned by wrap (or the raw store when wrap is nil); setup and auth
// always use the real repository. The signal is the one injected into the
// handler, so a wrapper may notify it to force deterministic observation
// rounds.
func newNativeHarnessWith(t *testing.T, wrap func(repository *store.Store, signal *outbox.Signal) Store) *nativeHarness {
	t.Helper()
	clock := nativeClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	repository, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "native.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	secret, err := repository.GenerateAPIToken(context.Background(), clock.now)
	if err != nil {
		t.Fatal(err)
	}
	limits := torrentmeta.Limits{MaxInputBytes: 1 << 20, MaxInfoBytes: 1 << 18, MaxFiles: 16, MaxNameBytes: 255, MaxPathBytes: 1024, MaxComponentBytes: 255, MaxTrackerCount: 16, MaxTrackerBytes: 1024, MaxTotalSize: 1 << 30}
	waker := &nativeWaker{}
	filesystem := &nativeFilesystem{err: fs.ErrNotExist}
	service, err := submission.New(submission.Config{
		CloudRoot: "/cloud", LocalRoot: "/local",
		TorrentLimits: limits,
	}, repository, clock, waker, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	signal := repository.EventSignal()
	shutdown := make(chan struct{})
	var repo Store = repository
	if wrap != nil {
		repo = wrap(repository, signal)
	}
	handler, err := NewHandler(Config{
		MaxRequestBytes: int64(limits.MaxInputBytes) + (64 << 10),
		TorrentLimits:   limits,
		Shutdown:        shutdown,
	}, service, repo, signal)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(repository)
	if err != nil {
		t.Fatal(err)
	}
	return &nativeHarness{handler: auth.Middleware(handler), repository: repository, clock: clock, waker: waker, filesystem: filesystem, limits: limits, secret: secret, signal: signal, shutdown: shutdown}
}

func (h *nativeHarness) upsertCategory(t *testing.T, name string, enabled bool) {
	t.Helper()
	now := h.clock.now
	if _, err := h.repository.UpsertCategory(context.Background(), domain.Category{
		Name: name, CloudPath: "/cloud/" + name, SavePath: "/local/" + name, Enabled: enabled, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *nativeHarness) request(t *testing.T, method, target, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Authorization", "Bearer "+string(h.secret))
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func (h *nativeHarness) postJSON(t *testing.T, value any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return h.request(t, http.MethodPost, "/api/v1/downloads", "application/json", bytes.NewReader(raw))
}

func (h *nativeHarness) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return h.request(t, http.MethodGet, target, "", nil)
}

func decodeObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode body %q: %v", response.Body.String(), err)
	}
	return value
}

func requireErrorBody(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", response.Code, status, response.Body.String())
	}
	if got := response.Body.String(); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestNativeSubmitJSONMagnetContract(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	response := harness.postJSON(t, map[string]any{
		"magnet":  "magnet:?xt=urn:btih:" + hash + "&dn=Example",
		"stopped": true,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/api/v1/downloads/"+hash {
		t.Fatalf("Location = %q, want /api/v1/downloads/%s", location, hash)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("wakes = %d, want 1", harness.waker.wakes)
	}
	body := decodeObject(t, response)
	if body["created"] != true {
		t.Fatalf("created = %v, want true", body["created"])
	}
	download, ok := body["download"].(map[string]any)
	if !ok {
		t.Fatalf("download = %#v", body["download"])
	}
	if download["hash"] != hash || download["name"] != "Example" || download["category"] != "" ||
		download["state"] != "STOPPED" || download["version"] != float64(0) ||
		download["terminal"] != false || download["outcome"] != nil ||
		download["content_path"] != nil || download["total_size"] != float64(0) ||
		download["error"] != nil || download["completed_at"] != nil {
		t.Fatalf("download model = %#v", download)
	}
	if progress, ok := download["progress"].(float64); !ok || progress != 0 {
		t.Errorf("progress = %#v, want 0", download["progress"])
	}
	for _, field := range []string{"created_at", "updated_at"} {
		raw, ok := download[field].(string)
		if !ok || !strings.HasSuffix(raw, "Z") {
			t.Errorf("%s = %#v, want UTC RFC3339Nano timestamp", field, download[field])
		}
	}
	links, ok := download["links"].(map[string]any)
	if !ok || links["self"] != "/api/v1/downloads/"+hash || links["wait"] != "/api/v1/downloads/"+hash+"/wait" {
		t.Fatalf("links = %#v", download["links"])
	}
}

func TestNativeGetDownloadModelAndOutcomes(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now

	// ACCEPTED: terminal false, outcome null, content and error absent.
	hash := "2222222222222222222222222222222222222222"
	response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Model"})
	if response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	queried := harness.get(t, "/api/v1/downloads/"+hash)
	if queried.Code != http.StatusOK {
		t.Fatalf("get = %d %q", queried.Code, queried.Body.String())
	}
	model := decodeObject(t, queried)
	if model["hash"] != hash || model["name"] != "Model" || model["category"] != "" ||
		model["state"] != "ACCEPTED" || model["version"] != float64(0) ||
		model["terminal"] != false || model["outcome"] != nil ||
		model["content_path"] != nil || model["error"] != nil || model["completed_at"] != nil {
		t.Fatalf("accepted model = %#v", model)
	}
	links := model["links"].(map[string]any)
	if links["self"] != "/api/v1/downloads/"+hash {
		t.Fatalf("self link = %#v", links["self"])
	}

	// COMPLETED: terminal completed, content_path and completed_at present.
	advanceToCompleted(t, harness.repository, now, hash)
	completed := decodeObject(t, harness.get(t, "/api/v1/downloads/"+hash))
	if completed["state"] != "COMPLETED" || completed["terminal"] != true || completed["outcome"] != "completed" ||
		completed["progress"] != float64(1) || completed["content_path"] != "/local/Model" ||
		completed["completed_at"] == nil {
		t.Fatalf("completed model = %#v", completed)
	}

	// FAILED with a magnet-tainted error: terminal failed, error redacted.
	failedHash := "8888888888888888888888888888888888888888"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + failedHash + "&dn=Failure"}); response.Code != http.StatusCreated {
		t.Fatalf("failed submit = %d %q", response.Code, response.Body.String())
	}
	claimTransition(t, harness.repository, now.Add(time.Hour), domain.StateFailed)
	failed := decodeObject(t, harness.get(t, "/api/v1/downloads/"+failedHash))
	if failed["state"] != "FAILED" || failed["terminal"] != true || failed["outcome"] != "failed" ||
		failed["error"] != domain.RedactedErrorText {
		t.Fatalf("failed model = %#v", failed)
	}

	// DELETED: still 200 with outcome deleted and bounded qbit progress.
	deletedHash := "9999999999999999999999999999999999999999"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + deletedHash + "&dn=Gone"}); response.Code != http.StatusCreated {
		t.Fatalf("deleted submit = %d %q", response.Code, response.Body.String())
	}
	claimTransition(t, harness.repository, now.Add(2*time.Hour), domain.StateDeleteRequested)
	claimTransition(t, harness.repository, now.Add(3*time.Hour), domain.StateDeleted)
	deleted := decodeObject(t, harness.get(t, "/api/v1/downloads/"+deletedHash))
	if deleted["state"] != "DELETED" || deleted["terminal"] != true || deleted["outcome"] != "deleted" ||
		deleted["progress"] != float64(0.5) {
		t.Fatalf("deleted model = %#v", deleted)
	}
}

func advanceToCompleted(t *testing.T, repository *store.Store, now time.Time, hash string) {
	t.Helper()
	states := []domain.State{domain.StateSubmittingOffline, domain.StateWaitingOffline, domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal, domain.StateCompleted}
	for _, state := range states {
		claim, err := repository.ClaimDue(context.Background(), "native-worker", now, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("claim for %s = (%+v, %v)", state, claim, err)
		}
		next := claim.Download
		next.State = state
		next.UpdatedAt = now
		next.PhaseStartedAt = now
		next.NextRunAt = &now
		switch state {
		case domain.StateSubmittingCopy, domain.StateWaitingCopy, domain.StateVerifyingLocal:
			next.CloudSourcePath = "/cloud/Model"
		case domain.StateCompleted:
			next.CloudSourcePath = "/cloud/Model"
			next.NextRunAt = nil
			next.ContentPath = "/local/Model"
			completedAt := now
			next.CompletedAt = &completedAt
			next.QbitProgress = 1
		}
		if err := repository.CommitClaim(context.Background(), *claim, next); err != nil {
			t.Fatalf("commit %s: %v", state, err)
		}
	}
}

func claimTransition(t *testing.T, repository *store.Store, now time.Time, state domain.State) {
	t.Helper()
	claim, err := repository.ClaimDue(context.Background(), "native-worker", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v), want a claim", claim, err)
	}
	next := claim.Download
	next.State = state
	next.UpdatedAt = now
	next.PhaseStartedAt = now
	next.NextRunAt = &now
	if state == domain.StateFailed {
		next.LastError = "magnet:?xt=urn:btih:" + claim.Download.Hash + " upstream failed"
	}
	if state == domain.StateDeleted {
		next.NextRunAt = nil
		next.QbitProgress = 0.5
	}
	if err := repository.CommitClaim(context.Background(), *claim, next); err != nil {
		t.Fatalf("CommitClaim(%s): %v", state, err)
	}
}

func TestNativeDuplicateSubmitReturnsExistingWithoutMutation(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "3333333333333333333333333333333333333333"
	body := map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Dup"}
	first := harness.postJSON(t, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first submit = %d %q", first.Code, first.Body.String())
	}
	before, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}

	second := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Changed", "stopped": true})
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate submit = %d %q", second.Code, second.Body.String())
	}
	if location := second.Header().Get("Location"); location != "/api/v1/downloads/"+hash {
		t.Fatalf("duplicate Location = %q", location)
	}
	bodyMap := decodeObject(t, second)
	if bodyMap["created"] != false {
		t.Fatalf("duplicate created = %v, want false", bodyMap["created"])
	}
	after, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Dup" || after.State != domain.StateAccepted || after.RowVersion != before.RowVersion ||
		!after.CreatedAt.Equal(before.CreatedAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("duplicate mutated the row: before=%+v after=%+v", before, after)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("wakes = %d, want 1", harness.waker.wakes)
	}
}

func TestNativeRevivedDeletedAndRetainedContent(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	harness.upsertCategory(t, "movies", true)
	hash := "4444444444444444444444444444444444444444"
	now := harness.clock.now
	response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Retained", "category": "movies"})
	if response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}

	// DELETE the row but retain its content evidence on disk.
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	claim, err := harness.repository.ClaimDue(context.Background(), "native-delete", now.Add(time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	deleted := claim.Download
	deleted.State = domain.StateDeleted
	deleted.NextRunAt = nil
	deleted.UpdatedAt = now.Add(time.Minute)
	multiFile := false
	deleted.IsMultiFile = &multiFile
	deleted.ContentPath = "/local/movies/Retained"
	deleted.CloudSourcePath = "/cloud/movies/Retained"
	if err := harness.repository.CommitClaim(context.Background(), *claim, deleted); err != nil {
		t.Fatal(err)
	}

	// DELETED rows stay queryable with outcome deleted.
	queried := decodeObject(t, harness.get(t, "/api/v1/downloads/"+hash))
	if queried["state"] != "DELETED" || queried["terminal"] != true || queried["outcome"] != "deleted" {
		t.Fatalf("deleted query = %#v", queried)
	}

	// A fresh submission with verified retained content revives the row.
	harness.filesystem.content = "/local/movies/Retained"
	harness.filesystem.size = 42
	harness.filesystem.err = nil
	revived := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash, "category": "movies"})
	if revived.Code != http.StatusCreated {
		t.Fatalf("revive submit = %d %q", revived.Code, revived.Body.String())
	}
	if location := revived.Header().Get("Location"); location != "/api/v1/downloads/"+hash {
		t.Fatalf("revive Location = %q", location)
	}
	stored, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.StateVerifyingLocal || stored.ContentPath != "/local/movies/Retained" ||
		stored.CloudSourcePath != "/cloud/movies/Retained" || stored.TotalSize != 42 ||
		stored.LastUpstreamStatus != domain.UpstreamRetainedContent {
		t.Fatalf("revived download lost retained evidence: %+v", stored)
	}
	model := decodeObject(t, harness.get(t, "/api/v1/downloads/"+hash))
	if model["state"] != "VERIFYING_LOCAL" || model["content_path"] != "/local/movies/Retained" ||
		model["terminal"] != false || model["outcome"] != nil {
		t.Fatalf("revived model = %#v", model)
	}
}

func TestNativeSubmitMultipartTorrentContract(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	harness.upsertCategory(t, "tv", true)
	torrent := []byte("d4:infod6:lengthi3e4:name4:demo12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	metadata, err := torrentmeta.ParseTorrent(torrent, harness.limits)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("category", "TV"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("stopped", "TRUE"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("torrent", "demo.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(torrent); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response := harness.request(t, http.MethodPost, "/api/v1/downloads", writer.FormDataContentType(), &body)
	if response.Code != http.StatusCreated {
		t.Fatalf("multipart submit = %d %q", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/api/v1/downloads/"+metadata.Hash {
		t.Fatalf("Location = %q", location)
	}
	model := decodeObject(t, response)
	download := model["download"].(map[string]any)
	if download["hash"] != metadata.Hash || download["state"] != "STOPPED" || download["category"] != "tv" {
		t.Fatalf("torrent model = %#v", download)
	}
	stored, err := harness.repository.GetDownload(context.Background(), metadata.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.StateStopped || stored.Category != "tv" || stored.SourceKind != domain.SourceTorrent {
		t.Fatalf("stored torrent = %+v", stored)
	}
	files, err := harness.repository.ListDownloadFiles(context.Background(), metadata.Hash)
	if err != nil || len(files) != 1 || files[0].RelativePath != "demo" {
		t.Fatalf("torrent files = %+v, %v", files, err)
	}
}

func TestNativeStrictJSONParsing(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "5555555555555555555555555555555555555555"
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Strict"
	cases := []struct {
		name string
		body string
	}{
		{"unknown field", `{"magnet":"` + magnet + `","bogus":1}`},
		{"trailing json", `{"magnet":"` + magnet + `"} {"x":1}`},
		{"missing magnet", `{}`},
		{"null magnet", `{"magnet":null}`},
		{"magnet not string", `{"magnet":5}`},
		{"category not string", `{"magnet":"` + magnet + `","category":5}`},
		{"stopped not bool", `{"magnet":"` + magnet + `","stopped":"true"}`},
		{"array body", `[1]`},
		{"string body", `"magnet"`},
		{"empty body", ``},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := harness.request(t, http.MethodPost, "/api/v1/downloads", "application/json", strings.NewReader(test.body))
			requireErrorBody(t, response, http.StatusBadRequest, invalidRequestJSON)
		})
	}
	if harness.waker.wakes != 0 {
		t.Fatalf("invalid submissions woke %d times, want 0", harness.waker.wakes)
	}
}

func TestNativeInvalidSubmissions(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	harness.upsertCategory(t, "disabled", false)
	hash := "6666666666666666666666666666666666666666"
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bad magnet", map[string]any{"magnet": "not a magnet"}},
		{"empty magnet", map[string]any{"magnet": "  "}},
		{"multiline magnet", map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "\n"}},
		{"missing category", map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash, "category": "missing"}},
		{"disabled category", map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash, "category": "disabled"}},
		{"invalid category", map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash, "category": "bad/category"}},
		{"dot category", map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash, "category": ".."}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := harness.postJSON(t, test.body)
			requireErrorBody(t, response, http.StatusUnprocessableEntity, invalidSubmissionJSON)
		})
	}
	if harness.waker.wakes != 0 {
		t.Fatalf("invalid submissions woke %d times, want 0", harness.waker.wakes)
	}
}

func TestNativeMultipartStrictFields(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	torrent := []byte("d4:infod6:lengthi3e4:name4:demo12:piece lengthi16384e6:pieces20:01234567890123456789ee")

	submit := func(t *testing.T, build func(*multipart.Writer) error) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := build(writer); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return harness.request(t, http.MethodPost, "/api/v1/downloads", writer.FormDataContentType(), &body)
	}
	filePart := func(writer *multipart.Writer, name, filename string) error {
		part, err := writer.CreateFormFile(name, filename)
		if err != nil {
			return err
		}
		_, err = part.Write(torrent)
		return err
	}

	cases := []struct {
		name  string
		build func(*multipart.Writer) error
	}{
		{"no file", func(w *multipart.Writer) error { return nil }},
		{"wrong file name", func(w *multipart.Writer) error { return filePart(w, "magnet", "x.torrent") }},
		{"multiple files", func(w *multipart.Writer) error {
			if err := filePart(w, "torrent", "a.torrent"); err != nil {
				return err
			}
			return filePart(w, "torrent", "b.torrent")
		}},
		{"extra file field", func(w *multipart.Writer) error {
			if err := filePart(w, "torrent", "a.torrent"); err != nil {
				return err
			}
			return filePart(w, "extra", "b.bin")
		}},
		{"paused field", func(w *multipart.Writer) error {
			if err := w.WriteField("paused", "true"); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
		{"magnet field", func(w *multipart.Writer) error {
			if err := w.WriteField("magnet", "magnet:?xt=urn:btih:0000000000000000000000000000000000000000"); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
		{"duplicate category", func(w *multipart.Writer) error {
			if err := w.WriteField("category", "a"); err != nil {
				return err
			}
			if err := w.WriteField("category", "b"); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
		{"duplicate stopped", func(w *multipart.Writer) error {
			if err := w.WriteField("stopped", "true"); err != nil {
				return err
			}
			if err := w.WriteField("stopped", "false"); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
		{"invalid stopped value", func(w *multipart.Writer) error {
			if err := w.WriteField("stopped", "yes"); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
		{"empty stopped value", func(w *multipart.Writer) error {
			if err := w.WriteField("stopped", ""); err != nil {
				return err
			}
			return filePart(w, "torrent", "a.torrent")
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := submit(t, test.build)
			requireErrorBody(t, response, http.StatusBadRequest, invalidRequestJSON)
		})
	}
	if harness.waker.wakes != 0 {
		t.Fatalf("invalid multipart submissions woke %d times, want 0", harness.waker.wakes)
	}
}

func TestNativeMediaTypeAndRequestLimits(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)

	if response := harness.request(t, http.MethodPost, "/api/v1/downloads", "text/plain", strings.NewReader("hello")); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d %q, want 415", response.Code, response.Body.String())
	}
	if response := harness.request(t, http.MethodPost, "/api/v1/downloads", "", strings.NewReader("hello")); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type = %d %q, want 415", response.Code, response.Body.String())
	}
	if response := harness.request(t, http.MethodPost, "/api/v1/downloads", "application/x-www-form-urlencoded", strings.NewReader("magnet=x")); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form encoded = %d %q, want 415", response.Code, response.Body.String())
	}

	// A JSON body beyond MaxRequestBytes is 413, never a raw error.
	huge := `{"magnet":"` + strings.Repeat("a", 2<<20) + `"}`
	response := harness.request(t, http.MethodPost, "/api/v1/downloads", "application/json", strings.NewReader(huge))
	requireErrorBody(t, response, http.StatusRequestEntityTooLarge, requestTooLargeJSON)

	// A multipart body whose file exceeds the request bound is also 413.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("torrent", "huge.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 2<<20)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response = harness.request(t, http.MethodPost, "/api/v1/downloads", writer.FormDataContentType(), &body)
	requireErrorBody(t, response, http.StatusRequestEntityTooLarge, requestTooLargeJSON)
}

func TestNativeAuthAndSIDRejection(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)

	// A SID cookie alone is not a credential: identical 401.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/0123456789abcdef0123456789abcdef01234567", nil)
	request.AddCookie(&http.Cookie{Name: "SID", Value: "operator-session"})
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	requireErrorBody(t, response, http.StatusUnauthorized, unauthorizedJSON)

	// A wrong Bearer token is an identical 401.
	wrong := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/0123456789abcdef0123456789abcdef01234567", nil)
	wrong.Header.Set("Authorization", "Bearer cd211_api_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	wrongResponse := httptest.NewRecorder()
	harness.handler.ServeHTTP(wrongResponse, wrong)
	requireErrorBody(t, wrongResponse, http.StatusUnauthorized, unauthorizedJSON)
}

func TestNativeRoutingMethodAndNotFound(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "7777777777777777777777777777777777777777"

	cases := []struct {
		name   string
		method string
		target string
		status int
		allow  string
		body   string
	}{
		{"get collection", http.MethodGet, "/api/v1/downloads", http.StatusMethodNotAllowed, http.MethodPost, methodNotAllowedJSON},
		{"delete collection", http.MethodDelete, "/api/v1/downloads", http.StatusMethodNotAllowed, http.MethodPost, methodNotAllowedJSON},
		{"post hash", http.MethodPost, "/api/v1/downloads/" + hash, http.StatusMethodNotAllowed, http.MethodGet, methodNotAllowedJSON},
		{"delete hash", http.MethodDelete, "/api/v1/downloads/" + hash, http.StatusMethodNotAllowed, http.MethodGet, methodNotAllowedJSON},
		{"unknown path", http.MethodGet, "/api/v1/unknown", http.StatusNotFound, "", downloadNotFoundJSON},
		{"nested unknown", http.MethodGet, "/api/v1/downloads/" + hash + "/extra", http.StatusNotFound, "", downloadNotFoundJSON},
		{"trailing slash", http.MethodGet, "/api/v1/downloads/", http.StatusNotFound, "", downloadNotFoundJSON},
		{"never existed", http.MethodGet, "/api/v1/downloads/" + hash, http.StatusNotFound, "", downloadNotFoundJSON},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := harness.request(t, test.method, test.target, "", nil)
			requireErrorBody(t, response, test.status, test.body)
			if test.allow != "" && response.Header().Get("Allow") != test.allow {
				t.Errorf("Allow = %q, want %q", response.Header().Get("Allow"), test.allow)
			}
		})
	}

	// Invalid hash syntax is 400, distinct from a valid never-existing hash.
	response := harness.request(t, http.MethodGet, "/api/v1/downloads/not-a-hash", "", nil)
	requireErrorBody(t, response, http.StatusBadRequest, invalidRequestJSON)
}

func TestNativeHandlerRejectsInvalidConstructorInputs(t *testing.T) {
	service, err := submission.New(submission.Config{
		CloudRoot: "/cloud", LocalRoot: "/local",
		TorrentLimits: torrentmeta.Limits{MaxInputBytes: 1 << 10, MaxInfoBytes: 1 << 10, MaxFiles: 1, MaxNameBytes: 1 << 10, MaxPathBytes: 1 << 10, MaxComponentBytes: 1 << 10, MaxTrackerCount: 1, MaxTrackerBytes: 1 << 10, MaxTotalSize: 1 << 10},
	}, stubStore{}, nativeClock{}, &nativeWaker{}, &nativeFilesystem{})
	if err != nil {
		t.Fatal(err)
	}
	limits := torrentmeta.Limits{MaxInputBytes: 1 << 10, MaxInfoBytes: 1 << 10, MaxFiles: 1, MaxNameBytes: 1 << 10, MaxPathBytes: 1 << 10, MaxComponentBytes: 1 << 10, MaxTrackerCount: 1, MaxTrackerBytes: 1 << 10, MaxTotalSize: 1 << 10}
	signal := outbox.NewSignal()
	shutdown := make(chan struct{})
	valid := Config{MaxRequestBytes: int64(limits.MaxInputBytes) + (64 << 10), TorrentLimits: limits, Shutdown: shutdown}
	if handler, err := NewHandler(valid, nil, stubStore{}, signal); err == nil || handler != nil {
		t.Errorf("NewHandler(nil service) = (%v, %v), want error", handler, err)
	}
	if handler, err := NewHandler(valid, service, nil, signal); err == nil || handler != nil {
		t.Errorf("NewHandler(nil store) = (%v, %v), want error", handler, err)
	}
	if handler, err := NewHandler(valid, service, stubStore{}, nil); err == nil || handler != nil {
		t.Errorf("NewHandler(nil signal) = (%v, %v), want error", handler, err)
	}
	if handler, err := NewHandler(Config{MaxRequestBytes: valid.MaxRequestBytes, TorrentLimits: limits}, service, stubStore{}, signal); err == nil || handler != nil {
		t.Errorf("NewHandler(nil shutdown) = (%v, %v), want error", handler, err)
	}
	if handler, err := NewHandler(Config{Shutdown: shutdown}, service, stubStore{}, signal); err == nil || handler != nil {
		t.Errorf("NewHandler(zero limits) = (%v, %v), want error", handler, err)
	}
}

type stubStore struct{}

func (stubStore) GetDownload(context.Context, string) (domain.Download, error) {
	return domain.Download{}, store.ErrNotFound
}
func (stubStore) LatestEventSequence(context.Context) (int64, error) {
	return 0, nil
}
func (stubStore) ListDownloadEvents(context.Context, outbox.EventQuery) ([]outbox.Event, error) {
	return nil, nil
}
func (stubStore) GetCategory(context.Context, string) (domain.Category, error) {
	return domain.Category{}, store.ErrNotFound
}

func (stubStore) CreateSubmission(context.Context, domain.Submission) (domain.Download, bool, error) {
	return domain.Download{}, false, nil
}

// pendingResponseWriter records handler writes while serializing access to the
// underlying recorder.
type pendingResponseWriter struct {
	recorder *httptest.ResponseRecorder
	mu       sync.Mutex
	wrote    bool
}

func (w *pendingResponseWriter) Header() http.Header {
	return w.recorder.Header()
}

func (w *pendingResponseWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wrote = true
	return w.recorder.Write(body)
}

func (w *pendingResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wrote = true
	w.recorder.WriteHeader(status)
}

func (w *pendingResponseWriter) didWrite() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wrote
}

// pendingRequest is an authenticated request running in a goroutine with a
// cancellable context, so tests can observe in-flight waits deterministically.
type pendingRequest struct {
	recorder *httptest.ResponseRecorder
	writer   *pendingResponseWriter
	cancel   context.CancelFunc
	done     <-chan struct{}
}

// serveAsync starts an authenticated GET request and returns handles for
// deterministic synchronization. The recorder is unmodified until done.
func (h *nativeHarness) serveAsync(t *testing.T, target string) *pendingRequest {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+string(h.secret))
	recorder := httptest.NewRecorder()
	writer := &pendingResponseWriter{recorder: recorder}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.ServeHTTP(writer, request)
	}()
	return &pendingRequest{recorder: recorder, writer: writer, cancel: cancel, done: done}
}

// finish waits for the request to complete, failing the test after a bounded
// deadline so a hung waiter is reported instead of stalling the suite.
func (p *pendingRequest) finish(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.cancel()
		t.Fatal("request did not finish within 5s")
	}
}

// eventually polls condition until it holds or timeout elapses.
func eventually(t *testing.T, timeout time.Duration, message string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

// scriptedStore wraps a real store so tests can count the handler's queries
// and force per-call outcomes, making observation-round races deterministic.
type scriptedStore struct {
	repo   Store
	signal *outbox.Signal
	mu     sync.Mutex
	counts map[string]int
	notify map[string]bool           // hash: notify the signal once before the first query returns
	script map[string][]scriptedCall // hash: per-call override; zero value = passthrough
	// corruptEvents replaces the first event list with one invalid-payload
	// event, simulating storage corruption.
	corruptEvents bool
}

type scriptedCall struct {
	download domain.Download
	err      error
}

func newScriptedStore(repo Store, signal *outbox.Signal) *scriptedStore {
	return &scriptedStore{repo: repo, signal: signal, counts: map[string]int{}, notify: map[string]bool{}, script: map[string][]scriptedCall{}}
}

func (s *scriptedStore) GetDownload(ctx context.Context, hash string) (domain.Download, error) {
	s.mu.Lock()
	s.counts[hash]++
	call := s.counts[hash] - 1
	doNotify := s.notify[hash]
	s.notify[hash] = false
	scripted := s.script[hash]
	override := scriptedCall{}
	if call < len(scripted) {
		override = scripted[call]
	}
	s.mu.Unlock()
	if doNotify {
		s.signal.Notify()
	}
	if call < len(scripted) && (override.err != nil || override.download.Hash != "" || override.download.State != "") {
		return override.download, override.err
	}
	return s.repo.GetDownload(ctx, hash)
}

func (s *scriptedStore) LatestEventSequence(ctx context.Context) (int64, error) {
	s.mu.Lock()
	s.counts["sequence"]++
	s.mu.Unlock()
	return s.repo.LatestEventSequence(ctx)
}

func (s *scriptedStore) ListDownloadEvents(ctx context.Context, query outbox.EventQuery) ([]outbox.Event, error) {
	s.mu.Lock()
	s.counts["events"]++
	doNotify := s.notify["events"]
	s.notify["events"] = false
	corrupt := s.corruptEvents
	s.corruptEvents = false
	s.mu.Unlock()
	if doNotify {
		s.signal.Notify()
	}
	if corrupt {
		return []outbox.Event{{Sequence: 1, Payload: []byte("{not-json")}}, nil
	}
	return s.repo.ListDownloadEvents(ctx, query)
}

func (s *scriptedStore) count(hash string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[hash]
}

func (s *scriptedStore) notifyOnce(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify[hash] = true
}

func (s *scriptedStore) scriptCalls(hash string, calls ...scriptedCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script[hash] = calls
}

func waitTarget(hash string) string { return "/api/v1/downloads/" + hash + "/wait" }

// TestNativeWaitTerminalBeforeCall covers all four terminal outcomes: an
// already-terminal row answers 200 with the exact query model immediately.
func TestNativeWaitTerminalBeforeCall(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	now := harness.clock.now
	cases := []struct {
		name    string
		hash    string
		advance func(t *testing.T, hash string)
		state   string
		outcome string
	}{
		{"completed", "1111111111111111111111111111111111111111", func(t *testing.T, hash string) { advanceToCompleted(t, harness.repository, now, hash) }, "COMPLETED", "completed"},
		{"failed", "1212121212121212121212121212121212121212", func(t *testing.T, hash string) {
			claimTransition(t, harness.repository, now.Add(time.Hour), domain.StateFailed)
		}, "FAILED", "failed"},
		{"cancelled", "1313131313131313131313131313131313131313", func(t *testing.T, hash string) {
			if err := harness.repository.Cancel(context.Background(), hash, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			claimTransition(t, harness.repository, now.Add(2*time.Hour), domain.StateCancelled)
		}, "CANCELLED", "cancelled"},
		{"deleted", "1414141414141414141414141414141414141414", func(t *testing.T, hash string) {
			if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			claimTransition(t, harness.repository, now.Add(2*time.Hour), domain.StateDeleted)
		}, "DELETED", "deleted"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			hash := test.hash
			if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Wait"}); response.Code != http.StatusCreated {
				t.Fatalf("submit = %d %q", response.Code, response.Body.String())
			}
			test.advance(t, hash)
			waited := harness.get(t, waitTarget(hash))
			if waited.Code != http.StatusOK {
				t.Fatalf("wait = %d %q, want 200", waited.Code, waited.Body.String())
			}
			model := decodeObject(t, waited)
			if model["state"] != test.state || model["terminal"] != true || model["outcome"] != test.outcome {
				t.Fatalf("wait model = %#v, want state %s outcome %s", model, test.state, test.outcome)
			}
			// The wait answer is the exact existing query model, byte for byte.
			queried := harness.get(t, "/api/v1/downloads/"+hash)
			if queried.Code != http.StatusOK || queried.Body.String() != waited.Body.String() {
				t.Fatalf("wait body %q differs from GET body %q", waited.Body.String(), queried.Body.String())
			}
		})
	}
}

// TestNativeWaitTimeoutParameters pins the strict timeout query contract:
// only one optional "timeout" Go duration in [1s, 25s], everything else 400,
// with the default and both bounds accepted on a terminal row.
func TestNativeWaitTimeoutParameters(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "1616161616161616161616161616161616161616"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Params"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}

	cases := []struct {
		name   string
		target string
	}{
		{"empty", waitTarget(hash) + "?timeout="},
		{"invalid", waitTarget(hash) + "?timeout=abc"},
		{"negative", waitTarget(hash) + "?timeout=-1s"},
		{"below min", waitTarget(hash) + "?timeout=999ms"},
		{"above max", waitTarget(hash) + "?timeout=30s"},
		{"duplicate", waitTarget(hash) + "?timeout=1s&timeout=2s"},
		{"unknown parameter", waitTarget(hash) + "?foo=1"},
		{"bad value unknown", waitTarget(hash) + "?timeout=1s&foo=1"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			requireErrorBody(t, harness.get(t, test.target), http.StatusBadRequest, invalidRequestJSON)
		})
	}

	// A terminal row answers 200 instantly, so both bounds and the default
	// prove acceptance without waiting out a full timeout.
	advanceToCompleted(t, harness.repository, harness.clock.now, hash)
	for _, target := range []string{waitTarget(hash), waitTarget(hash) + "?timeout=1s", waitTarget(hash) + "?timeout=25s"} {
		if response := harness.get(t, target); response.Code != http.StatusOK {
			t.Errorf("wait %s = %d %q, want 200", target, response.Code, response.Body.String())
		}
	}
}

// TestNativeWaitRouteMethodAndNotFound covers the wait sub-resource routing:
// only GET on {hash}/wait, canonical hash syntax, and 404 for unknown rows.
func TestNativeWaitRouteMethodAndNotFound(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "1717171717171717171717171717171717171717"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Routes"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		response := harness.request(t, method, waitTarget(hash), "", nil)
		requireErrorBody(t, response, http.StatusMethodNotAllowed, methodNotAllowedJSON)
		if got := response.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("Allow = %q, want GET", got)
		}
	}

	cases := []struct {
		name   string
		target string
		status int
		body   string
	}{
		{"malformed hash", "/api/v1/downloads/not-a-hash/wait", http.StatusBadRequest, invalidRequestJSON},
		{"never existed", waitTarget("9999999999999999999999999999999999999999"), http.StatusNotFound, downloadNotFoundJSON},
		{"nested extra", "/api/v1/downloads/" + hash + "/wait/extra", http.StatusNotFound, downloadNotFoundJSON},
		{"hash extra", "/api/v1/downloads/" + hash + "/extra", http.StatusNotFound, downloadNotFoundJSON},
		{"valid timeout missing row", waitTarget("9999999999999999999999999999999999999999") + "?timeout=1s", http.StatusNotFound, downloadNotFoundJSON},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			requireErrorBody(t, harness.get(t, test.target), test.status, test.body)
		})
	}
}

// TestNativeWaitTimeoutReturns204ForNonterminal drives a real 1s timeout on a
// STOPPED row: 204, empty body, no-store, latest nonterminal version, and no
// JSON content type.
func TestNativeWaitTimeoutReturns204ForNonterminal(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "1818181818181818181818181818181818181818"
	// STOPPED is nonterminal regardless of row_version.
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Stopped", "stopped": true}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	start := time.Now()
	waited := harness.get(t, waitTarget(hash)+"?timeout=1s")
	elapsed := time.Since(start)
	if waited.Code != http.StatusNoContent {
		t.Fatalf("wait = %d %q, want 204", waited.Code, waited.Body.String())
	}
	if waited.Body.Len() != 0 {
		t.Errorf("204 body = %q, want empty", waited.Body.String())
	}
	if got := waited.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := waited.Header().Get(versionHeader); got != "0" {
		t.Errorf("%s = %q, want 0", versionHeader, got)
	}
	if got := waited.Header().Get("Content-Type"); got != "" {
		t.Errorf("Content-Type = %q, want empty on 204", got)
	}
	if elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("timeout answered after %v, want ~1s", elapsed)
	}
}

// TestNativeWaitStoppedRemainsPendingUntilCancelled proves a nonterminal
// STOPPED row keeps the request waiting on the default 25s deadline instead
// of answering early, and that a cancelled request returns without writing.
func TestNativeWaitStoppedRemainsPendingUntilCancelled(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "1919191919191919191919191919191919191919"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Default", "stopped": true}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	pending := harness.serveAsync(t, waitTarget(hash))
	time.Sleep(50 * time.Millisecond)
	select {
	case <-pending.done:
		t.Fatalf("default wait completed early")
	default:
	}
	pending.cancel()
	pending.finish(t)
	if pending.writer.didWrite() {
		t.Errorf("cancelled wait wrote a response, want no response")
	}
}
func TestNativeWaitRequestCancellation(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "2020202020202020202020202020202020202020"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Cancel"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	pending := harness.serveAsync(t, waitTarget(hash)+"?timeout=25s")
	time.Sleep(50 * time.Millisecond)
	pending.cancel()
	pending.finish(t)
	if pending.writer.didWrite() {
		t.Errorf("cancelled wait wrote a response, want no response")
	}
}

// TestNativeWaitTransitionAfterWait covers a terminal transition committing
// while a waiter is parked: the commit's notify wakes the waiter, whose next
// authoritative read answers 200 with the terminal model.
func TestNativeWaitTransitionAfterWait(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	now := harness.clock.now
	hash := "2121212121212121212121212121212121212121"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Transition"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	pending := harness.serveAsync(t, waitTarget(hash))
	// The waiter's first authoritative read completed; it is now parked.
	eventually(t, 2*time.Second, "waiter never queried", func() bool { return script.count(hash) >= 1 })
	advanceToCompleted(t, harness.repository, now, hash)
	pending.finish(t)
	if pending.recorder.Code != http.StatusOK {
		t.Fatalf("wait = %d %q, want 200 after transition", pending.recorder.Code, pending.recorder.Body.String())
	}
	model := decodeObject(t, pending.recorder)
	if model["state"] != "COMPLETED" || model["outcome"] != "completed" || model["terminal"] != true {
		t.Fatalf("wait model = %#v, want completed", model)
	}
}

// TestNativeWaitCommitBetweenSnapshotAndQuery covers the signal race: a
// notify landing between the waiter's snapshot and its authoritative query
// must be observed, forcing an immediate re-query without any further signal.
func TestNativeWaitCommitBetweenSnapshotAndQuery(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	hash := "2222222222222222222222222222222222222222"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Race"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	// The first query closes the snapshot the handler took before it, so the
	// closed-channel wake must trigger a second round without any new signal.
	script.notifyOnce(hash)
	pending := harness.serveAsync(t, waitTarget(hash))
	eventually(t, 2*time.Second, "missed closed-snapshot wake: no re-query", func() bool { return script.count(hash) >= 2 })
	pending.cancel()
	pending.finish(t)
	if pending.writer.didWrite() {
		t.Errorf("cancelled wait wrote a response, want no response")
	}
}

// TestNativeWaitUnrelatedWakeDoesNotExtendDeadline proves the deadline is a
// fixed absolute bound: unrelated notifications wake and re-query but the
// 1s timeout still fires on schedule instead of restarting.
func TestNativeWaitUnrelatedWakeDoesNotExtendDeadline(t *testing.T) {
	t.Parallel()
	harness := newNativeHarness(t)
	hash := "2323232323232323232323232323232323232323"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Wake"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	start := time.Now()
	waited := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		waited <- harness.get(t, waitTarget(hash)+"?timeout=1s")
	}()
	time.Sleep(100 * time.Millisecond)
	harness.signal.Notify()
	time.Sleep(600 * time.Millisecond)
	harness.signal.Notify()
	response := <-waited
	elapsed := time.Since(start)
	if response.Code != http.StatusNoContent {
		t.Fatalf("wait = %d %q, want 204", response.Code, response.Body.String())
	}
	// A deadline restarted by either unrelated wake would fire at ~1.7s.
	if elapsed < 900*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Errorf("timeout answered after %v, want fixed ~1s deadline", elapsed)
	}
}

// TestNativeWaitBroadcastWakesAllWaiters covers one signal waking every
// waiter parked on the same snapshot: two concurrent waits both answer 200
// after a single terminal commit.
func TestNativeWaitBroadcastWakesAllWaiters(t *testing.T) {
	t.Parallel()
	var script *scriptedStore
	harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
		script = newScriptedStore(repository, signal)
		return script
	})
	now := harness.clock.now
	hash := "2424242424242424242424242424242424242424"
	if response := harness.postJSON(t, map[string]any{"magnet": "magnet:?xt=urn:btih:" + hash + "&dn=Broadcast"}); response.Code != http.StatusCreated {
		t.Fatalf("submit = %d %q", response.Code, response.Body.String())
	}
	first := harness.serveAsync(t, waitTarget(hash))
	second := harness.serveAsync(t, waitTarget(hash))
	// Both waiters snapshotted before their first read and are now parked on
	// the same channel; one commit closes it for both.
	eventually(t, 2*time.Second, "waiters never both queried", func() bool { return script.count(hash) >= 2 })
	advanceToCompleted(t, harness.repository, now, hash)
	first.finish(t)
	second.finish(t)
	for name, pending := range map[string]*pendingRequest{"first": first, "second": second} {
		if pending.recorder.Code != http.StatusOK {
			t.Fatalf("%s wait = %d %q, want 200", name, pending.recorder.Code, pending.recorder.Body.String())
		}
		if model := decodeObject(t, pending.recorder); model["state"] != "COMPLETED" || model["outcome"] != "completed" {
			t.Errorf("%s wait model = %#v, want completed", name, model)
		}
	}
}

// TestNativeWaitLifecycleShutdownFinalRead covers the runtime retirement
// path: closing the shutdown channel performs the same final authoritative
// read as a timeout (terminal 200, missing 404, failure 500, otherwise 204
// with the observed nonterminal version) and never waits out the deadline.
func TestNativeWaitLifecycleShutdownFinalRead(t *testing.T) {
	t.Parallel()
	completedAt := harnessTime.Add(time.Hour)
	terminal := domain.Download{
		Hash: "2525252525252525252525252525252525252525", Name: "Done", SourceKind: domain.SourceMagnet,
		State: domain.StateCompleted, RowVersion: 7, CreatedAt: harnessTime, UpdatedAt: harnessTime, CompletedAt: &completedAt,
	}
	cases := []struct {
		name   string
		script func(t *testing.T, repository *store.Store, signal *outbox.Signal, script *scriptedStore)
		hash   string
		status int
		check  func(t *testing.T, response *httptest.ResponseRecorder)
	}{
		{
			name: "nonterminal answers 204 with latest version",
			script: func(t *testing.T, repository *store.Store, signal *outbox.Signal, script *scriptedStore) {
				if response := harnessPost(t, repository, "2626262626262626262626262626262626262626", harnessTime); response != http.StatusCreated {
					t.Fatalf("submit = %d", response)
				}
				if err := repository.Cancel(context.Background(), "2626262626262626262626262626262626262626", harnessTime.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			},
			hash:   "2626262626262626262626262626262626262626",
			status: http.StatusNoContent,
			check: func(t *testing.T, response *httptest.ResponseRecorder) {
				if response.Body.Len() != 0 {
					t.Errorf("204 body = %q, want empty", response.Body.String())
				}
				if got := response.Header().Get(versionHeader); got != "1" {
					t.Errorf("%s = %q, want 1", versionHeader, got)
				}
				if got := response.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
			},
		},
		{
			name: "terminal at final read answers 200",
			script: func(t *testing.T, repository *store.Store, signal *outbox.Signal, script *scriptedStore) {
				hash := "2525252525252525252525252525252525252525"
				if response := harnessPost(t, repository, hash, harnessTime); response != http.StatusCreated {
					t.Fatalf("submit = %d", response)
				}
				script.scriptCalls(hash, scriptedCall{}, scriptedCall{download: terminal})
			},
			hash:   "2525252525252525252525252525252525252525",
			status: http.StatusOK,
			check: func(t *testing.T, response *httptest.ResponseRecorder) {
				model := decodeObject(t, response)
				if model["state"] != "COMPLETED" || model["outcome"] != "completed" || model["terminal"] != true || model["version"] != float64(7) {
					t.Fatalf("final read model = %#v, want completed v7", model)
				}
			},
		},
		{
			name: "missing at final read answers 404",
			script: func(t *testing.T, repository *store.Store, signal *outbox.Signal, script *scriptedStore) {
				hash := "2727272727272727272727272727272727272727"
				if response := harnessPost(t, repository, hash, harnessTime); response != http.StatusCreated {
					t.Fatalf("submit = %d", response)
				}
				script.scriptCalls(hash, scriptedCall{}, scriptedCall{err: store.ErrNotFound})
			},
			hash:   "2727272727272727272727272727272727272727",
			status: http.StatusNotFound,
			check: func(t *testing.T, response *httptest.ResponseRecorder) {
				requireErrorBody(t, response, http.StatusNotFound, downloadNotFoundJSON)
			},
		},
		{
			name: "store failure at final read answers 500",
			script: func(t *testing.T, repository *store.Store, signal *outbox.Signal, script *scriptedStore) {
				hash := "2828282828282828282828282828282828282828"
				if response := harnessPost(t, repository, hash, harnessTime); response != http.StatusCreated {
					t.Fatalf("submit = %d", response)
				}
				script.scriptCalls(hash, scriptedCall{}, scriptedCall{err: errors.New("boom")})
			},
			hash:   "2828282828282828282828282828282828282828",
			status: http.StatusInternalServerError,
			check: func(t *testing.T, response *httptest.ResponseRecorder) {
				requireErrorBody(t, response, http.StatusInternalServerError, internalBody)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var script *scriptedStore
			harness := newNativeHarnessWith(t, func(repository *store.Store, signal *outbox.Signal) Store {
				script = newScriptedStore(repository, signal)
				return script
			})
			test.script(t, harness.repository, harness.signal, script)
			pending := harness.serveAsync(t, waitTarget(test.hash))
			eventually(t, 2*time.Second, "waiter never queried", func() bool { return script.count(test.hash) >= 1 })
			close(harness.shutdown)
			pending.finish(t)
			if pending.recorder.Code != test.status {
				t.Fatalf("wait = %d %q, want %d", pending.recorder.Code, pending.recorder.Body.String(), test.status)
			}
			test.check(t, pending.recorder)
		})
	}
}

var harnessTime = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// harnessPost seeds an accepted download row directly, returning the status
// a submission would have produced.
func harnessPost(t *testing.T, repository *store.Store, hash string, now time.Time) int {
	t.Helper()
	if _, _, err := repository.CreateSubmission(context.Background(), domain.Submission{
		Download: domain.Download{
			Hash: hash, Name: "Wait", SourceKind: domain.SourceMagnet, SubmissionURI: "magnet:?xt=urn:btih:" + hash,
			CloudFolder: "/cloud", SavePath: "/local", TotalSize: 1, State: domain.StateAccepted,
			PhaseStartedAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	return http.StatusCreated
}
