package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/turygo/cd211/internal/token"
)

// generateAPITokenThroughUI 生成令牌，并从重定向后的设置页读取原文。
func generateAPITokenThroughUI(t *testing.T, fixture *webFixture) string {
	t.Helper()
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/api-token/generate"`)
	requireAbsent(t, page.Body.String(), "cd211_api_")

	response := fixture.post("/settings/api-token/generate", nil)
	requireStatus(t, response, http.StatusSeeOther)
	if location := response.Header().Get("Location"); location != "/settings" {
		t.Fatalf("generate Location = %q, want /settings", location)
	}
	page = fixture.request(http.MethodGet, response.Header().Get("Location"), nil, true)
	requireStatus(t, page, http.StatusOK)
	_, remainder, ok := strings.Cut(page.Body.String(), `data-copy-value="cd211_api_`)
	if !ok {
		t.Fatal("settings page omitted saved token")
	}
	value, _, ok := strings.Cut(remainder, `"`)
	if !ok {
		t.Fatal("settings page has an incomplete saved token")
	}
	return "cd211_api_" + value
}

func TestAPITokenSettingsRecoversMaskedTokenOnRepeatedVisits(t *testing.T) {
	fixture := newWebFixture(t)
	secret := generateAPITokenThroughUI(t, fixture)
	info, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken(): %v", err)
	}
	for range 2 {
		page := fixture.request(http.MethodGet, "/settings", nil, true)
		requireStatus(t, page, http.StatusOK)
		if got := page.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("settings Cache-Control = %q, want no-store", got)
		}
		body := page.Body.String()
		requireContains(t, body, `data-copy-value="`+secret+`"`, `data-credential-value>`+info.Hint+`</code>`, `aria-pressed="false" aria-controls="api-token-value"`, `action="/settings/api-token/revoke"`)
		requireAbsent(t, body, `>`+secret+`<`, `title="`+secret+`"`, tr(LangEN).APITokenSecretUnavailable, "token_hash")
	}
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

func TestSettingsCredentialsRequireAuthentication(t *testing.T) {
	fixture := newWebFixture(t)
	tokenSecret := generateAPITokenThroughUI(t, fixture)
	qbtSecret := generateQBTAPIKeyThroughUI(t, fixture)
	page := fixture.request(http.MethodGet, "/settings", nil, false)
	requireStatus(t, page, http.StatusSeeOther)
	if location := page.Header().Get("Location"); location != "/login" {
		t.Errorf("anonymous settings Location = %q, want /login", location)
	}
	requireAbsent(t, page.Body.String(), tokenSecret, qbtSecret, "data-copy-value", "data-credential-toggle")
}

func TestSettingsLegacyCredentialsExplainRecovery(t *testing.T) {
	fixture := newWebFixture(t)
	tokenSecret := generateAPITokenThroughUI(t, fixture)
	qbtSecret := generateQBTAPIKeyThroughUI(t, fixture)
	database, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecContext(context.Background(), `UPDATE api_token SET token_secret = ''; UPDATE qbt_api_key SET key_secret = '';`); err != nil {
		t.Fatalf("clear legacy credential plaintext: %v", err)
	}
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), tr(LangEN).APITokenSecretUnavailable, tr(LangEN).QBTAPIKeySecretUnavailable, `action="/settings/api-token/revoke"`, `action="/settings/qbt-api-key/revoke"`)
	requireAbsent(t, page.Body.String(), tokenSecret, qbtSecret, "data-copy-value", "data-credential-toggle", `action="/settings/api-token/generate"`, `action="/settings/qbt-api-key/generate"`)
}

func TestAPITokenRotateRouteRemoved(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusNotFound)
	requireContains(t, response.Body.String(), "Not Found\n")
}
