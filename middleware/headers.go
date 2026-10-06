package middleware

import "net/http"

// SecurityHeaders sets the cheap, no-regret security headers. The CSP is
// deliberately limited to frame-ancestors: the templates use inline
// <script> blocks, so a script-src policy would need per-page hashes —
// separate project. 'self' keeps the PDF viewer's same-origin iframe
// working while shutting the page out of foreign frames (clickjacking).
func SecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Content-Security-Policy", "frame-ancestors 'self'")
			next.ServeHTTP(w, r)
		})
	}
}
