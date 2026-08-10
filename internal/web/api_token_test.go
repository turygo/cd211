package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/token"
)

// generateAPITokenThroughUI drives the settings page end to end: generate,
// assert the one-time reveal, and return the revealed secret.
func generateAPITokenThroughUI(t *testing.T, fixture *webFixture) string {
	t.Helper()
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/api-token/generate"`)
	requireAbsent(t, page.Body.String(), "cd211_api_")

	response := fixture.post("/settings/api-token/generate", nil)
	requireStatus(t, response, http.StatusOK)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("generate Cache-Control = %q, want no-store", got)
	}
	body := response.Body.String()
	start := strings.Index(body, "cd211_api_")
	if start < 0 {
		t.Fatalf("secret page lacks the token prefix: %q", body)
	}
	secret := body[start : start+token.SecretLength]
	if !token.Valid(token.Secret(secret)) {
		t.Fatalf("revealed value %q is not a well-formed token", secret)
	}
	requireContains(t, body, `href="/settings"`)
	return secret
}

func TestAPITokenGenerateRevealAndSettingsDisplay(t *testing.T) {
	fixture := newWebFixture(t)

	secret := generateAPITokenThroughUI(t, fixture)

	// The stored row holds the digest and hint only; the plaintext secret is
	// never persisted anywhere.
	info, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken(): %v", err)
	}
	if info.RowVersion != 0 || info.CreatedAt.IsZero() || !info.CreatedAt.Equal(info.UpdatedAt) {
		t.Errorf("stored token metadata = %+v, want version 0 and equal create/update", info)
	}
	if info.Hint != "cd211_api_…"+string(secret[len(secret)-6:]) {
		t.Errorf("stored hint = %q, want prefix + ellipsis + final 6 characters", info.Hint)
	}

	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	body := page.Body.String()
	requireContains(t, body, `action="/settings/api-token/rotate"`, `action="/settings/api-token/revoke"`,
		`name="expected_version" value="0"`, info.Hint, "Automation API token")
	requireAbsent(t, body, secret, "sha256", "token_hash")
}

func TestAPITokenGenerateWhenPresentConflicts(t *testing.T) {
	fixture := newWebFixture(t)
	generateAPITokenThroughUI(t, fixture)

	response := fixture.post("/settings/api-token/generate", nil)
	requireStatus(t, response, http.StatusConflict)
	requireContains(t, response.Body.String(), "Conflict\n")
}

func TestAPITokenRotateReplacesTokenOnce(t *testing.T) {
	fixture := newWebFixture(t)
	oldSecret := generateAPITokenThroughUI(t, fixture)

	info, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken(): %v", err)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	response := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusOK)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("rotate Cache-Control = %q, want no-store", got)
	}
	body := response.Body.String()
	start := strings.Index(body, "cd211_api_")
	if start < 0 {
		t.Fatalf("rotated secret page lacks the token prefix: %q", body)
	}
	newSecret := body[start : start+token.SecretLength]
	if !token.Valid(token.Secret(newSecret)) || newSecret == oldSecret {
		t.Fatalf("rotated reveal %q is not a fresh valid token", newSecret)
	}

	rotated, err := fixture.store.GetAPIToken(context.Background())
	if err != nil {
		t.Fatalf("GetAPIToken() after rotate: %v", err)
	}
	if rotated.RowVersion != 1 || !rotated.CreatedAt.Equal(info.CreatedAt) || !rotated.UpdatedAt.After(info.UpdatedAt) {
		t.Errorf("rotated metadata = %+v, want preserved created_at, bumped version and updated_at", rotated)
	}
	if token.Verify(token.Secret(oldSecret), rotated.Digest) {
		t.Error("old token still verifies after rotation")
	}

	// A stale form (the pre-rotation version) is rejected as a conflict.
	stale := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, stale, http.StatusConflict)
	requireContains(t, stale.Body.String(), "Conflict\n")
}

func TestAPITokenRotateAbsentNotFound(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusNotFound)
	requireContains(t, response.Body.String(), "Not Found\n")
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

	// Revoking an already-absent token is an idempotent success.
	idempotent := fixture.post("/settings/api-token/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, idempotent, http.StatusSeeOther)
}

func TestAPITokenRevokeStaleVersionConflicts(t *testing.T) {
	fixture := newWebFixture(t)
	generateAPITokenThroughUI(t, fixture)

	if _, err := fixture.store.RotateAPIToken(context.Background(), 0, fixture.clock.Now().Add(time.Minute)); err != nil {
		t.Fatalf("RotateAPIToken(): %v", err)
	}
	response := fixture.post("/settings/api-token/revoke", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusConflict)
	if _, err := fixture.store.GetAPIToken(context.Background()); err != nil {
		t.Errorf("stale revoke removed the token: %v", err)
	}
}

func TestAPITokenActionsRequireAuthAndCSRF(t *testing.T) {
	fixture := newWebFixture(t)

	// Unauthenticated POST is redirected to the login page.
	anonymous := fixture.request(http.MethodPost, "/settings/api-token/generate", url.Values{}, false)
	requireStatus(t, anonymous, http.StatusSeeOther)
	if got := anonymous.Header().Get("Location"); got != "/login" {
		t.Errorf("anonymous Location = %q, want /login", got)
	}

	// Authenticated POST without a valid CSRF token is forbidden.
	forbidden := fixture.request(http.MethodPost, "/settings/api-token/generate", url.Values{"csrf_token": {"wrong"}}, true)
	requireStatus(t, forbidden, http.StatusForbidden)
	if _, err := fixture.store.GetAPIToken(context.Background()); !errors.Is(err, token.ErrNotFound) {
		t.Errorf("CSRF-rejected generate created a token: %v", err)
	}
}

func TestAPITokenMalformedExpectedVersionRejected(t *testing.T) {
	fixture := newWebFixture(t)
	for _, raw := range []string{"abc", "-1", "1.5"} {
		response := fixture.post("/settings/api-token/rotate", url.Values{"expected_version": {raw}})
		requireStatus(t, response, http.StatusBadRequest)
	}
}
