package httpapi

import (
	"encoding/base64"
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/authn"
	"github.com/turygo/cd211/internal/logging"
	"github.com/turygo/cd211/internal/qbtkey"
	"github.com/turygo/cd211/internal/session"
)

func (h *handler) authBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) > 1 {
			logging.SetAuthAttempt(r, "qbt", "authorization")
			forbidden(w)
			return
		}
		if len(values) == 1 {
			logging.SetAuthAttempt(r, "qbt", qbtAuthAttempt(values[0]))
			scheme, value, ok := authValue(values[0])
			if !ok {
				forbidden(w)
				return
			}
			switch strings.ToLower(scheme) {
			case "basic":
				username, password, ok := basicCredentials(value)
				if !ok {
					_ = h.sessions.AuthorizeLogin(r.RemoteAddr, false)
					forbidden(w)
					return
				}
				valid, err := h.creds.Verify(r.Context(), username, password)
				if err != nil {
					internalError(w)
					return
				}
				switch h.sessions.AuthorizeLogin(r.RemoteAddr, valid) {
				case session.LoginBanned:
					forbidden(w)
					return
				case session.LoginInvalid:
					forbidden(w)
					return
				}
				if !valid || !browserOriginAllowed(r) {
					forbidden(w)
					return
				}
				principal := authn.Principal{Kind: authn.OperatorPrincipal, Method: authn.BasicMethod}
				logging.SetAuthSuccess(r, principal)
				r = r.WithContext(authn.WithPrincipal(r.Context(), principal))
			case "bearer":
				secret := qbtkey.Secret(value)
				if !qbtkey.Valid(secret) {
					forbidden(w)
					return
				}
				key, err := h.qbtkeys.GetQBTAPIKey(r.Context())
				if err != nil {
					if errors.Is(err, qbtkey.ErrNotFound) {
						forbidden(w)
						return
					}
					internalError(w)
					return
				}
				if !qbtkey.Verify(secret, key.Digest) || !browserOriginAllowed(r) {
					forbidden(w)
					return
				}
				principal := authn.Principal{Kind: authn.QBTClientPrincipal, Method: authn.QBTKeyMethod}
				logging.SetAuthSuccess(r, principal)
				r = r.WithContext(authn.WithPrincipal(r.Context(), principal))
			default:
				forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("SID")
		attempt := "none"
		if err == nil {
			attempt = "session"
		}
		logging.SetAuthAttempt(r, "qbt", attempt)
		if err != nil || cookie.Value == "" {
			forbidden(w)
			return
		}
		current, renewed, err := h.sessions.Get(r.Context(), cookie.Value, session.AudienceQBT)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				forbidden(w)
				return
			}
			internalError(w)
			return
		}
		if !browserOriginAllowed(r) {
			forbidden(w)
			return
		}
		if renewed {
			http.SetCookie(w, sidCookie(cookie.Value, false, r.TLS != nil, h.clock.Now(), current.ExpiresAt))
		}
		principal := authn.Principal{Kind: authn.QBTClientPrincipal, Method: authn.SessionMethod}
		logging.SetAuthSuccess(r, principal)
		r = r.WithContext(authn.WithPrincipal(r.Context(), principal))
		next.ServeHTTP(w, r)
	})
}

func qbtAuthAttempt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "authorization"
	}
	if index := strings.IndexAny(value, " \t"); index >= 0 {
		value = value[:index]
	}
	switch strings.ToLower(value) {
	case "basic":
		return "basic"
	case "bearer":
		return "qbt_key"
	default:
		return "authorization"
	}
}

func authValue(value string) (scheme, credentials string, ok bool) {
	if strings.TrimSpace(value) != value {
		return "", "", false
	}
	space := strings.IndexByte(value, ' ')
	if space <= 0 || space == len(value)-1 || strings.IndexByte(value[space+1:], ' ') >= 0 || strings.ContainsAny(value, "\t\r\n") {
		return "", "", false
	}
	return value[:space], value[space+1:], true
}

func basicCredentials(value string) (username, password string, ok bool) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || strings.ContainsAny(string(decoded), "\x00\r\n") {
		return "", "", false
	}
	username, password, ok = strings.Cut(string(decoded), ":")
	return username, password, ok && username != ""
}

func browserOriginAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, r.Host)
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	logging.SetAuthAttempt(r, "qbt", "basic")
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	username, usernameOK := exactlyOne(form["username"])
	password, passwordOK := exactlyOne(form["password"])
	credentialsValid := usernameOK && passwordOK
	if credentialsValid {
		match, err := h.creds.Verify(r.Context(), username, password)
		if err != nil {
			internalError(w)
			return
		}
		credentialsValid = match
	}
	switch h.sessions.AuthorizeLogin(r.RemoteAddr, credentialsValid) {
	case session.LoginBanned:
		plain(w, http.StatusForbidden, "Fails.")
		return
	case session.LoginInvalid:
		plain(w, http.StatusOK, "Fails.")
		return
	}
	sid, current, err := h.sessions.Create(r.Context(), session.AudienceQBT)
	if err != nil {
		internalError(w)
		return
	}
	http.SetCookie(w, sidCookie(sid, false, r.TLS != nil, h.clock.Now(), current.ExpiresAt))
	logging.SetAuthSuccess(r, authn.Principal{Kind: authn.OperatorPrincipal, Method: authn.BasicMethod})
	plain(w, http.StatusOK, "Ok.")
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseURLEncodedForm(w, r, formLimit); !ok {
		return
	}
	if cookie, err := r.Cookie("SID"); err == nil {
		if err := h.sessions.Revoke(r.Context(), cookie.Value, session.AudienceQBT); err != nil {
			internalError(w)
			return
		}
	}
	http.SetCookie(w, sidCookie("", true, r.TLS != nil, time.Time{}, time.Time{}))
	w.WriteHeader(http.StatusOK)
}

func sidCookie(value string, expired, secure bool, now, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{Name: "SID", Value: value, Path: "/api/v2", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure}
	if expired {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0).UTC()
		return cookie
	}
	cookie.MaxAge = int(math.Ceil(expiresAt.Sub(now).Seconds()))
	cookie.Expires = expiresAt.UTC()
	return cookie
}

func parseURLEncodedForm(w http.ResponseWriter, r *http.Request, limit int64) (map[string][]string, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		badRequest(w)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseForm(); err != nil {
		badRequest(w)
		return nil, false
	}
	return r.PostForm, true
}

func exactlyOne(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}
