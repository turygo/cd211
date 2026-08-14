package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

// testUpdateRow mirrors the exact downloadUpdateRow JSON contract.
type testUpdateRow struct {
	Hash       string `json:"hash"`
	RowVersion int64  `json:"row_version"`
	State      string `json:"state"`
	Terminal   bool   `json:"terminal"`
	HTML       string `json:"html"`
}

// fixtureRequest issues an authenticated request with custom headers.
func fixtureRequest(fixture *webFixture, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	fixture.t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: "SID", Value: fixture.sid})
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

// advanceTo moves a single seeded download to a new durable state through the
// real store, mirroring the reconciler.
func advanceTo(t *testing.T, fixture *webFixture, hash string, to domain.State) domain.Download {
	t.Helper()
	claim, err := fixture.store.ClaimDue(context.Background(), "advance", fixture.clock.now, time.Minute)
	if err != nil || claim == nil || claim.Download.Hash != hash {
		t.Fatalf("ClaimDue(): claim=%+v err=%v", claim, err)
	}
	next := claim.Download
	next.State = to
	next.UpdatedAt = fixture.clock.now.Add(time.Second)
	next.PhaseStartedAt = next.UpdatedAt
	next.NextRunAt = nil
	if to == domain.StateCompleted {
		completed := next.UpdatedAt
		next.CompletedAt = &completed
		next.OfflineProgress = 1
		next.CopyProgress = 1
		next.ContentPath = filepath.Join(next.SavePath, next.Name)
		next.QbitProgress = 1
	}
	if err := fixture.store.CommitClaim(context.Background(), *claim, next); err != nil {
		t.Fatalf("CommitClaim(): %v", err)
	}
	stored, err := fixture.store.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetDownload(): %v", err)
	}
	return stored
}

// assertExactKeys verifies the JSON carries exactly the given contract keys.
func assertExactKeys(t *testing.T, body []byte, wantKeys ...string) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("invalid JSON: %v; body=%s", err, body)
	}
	want := make(map[string]bool, len(wantKeys))
	for _, key := range wantKeys {
		want[key] = true
	}
	if len(raw) != len(want) {
		t.Errorf("JSON keys = %v, want exactly %v", keysOf(raw), wantKeys)
	}
	for key := range raw {
		if !want[key] {
			t.Errorf("unexpected JSON key %q", key)
		}
	}
	return raw
}

func keysOf(raw map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	return keys
}

