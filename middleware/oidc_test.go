package middleware

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestExtractToken(t *testing.T) {
	request := func(cookies map[string]string, bearer string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for name, value := range cookies {
			r.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		return r
	}

	tests := []struct {
		name    string
		cookies map[string]string
		bearer  string
		want    string
	}{
		{
			name:    "id_token wins over bearer",
			cookies: map[string]string{"id_token": "from-app"},
			bearer:  "from-header",
			want:    "from-app",
		},
		{
			name:   "falls back to bearer when no cookie present",
			bearer: "from-header",
			want:   "from-header",
		},
		{
			name:    "no token anywhere",
			cookies: map[string]string{"other": "x"},
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractToken(request(tc.cookies, tc.bearer)); got != tc.want {
				t.Errorf("extractToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A proxy-forwarded token must not touch session lifetime: the middleware
// reads the token and never sets cookies itself, so the app's own 1h
// id_token expiry never caps the proxy session. The only expiry enforced
// on a proxy-supplied token is the token's own exp claim.
func TestOIDCMiddlewareBearerNoSessionExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "testkey",
			"n": b64(key.PublicKey.N.Bytes()),
			"e": b64(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}
	srv := httptest.NewServer(http.HandlerFunc(jwks))
	defer srv.Close()
	issuer := srv.URL + "/dex"
	verifier := oidc.NewVerifier(issuer, oidc.NewRemoteKeySet(context.Background(), issuer+"/keys"),
		&oidc.Config{ClientID: "gist-client"})

	sign := func(exp time.Time, sub string) string {
		enc := func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				panic(err)
			}
			return b64(b)
		}
		head := enc(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "testkey"})
		body := enc(map[string]any{
			"iss": issuer, "aud": "gist-client", "sub": sub,
			"email": "alice@example.com", "iat": time.Now().Unix(), "exp": exp.Unix(),
		})
		h := sha256.Sum256([]byte(head + "." + body))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
		if err != nil {
			panic(err)
		}
		return head + "." + body + "." + b64(sig)
	}

	serve := func(t *testing.T, mw http.Handler, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/gists", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, r)
		return w
	}

	t.Run("valid bearer token passes with no cookies set", func(t *testing.T) {
		var userID, email string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID = GetUserID(r.Context())
			email = GetUserEmail(r.Context())
		})
		mw := OIDCMiddleware(verifier, true)(next)
		w := serve(t, mw, sign(time.Now().Add(time.Hour), "alice"))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body)
		}
		// sha256("alice") truncated to 16 hex chars: the storage layer sees
		// this, never the email.
		if userID != "2bd806c97f0e00af" {
			t.Errorf("user id = %q, want the opaque id", userID)
		}
		if email != "alice@example.com" {
			t.Errorf("email = %q, want it passed through for display", email)
		}
		// The regression this pins: no auth cookie written by the app — no
		// Max-Age, no expiry, nothing the browser would age out.
		if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("middleware set cookies: %v", got)
		}
	})

	t.Run("token without sub is rejected", func(t *testing.T) {
		mw := OIDCMiddleware(verifier, true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("handler ran without a sub claim")
		}))
		w := serve(t, mw, sign(time.Now().Add(time.Hour), ""))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("empty sub: code %d, want 401", w.Code)
		}
	})

	t.Run("expired bearer token redirects despite valid signature", func(t *testing.T) {
		mw := OIDCMiddleware(verifier, true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("handler ran with an expired token")
		}))
		w := serve(t, mw, sign(time.Now().Add(-time.Minute), "alice"))
		if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?redirect=") {
			t.Errorf("expired token: code %d, location %q", w.Code, w.Header().Get("Location"))
		}
	})

	// Without a built-in login there is no /login to send anyone to: a 401
	// is what an authenticating proxy needs to see to re-authenticate.
	t.Run("login disabled answers 401 for missing and invalid tokens", func(t *testing.T) {
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("handler ran for an unauthenticated request")
		})
		mw := OIDCMiddleware(verifier, false)(next)
		w := serve(t, mw, sign(time.Now().Add(-time.Minute), "alice"))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expired token, login disabled: code %d, want 401", w.Code)
		}
		r := httptest.NewRequest(http.MethodGet, "/gists", nil)
		w = httptest.NewRecorder()
		mw.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("missing token, login disabled: code %d, want 401", w.Code)
		}
	})
}
