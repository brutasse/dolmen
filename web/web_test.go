package web

import (
	"bytes"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestAssetURLsContentAddressed(t *testing.T) {
	for _, name := range []string{"app.css", "chroma.css", "app.js", "favicon.svg", "vendor/tex-svg.js", "vendor/mermaid.min.js", "fonts/inter-var.woff2"} {
		u, ok := assetURLs[name]
		if !ok {
			t.Fatalf("no content-addressed url for %s", name)
		}
		ext := name[strings.LastIndexByte(name, '.'):]
		base := strings.TrimSuffix(name, ext)
		re := regexp.MustCompile(`^/static/` + regexp.QuoteMeta(base) + `\.[0-9a-f]{10}` + regexp.QuoteMeta(ext) + `$`)
		if !re.MatchString(u) {
			t.Errorf("assetURLs[%q] = %q, want hashed form", name, u)
		}
	}
}

func TestStaticServesHashedImmutable(t *testing.T) {
	u := assetURLs["app.css"]
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", u, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d, want 200", u, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	css := rec.Body.String()
	if !strings.Contains(css, `url("/static/fonts/inter-var.`) {
		t.Error("font url not rewritten to hashed form")
	}
	if strings.Contains(css, `/static/fonts/inter-var.woff2"`) {
		t.Error("plain font url remains in served css")
	}
}

func TestStaticPlainRevalidatesAndStaleHash404s(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/fonts/inter-var.woff2", nil))
	if rec.Code != 200 {
		t.Fatalf("plain font GET = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
		t.Errorf("Content-Type = %q, want font/woff2", ct)
	}

	rec = httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.deadbeef00.css", nil))
	if rec.Code != 404 {
		t.Errorf("stale hash GET = %d, want 404", rec.Code)
	}
}

func TestRenderUsesHashedAssetURLs(t *testing.T) {
	data := map[string]any{
		"CSRF":  "tok",
		"Email": "alice@example.com",
		"Gist":  map[string]any{"ID": "", "Files": []any{}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, "edit", data); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`href="/static/app\.[0-9a-f]{10}\.css"`),
		regexp.MustCompile(`href="/static/chroma\.[0-9a-f]{10}\.css"`),
	} {
		if !re.MatchString(body) {
			t.Errorf("rendered page lacks %s", re)
		}
	}
}

func TestRenderHeaderAndContextualBackLink(t *testing.T) {
	render := func(t *testing.T, id string) string {
		t.Helper()
		var buf bytes.Buffer
		data := map[string]any{
			"CSRF":  "tok",
			"Email": "alice@example.com",
			"Gist":  map[string]any{"ID": id, "Files": []any{}},
		}
		if err := Render(&buf, "edit", data); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	home := render(t, "")
	if !strings.Contains(home, `<span class="avatar">A</span>alice</span>`) {
		t.Error("avatar pill with local part missing")
	}
	if !strings.Contains(home, `title="Signed in as alice@example.com"`) {
		t.Error("full email not in tooltip")
	}
	if strings.Contains(home, "Back to gists") {
		t.Error("create homepage should not show back link")
	}
	if strings.Contains(home, ">Cancel</a>") {
		t.Error("create page must not offer Cancel (nowhere to go back to)")
	}
	edit := render(t, "abc123")
	if strings.Contains(edit, "Back to gists") {
		t.Error("edit page must not show a back link (top nav covers it)")
	}
	if !strings.Contains(edit, `href="/gist/abc123"`) {
		t.Error("edit page must offer a Cancel link back to the gist")
	}
	if !strings.Contains(edit, `action="/edit/abc123"`) {
		t.Error("edit form action wrong")
	}
}

// template.URL must survive escaping; unknown names fall back to the plain
// (revalidating) path.
func TestAssetFuncFallsBackForUnknown(t *testing.T) {
	if got := string(assetFunc("does-not-exist.css")); got != "/static/does-not-exist.css" {
		t.Errorf("assetFunc fallback = %q, want /static/does-not-exist.css", got)
	}
	if got := string(assetFunc("app.css")); !strings.HasPrefix(got, "/static/app.") {
		t.Errorf("assetFunc = %q, want hashed app.css url", got)
	}
}

// The .hidden utility must carry !important: it is defined before rules
// like .tabs { display: flex } and would lose the cascade, leaking hidden
// UI (Preview tabs on non-markdown entries). Markup tests cannot see this.
func TestHiddenUtilityBeatsCascade(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", assetURLs["app.css"], nil))
	if !strings.Contains(rec.Body.String(), `.hidden { display: none !important; }`) {
		t.Error(`.hidden utility lost !important; hidden UI can render anyway`)
	}
}

// The favicon must be served as an image and referenced by the layout head.
func TestFaviconServedAndLinked(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", assetURLs["favicon.svg"], nil))
	if rec.Code != 200 {
		t.Fatalf("GET favicon = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	var buf bytes.Buffer
	if err := Render(&buf, "list", map[string]any{"Email": "alice@example.com", "Gists": []any{}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `<link rel="icon" href="`+assetURLs["favicon.svg"]+`"`) {
		t.Error("layout head does not link the favicon by hashed url")
	}
}
