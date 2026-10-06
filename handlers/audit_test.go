package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dolmen/config"
	"github.com/google/uuid"
)

// Regression coverage for the run-1 security audit fixes.

func TestValidGistID(t *testing.T) {
	good := uuid.NewString()
	if !validGistID(good) {
		t.Errorf("%q must be valid", good)
	}
	for _, bad := range []string{
		"", "x", strings.ToUpper(good),
		"{" + good + "}", "urn:uuid:" + good,
		strings.ReplaceAll(good, "-", ""),
		"../../etc/passwd", "a b", good + " ",
	} {
		if validGistID(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestValidUploadKeyCanonicalUUIDOnly(t *testing.T) {
	id := uuid.NewString()
	if !validUploadKey("uploads/u1/"+id, "u1") {
		t.Fatalf("canonical key must be valid")
	}
	for _, bad := range []string{
		"uploads/u1/" + strings.ToUpper(id),
		"uploads/u1/{" + id + "}",
		"uploads/u1/urn:uuid:" + id,
		"uploads/u1/" + strings.ReplaceAll(id, "-", ""),
		"uploads/u2/" + id,
		"uploads/u1/" + id + "/extra",
		"uploads/u1/" + id + ".json",
	} {
		if validUploadKey(bad, "u1") {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func isHexUp(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'F')
}

func TestContentDispositionSafeCharset(t *testing.T) {
	const mid = `"; filename*=UTF-8''`
	for b := 0; b < 256; b++ {
		got := contentDisposition(fmt.Sprintf("a%cb.in", byte(b)))
		parts := strings.SplitN(got, mid, 2)
		if len(parts) != 2 {
			t.Fatalf("malformed disposition for byte %d: %q", b, got)
		}
		fallback := strings.TrimPrefix(parts[0], `attachment; filename="`)
		for _, rr := range fallback {
			if rr < 0x20 || rr > 0x7e || rr == '"' || rr == '\\' {
				t.Errorf("fallback leaks %q for byte %d: %q", rr, b, got)
			}
		}
		enc := parts[1]
		for i := 0; i < len(enc); i++ {
			if enc[i] == '%' {
				if i+2 >= len(enc) || !isHexUp(enc[i+1]) || !isHexUp(enc[i+2]) {
					t.Errorf("filename* has stray percent at %d for byte %d: %q", i, b, got)
				}
				i += 2
				continue
			}
			if strings.IndexByte(attrChars, enc[i]) < 0 {
				t.Errorf("filename* leaks byte %#x for byte %d: %q", enc[i], b, got)
			}
		}
	}
	if got := contentDisposition("café.png"); !strings.Contains(got, `filename*=UTF-8''caf%C3%A9.png`) {
		t.Errorf("UTF-8 name not RFC 5987-encoded: %q", got)
	}
	if got := contentDisposition("a%0d%0aX"); !strings.Contains(got, `filename*=UTF-8''a%250d%250aX`) {
		t.Errorf("percent-literal not re-encoded: %q", got)
	}
}

func TestCallbackRejectsEmptyStateAndExpiresFlowCookies(t *testing.T) {
	h := &Handler{baseURL: "https://gist.example", oidcCfg: config.OIDCConfig{ClientSecret: "s"}}
	req := httptest.NewRequest("GET", "/callback?code=x&state=", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: ""})
	req.AddCookie(&http.Cookie{Name: "oauth_code_verifier", Value: "v"})
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-vs-empty state passed: %d", rec.Code)
	}
	set := rec.Result().Header.Values("Set-Cookie")
	want := map[string]bool{"oauth_state": false, "oauth_code_verifier": false, "redirect_url": false}
	for _, c := range set {
		for name := range want {
			// Go serializes MaxAge -1 as "Max-Age=0".
			if strings.HasPrefix(c, name+"=") && strings.Contains(c, "Max-Age=0") {
				want[name] = true
			}
		}
	}
	for name, expired := range want {
		if !expired {
			t.Errorf("%s not expired on the response: %v", name, set)
		}
	}
}

func TestCallbackStateMismatchRejected(t *testing.T) {
	h := &Handler{oidcCfg: config.OIDCConfig{ClientSecret: "s"}}
	req := httptest.NewRequest("GET", "/callback?code=x&state=attacker", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "victim"})
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch accepted: %d", rec.Code)
	}
}

func TestSetCookieAttributes(t *testing.T) {
	https := &Handler{baseURL: "https://gist.example"}
	rec := httptest.NewRecorder()
	https.setCookie(rec, "id_token", "v", 3600, http.SameSiteLaxMode)
	if c := rec.Result().Header.Get("Set-Cookie"); !strings.Contains(c, "SameSite=Lax") || !strings.Contains(c, "Secure") {
		t.Errorf("https id_token: %q", c)
	}
	http1 := &Handler{baseURL: "http://localhost:8080"}
	rec = httptest.NewRecorder()
	http1.setCookie(rec, "oauth_state", "v", 600, http.SameSiteDefaultMode)
	if c := rec.Result().Header.Get("Set-Cookie"); strings.Contains(c, "SameSite") || strings.Contains(c, "Secure") {
		t.Errorf("plain-http flow cookie must not force SameSite/Secure: %q", c)
	}
}

func TestGistFromFormNameLengthCap(t *testing.T) {
	long := url.Values{"file0_name": {strings.Repeat("x", maxFileNameLen+1)}, "file0_content": {"x"}}
	if _, err := gistFromForm(long); !errors.Is(err, errNameTooLong) {
		t.Errorf("over-long name accepted: %v", err)
	}
	ok := url.Values{"file0_name": {strings.Repeat("x", maxFileNameLen)}, "file0_content": {"x"}}
	if _, err := gistFromForm(ok); err != nil {
		t.Errorf("max-length name rejected: %v", err)
	}
}