func decodeRows(t *testing.T, body []byte) []testUpdateRow {
	t.Helper()
	var payload struct {
		Rows []testUpdateRow `json:"rows"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	return payload.Rows
}

func TestDownloadsUpdatesAuthenticationAndMethods(t *testing.T) {
	fixture := newWebFixture(t)
	hash := strings.Repeat("a", 40)
	fixture.seedDownload("a", domain.StateAccepted, nil)

	for _, target := range []string{"/downloads/updates", "/downloads/" + hash + "/updates"} {
		unauthenticated := fixture.request(http.MethodGet, target, nil, false)
		requireStatus(t, unauthenticated, http.StatusSeeOther)
		if location := unauthenticated.Header().Get("Location"); location != "/login" {
			t.Errorf("%s unauthenticated Location = %q, want /login", target, location)
		}
		post := fixture.request(http.MethodPost, target, nil, true)
		requireStatus(t, post, http.StatusMethodNotAllowed)
	}
}

func TestDownloadsUpdatesSnapshotContract(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedCategory("movies", true)
	active := fixture.seedDownload("a", domain.StateAccepted, func(download *domain.Download) {
		download.Category = "movies"
		download.SubmissionURI = "magnet:?xt=urn:btih:" + download.Hash + "&tr=https://tracker.invalid/secret-token"
	})
	fixture.seedDownload("b", domain.StateWaitingCopy, func(download *domain.Download) {
		download.Category = "movies"
	})
	fixture.seedDownload("c", domain.StateCompleted, func(download *domain.Download) {
		download.Category = "movies"
	})

	response := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?category=movies", nil)
	requireStatus(t, response, http.StatusOK)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if response.Header().Get("ETag") == "" {
		t.Error("ETag header missing")
	}
	body := response.Body.Bytes()
	requireAbsent(t, string(body), "magnet:?", "tracker.invalid", "secret-token", fixture.sid)
	assertExactKeys(t, body,
		"rows", "rows_exited", "total_rows", "page_start", "page_end",
		"page_number", "total_pages", "has_active", "pagination_html", "empty_html")

	var payload struct {
		Rows           []testUpdateRow `json:"rows"`
		RowsExited     []testUpdateRow `json:"rows_exited"`
		TotalRows      int             `json:"total_rows"`
		PageStart      int             `json:"page_start"`
		PageEnd        int             `json:"page_end"`
		PageNumber     int             `json:"page_number"`
		TotalPages     int             `json:"total_pages"`
		HasActive      bool            `json:"has_active"`
		PaginationHTML string          `json:"pagination_html"`
		EmptyHTML      string          `json:"empty_html"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.TotalRows != 3 || payload.PageStart != 1 || payload.PageEnd != 3 ||
		payload.PageNumber != 1 || payload.TotalPages != 1 || !payload.HasActive {
		t.Errorf("snapshot = %d rows %d-%d page %d/%d active %t",
			payload.TotalRows, payload.PageStart, payload.PageEnd, payload.PageNumber, payload.TotalPages, payload.HasActive)
	}
	if !strings.Contains(payload.PaginationHTML, `class="pagination"`) || !strings.Contains(payload.PaginationHTML, "1–3 of 3") {
		t.Errorf("pagination_html = %q", payload.PaginationHTML)
	}
	if payload.EmptyHTML != "" {
		t.Errorf("empty_html = %q, want empty with rows", payload.EmptyHTML)
	}
	if len(payload.RowsExited) != 0 {
		t.Errorf("rows_exited = %+v, want []", payload.RowsExited)
	}

	stored, err := fixture.store.GetDownload(context.Background(), active.Hash)
	if err != nil {
		t.Fatal(err)
	}
	rows := decodeRows(t, body)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	byHash := map[string]testUpdateRow{}
	for _, row := range rows {
		byHash[row.Hash] = row
	}
	row := byHash[active.Hash]
	if row.RowVersion != stored.RowVersion {
		t.Errorf("row_version = %d, want durable %d", row.RowVersion, stored.RowVersion)
	}
	if row.State != string(domain.StateAccepted) || row.Terminal {
		t.Errorf("state/terminal = %s/%t", row.State, row.Terminal)
	}
	if !strings.Contains(row.HTML, `data-download-hash="`+active.Hash+`"`) ||
		!strings.Contains(row.HTML, `data-row-version="`+fmt.Sprintf("%d", stored.RowVersion)+`"`) ||
		!strings.Contains(row.HTML, `data-state="ACCEPTED"`) {
		t.Errorf("row html lacks keyed metadata: %s", row.HTML)
	}
	requireAbsent(t, row.HTML, "magnet:?", "tracker.invalid", "secret-token")
}

func TestDownloadsUpdatesTerminalOnlyAndEmpty(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedDownload("c", domain.StateCompleted, nil)
	fixture.seedDownload("f", domain.StateFailed, nil)

	response := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?view=completed", nil)
	requireStatus(t, response, http.StatusOK)
	var payload struct {
		HasActive bool `json:"has_active"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HasActive {
		t.Error("has_active = true for a terminal-only snapshot")
	}

	empty := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?view=completed&category=nope", nil)
	requireStatus(t, empty, http.StatusOK)
	var emptyPayload struct {
		Rows      []testUpdateRow `json:"rows"`
		EmptyHTML string          `json:"empty_html"`
		TotalRows int             `json:"total_rows"`
		HasActive bool            `json:"has_active"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if len(emptyPayload.Rows) != 0 || emptyPayload.TotalRows != 0 || emptyPayload.HasActive {
		t.Errorf("empty snapshot = %+v", emptyPayload)
	}
	if !strings.Contains(emptyPayload.EmptyHTML, "No downloads match this filter.") {
		t.Errorf("empty_html = %q", emptyPayload.EmptyHTML)
	}
}

func TestDownloadsUpdatesETagAnd304(t *testing.T) {
	fixture := newWebFixture(t)
	hash := strings.Repeat("a", 40)
	fixture.seedDownload("a", domain.StateAccepted, func(download *domain.Download) {
		nextRun := fixture.clock.now
		download.NextRunAt = &nextRun
	})

	first := fixtureRequest(fixture, http.MethodGet, "/downloads/updates", nil)
	requireStatus(t, first, http.StatusOK)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	notModified := fixtureRequest(fixture, http.MethodGet, "/downloads/updates", map[string]string{"If-None-Match": etag})
	requireStatus(t, notModified, http.StatusNotModified)
	if notModified.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", notModified.Body.String())
	}
	if got := notModified.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %q, want %q", got, etag)
	}

	// A weak-form echo must also match.
	weak := fixtureRequest(fixture, http.MethodGet, "/downloads/updates", map[string]string{"If-None-Match": "W/" + etag})
	requireStatus(t, weak, http.StatusNotModified)

	// A durable change invalidates the ETag.
	advanceTo(t, fixture, hash, domain.StateSubmittingOffline)
	changed := fixtureRequest(fixture, http.MethodGet, "/downloads/updates", map[string]string{"If-None-Match": etag})
	requireStatus(t, changed, http.StatusOK)
	nextETag := changed.Header().Get("ETag")
	if nextETag == "" || nextETag == etag {
		t.Errorf("ETag after change = %q, want different from %q", nextETag, etag)
	}
}

