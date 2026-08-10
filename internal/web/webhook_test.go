package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/outbox"
)

// fakeWebhookStore implements WebhookStore in memory for handler tests. It
// mirrors the contract: ordinary reads omit secrets, Create/Rotate return the
// HMAC secret exactly once, and List/Get never return BearerToken.
type fakeWebhookStore struct {
	mu          sync.Mutex
	endpoints   map[int64]outbox.Endpoint
	deliveries  map[int64]outbox.Delivery
	nextID      int64
	nextDelivID int64
	secret      string
	created     []outbox.EndpointInput
	updated     []fakeWebhookUpdate
	toggled     []fakeWebhookToggle
	rotated     []int64
	deleted     []int64
	tested      []int64
	replayed    []int64
	filters     []outbox.DeliveryFilter
	page        outbox.Page
	createErr   error
	updateErr   error
	listErr     error
	getErr      error
	enableErr   error
	rotateErr   error
	deleteErr   error
	deliverErr  error
	replayErr   error
	testErr     error
}

type fakeWebhookUpdate struct {
	id    int64
	input outbox.EndpointInput
}

type fakeWebhookToggle struct {
	id      int64
	enabled bool
}

func newFakeWebhookStore() *fakeWebhookStore {
	return &fakeWebhookStore{
		endpoints:  make(map[int64]outbox.Endpoint),
		deliveries: make(map[int64]outbox.Delivery),
		secret:     "test-hmac-secret",
		page:       outbox.Page{NextCursor: "c2VlLW5leHQ", HasMore: false},
	}
}

// publicEndpoint strips secrets for ordinary reads.
func (store *fakeWebhookStore) publicEndpoint(endpoint outbox.Endpoint) outbox.Endpoint {
	endpoint.HMACSecret = ""
	endpoint.BearerToken = ""
	return endpoint
}

func fakeDisplayURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery == "" {
		return raw
	}
	parsed.RawQuery = "…"
	return parsed.String()
}

func (store *fakeWebhookStore) CreateWebhookEndpoint(ctx context.Context, input outbox.EndpointInput, now time.Time) (outbox.Endpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.createErr != nil {
		return outbox.Endpoint{}, store.createErr
	}
	store.created = append(store.created, input)
	store.nextID++
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	endpoint := outbox.Endpoint{
		ID: store.nextID, Name: input.Name,
		URL: input.URL, DisplayURL: fakeDisplayURL(input.URL),
		HMACSecret:         store.secret + "-" + time.Now().Format("150405"),
		Enabled:            enabled,
		SubscribeCompleted: input.SubscribeCompleted,
		SubscribeFailed:    input.SubscribeFailed,
		CreatedAt:          now, UpdatedAt: now, RowVersion: 1,
	}
	store.endpoints[endpoint.ID] = endpoint
	return endpoint, nil
}

