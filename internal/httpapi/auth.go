package httpapi

import (
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/session"
)

func (h *handler) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("SID")
		if err != nil || cookie.Value == "" {
			forbidden(w)
			return
		}
		current, renewed, err := h.sessions.Get(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				forbidden(w)
				return
			}
			internalError(w)
			return
		}
		if renewed {
			http.SetCookie(w, sidCookie(cookie.Value, false, r.TLS != nil, h.clock.Now(), current.ExpiresAt))
		}
		if !browserOriginAllowed(r) {
			forbidden(w)
			return
		}
		next(w, r)
	})
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
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, r.Host)
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
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
	sid, current, err := h.sessions.Create(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	http.SetCookie(w, sidCookie(sid, false, r.TLS != nil, h.clock.Now(), current.ExpiresAt))
	plain(w, http.StatusOK, "Ok.")
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseURLEncodedForm(w, r, formLimit); !ok {
		return
	}
	if cookie, err := r.Cookie("SID"); err == nil {
		if err := h.sessions.Revoke(r.Context(), cookie.Value); err != nil {
			internalError(w)
			return
		}
	}
	http.SetCookie(w, sidCookie("", true, r.TLS != nil, time.Time{}, time.Time{}))
	w.WriteHeader(http.StatusOK)
}

func sidCookie(value string, expired, secure bool, now, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name:     "SID",
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
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
