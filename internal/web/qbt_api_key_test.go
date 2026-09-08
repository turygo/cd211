package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turygo/cd211/internal/qbtkey"
)

func generateQBTAPIKeyThroughUI(t *testing.T, fixture *webFixture) string {
	t.Helper()
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/qbt-api-key/generate"`)
	requireAbsent(t, page.Body.String(), "qbt_")

	response := fixture.post("/settings/qbt-api-key/generate", nil)
	requireStatus(t, response, http.StatusSeeOther)
	if location := response.Header().Get("Location"); location != "/settings" {
		t.Fatalf("generate Location = %q, want /settings", location)
	}
	page = fixture.request(http.MethodGet, response.Header().Get("Location"), nil, true)
	requireStatus(t, page, http.StatusOK)
	_, remainder, ok := strings.Cut(page.Body.String(), `data-copy-value="qbt_`)
	if !ok {
		t.Fatal("settings page omitted saved qBittorrent key")
	}
	value, _, ok := strings.Cut(remainder, `"`)
	if !ok {
		t.Fatal("settings page has an incomplete saved qBittorrent key")
	}
	return "qbt_" + value
}

func TestQBTAPIKeySettingsRecoversMaskedKeyOnRepeatedVisits(t *testing.T) {
	fixture := newWebFixture(t)
	secret := generateQBTAPIKeyThroughUI(t, fixture)
	info, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey(): %v", err)
	}
	for range 2 {
		page := fixture.request(http.MethodGet, "/settings", nil, true)
		requireStatus(t, page, http.StatusOK)
		if got := page.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("settings Cache-Control = %q, want no-store", got)
		}
		body := page.Body.String()
		requireContains(t, body, `data-copy-value="`+secret+`"`, `data-credential-value>`+info.Hint+`</code>`, `aria-pressed="false" aria-controls="qbt-api-key-value"`, `action="/settings/qbt-api-key/revoke"`)
		requireAbsent(t, body, `>`+secret+`<`, `title="`+secret+`"`, tr(LangEN).QBTAPIKeySecretUnavailable, "qbt_api_key")
	}
}

func TestQBTAPIKeyGenerateWhenPresentConflicts(t *testing.T) {
	fixture := newWebFixture(t)
	generateQBTAPIKeyThroughUI(t, fixture)
	response := fixture.post("/settings/qbt-api-key/generate", nil)
	requireStatus(t, response, http.StatusConflict)
}

func TestQBTAPIKeyRevokeDisablesAndIsIdempotent(t *testing.T) {
	fixture := newWebFixture(t)
	generateQBTAPIKeyThroughUI(t, fixture)
	response := fixture.post("/settings/qbt-api-key/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusSeeOther)
	location := response.Header().Get("Location")
	if location != "/settings?qbt-api-key-revoked=1" {
		t.Errorf("revoke Location = %q, want qbt notice", location)
	}
	if _, err := fixture.store.GetQBTAPIKey(context.Background()); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Errorf("GetQBTAPIKey() after revoke = %v, want qbtkey.ErrNotFound", err)
	}
	page := fixture.request(http.MethodGet, location, nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), tr(LangEN).QBTAPIKeyRevoked, `action="/settings/qbt-api-key/generate"`)
	requireAbsent(t, page.Body.String(), "qbt_")
	idempotent := fixture.post("/settings/qbt-api-key/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, idempotent, http.StatusSeeOther)
}

func TestQBTAPIKeyActionsRequireAuthAndCSRF(t *testing.T) {
	fixture := newWebFixture(t)
	anonymous := fixture.request(http.MethodPost, "/settings/qbt-api-key/generate", url.Values{}, false)
	requireStatus(t, anonymous, http.StatusSeeOther)
	forbidden := fixture.request(http.MethodPost, "/settings/qbt-api-key/generate", url.Values{"csrf_token": {"wrong"}}, true)
	requireStatus(t, forbidden, http.StatusForbidden)
	if _, err := fixture.store.GetQBTAPIKey(context.Background()); !errors.Is(err, qbtkey.ErrNotFound) {
		t.Errorf("CSRF-rejected generate created qbt key: %v", err)
	}
}

func TestQBTAPIKeyRotateRouteRemoved(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.post("/settings/qbt-api-key/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusNotFound)
	requireContains(t, response.Body.String(), "Not Found\n")
}
