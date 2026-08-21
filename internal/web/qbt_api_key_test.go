package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/qbtkey"
)

func generateQBTAPIKeyThroughUI(t *testing.T, fixture *webFixture) string {
	t.Helper()
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	requireContains(t, page.Body.String(), `action="/settings/qbt-api-key/generate"`)
	requireAbsent(t, page.Body.String(), "qbt_")

	response := fixture.post("/settings/qbt-api-key/generate", nil)
	requireStatus(t, response, http.StatusOK)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("generate Cache-Control = %q, want no-store", got)
	}
	body := response.Body.String()
	start := strings.Index(body, qbtkey.Prefix)
	if start < 0 {
		t.Fatalf("secret page lacks qbt key prefix: %q", body)
	}
	secret := body[start : start+qbtkey.SecretLength]
	if !qbtkey.Valid(qbtkey.Secret(secret)) {
		t.Fatalf("revealed value %q is not a well-formed qbt key", secret)
	}
	requireContains(t, body, `href="/settings"`)
	return secret
}

func TestQBTAPIKeyGenerateRevealAndSettingsDisplay(t *testing.T) {
	fixture := newWebFixture(t)
	secret := generateQBTAPIKeyThroughUI(t, fixture)
	info, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey(): %v", err)
	}
	if info.RowVersion != 0 || info.CreatedAt.IsZero() || !info.CreatedAt.Equal(info.UpdatedAt) {
		t.Errorf("stored qbt key metadata = %+v, want version 0 and equal create/update", info)
	}
	if info.Hint != "qbt_…"+secret[len(secret)-6:] {
		t.Errorf("stored hint = %q, want prefix + ellipsis + final 6 characters", info.Hint)
	}
	page := fixture.request(http.MethodGet, "/settings", nil, true)
	requireStatus(t, page, http.StatusOK)
	body := page.Body.String()
	requireContains(t, body, `action="/settings/qbt-api-key/rotate"`, `action="/settings/qbt-api-key/revoke"`, `name="expected_version" value="0"`, info.Hint, "qBittorrent API key")
	requireAbsent(t, body, secret, "sha256", "qbt_api_key")
}

func TestQBTAPIKeyGenerateWhenPresentConflicts(t *testing.T) {
	fixture := newWebFixture(t)
	generateQBTAPIKeyThroughUI(t, fixture)
	response := fixture.post("/settings/qbt-api-key/generate", nil)
	requireStatus(t, response, http.StatusConflict)
}

func TestQBTAPIKeyRotateReplacesTokenOnce(t *testing.T) {
	fixture := newWebFixture(t)
	oldSecret := generateQBTAPIKeyThroughUI(t, fixture)
	info, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey(): %v", err)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	response := fixture.post("/settings/qbt-api-key/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusOK)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("rotate Cache-Control = %q, want no-store", got)
	}
	body := response.Body.String()
	start := strings.Index(body, qbtkey.Prefix)
	if start < 0 {
		t.Fatalf("rotated secret page lacks qbt key prefix: %q", body)
	}
	newSecret := body[start : start+qbtkey.SecretLength]
	if !qbtkey.Valid(qbtkey.Secret(newSecret)) || newSecret == oldSecret {
		t.Fatalf("rotated reveal %q is not a fresh valid qbt key", newSecret)
	}
	rotated, err := fixture.store.GetQBTAPIKey(context.Background())
	if err != nil {
		t.Fatalf("GetQBTAPIKey() after rotate: %v", err)
	}
	if rotated.RowVersion != 1 || !rotated.CreatedAt.Equal(info.CreatedAt) || !rotated.UpdatedAt.After(info.UpdatedAt) {
		t.Errorf("rotated metadata = %+v, want preserved created_at, bumped version and updated_at", rotated)
	}
	if qbtkey.Verify(qbtkey.Secret(oldSecret), rotated.Digest) {
		t.Error("old qbt key still verifies after rotation")
	}
	stale := fixture.post("/settings/qbt-api-key/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, stale, http.StatusConflict)
}

func TestQBTAPIKeyRotateAbsentNotFound(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.post("/settings/qbt-api-key/rotate", url.Values{"expected_version": {"0"}})
	requireStatus(t, response, http.StatusNotFound)
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