func TestDownloadsUpdatesQuerySemantics(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedCategory("movies", true)
	fixture.seedDownload("a", domain.StateAccepted, func(download *domain.Download) { download.Category = "movies" })
	fixture.seedDownload("b", domain.StateWaitingCopy, func(download *domain.Download) {
		download.Category = ""
	})
	fixture.seedDownload("c", domain.StateCompleted, func(download *domain.Download) { download.Category = "movies" })

	active := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?view=active", nil)
	requireStatus(t, active, http.StatusOK)
	activeRows := decodeRows(t, active.Body.Bytes())
	if len(activeRows) != 2 {
		t.Errorf("view=active rows = %d, want 2", len(activeRows))
	}
	for _, row := range activeRows {
		if row.State == string(domain.StateCompleted) {
			t.Errorf("view=active included completed row %s", row.Hash)
		}
	}

	category := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?category=movies", nil)
	requireStatus(t, category, http.StatusOK)
	if rows := decodeRows(t, category.Body.Bytes()); len(rows) != 2 {
		t.Errorf("category rows = %d, want 2", len(rows))
	}

	search := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?q=release-b", nil)
	requireStatus(t, search, http.StatusOK)
	if rows := decodeRows(t, search.Body.Bytes()); len(rows) != 1 || rows[0].Hash != strings.Repeat("b", 40) {
		t.Errorf("q rows = %+v", rows)
	}

	for _, target := range []string{
		"/downloads/updates?view=pending",
		"/downloads/updates?q=" + strings.Repeat("x", 201),
		"/downloads/updates?page=0",
		"/downloads/updates?page=abc",
	} {
		requireStatus(t, fixtureRequest(fixture, http.MethodGet, target, nil), http.StatusBadRequest)
	}
}

func TestDownloadsUpdatesKnownValidation(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedDownload("a", domain.StateAccepted, nil)
	valid := strings.Repeat("a", 40) + ":1"

	for _, target := range []string{
		"/downloads/updates?known=short:1",
		"/downloads/updates?known=" + strings.Repeat("z", 40) + ":1",
		"/downloads/updates?known=" + valid + "&known=" + valid, // duplicate
		"/downloads/updates?known=" + valid + "&known=" + strings.Repeat("b", 40) + ":x",
		"/downloads/updates?known=" + valid + "&known=" + strings.Repeat("b", 40) + ":-1",
	} {
		requireStatus(t, fixtureRequest(fixture, http.MethodGet, target, nil), http.StatusBadRequest)
	}

	overLimit := make([]string, 26)
	for i := range overLimit {
		overLimit[i] = "known=" + strings.Repeat(string(rune('a'+i%23)), 40) + ":1"
	}
	requireStatus(t, fixtureRequest(fixture, http.MethodGet, "/downloads/updates?"+strings.Join(overLimit, "&"), nil), http.StatusBadRequest)
}

