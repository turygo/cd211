package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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
	if got := response.Header().Get("Location"); got != "/settings" {
		t.Fatalf("generate Location = %q, want /settings", got)
	}
	info, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey(): %v", err)
	}
	if !qbtkey.Valid(info.Secret) {
		t.Fatalf("persisted qbt key %q is invalid", info.Secret)
	}
	return string(info.Secret)
}

func TestQBTAPIKeyGeneratePersistsAndSettingsAlwaysDisplays(t *testing.T) {
	fixture := newWebFixture(t)
	secret := generateQBTAPIKeyThroughUI(t, fixture)
	info, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey(): %v", err)
	}
	if info.Secret != qbtkey.Secret(secret) || info.RowVersion != 0 || info.CreatedAt.IsZero() || !info.CreatedAt.Equal(info.UpdatedAt) {
		t.Errorf("stored qbt key = %+v, want persisted secret and version 0", info)
	}
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	if got := page.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("settings Cache-Control = %q, want no-store", got)
	}
	body := page.Body.String()
	requireContains(t, body, secret, tr(LangEN).QBTAPIKeyGeneratedLabel, `data-copy-value="`+secret+`"`, `action="/settings/qbt-api-key/revoke"`, "qBittorrent API key")
	requireAbsent(t, body, info.Hint, "First configured", "Key hint", `action="/settings/qbt-api-key/rotate"`, "Rotate key", "sha256", "qbt_api_key")
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
