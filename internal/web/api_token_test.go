package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turygo/cd211/internal/token"
)

// generateAPITokenThroughUI drives generation and returns the persisted secret
// that the Settings page renders after the redirect.
func generateAPITokenThroughUI(t *testing.T, fixture *webFixture) string {
	t.Helper()
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/api-token/generate"`)
	requireAbsent(t, page.Body.String(), "cd211_api_")

	response := fixture.post("/settings/api-token/generate", nil)
	requireStatus(t, response, http.StatusSeeOther)
	if got := response.Header().Get("Location"); got != "/settings" {
		t.Fatalf("generate Location = %q, want /settings", got)
	}
	info, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken(): %v", err)
	}
	if !token.Valid(info.Secret) {
		t.Fatalf("persisted token %q is invalid", info.Secret)
	}
	return string(info.Secret)
}

func TestAPITokenGeneratePersistsAndSettingsAlwaysDisplays(t *testing.T) {
	fixture := newWebFixture(t)
	secret := generateAPITokenThroughUI(t, fixture)

	info, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken(): %v", err)
	}
	if info.Secret != token.Secret(secret) || info.RowVersion != 0 || info.CreatedAt.IsZero() || !info.CreatedAt.Equal(info.UpdatedAt) {
		t.Errorf("stored token = %+v, want persisted secret and version 0", info)
	}

	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	if got := page.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("settings Cache-Control = %q, want no-store", got)
	}
	body := page.Body.String()
	requireContains(t, body, secret, tr(LangEN).APITokenGeneratedLabel, `data-copy-value="`+secret+`"`, `action="/settings/api-token/revoke"`, "Automation API token")
	requireAbsent(t, body, info.Hint, "First configured", "Token hint", `action="/settings/api-token/rotate"`, "Rotate token", "sha256", "token_hash")
}

func TestAPITokenGenerateWhenPresentConflicts(t *testing.T) {
	fixture := newWebFixture(t)
	generateAPITokenThroughUI(t, fixture)

	response := fixture.post("/settings/api-token/generate", nil)
	requireStatus(t, response, http.StatusConflict)
	requireContains(t, response.Body.String(), "Conflict\n")
}

func TestAPITokenRevokeDisablesAndIsIdempotent(t *testing.T) {
	fixture := newWebFixture(t)
	generateAPITokenThroughUI(t, fixture)

	response := fixture.post("/settings/api-token/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusSeeOther)
	location := response.Header().Get("Location")
	if location != "/settings?token-revoked=1" {
		t.Errorf("revoke Location = %q, want /settings?token-revoked=1", location)
	}
	if _, err := fixture.store.GetAPIToken(context.Background()); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("GetAPIToken() after revoke = %v, want token.ErrNotFound", err)
	}

	page := fixture.request(http.MethodGet, location, nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), tr(LangEN).APITokenRevoked, `action="/settings/api-token/generate"`)
	requireAbsent(t, page.Body.String(), "cd211_api_")

	idempotent := fixture.post("/settings/api-token/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, idempotent, http.StatusSeeOther)
}

func TestAPITokenActionsRequireAuthAndCSRF(t *testing.T) {
	fixture := newWebFixture(t)

	anonymous := fixture.request(http.MethodPost, "/settings/api-token/generate", url.Values{}, false)
	requireStatus(t, anonymous, http.StatusSeeOther)
	if got := anonymous.Header().Get("Location"); got != "/login" {
		t.Errorf("anonymous Location = %q, want /login", got)
	}

	forbidden := fixture.request(http.MethodPost, "/settings/api-token/generate", url.Values{"csrf_token": {"wrong"}}, true)
	requireStatus(t, forbidden, http.StatusForbidden)
	if _, err := fixture.store.GetAPIToken(context.Background()); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("CSRF-rejected generate created a token: %v", err)
	}
}

func TestAPITokenRotateRouteRemoved(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusNotFound)
	requireContains(t, response.Body.String(), "Not Found\n")
	if strings.Contains(response.Body.String(), "Rotate") {
		t.Fatal("removed rotate route exposed a rotation response")
	}
}
