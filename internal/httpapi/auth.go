package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
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
		if _, ok := h.sessions.Get(cookie.Value); !ok {
			forbidden(w)
			return
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
	usernameInput, usernameConfigured := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(h.config.Username))
	passwordInput, passwordConfigured := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(h.config.Password))
	usernameMatch := subtle.ConstantTimeCompare(usernameInput[:], usernameConfigured[:])
	passwordMatch := subtle.ConstantTimeCompare(passwordInput[:], passwordConfigured[:])
	credentialsValid := usernameOK && passwordOK && usernameMatch == 1 && passwordMatch == 1
	switch h.sessions.AuthorizeLogin(r.RemoteAddr, credentialsValid) {
	case session.LoginBanned:
		plain(w, http.StatusForbidden, "Fails.")
		return
	case session.LoginInvalid:
		plain(w, http.StatusOK, "Fails.")
		return
	}
	sid, _, err := h.sessions.Create()
	if err != nil {
		internalError(w)
		return
	}
	http.SetCookie(w, sidCookie(sid, false, r.TLS != nil))
	plain(w, http.StatusOK, "Ok.")
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseURLEncodedForm(w, r, formLimit); !ok {
		return
	}
	if cookie, err := r.Cookie("SID"); err == nil {
		h.sessions.Revoke(cookie.Value)
	}
	http.SetCookie(w, sidCookie("", true, r.TLS != nil))
	w.WriteHeader(http.StatusOK)
}

func sidCookie(value string, expired, secure bool) *http.Cookie {
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
	}
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