func TestDownloadsUpdatesRowsExited(t *testing.T) {
	fixture := newWebFixture(t)
	hash := strings.Repeat("a", 40)
	before := fixture.seedDownload("a", domain.StateVerifyingLocal, func(download *domain.Download) {
		nextRun := fixture.clock.now
		download.NextRunAt = &nextRun
	})
	completed := advanceTo(t, fixture, hash, domain.StateCompleted)

	response := fixtureRequest(fixture, http.MethodGet,
		"/downloads/updates?view=active&known="+hash+":"+fmt.Sprintf("%d", before.RowVersion), nil)
	requireStatus(t, response, http.StatusOK)
	var payload struct {
		Rows       []testUpdateRow `json:"rows"`
		RowsExited []testUpdateRow `json:"rows_exited"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Rows) != 0 {
		t.Errorf("active rows after completion = %+v, want none", payload.Rows)
	}
	if len(payload.RowsExited) != 1 {
		t.Fatalf("rows_exited = %+v, want the completed row", payload.RowsExited)
	}
	exit := payload.RowsExited[0]
	if exit.Hash != hash || exit.State != string(domain.StateCompleted) || !exit.Terminal {
		t.Errorf("rows_exited entry = %+v", exit)
	}
	if exit.RowVersion != completed.RowVersion {
		t.Errorf("rows_exited row_version = %d, want %d", exit.RowVersion, completed.RowVersion)
	}
	if !strings.Contains(exit.HTML, `data-download-hash="`+hash+`"`) ||
		!strings.Contains(exit.HTML, `data-state="COMPLETED"`) ||
		strings.Contains(exit.HTML, "stage-fill") {
		t.Errorf("rows_exited html lacks terminal fragment: %s", exit.HTML)
	}
}

func TestDownloadsUpdatesRowsExitedExcludesHiddenUnknownAndMembers(t *testing.T) {
	fixture := newWebFixture(t)
	hidden := fixture.seedDownload("d", domain.StateDeleteRequested, nil)
	unknown := strings.Repeat("b", 40)
	// A terminal row that is still a member of the current view (pushed to
	// another page by newer entries) must never be reported as exited.
	member := fixture.seedDownload("c", domain.StateCompleted, nil)

	response := fixtureRequest(fixture, http.MethodGet,
		"/downloads/updates?known="+hidden.Hash+":1&known="+unknown+":1&known="+member.Hash+":1", nil)
	requireStatus(t, response, http.StatusOK)
	var payload struct {
		RowsExited []testUpdateRow `json:"rows_exited"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.RowsExited) != 0 {
		t.Errorf("rows_exited = %+v, want none for hidden/missing/member hashes", payload.RowsExited)
	}
}

func TestDownloadsUpdatesPaginationCapsRows(t *testing.T) {
	fixture := newWebFixture(t)
	for index := 1; index <= 30; index++ {
		hash := fmt.Sprintf("%040x", index)
		fixture.seedDownload(hash, domain.StateStopped, func(download *domain.Download) {
			download.Name = "page-release-" + hash[:2]
		})
	}

	first := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?q=page-release", nil)
	requireStatus(t, first, http.StatusOK)
	var firstPayload struct {
		Rows       []testUpdateRow `json:"rows"`
		TotalRows  int             `json:"total_rows"`
		TotalPages int             `json:"total_pages"`
		PageEnd    int             `json:"page_end"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if len(firstPayload.Rows) != 25 || firstPayload.TotalRows != 30 ||
		firstPayload.TotalPages != 2 || firstPayload.PageEnd != 25 {
		t.Errorf("first page = rows:%d total:%d pages:%d end:%d",
			len(firstPayload.Rows), firstPayload.TotalRows, firstPayload.TotalPages, firstPayload.PageEnd)
	}

	second := fixtureRequest(fixture, http.MethodGet, "/downloads/updates?q=page-release&page=2", nil)
	requireStatus(t, second, http.StatusOK)
	var secondPayload struct {
		Rows []testUpdateRow `json:"rows"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
		t.Fatal(err)
	}
	if len(secondPayload.Rows) != 5 {
		t.Errorf("second page rows = %d, want 5", len(secondPayload.Rows))
	}
}

