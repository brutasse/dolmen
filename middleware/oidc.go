package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"

	"dolmen/config"
	"github.com/coreos/go-oidc/v3/oidc"
)

// UserIDKey is the context key for user ID
type UserIDKey struct{}

// EmailKey is the context key for the email address, carried for display
// only; storage keys use the opaque user ID.
type EmailKey struct{}

// opaqueUserID derives the storage user ID from the OIDC sub claim: stable
// (email renames no longer orphan gists), uniform, and path-safe.
func opaqueUserID(sub string) string {
	sum := sha256.Sum256([]byte(sub))
	return hex.EncodeToString(sum[:])[:16]
}

// OIDCMiddleware validates OIDC tokens and resolves an opaque user ID from
// the mandatory sub claim; the email claim goes along as display metadata.
// authCookieName names an extra cookie holding a signed ID token, e.g. one
// set by an authenticating proxy; it is consulted before the app's own
// id_token cookie.
func OIDCMiddleware(verifier *oidc.IDTokenVerifier, authCookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" || r.URL.Path == "/callback" {
				next.ServeHTTP(w, r)
				return
			}
			token := extractToken(r, authCookieName)
			if token == "" {
				redirectURL := url.QueryEscape(r.URL.Path + "?" + r.URL.RawQuery)
				http.Redirect(w, r, "/login?redirect="+redirectURL, http.StatusFound)
				return
			}

			idToken, err := verifier.Verify(r.Context(), token)
			if err != nil {
				redirectURL := url.QueryEscape(r.URL.Path + "?" + r.URL.RawQuery)
				http.Redirect(w, r, "/login?redirect="+redirectURL, http.StatusFound)
				return
			}

			var claims struct {
				Sub   string `json:"sub"`
				Email string `json:"email"`
			}
			if err := idToken.Claims(&claims); err != nil {
				http.Error(w, "Invalid claims", http.StatusUnauthorized)
				return
			}

			if claims.Sub == "" {
				http.Error(w, "Invalid claims", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey{}, opaqueUserID(claims.Sub))
			ctx = context.WithValue(ctx, EmailKey{}, claims.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken extracts the ID token from the configured proxy cookie, the
// app's own id_token cookie, or the Authorization header, in that order.
func extractToken(r *http.Request, authCookieName string) string {
	if authCookieName != "" {
		if cookie, err := r.Cookie(authCookieName); err == nil {
			return cookie.Value
		}
	}
	if cookie, err := r.Cookie("id_token"); err == nil {
		return cookie.Value
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

// GetUserID gets the user ID from context
func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(UserIDKey{}).(string); ok {
		return userID
	}
	return ""
}

// GetUserEmail gets the display email from context; empty when the identity
// provider omitted the claim.
func GetUserEmail(ctx context.Context) string {
	if email, ok := ctx.Value(EmailKey{}).(string); ok {
		return email
	}
	return ""
}

// NewOIDCProviderAndVerifier creates provider and verifier
func NewOIDCProviderAndVerifier(ctx context.Context, cfg config.OIDCConfig) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, nil, http.ErrNotSupported
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, nil, err
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return provider, verifier, nil
}
