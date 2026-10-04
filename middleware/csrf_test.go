package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFMiddlewareIssuesToken(t *testing.T) {
	var got string
	h := CSRF(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = CSRFToken(r.Context())
	}))

	// First request: fresh cookie + matching context token.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got == "" {
		t.Fatal("context token missing")
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != got {
		t.Fatalf("csrf cookie missing or mismatched: %+v", cookie)
	}
	if !cookie.HttpOnly {
		t.Error("csrf cookie must be HttpOnly")
	}

	// Returning browser: existing cookie is reused, no new Set-Cookie.
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "browser-token"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got != "browser-token" {
		t.Errorf("reused token: got %q", got)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			t.Error("existing cookie should not be re-issued")
		}
	}
}

func TestCheckCSRF(t *testing.T) {
	newReq := func(cookieVal string) *http.Request {
		r := httptest.NewRequest("POST", "/create", nil)
		if cookieVal != "" {
			r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: cookieVal})
		}
		return r
	}
	if !CheckCSRF(newReq("tok"), "tok") {
		t.Error("matching token rejected")
	}
	if CheckCSRF(newReq("tok"), "other") {
		t.Error("mismatched token accepted")
	}
	if CheckCSRF(newReq("tok"), "") {
		t.Error("empty field accepted")
	}
	if CheckCSRF(newReq(""), "tok") {
		t.Error("missing cookie accepted")
	}
}

func TestCookieSecure(t *testing.T) {
	if !CookieSecure("https://gist.example.com") {
		t.Error("https origin should produce Secure cookies")
	}
	if CookieSecure("http://localhost:8080") || CookieSecure("") {
		t.Error("non-https origin should not produce Secure cookies")
	}
}