func TestDetailUpdatesContract(t *testing.T) {
	fixture := newWebFixture(t)
	download := fixture.seedDownload("a", domain.StateWaitingCopy, func(download *domain.Download) {
		download.SubmissionURI = "magnet:?xt=urn:btih:" + download.Hash + "&tr=https://tracker.invalid/secret-token"
	})

	response := fixtureRequest(fixture, http.MethodGet, "/downloads/"+download.Hash+"/updates", nil)
	requireStatus(t, response, http.StatusOK)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	body := response.Body.Bytes()
	requireAbsent(t, string(body), "magnet:?", "tracker.invalid", "secret-token", fixture.sid)
	assertExactKeys(t, body, "hash", "row_version", "state", "terminal", "html")

	var payload struct {
		Hash       string `json:"hash"`
		RowVersion int64  `json:"row_version"`
		State      string `json:"state"`
		Terminal   bool   `json:"terminal"`
		HTML       string `json:"html"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if payload.Hash != download.Hash || payload.RowVersion != download.RowVersion ||
		payload.State != string(domain.StateWaitingCopy) || payload.Terminal {
		t.Errorf("detail metadata = %+v", payload)
	}
	if !strings.Contains(payload.HTML, `data-live-detail`) ||
		!strings.Contains(payload.HTML, `data-hash="`+download.Hash+`"`) ||
		!strings.Contains(payload.HTML, `data-row-version="`+fmt.Sprintf("%d", download.RowVersion)+`"`) ||
		!strings.Contains(payload.HTML, `data-state="WAITING_COPY"`) ||
		!strings.Contains(payload.HTML, `data-state-label="`) ||
		!strings.Contains(payload.HTML, `data-live-route`) {
		t.Errorf("detail html lacks live-region hooks: %s", payload.HTML)
	}
	requireAbsent(t, payload.HTML, "magnet:?", "tracker.invalid", "secret-token")

	// 304 on the same ETag.
	etag := response.Header().Get("ETag")
	notModified := fixtureRequest(fixture, http.MethodGet, "/downloads/"+download.Hash+"/updates", map[string]string{"If-None-Match": etag})
	requireStatus(t, notModified, http.StatusNotModified)

	// Unknown hash -> 404, invalid hash -> 400, hidden download -> 404.
	requireStatus(t, fixtureRequest(fixture, http.MethodGet, "/downloads/"+strings.Repeat("b", 40)+"/updates", nil), http.StatusNotFound)
	requireStatus(t, fixtureRequest(fixture, http.MethodGet, "/downloads/short/updates", nil), http.StatusBadRequest)
	hidden := fixture.seedDownload("c", domain.StateDeleteRequested, nil)
	requireStatus(t, fixtureRequest(fixture, http.MethodGet, "/downloads/"+hidden.Hash+"/updates", nil), http.StatusNotFound)
}

func TestDetailUpdatesReflectsCompletion(t *testing.T) {
	fixture := newWebFixture(t)
	hash := strings.Repeat("a", 40)
	before := fixture.seedDownload("a", domain.StateVerifyingLocal, func(download *domain.Download) {
		nextRun := fixture.clock.now
		download.NextRunAt = &nextRun
	})
	advanceTo(t, fixture, hash, domain.StateCompleted)

	response := fixtureRequest(fixture, http.MethodGet, "/downloads/"+hash+"/updates", nil)
	requireStatus(t, response, http.StatusOK)
	var payload struct {
		RowVersion int64  `json:"row_version"`
		State      string `json:"state"`
		Terminal   bool   `json:"terminal"`
		HTML       string `json:"html"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != string(domain.StateCompleted) || !payload.Terminal || payload.RowVersion <= before.RowVersion {
		t.Errorf("detail after completion = %+v", payload)
	}
	if !strings.Contains(payload.HTML, `data-state="COMPLETED"`) ||
		strings.Contains(payload.HTML, "stage-fill") ||
		strings.Count(payload.HTML, `class="stage is-current"`) != 0 ||
		strings.Count(payload.HTML, `class="stage is-passed"`) != 3 {
		t.Errorf("completed detail html: %s", payload.HTML)
	}
}

func TestDownloadsLiveScriptHooksAndVersions(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedDownload("a", domain.StateAccepted, nil)

	list := fixture.request(http.MethodGet, "/", nil, true)
	requireStatus(t, list, http.StatusOK)
	listBody := list.Body.String()
	requireContains(t, listBody, `<script type="module" src="/static/downloads-live.js?v=2"></script>`)
	requireContains(t, listBody, `data-download-list`, `data-record-count`, `data-format="%d shown"`,
		`data-table-wrap`, `data-pagination`, `data-empty-state`, `data-live-announcer`)

	detail := fixture.request(http.MethodGet, "/downloads/"+strings.Repeat("a", 40), nil, true)
	requireStatus(t, detail, http.StatusOK)
	detailBody := detail.Body.String()
	requireContains(t, detailBody, `<script type="module" src="/static/downloads-live.js?v=2"></script>`)
	requireContains(t, detailBody, `data-live-detail`, `data-live-badge`, `data-live-route`,
		`data-live-actions`, `data-live-chronology`, `data-live-files`, `data-live-announcer`)
	requireAbsent(t, detailBody, "magnet:?", "tracker.invalid", "secret-token")
}
