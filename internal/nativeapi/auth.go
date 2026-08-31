// Package nativeapi implements the native automation API surface. Phase 1
// ships only the reusable Bearer authentication middleware; the submit/query/
// wait/events handlers arrive in later phases. The middleware enforces the
// single global API token and rejects every other credential (SID cookie,
// operator password, other Authorization schemes).
package nativeapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/turygo/cd211/internal/authn"
	"github.com/turygo/cd211/internal/logging"
	"github.com/turygo/cd211/internal/token"
)

// TokenRepository is the narrow read boundary the auth middleware needs.
// *store.Store implements it.
type TokenRepository interface {
	GetAPIToken(context.Context) (token.Token, error)
}

// Auth enforces Bearer authentication for native API routes.
type Auth struct {
	tokens TokenRepository
}

// NewAuth constructs the middleware from the token read boundary. The
// repository must be non-nil; nil is a programming error, not a runtime
// fallback.
func NewAuth(tokens TokenRepository) (*Auth, error) {
	if tokens == nil {
		return nil, errors.New("nativeapi: token repository is nil")
	}
	return &Auth{tokens: tokens}, nil
}

// Stable JSON error bodies. They are pre-rendered constants so auth-path
// responses allocate nothing and never vary: the 401 body is byte-identical
// for every invalid-token outcome, and the 500 body never leaks the raw
// repository error.
const (
	unauthorizedBody = "{\"error\":{\"code\":\"unauthorized\",\"message\":\"API token is invalid\"}}\n"
	internalBody     = "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal Server Error\"}}\n"
)

// Middleware wraps next so that only requests carrying the exact
// Authorization: Bearer <token> header pass through. Missing, multiple, or
// malformed Authorization values, an unconfigured token, and a token that
// does not match the stored digest all yield the identical 401 response, so
// the endpoint never reveals whether a token exists. Repository failures
// yield a stable 500; the raw error and the request header are never logged
// or exposed.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logging.SetAuthAttempt(r, "native", nativeAuthAttempt(r))
		secret, ok := bearerToken(r)
		if !ok {
			unauthorized(w)
			return
		}
		info, err := a.tokens.GetAPIToken(r.Context())
		if err != nil {
			if errors.Is(err, token.ErrNotFound) {
				unauthorized(w)
				return
			}
			internalError(w)
			return
		}
		if !token.Verify(token.Secret(secret), info.Digest) {
			unauthorized(w)
			return
		}
		principal := authn.Principal{Kind: authn.NativeClientPrincipal, Method: authn.NativeTokenMethod}
		logging.SetAuthSuccess(r, principal)
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), principal)))
	})
}

func nativeAuthAttempt(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "authorization"
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "authorization"
	}
	if index := strings.IndexAny(value, " \t"); index >= 0 {
		value = value[:index]
	}
	if strings.EqualFold(value, "bearer") {
		return "native_token"
	}
	return "authorization"
}

// bearerToken extracts the exact single "Bearer <token>" value. It rejects a
// missing header, multiple Authorization headers, any other scheme, and any
// value that is not exactly one scheme, one space, and one non-empty token
// without inner whitespace.
func bearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	const scheme = "Bearer "
	if !strings.HasPrefix(value, scheme) {
		return "", false
	}
	secret := strings.TrimPrefix(value, scheme)
	if secret == "" || strings.ContainsAny(secret, " \t") {
		return "", false
	}
	return secret, true
}

func unauthorized(w http.ResponseWriter) {
	authHeaders(w)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(unauthorizedBody))
}

func internalError(w http.ResponseWriter) {
	authHeaders(w)
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(internalBody))
}

// authHeaders marks every auth-path response uncacheable so a one-time token
// or an invalid-state answer can never be replayed from a cache.
func authHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
}