func (store *fakeWebhookStore) UpdateWebhookEndpoint(ctx context.Context, id int64, input outbox.EndpointInput, now time.Time) (outbox.Endpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.updateErr != nil {
		return outbox.Endpoint{}, store.updateErr
	}
	store.updated = append(store.updated, fakeWebhookUpdate{id: id, input: input})
	endpoint, ok := store.endpoints[id]
	if !ok || endpoint.DeletedAt != nil {
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	if input.Name != "" {
		endpoint.Name = input.Name
	}
	if input.URL != "" {
		endpoint.URL = input.URL
		endpoint.DisplayURL = fakeDisplayURL(input.URL)
	}
	endpoint.SubscribeCompleted = input.SubscribeCompleted
	endpoint.SubscribeFailed = input.SubscribeFailed
	if input.Enabled != nil {
		endpoint.Enabled = *input.Enabled
	}
	if input.ClearBearerToken {
		endpoint.BearerToken = ""
	} else if input.BearerToken != "" {
		endpoint.BearerToken = input.BearerToken
	}
	endpoint.UpdatedAt = now
	endpoint.RowVersion++
	store.endpoints[id] = endpoint
	return endpoint, nil
}

func (store *fakeWebhookStore) ListWebhookEndpoints(ctx context.Context) ([]outbox.Endpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.listErr != nil {
		return nil, store.listErr
	}
	var rows []outbox.Endpoint
	for _, endpoint := range store.endpoints {
		if endpoint.DeletedAt == nil {
			rows = append(rows, store.publicEndpoint(endpoint))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (store *fakeWebhookStore) GetWebhookEndpoint(ctx context.Context, id int64) (outbox.Endpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.getErr != nil {
		return outbox.Endpoint{}, store.getErr
	}
	endpoint, ok := store.endpoints[id]
	if !ok || endpoint.DeletedAt != nil {
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	return store.publicEndpoint(endpoint), nil
}

func (store *fakeWebhookStore) SetWebhookEndpointEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.enableErr != nil {
		return store.enableErr
	}
	store.toggled = append(store.toggled, fakeWebhookToggle{id: id, enabled: enabled})
	endpoint, ok := store.endpoints[id]
	if !ok || endpoint.DeletedAt != nil {
		return outbox.ErrNotFound
	}
	endpoint.Enabled = enabled
	endpoint.UpdatedAt = now
	endpoint.RowVersion++
	store.endpoints[id] = endpoint
	return nil
}

func (store *fakeWebhookStore) RotateWebhookEndpointSecret(ctx context.Context, id int64, now time.Time) (outbox.Endpoint, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.rotateErr != nil {
		return outbox.Endpoint{}, store.rotateErr
	}
	store.rotated = append(store.rotated, id)
	endpoint, ok := store.endpoints[id]
	if !ok || endpoint.DeletedAt != nil {
		return outbox.Endpoint{}, outbox.ErrNotFound
	}
	endpoint.HMACSecret = store.secret + "-rotated"
	endpoint.UpdatedAt = now
	endpoint.RowVersion++
	store.endpoints[id] = endpoint
	return endpoint, nil
}

func (store *fakeWebhookStore) DeleteWebhookEndpoint(ctx context.Context, id int64, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deleteErr != nil {
		return store.deleteErr
	}
	store.deleted = append(store.deleted, id)
	endpoint, ok := store.endpoints[id]
	if !ok {
		return outbox.ErrNotFound
	}
	endpoint.DeletedAt = &now
	endpoint.UpdatedAt = now
	endpoint.RowVersion++
	store.endpoints[id] = endpoint
	return nil
}

func (store *fakeWebhookStore) ListWebhookDeliveries(ctx context.Context, filter outbox.DeliveryFilter) ([]outbox.Delivery, outbox.Page, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deliverErr != nil {
		return nil, outbox.Page{}, store.deliverErr
	}
	store.filters = append(store.filters, filter)
	var rows []outbox.Delivery
	for _, delivery := range store.deliveries {
		if filter.EndpointID != nil && delivery.EndpointID != *filter.EndpointID {
			continue
		}
		if filter.EventType != "" && delivery.EventType != filter.EventType {
			continue
		}
		if filter.Status != "" && delivery.Status != filter.Status {
			continue
		}
		rows = append(rows, delivery)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	if len(rows) > filter.Limit {
		rows = rows[:filter.Limit]
	}
	return rows, store.page, nil
}

func (store *fakeWebhookStore) GetWebhookDelivery(ctx context.Context, id int64) (outbox.Delivery, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delivery, ok := store.deliveries[id]
	if !ok {
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	return delivery, nil
}

func (store *fakeWebhookStore) ReplayWebhookDelivery(ctx context.Context, id int64, now time.Time) (outbox.Delivery, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.replayErr != nil {
		return outbox.Delivery{}, store.replayErr
	}
	store.replayed = append(store.replayed, id)
	delivery, ok := store.deliveries[id]
	if !ok {
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	return delivery, nil
}

func (store *fakeWebhookStore) EnqueueTestDelivery(ctx context.Context, endpointID int64, now time.Time) (outbox.Delivery, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.testErr != nil {
		return outbox.Delivery{}, store.testErr
	}
	store.tested = append(store.tested, endpointID)
	endpoint, ok := store.endpoints[endpointID]
	if !ok || endpoint.DeletedAt != nil {
		return outbox.Delivery{}, outbox.ErrNotFound
	}
	store.nextDelivID++
	delivery := outbox.Delivery{
		ID: store.nextDelivID, EndpointID: endpointID, EndpointName: endpoint.Name,
		EventType: outbox.EventTypeTest, Status: outbox.StatusPending,
		CreatedAt: now, UpdatedAt: now,
	}
	store.deliveries[delivery.ID] = delivery
	return delivery, nil
}

// seedWebhookEndpoint inserts an endpoint directly for lifecycle tests.
func (fixture *webFixture) seedWebhookEndpoint(id int64, name, rawURL string, enabled, completed, failed bool) outbox.Endpoint {
	fixture.t.Helper()
	endpoint := outbox.Endpoint{
		ID: id, Name: name, URL: rawURL, DisplayURL: fakeDisplayURL(rawURL),
		HMACSecret:         fixture.webhooks.secret + "-" + strings.Repeat("x", 8),
		Enabled:            enabled,
		SubscribeCompleted: completed,
		SubscribeFailed:    failed,
		CreatedAt:          fixture.clock.now.Add(-time.Hour), UpdatedAt: fixture.clock.now, RowVersion: 1,
	}
	fixture.webhooks.mu.Lock()
	defer fixture.webhooks.mu.Unlock()
	fixture.webhooks.endpoints[id] = endpoint
	if id > fixture.webhooks.nextID {
		fixture.webhooks.nextID = id
	}
	return endpoint
}

// seedWebhookDelivery inserts a delivery directly for history tests.
func (fixture *webFixture) seedWebhookDelivery(id, endpointID int64, endpointName, eventType string, status outbox.DeliveryStatus, attempts int64) {
	fixture.t.Helper()
	delivery := outbox.Delivery{
		ID: id, EventID: "evt_00000000000000000000000000000000", EndpointID: endpointID,
		EndpointName: endpointName, EventType: eventType, Status: status,
		AttemptCount: attempts, LastError: "request timeout",
		CreatedAt: fixture.clock.now.Add(-time.Hour), UpdatedAt: fixture.clock.now, RowVersion: 1,
	}
	fixture.webhooks.mu.Lock()
	defer fixture.webhooks.mu.Unlock()
	fixture.webhooks.deliveries[id] = delivery
	if id > fixture.webhooks.nextDelivID {
		fixture.webhooks.nextDelivID = id
	}
}

func TestWebhookAuthenticationAndMethodBoundaries(t *testing.T) {
	fixture := newWebFixture(t)

	redirect := fixture.request(http.MethodGet, "/webhooks", nil, false)
	requireStatus(t, redirect, http.StatusSeeOther)
	if location := redirect.Header().Get("Location"); location != "/login" {
		t.Errorf("unauthenticated /webhooks Location = %q, want /login", location)
	}

	list := fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireStatus(t, list, http.StatusOK)
	requireContains(t, list.Body.String(), "Webhook endpoints", "No endpoints yet")

	history := fixture.request(http.MethodGet, "/webhook-deliveries", nil, true)
	requireStatus(t, history, http.StatusOK)
	requireContains(t, history.Body.String(), "Webhook deliveries")

	create := fixture.request(http.MethodGet, "/webhooks/new", nil, true)
	requireStatus(t, create, http.StatusOK)
	requireContains(t, create.Body.String(), "Add endpoint", "Save receiver")

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		response := fixture.request(method, "/webhooks", nil, true)
		requireStatus(t, response, http.StatusMethodNotAllowed)
		response = fixture.request(method, "/webhooks/1", nil, true)
		requireStatus(t, response, http.StatusMethodNotAllowed)
	}
	// The endpoint detail path only exists as POST /webhooks/{id}; GET 405s.
	getUpdate := fixture.request(http.MethodGet, "/webhooks/1", nil, true)
	requireStatus(t, getUpdate, http.StatusMethodNotAllowed)
	// GET on the replay path 405s.
	getReplay := fixture.request(http.MethodGet, "/webhook-deliveries/1/replay", nil, true)
	requireStatus(t, getReplay, http.StatusMethodNotAllowed)

	// CSRF protects every mutating route.
	noCSRF := fixture.request(http.MethodPost, "/webhooks", url.Values{"name": {"x"}, "url": {"https://example.test/hook"}, "enabled": {"true"}, "completed": {"true"}}, true)
	requireStatus(t, noCSRF, http.StatusForbidden)
	badCSRF := fixture.request(http.MethodPost, "/webhooks/1/delete", url.Values{"csrf_token": {"wrong"}}, true)
	requireStatus(t, badCSRF, http.StatusForbidden)
	if len(fixture.webhooks.created) != 0 || len(fixture.webhooks.deleted) != 0 {
		t.Fatal("CSRF failures mutated the store")
	}
}
func TestWebhookFormExplainsAuthenticationPayloadsAndTesting(t *testing.T) {
	fixture := newWebFixture(t)

	create := fixture.requestLang(http.MethodGet, "/webhooks/new", true, "zh")
	requireStatus(t, create, http.StatusOK)
	createBody := html.UnescapeString(create.Body.String())
	requireContains(t, createBody,
		"添加接收方",
		"接收地址",
		"可选鉴权",
		"仅当接收服务要求 Bearer 鉴权时填写",
		`type="password"`,
		"触发条件",
		"download.completed",
		"download.failed",
		`"schema_version": 1`,
		`"content_path": "/downloads/movies/Example.Movie.2026"`,
		`"error": "copy task failed"`,
		"保存后可继续进入接收方设置，发送测试请求",
	)
	requireAbsent(t, createBody, "留空则保留当前令牌", "端点")

	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)
	edit := fixture.requestLang(http.MethodGet, "/webhooks/1/edit", true, "zh")
	requireStatus(t, edit, http.StatusOK)
	editBody := html.UnescapeString(edit.Body.String())
	requireContains(t, editBody,
		"编辑接收方",
		"当前令牌不会显示。留空保持不变",
		"测试 Webhook",
		`action="/webhooks/1/test?from=edit"`,
		"发送测试请求",
		`href="/webhook-deliveries?endpoint=1"`,
		"查看投递记录",
	)
	requireAbsent(t, editBody, "仅当接收服务要求 Bearer 鉴权时填写", "端点")
}

func TestWebhookCreateValidationAndSecretReveal(t *testing.T) {
	fixture := newWebFixture(t)
	valid := url.Values{
		"name":      {"receiver-a"},
		"url":       {"https://example.test/hook?x=1"},
		"enabled":   {"true"},
		"completed": {"true"},
		"failed":    {"true"},
	}
	reveal := fixture.post("/webhooks", valid)
	requireStatus(t, reveal, http.StatusOK)
	if cache := reveal.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("reveal Cache-Control = %q, want no-store", cache)
	}
	body := reveal.Body.String()
	requireContains(t, body, "Webhook signing secret", "receiver-a", `href="/webhooks/1/edit"`, "Continue to settings and test")
	if !strings.Contains(body, fixture.webhooks.secret) {
		t.Errorf("reveal page does not contain the generated secret")
	}
	if len(fixture.webhooks.created) != 1 || fixture.webhooks.created[0].Name != "receiver-a" || !fixture.webhooks.created[0].SubscribeCompleted || !fixture.webhooks.created[0].SubscribeFailed {
		t.Fatalf("created inputs = %+v", fixture.webhooks.created)
	}

	// The secret must not leak into any later view.
	list := fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireAbsent(t, list.Body.String(), fixture.webhooks.secret, "test-hmac-secret")
	edit := fixture.request(http.MethodGet, "/webhooks/1/edit", nil, true)
	requireStatus(t, edit, http.StatusOK)
	requireAbsent(t, edit.Body.String(), fixture.webhooks.secret)

	// Query-bearing URLs render redacted and are not placed into form fields.
	requireContains(t, list.Body.String(), "https://example.test/hook?…")
	requireAbsent(t, list.Body.String(), "x=1", "?x=")

	validationCases := []struct {
		name   string
		values url.Values
		want   string
	}{
		{"missing name", url.Values{"name": {""}, "url": {"https://example.test/hook"}, "enabled": {"true"}, "completed": {"true"}}, "The name is required"},
		{"invalid URL", url.Values{"name": {"b"}, "url": {"ftp://example.test/hook"}, "enabled": {"true"}, "completed": {"true"}}, "The URL must be an absolute http or https URL"},
		{"userinfo URL", url.Values{"name": {"c"}, "url": {"https://user:pass@example.test/hook"}, "enabled": {"true"}, "completed": {"true"}}, "The URL must be an absolute http or https URL"},
		{"fragment URL", url.Values{"name": {"d"}, "url": {"https://example.test/hook#frag"}, "enabled": {"true"}, "completed": {"true"}}, "The URL must be an absolute http or https URL"},
		{"no subscription", url.Values{"name": {"e"}, "url": {"https://example.test/hook"}, "enabled": {"true"}}, "Choose at least one delivery trigger"},
		{"bearer too long", url.Values{"name": {"f"}, "url": {"https://example.test/hook"}, "enabled": {"true"}, "completed": {"true"}, "bearer": {strings.Repeat("t", 4097)}}, "at most 4096 bytes"},
	}
	for _, item := range validationCases {
		response := fixture.post("/webhooks", item.values)
		requireStatus(t, response, http.StatusBadRequest)
		requireContains(t, response.Body.String(), item.want)
	}
	if len(fixture.webhooks.created) != 1 {
		t.Fatalf("validation failures created %d endpoints, want 1", len(fixture.webhooks.created))
	}

	// A malformed submission (missing enabled field) renders plain 400.
	malformed := fixture.post("/webhooks", url.Values{"name": {"g"}, "url": {"https://example.test/hook"}, "completed": {"true"}})
	requireStatus(t, malformed, http.StatusBadRequest)
}

func TestWebhookNameConflictRendersConflict(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.webhooks.createErr = outbox.ErrNameConflict
	response := fixture.post("/webhooks", url.Values{
		"name":      {"dupe"},
		"url":       {"https://example.test/hook"},
		"enabled":   {"true"},
		"completed": {"true"},
	})
	requireStatus(t, response, http.StatusConflict)
}

func TestWebhookEditPreservesAndClears(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook?x=1", true, true, true)

	// Query-bearing URL is not prefilled into the form field.
	edit := fixture.request(http.MethodGet, "/webhooks/1/edit", nil, true)
	requireStatus(t, edit, http.StatusOK)
	requireContains(t, edit.Body.String(), "receiver-a", "Rotate secret", "https://example.test/hook?…")
	requireAbsent(t, edit.Body.String(), "value=\"https://example.test/hook?x=1\"", "?x=")

	// Empty URL and bearer preserve; clear_bearer=true clears.
	preserve := fixture.post("/webhooks/1", url.Values{
		"name":      {"receiver-a"},
		"url":       {""},
		"enabled":   {"true"},
		"completed": {"true"},
		"failed":    {"false"},
	})
	requireStatus(t, preserve, http.StatusSeeOther)
	if location := preserve.Header().Get("Location"); location != "/webhooks?updated=1" {
		t.Errorf("update Location = %q, want /webhooks?updated=1", location)
	}
	if len(fixture.webhooks.updated) != 1 || fixture.webhooks.updated[0].input.URL != "" || fixture.webhooks.updated[0].input.BearerToken != "" || fixture.webhooks.updated[0].input.ClearBearerToken {
		t.Fatalf("preserve update = %+v", fixture.webhooks.updated[0].input)
	}
	fixture.webhooks.updated = nil

	clear := fixture.post("/webhooks/1", url.Values{
		"name":         {"receiver-a"},
		"url":          {""},
		"enabled":      {"false"},
		"completed":    {"true"},
		"failed":       {"true"},
		"bearer":       {""},
		"clear_bearer": {"true"},
	})
	requireStatus(t, clear, http.StatusSeeOther)
	if len(fixture.webhooks.updated) != 1 || !fixture.webhooks.updated[0].input.ClearBearerToken {
		t.Fatalf("clear update = %+v", fixture.webhooks.updated[0].input)
	}
	fixture.webhooks.updated = nil

	replace := fixture.post("/webhooks/1", url.Values{
		"name":      {"renamed"},
		"url":       {"https://new.test/hook"},
		"enabled":   {"true"},
		"completed": {"true"},
		"failed":    {"true"},
		"bearer":    {"token-abc"},
	})
	requireStatus(t, replace, http.StatusSeeOther)
	if len(fixture.webhooks.updated) != 1 {
		t.Fatalf("replace update = %+v", fixture.webhooks.updated)
	}
	input := fixture.webhooks.updated[0].input
	if input.Name != "renamed" || input.URL != "https://new.test/hook" || input.BearerToken != "token-abc" || input.ClearBearerToken {
		t.Fatalf("replace input = %+v", input)
	}

	// The updated list shows the new URL redacted and never the bearer.
	list := fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireContains(t, list.Body.String(), "renamed", "https://new.test/hook")
	requireAbsent(t, list.Body.String(), "token-abc")

	// Edit and update on a missing endpoint render not found.
	missingEdit := fixture.request(http.MethodGet, "/webhooks/99/edit", nil, true)
	requireStatus(t, missingEdit, http.StatusNotFound)
	missingUpdate := fixture.post("/webhooks/99", url.Values{"name": {"x"}, "url": {""}, "enabled": {"true"}, "completed": {"true"}})
	requireStatus(t, missingUpdate, http.StatusNotFound)
}

func TestWebhookLifecycleActions(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)

	disable := fixture.post("/webhooks/1/disable", nil)
	requireStatus(t, disable, http.StatusSeeOther)
	if len(fixture.webhooks.toggled) != 1 || fixture.webhooks.toggled[0].id != 1 || fixture.webhooks.toggled[0].enabled {
		t.Fatalf("disable toggles = %+v", fixture.webhooks.toggled)
	}
	enable := fixture.post("/webhooks/1/enable", nil)
	requireStatus(t, enable, http.StatusSeeOther)
	if len(fixture.webhooks.toggled) != 2 || !fixture.webhooks.toggled[1].enabled {
		t.Fatalf("enable toggles = %+v", fixture.webhooks.toggled)
	}

	rotate := fixture.post("/webhooks/1/rotate-secret", nil)
	requireStatus(t, rotate, http.StatusOK)
	if cache := rotate.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("rotate reveal Cache-Control = %q, want no-store", cache)
	}
	requireContains(t, rotate.Body.String(), "test-hmac-secret-rotated")
	if len(fixture.webhooks.rotated) != 1 || fixture.webhooks.rotated[0] != 1 {
		t.Fatalf("rotations = %+v", fixture.webhooks.rotated)
	}
	list := fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireAbsent(t, list.Body.String(), "test-hmac-secret-rotated")

	test := fixture.post("/webhooks/1/test", nil)
	requireStatus(t, test, http.StatusSeeOther)
	if location := test.Header().Get("Location"); location != "/webhooks?test=1" {
		t.Errorf("test Location = %q, want /webhooks?test=1", location)
	}
	if len(fixture.webhooks.tested) != 1 || fixture.webhooks.tested[0] != 1 {
		t.Fatalf("tests = %+v", fixture.webhooks.tested)
	}
	noticed := fixture.request(http.MethodGet, "/webhooks?test=1", nil, true)
	requireContains(t, noticed.Body.String(), "Test request enqueued.")
	editTest := fixture.post("/webhooks/1/test?from=edit", nil)
	requireStatus(t, editTest, http.StatusSeeOther)
	if location := editTest.Header().Get("Location"); location != "/webhooks/1/edit?test=1" {
		t.Errorf("edit test Location = %q, want /webhooks/1/edit?test=1", location)
	}
	editNotice := fixture.request(http.MethodGet, "/webhooks/1/edit?test=1", nil, true)
	requireStatus(t, editNotice, http.StatusOK)
	requireContains(t, editNotice.Body.String(), "Test request enqueued.")
	if len(fixture.webhooks.tested) != 2 {
		t.Fatalf("edit test count = %d, want 2", len(fixture.webhooks.tested))
	}

	invalidReturn := fixture.post("/webhooks/1/test?from=unknown", nil)
	requireStatus(t, invalidReturn, http.StatusBadRequest)
	if len(fixture.webhooks.tested) != 2 {
		t.Fatal("invalid test return target enqueued a delivery")
	}

	remove := fixture.post("/webhooks/1/delete", nil)
	requireStatus(t, remove, http.StatusSeeOther)
	if len(fixture.webhooks.deleted) != 1 || fixture.webhooks.deleted[0] != 1 {
		t.Fatalf("deletions = %+v", fixture.webhooks.deleted)
	}
	// Soft-deleted endpoints disappear from the list and read as not found.
	list = fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireAbsent(t, list.Body.String(), "receiver-a")
	gone := fixture.request(http.MethodGet, "/webhooks/1/edit", nil, true)
	requireStatus(t, gone, http.StatusNotFound)

	// Actions on a deleted endpoint render not found; delete itself stays
	// idempotent at the store boundary.
	for _, target := range []string{"/webhooks/1/enable", "/webhooks/1/disable", "/webhooks/1/rotate-secret", "/webhooks/1/test"} {
		requireStatus(t, fixture.post(target, nil), http.StatusNotFound)
	}
	requireStatus(t, fixture.post("/webhooks/1/delete", nil), http.StatusSeeOther)
}

func TestWebhookDeliveriesFiltersPaginationAndReplay(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)
	fixture.seedWebhookEndpoint(2, "receiver-b", "https://example.test/second", true, false, true)
	fixture.seedWebhookDelivery(1, 1, "receiver-a", outbox.EventTypeCompleted, outbox.StatusSucceeded, 1)
	fixture.seedWebhookDelivery(2, 1, "receiver-a", outbox.EventTypeFailed, outbox.StatusDead, 5)
	fixture.seedWebhookDelivery(3, 2, "receiver-b", outbox.EventTypeTest, outbox.StatusPending, 0)
	fixture.seedWebhookDelivery(4, 1, "receiver-a", outbox.EventTypeCompleted, outbox.StatusDelivering, 1)

	page := fixture.request(http.MethodGet, "/webhook-deliveries", nil, true)
	requireStatus(t, page, http.StatusOK)
	body := page.Body.String()
	requireContains(t, body, "receiver-a", "receiver-b", "Dead letter", "Succeeded", "Pending", "Delivering", "Replay")
	requireAbsent(t, body, fixture.webhooks.secret)

	filtered := fixture.request(http.MethodGet, "/webhook-deliveries?endpoint=1&event=download.completed&status=succeeded&limit=10", nil, true)
	requireStatus(t, filtered, http.StatusOK)
	requireContains(t, filtered.Body.String(), "receiver-a")
	if len(fixture.webhooks.filters) != 2 {
		t.Fatalf("filters captured = %+v", fixture.webhooks.filters)
	}
	last := fixture.webhooks.filters[1]
	if last.EndpointID == nil || *last.EndpointID != 1 || last.EventType != outbox.EventTypeCompleted || last.Status != outbox.StatusSucceeded || last.Limit != 10 {
		t.Fatalf("delivery filter = %+v", last)
	}

	// Filter defaults flow through exactly.
	fixture.webhooks.filters = nil
	_ = fixture.request(http.MethodGet, "/webhook-deliveries", nil, true)
	if len(fixture.webhooks.filters) != 1 {
		t.Fatal("default filter missing")
	}
	if filter := fixture.webhooks.filters[0]; filter.EndpointID != nil || filter.EventType != "" || filter.Status != "" || filter.Cursor != "" || filter.Limit != 50 {
		t.Fatalf("default filter = %+v", filter)
	}

	// HasMore renders the next-page link carrying the opaque cursor.
	fixture.webhooks.mu.Lock()
	fixture.webhooks.page = outbox.Page{NextCursor: "c2VlLW5leHQ", HasMore: true}
	fixture.webhooks.mu.Unlock()
	paged := fixture.request(http.MethodGet, "/webhook-deliveries?status=dead", nil, true)
	requireContains(t, paged.Body.String(), "cursor=c2VlLW5leHQ", "Next")

	invalid := map[string]string{
		"endpoint=abc":            "endpoint must be numeric",
		"endpoint=0":              "endpoint must be positive",
		"endpoint=1&event=bogus":  "event must be exact",
		"endpoint=1&status=bogus": "status must be exact",
		"limit=0":                 "limit must be 1..100",
		"limit=101":               "limit must be 1..100",
		"limit=abc":               "limit must be numeric",
		"cursor=%21%21%21":        "cursor must be base64",
		"cursor=Z2FyYmFnZQ":       "cursor must decode to timestamp+id",
		"endpoint=1&endpoint=2":   "duplicate filter",
		"limit=5&limit=5":         "duplicate limit",
	}
	for query, reason := range invalid {
		response := fixture.request(http.MethodGet, "/webhook-deliveries?"+query, nil, true)
		requireStatus(t, response, http.StatusBadRequest)
		t.Logf("query %q rejected (%s)", query, reason)
	}

	// Replay reopens a dead-letter row through the store and notices on return.
	replay := fixture.post("/webhook-deliveries/2/replay", nil)
	requireStatus(t, replay, http.StatusSeeOther)
	if location := replay.Header().Get("Location"); location != "/webhook-deliveries?replayed=1" {
		t.Errorf("replay Location = %q, want /webhook-deliveries?replayed=1", location)
	}
	if len(fixture.webhooks.replayed) != 1 || fixture.webhooks.replayed[0] != 2 {
		t.Fatalf("replays = %+v", fixture.webhooks.replayed)
	}
	noticed := fixture.request(http.MethodGet, "/webhook-deliveries?replayed=1", nil, true)
	requireContains(t, noticed.Body.String(), "Delivery reopened for replay.")

	missing := fixture.post("/webhook-deliveries/99/replay", nil)
	requireStatus(t, missing, http.StatusNotFound)
}

func TestWebhookReplayOnlyDeadRowsOfferButton(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)
	fixture.seedWebhookDelivery(1, 1, "receiver-a", outbox.EventTypeCompleted, outbox.StatusSucceeded, 1)
	fixture.seedWebhookDelivery(2, 1, "receiver-a", outbox.EventTypeFailed, outbox.StatusDead, 5)
	fixture.seedWebhookDelivery(3, 1, "receiver-a", outbox.EventTypeFailed, outbox.StatusCancelled, 2)

	page := fixture.request(http.MethodGet, "/webhook-deliveries", nil, true)
	body := page.Body.String()
	if !strings.Contains(body, "/webhook-deliveries/2/replay") {
		t.Error("dead row does not offer replay")
	}
	for _, id := range []string{"1", "3"} {
		if strings.Contains(body, "/webhook-deliveries/"+id+"/replay") {
			t.Errorf("non-dead row %s offers replay", id)
		}
	}
	requireContains(t, body, "Cancelled")
}

