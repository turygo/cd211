package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/turygo/cd211/internal/qbtkey"
)

func qbtTestKey(t *testing.T) qbtkey.Secret {
	t.Helper()
	secret, err := qbtkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func setQBTTestKey(t *testing.T, harness *contractHarness, secret qbtkey.Secret) {
	t.Helper()
	harness.qbtkeys.err = nil
	harness.qbtkeys.key = qbtkey.Key{Digest: qbtkey.Hash(secret)}
}

func doAuthorization(t *testing.T, handler http.Handler, method, target, body string, cookie *http.Cookie, values []string, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, value := range values {
		request.Header.Add("Authorization", value)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestQBTBearerAuthenticationContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	secret := qbtTestKey(t)
	setQBTTestKey(t, harness, secret)

	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "GET", method: http.MethodGet, target: "/api/v2/app/version"},
		{name: "POST", method: http.MethodPost, target: "/api/v2/torrents/setShareLimits", body: url.Values{}.Encode()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := doAuthorization(t, harness.api, test.method, test.target, test.body, nil, []string{"Bearer " + string(secret)}, "")
			if response.Code != http.StatusOK {
				t.Fatalf("Bearer %s = %d %q, want 200", test.name, response.Code, response.Body.String())
			}
			if cookies := response.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("Bearer %s emitted SID mutation: %#v", test.name, cookies)
			}
		})
	}
}

func TestQBTBearerPrecedenceAndFailures(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	secret := qbtTestKey(t)
	setQBTTestKey(t, harness, secret)
	cookie := harness.login(t)

	tests := []struct {
		name       string
		headers    []string
		err        error
		digest     []byte
		cookie     *http.Cookie
		wantStatus int
	}{
		{name: "missing SID", headers: nil, wantStatus: http.StatusForbidden},
		{name: "malformed empty", headers: []string{"Bearer "}, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "malformed token", headers: []string{"Bearer qbt_invalid"}, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "wrong scheme", headers: []string{"bearer " + string(secret)}, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "internal whitespace", headers: []string{"Bearer " + string(secret)[:5] + " " + string(secret)[5:]}, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "multiple headers", headers: []string{"Bearer " + string(secret), "Bearer " + string(secret)}, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "wrong digest", headers: []string{"Bearer " + string(secret)}, digest: qbtkey.Hash(qbtkey.Secret("qbt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")), cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "not found", headers: []string{"Bearer " + string(secret)}, err: qbtkey.ErrNotFound, cookie: cookie, wantStatus: http.StatusForbidden},
		{name: "repository failure", headers: []string{"Bearer " + string(secret)}, err: errors.New("qbt key lookup failed"), cookie: cookie, wantStatus: http.StatusInternalServerError},
		{name: "valid with SID", headers: []string{"Bearer " + string(secret)}, cookie: cookie, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness.qbtkeys.err = test.err
			harness.qbtkeys.key.Digest = test.digest
			if test.digest == nil && test.err == nil {
				harness.qbtkeys.key.Digest = qbtkey.Hash(secret)
			}
			response := doAuthorization(t, harness.api, http.MethodGet, "/api/v2/app/version", "", test.cookie, test.headers, "")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d %q, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
		})
	}

	harness.qbtkeys.err = nil
	harness.qbtkeys.key.Digest = qbtkey.Hash(secret)
	if response := doAuthorization(t, harness.api, http.MethodGet, "/api/v2/app/version", "", cookie, []string{"Bearer qbt_invalid"}, ""); response.Code != http.StatusForbidden {
		t.Fatalf("invalid Bearer with valid SID = %d, want 403", response.Code)
	}
	if response := doRequest(t, harness.api, http.MethodGet, "/api/v2/app/version", nil, cookie); response.Code != http.StatusOK {
		t.Fatalf("SID after rejected Bearer = %d, want 200", response.Code)
	}
	if harness.qbtkeys.gets == 0 {
		t.Fatal("Bearer requests did not consult qBittorrent key repository")
	}
}

func TestQBTBearerOriginLoginAndLogoutContract(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	secret := qbtTestKey(t)
	setQBTTestKey(t, harness, secret)
	header := []string{"Bearer " + string(secret)}

	crossOrigin := doAuthorization(t, harness.api, http.MethodPost, "/api/v2/torrents/setShareLimits", url.Values{}.Encode(), nil, header, "https://evil.example")
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin Bearer mutation = %d, want 403", crossOrigin.Code)
	}

	login := doAuthorization(t, harness.api, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {"user"}, "password": {"password"}}.Encode(), nil, []string{"Bearer malformed"}, "")
	if login.Code != http.StatusOK || login.Body.String() != "Ok." {
		t.Fatalf("login with Authorization = %d %q, want independent success", login.Code, login.Body.String())
	}

	logout := doAuthorization(t, harness.api, http.MethodPost, "/api/v2/auth/logout", url.Values{}.Encode(), nil, header, "")
	if logout.Code != http.StatusOK {
		t.Fatalf("Bearer-only logout = %d %q, want 200", logout.Code, logout.Body.String())
	}
	cookies := logout.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "SID" || cookies[0].MaxAge >= 0 {
		t.Fatalf("Bearer-only logout cookies = %#v, want expired SID", cookies)
	}
}
