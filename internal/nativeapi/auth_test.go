package nativeapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turygo/cd211/internal/token"
)

const (
	unauthorizedJSON = "{\"error\":{\"code\":\"unauthorized\",\"message\":\"API token is invalid\"}}\n"
	internalJSON     = "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal Server Error\"}}\n"
)

type fakeTokenRepository struct {
	info token.Token
	err  error
}

func (fake *fakeTokenRepository) GetAPIToken(context.Context) (token.Token, error) {
	return fake.info, fake.err
}

func TestNewAuthRejectsNilRepository(t *testing.T) {
	if auth, err := NewAuth(nil); err == nil || auth != nil {
		t.Errorf("NewAuth(nil) = (%v, %v), want validation error", auth, err)
	}
}

// exerciseAuth runs the middleware over a request carrying the given
// Authorization value and returns the response.
func exerciseAuth(t *testing.T, repo TokenRepository, authorization string, extraValues ...string) *httptest.ResponseRecorder {
	t.Helper()
	auth, err := NewAuth(repo)
	if err != nil {
		t.Fatalf("NewAuth(): %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/abc", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	for _, value := range extraValues {
		request.Header.Add("Authorization", value)
	}
	response := httptest.NewRecorder()
	auth.Middleware(next).ServeHTTP(response, request)
	return response
}

func requireUnauthorized(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := response.Body.String(); got != unauthorizedJSON {
		t.Errorf("body = %q, want identical 401 JSON %q", got, unauthorizedJSON)
	}
}

func TestAuthRejectsMissingMalformedAndMultipleHeaders(t *testing.T) {
	secret, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate(): %v", err)
	}
	repo := &fakeTokenRepository{info: token.Token{Digest: token.Hash(secret)}}

	cases := []struct {
		name        string
		authorized  string
		extraValues []string
	}{
		{"missing header", "", nil},
		{"wrong scheme", "Basic dXNlcjpwYXNz", nil},
		{"lowercase scheme", "bearer " + string(secret), nil},
		{"malformed double space", "Bearer  " + string(secret), nil},
		{"token with inner space", "Bearer " + string(secret) + " x", nil},
		{"empty token", "Bearer ", nil},
		{"malformed token shape", "Bearer not_a_token", nil},
		{"wrong token", "Bearer " + string(secret) + "AAAA", nil},
		{"multiple headers", "Bearer " + string(secret), []string{"Bearer another"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := exerciseAuth(t, repo, test.authorized, test.extraValues...)
			requireUnauthorized(t, response)
		})
	}
}

func TestAuthAcceptsExactBearerToken(t *testing.T) {
	secret, err := token.Generate()
	if err != nil {
		t.Fatalf("token.Generate(): %v", err)
	}
	repo := &fakeTokenRepository{info: token.Token{Digest: token.Hash(secret)}}
	response := exerciseAuth(t, repo, "Bearer "+string(secret))
	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Errorf("authenticated response = (%d, %q), want 200 ok", response.Code, response.Body.String())
	}

	// A SID cookie alongside a valid Bearer token is ignored and does not
	// interfere with the accepted credential.
	auth, err := NewAuth(repo)
	if err != nil {
		t.Fatalf("NewAuth(): %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/abc", nil)
	request.Header.Set("Authorization", "Bearer "+string(secret))
	request.AddCookie(&http.Cookie{Name: "SID", Value: "attacker-controlled"})
	response = httptest.NewRecorder()
	auth.Middleware(next).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Errorf("cookie-carrying authenticated request status = %d, want 204", response.Code)
	}
}

func TestAuthUnconfiguredTokenIsIdenticalUnauthorized(t *testing.T) {
	response := exerciseAuth(t, &fakeTokenRepository{err: token.ErrNotFound}, "Bearer whatever")
	requireUnauthorized(t, response)
}

func TestAuthRepositoryFailureIsStableInternalError(t *testing.T) {
	response := exerciseAuth(t, &fakeTokenRepository{err: errors.New("database exploded")}, "Bearer whatever")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%q", response.Code, response.Body.String())
	}
	if got := response.Body.String(); got != internalJSON {
		t.Errorf("body = %q, want stable 500 JSON %q", got, internalJSON)
	}
	if strings.Contains(response.Body.String(), "database exploded") {
		t.Error("500 body leaks the raw repository error")
	}
}