func TestWebhookStoreFailureRendersServerError(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.webhooks.listErr = context.DeadlineExceeded
	requireStatus(t, fixture.request(http.MethodGet, "/webhooks", nil, true), http.StatusInternalServerError)
	fixture.webhooks.listErr = nil

	fixture.webhooks.deliverErr = context.DeadlineExceeded
	requireStatus(t, fixture.request(http.MethodGet, "/webhook-deliveries", nil, true), http.StatusInternalServerError)
}

func TestWebhookEnabledCreateAndUpdate(t *testing.T) {
	fixture := newWebFixture(t)

	// Creating with enabled=false must reach the store as an explicit non-nil
	// false pointer and persist onto the created endpoint, so the endpoint
	// cannot fan out before being enabled.
	disabled := fixture.post("/webhooks", url.Values{
		"name":      {"off-by-default"},
		"url":       {"https://example.test/off"},
		"enabled":   {"false"},
		"completed": {"true"},
		"failed":    {"true"},
	})
	requireStatus(t, disabled, http.StatusOK)
	if len(fixture.webhooks.created) != 1 || fixture.webhooks.created[0].Enabled == nil || *fixture.webhooks.created[0].Enabled {
		t.Fatalf("create inputs = %+v", fixture.webhooks.created)
	}
	fixture.webhooks.mu.Lock()
	created := fixture.webhooks.endpoints[1]
	fixture.webhooks.mu.Unlock()
	if created.Enabled {
		t.Fatalf("created endpoint Enabled = %v, want false", created.Enabled)
	}
	list := fixture.request(http.MethodGet, "/webhooks", nil, true)
	requireContains(t, list.Body.String(), `data-state="disabled"`, `action="/webhooks/1/enable"`)
	requireAbsent(t, list.Body.String(), `action="/webhooks/1/disable"`)

	// Creating with enabled=true submits an explicit true pointer.
	enabled := fixture.post("/webhooks", url.Values{
		"name":      {"on-by-default"},
		"url":       {"https://example.test/on"},
		"enabled":   {"true"},
		"completed": {"true"},
		"failed":    {"true"},
	})
	requireStatus(t, enabled, http.StatusOK)
	if len(fixture.webhooks.created) != 2 || fixture.webhooks.created[1].Enabled == nil || !*fixture.webhooks.created[1].Enabled {
		t.Fatalf("create inputs = %+v", fixture.webhooks.created)
	}

	// Updating the enabled state travels through the store input as an
	// explicit pointer and persists atomically onto the endpoint.
	fixture.seedWebhookEndpoint(10, "toggle", "https://example.test/toggle", true, true, true)
	disable := fixture.post("/webhooks/10", url.Values{
		"name":      {"toggle"},
		"url":       {""},
		"enabled":   {"false"},
		"completed": {"true"},
		"failed":    {"true"},
	})
	requireStatus(t, disable, http.StatusSeeOther)
	if len(fixture.webhooks.updated) != 1 || fixture.webhooks.updated[0].input.Enabled == nil || *fixture.webhooks.updated[0].input.Enabled {
		t.Fatalf("disable update = %+v", fixture.webhooks.updated[0].input)
	}
	fixture.webhooks.mu.Lock()
	stored := fixture.webhooks.endpoints[10]
	fixture.webhooks.mu.Unlock()
	if stored.Enabled {
		t.Fatalf("endpoint after disable update = %+v, want disabled", stored)
	}

	fixture.webhooks.updated = nil
	enable := fixture.post("/webhooks/10", url.Values{
		"name":      {"toggle"},
		"url":       {""},
		"enabled":   {"true"},
		"completed": {"true"},
		"failed":    {"true"},
	})
	requireStatus(t, enable, http.StatusSeeOther)
	if len(fixture.webhooks.updated) != 1 || fixture.webhooks.updated[0].input.Enabled == nil || !*fixture.webhooks.updated[0].input.Enabled {
		t.Fatalf("enable update = %+v", fixture.webhooks.updated[0].input)
	}
	fixture.webhooks.mu.Lock()
	stored = fixture.webhooks.endpoints[10]
	fixture.webhooks.mu.Unlock()
	if !stored.Enabled {
		t.Fatalf("endpoint after enable update = %+v, want enabled", stored)
	}
}

