package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// CSRFCookieName is the double-submit CSRF cookie shared by the browser and
// the hidden form fields the templates render.
const CSRFCookieName = "csrf_token"

type csrfTokenKey struct{}

// CookieSecure decides the Secure cookie flag from the deployment origin:
// https origins get Secure cookies; http origins cannot use them.
func CookieSecure(baseURL string) bool {
	return strings.HasPrefix(baseURL, "https://")
}

// CSRF ensures every request carries a per-browser token: safe methods get a
// fresh HttpOnly cookie when none exists, and the token travels to handlers
// through the context so templates can embed it in forms. POST handlers
// compare the submitted field against the cookie themselves (CheckCSRF) —
// they own the request body, so the middleware must not parse it.
func CSRF(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(CSRFCookieName); err == nil {
				token = c.Value
			}
			if token == "" {
				b := make([]byte, 32)
				if _, err := rand.Read(b); err != nil {
					http.Error(w, "Failed to generate CSRF token", http.StatusInternalServerError)
					return
				}
				token = base64.URLEncoding.EncodeToString(b)
				http.SetCookie(w, &http.Cookie{
					Name:     CSRFCookieName,
					Value:    token,
					Path:     "/",
					HttpOnly: true,
					Secure:   secure,
					SameSite: http.SameSiteLaxMode,
				})
			}
			ctx := context.WithValue(r.Context(), csrfTokenKey{}, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CSRFToken returns the request's CSRF token as installed by CSRF.
func CSRFToken(ctx context.Context) string {
	tok, _ := ctx.Value(csrfTokenKey{}).(string)
	return tok
}

// CheckCSRF reports whether provided (the form field) matches the CSRF
// cookie. Both must be present and equal.
func CheckCSRF(r *http.Request, provided string) bool {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || c.Value == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(provided)) == 1
}