func TestWebhookEnabledSelectIsStrict(t *testing.T) {
	fixture := newWebFixture(t)
	for _, value := range []string{"", "1", "0", "yes", "on"} {
		response := fixture.post("/webhooks", url.Values{
			"name":      {"x"},
			"url":       {"https://example.test/hook"},
			"enabled":   {value},
			"completed": {"true"},
		})
		requireStatus(t, response, http.StatusBadRequest)
	}
	// Missing enabled is malformed and never defaults to true.
	missing := fixture.post("/webhooks", url.Values{
		"name":      {"x"},
		"url":       {"https://example.test/hook"},
		"completed": {"true"},
	})
	requireStatus(t, missing, http.StatusBadRequest)
	if len(fixture.webhooks.created) != 0 {
		t.Fatalf("malformed enabled created %d endpoints", len(fixture.webhooks.created))
	}
}

func TestWebhookErrorRenderingNeverEchoesCredentials(t *testing.T) {
	fixture := newWebFixture(t)
	const sentinelBearer = "bearer-sentinel-8f3a"
	const sentinelQuery = "token=sentinel-query-7c1b"

	// A validation failure re-renders the form with the query redacted and the
	// bearer cleared, and is never cached.
	invalid := fixture.post("/webhooks", url.Values{
		"name":      {""},
		"url":       {"https://example.test/hook?" + sentinelQuery},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, invalid, http.StatusBadRequest)
	if cache := invalid.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("validation error Cache-Control = %q, want no-store", cache)
	}
	body := invalid.Body.String()
	requireContains(t, body, "https://example.test/hook?…")
	requireAbsent(t, body, sentinelBearer, sentinelQuery, "token=")

	// A userinfo URL is a credential source: it renders empty, never echoed.
	userinfo := fixture.post("/webhooks", url.Values{
		"name":      {"userinfo-case"},
		"url":       {"https://user:pass@example.test/hook?x=1"},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, userinfo, http.StatusBadRequest)
	requireAbsent(t, userinfo.Body.String(), sentinelBearer, "user:pass", "x=1")
	requireContains(t, userinfo.Body.String(), `name="url" type="text" value=""`)

	// A URL that does not parse into an absolute http/https URL renders empty.
	malformedURL := fixture.post("/webhooks", url.Values{
		"name":      {"malformed-url"},
		"url":       {"not-a-url-" + sentinelQuery},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, malformedURL, http.StatusBadRequest)
	requireAbsent(t, malformedURL.Body.String(), sentinelBearer, sentinelQuery)
	requireContains(t, malformedURL.Body.String(), `name="url" type="text" value=""`)

	// A malformed submission is a plain 400 that is never cached and never
	// echoes the form.
	malformed := fixture.post("/webhooks", url.Values{
		"name":      {"malformed"},
		"url":       {"https://example.test/hook?" + sentinelQuery},
		"enabled":   {"1"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, malformed, http.StatusBadRequest)
	if cache := malformed.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("malformed Cache-Control = %q, want no-store", cache)
	}
	requireAbsent(t, malformed.Body.String(), sentinelBearer, sentinelQuery)

	// A store failure after a valid parse is a plain 500 that is never cached
	// and never echoes the submitted credentials.
	fixture.webhooks.createErr = context.DeadlineExceeded
	failed := fixture.post("/webhooks", url.Values{
		"name":      {"store-failure"},
		"url":       {"https://example.test/hook?" + sentinelQuery},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, failed, http.StatusInternalServerError)
	if cache := failed.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("create store error Cache-Control = %q, want no-store", cache)
	}
	requireAbsent(t, failed.Body.String(), sentinelBearer, sentinelQuery)
	fixture.webhooks.createErr = nil

	// Update carries the same protections: validation errors and store
	// failures never echo and are never cached.
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)
	updateInvalid := fixture.post("/webhooks/1", url.Values{
		"name":      {"receiver-a"},
		"url":       {"https://user:pass@example.test/hook?x=1"},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, updateInvalid, http.StatusBadRequest)
	if cache := updateInvalid.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("update validation error Cache-Control = %q, want no-store", cache)
	}
	requireAbsent(t, updateInvalid.Body.String(), sentinelBearer, "user:pass", "x=1")
	requireContains(t, updateInvalid.Body.String(), `name="url" type="text" value=""`)

	fixture.webhooks.updateErr = context.DeadlineExceeded
	updateFailed := fixture.post("/webhooks/1", url.Values{
		"name":      {"receiver-a"},
		"url":       {"https://example.test/hook?" + sentinelQuery},
		"enabled":   {"true"},
		"completed": {"true"},
		"bearer":    {sentinelBearer},
	})
	requireStatus(t, updateFailed, http.StatusInternalServerError)
	if cache := updateFailed.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("update store error Cache-Control = %q, want no-store", cache)
	}
	requireAbsent(t, updateFailed.Body.String(), sentinelBearer, sentinelQuery)
}

func TestWebhookDeliveriesRenderLastErrorBilingually(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.seedWebhookEndpoint(1, "receiver-a", "https://example.test/hook", true, true, true)
	fixture.seedWebhookDelivery(1, 1, "receiver-a", outbox.EventTypeFailed, outbox.StatusDead, 5)
	fixture.webhooks.mu.Lock()
	delivery := fixture.webhooks.deliveries[1]
	delivery.LastError = "HTTP 500"
	fixture.webhooks.deliveries[1] = delivery
	fixture.webhooks.mu.Unlock()

	page := fixture.request(http.MethodGet, "/webhook-deliveries", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), "Last error", "HTTP 500")

	chinese := fixture.requestLang(http.MethodGet, "/webhook-deliveries", true, "zh")
	requireStatus(t, chinese, http.StatusOK)
	requireContains(t, chinese.Body.String(), "最新错误", "HTTP 500")
}
