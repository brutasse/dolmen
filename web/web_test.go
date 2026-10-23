package web

import (
	"bytes"
	"math"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
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

// petroglyph injects the alert title (octicon + label) as a
// .markdown-alert-title paragraph; a CSS-generated label on top of it
// printed the type twice, once colliding with the icon and uncolored. The
// per-type --alert-fg is what colors both octicon (fill=currentColor) and
// label. TestHiddenUtilityBeatsCascade explains why css content is asserted.
func TestAlertTitleStyledNotDuplicated(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", assetURLs["app.css"], nil))
	css := rec.Body.String()
	if !strings.Contains(css, ".markdown-alert-title") {
		t.Error(`no .markdown-alert-title rule; the injected octicon+label is unstyled`)
	}
	if !strings.Contains(css, "color: var(--alert-fg)") {
		t.Error(`alert title does not take --alert-fg; octicon and label render in --fg`)
	}
	for _, typ := range []string{"Note", "Tip", "Important", "Warning", "Caution"} {
		if strings.Contains(css, `content: `+`"`+typ+`"`) {
			t.Errorf(`css still generates a %q label; the alert type renders twice`, typ)
		}
	}
}

// The theme is three-way (system, light, dark) and app.css drives it from
// color-scheme, not from a media query: a dark @media block cannot be
// overridden by a manual light choice. light-dark() must stay behind
// @supports, because custom properties do not fail at parse time — a bare
// light-dark() declaration would win the cascade on an old browser and take
// every color with it. TestHiddenUtilityBeatsCascade explains why css content
// is asserted.
func TestThemeSwitchesOnColorScheme(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", assetURLs["app.css"], nil))
	css := rec.Body.String()
	if !strings.Contains(css, "color-scheme: light dark") {
		t.Error(`no "color-scheme: light dark" default; nothing follows the OS`)
	}
	for _, override := range []string{`html[data-theme="light"] { color-scheme: light; }`, `html[data-theme="dark"] { color-scheme: dark; }`} {
		if !strings.Contains(css, override) {
			t.Errorf("missing %s; the manual choice cannot override the OS", override)
		}
	}
	if !strings.Contains(css, "@supports (color: light-dark(red, blue))") {
		t.Error(`light-dark() tokens are not guarded by @supports; old browsers lose every color`)
	}
	if strings.Contains(css, "prefers-color-scheme") {
		t.Error("app.css still carries a dark @media block; a manual light choice cannot win")
	}
	// --code-bg missing a dark twin is what made code blocks render light-on-
	// light, the bug the theme control would otherwise have exposed.
	if !strings.Contains(css, "--code-bg: light-dark(#f6f8fa, #0d1117)") {
		t.Error("--code-bg has no dark value; code surfaces stay light in dark mode")
	}
}

// The code palette is two blocks in one sheet: plain light values in :root, and
// the light-dark() twins behind @supports, so the color-scheme switch app.css
// sets picks the palette and a manual theme choice beats the OS without a media
// query in this file. TestThemeSwitchesOnColorScheme explains why a media query
// cannot do that job.
//
// Three things keep those blocks one palette. A rule may name a variable and
// never a colour, or the blocks drift one rule at a time. The light half of each
// light-dark() pair must equal the plain :root value, because the pair is what a
// modern browser paints and the plain block is what an old one paints — different
// light halves are two light themes, and only one of them gets read here. And
// every colour must clear AA contrast on the surface it is drawn on, which is
// --code-bg: .chroma paints that variable, so the renderer's canvas and the
// inline code chips share one surface, read from app.css rather than pinned.
func TestChromaPalette(t *testing.T) {
	chromaCSS, appCSS := embeddedAsset(t, "chroma.css"), embeddedAsset(t, "app.css")

	supports := chromaSupportsRe.FindAllStringSubmatch(chromaCSS, -1)
	if len(supports) != 1 {
		t.Fatalf("chroma.css: %d @supports palette blocks, want exactly 1", len(supports))
	}
	pairs := cssDeclarations(supports[0][1])
	withoutSupports := chromaSupportsRe.ReplaceAllString(chromaCSS, "")
	root := chromaRootRe.FindAllStringSubmatch(withoutSupports, -1)
	if len(root) != 1 {
		t.Fatalf("chroma.css: %d plain :root palette blocks, want exactly 1", len(root))
	}
	light := cssDeclarations(root[0][1])

	dark := map[string]string{}
	for _, name := range paletteNames(pairs) {
		m := lightDarkRe.FindStringSubmatch(pairs[name])
		if m == nil {
			t.Errorf("%s is %q in the @supports block; one light-dark() pair must carry both themes", name, pairs[name])
			continue
		}
		dark[name] = strings.TrimSpace(m[2])
		if plain, ok := light[name]; !ok {
			t.Errorf("%s is in the @supports palette but not the plain :root one; an old browser loses that token", name)
		} else if got := strings.TrimSpace(m[1]); got != plain {
			t.Errorf("%s: the @supports light half is %s, the plain :root value is %s; the fallback is not the light theme", name, got, plain)
		}
	}
	for _, name := range paletteNames(light) {
		if _, ok := pairs[name]; !ok {
			t.Errorf("%s is in the plain :root palette but not the @supports one; that token stays light in dark mode", name)
		}
	}

	rules := cssCommentRe.ReplaceAllString(chromaRootRe.ReplaceAllString(withoutSupports, ""), "")
	for _, stray := range append(cssHexRe.FindAllString(rules, 1), cssRGBARe.FindAllString(rules, 1)...) {
		t.Errorf("chroma.css colours a token outside its palettes: %s", stray)
	}
	for _, m := range cssVarRe.FindAllStringSubmatch(rules, -1) {
		name := m[1]
		if _, ok := pairs[name]; ok {
			continue
		}
		if !strings.Contains(appCSS, name+":") {
			t.Errorf("chroma.css uses %s, which no palette block defines", name)
		}
	}

	surfaceLight, surfaceDark := codeSurfaces(t, appCSS)
	for _, b := range []struct {
		which   string
		colors  map[string]string
		surface string
	}{
		{"light", light, surfaceLight},
		{"dark", dark, surfaceDark},
	} {
		for _, name := range paletteNames(b.colors) {
			if strings.HasSuffix(name, "-bg") {
				continue // a surface, read under the text drawn on it
			}
			bg := b.surface
			if tint, ok := b.colors[name+"-bg"]; ok {
				bg = tint
			}
			if r := contrastRatio(cssRGB(t, b.colors[name], b.surface), cssRGB(t, bg, b.surface)); r < codeContrastMin {
				t.Errorf("%s theme: %s %s on %s is %.2f:1, under %.1f:1",
					b.which, name, b.colors[name], bg, r, codeContrastMin)
			}
		}
	}
}

// The vendored renderer has no theme API to hand a diagram to, so the container
// it replaces carries the white ground mermaid's default theme is authored for;
// a transparent one would paint mermaid's dark text on the dark page.
func TestDiagramCardStaysWhite(t *testing.T) {
	css := embeddedAsset(t, "app.css")
	block := regexp.MustCompile(`(?s)pre\.mermaid \{([^}]*)\}`).FindStringSubmatch(css)
	if block == nil {
		t.Fatal("app.css has no pre.mermaid rule; the diagram container is unstyled")
	}
	if !strings.Contains(block[1], "background: #fff") {
		t.Errorf("pre.mermaid background is %q; a themed diagram would inherit the dark canvas", strings.TrimSpace(block[1]))
	}
}

// The choice is read from localStorage before the stylesheets load, and the
// button stays hidden until app.js can paint its state: a visible control that
// only app.js understands would sit there reporting "System" in the wrong
// theme, and without the head script the whole page flashes light first.
func TestThemeHeadScriptAndToggleWired(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, "list", map[string]any{"Email": "alice@example.com", "Gists": []any{}}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `<meta name="color-scheme" content="light dark">`) {
		t.Error("head does not declare color-scheme; the UA paints form controls light")
	}
	if !strings.Contains(html, `localStorage.getItem("theme")`) {
		t.Error("head does not read the stored theme; a manual choice flashes the wrong palette")
	}
	script := strings.Index(html, `localStorage.getItem("theme")`)
	style := strings.Index(html, `<link rel="stylesheet"`)
	if script > style {
		t.Error("the theme script runs after the stylesheets; the page paints the wrong theme first")
	}
	if !strings.Contains(html, `class="btn btn-secondary btn-sm theme-toggle hidden"`) {
		t.Error("theme toggle is not hidden by default; it renders before app.js can label it")
	}
}
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

// --- code palette helpers -------------------------------------------------

// codeContrastMin is WCAG's AA bar for the .82rem text a code block holds.
const codeContrastMin = 4.5

var (
	chromaSupportsRe = regexp.MustCompile(`(?s)@supports \(color: light-dark\(red, blue\)\) \{\s*:root \{([^}]*)\}\s*\}`)
	chromaRootRe     = regexp.MustCompile(`(?s):root \{([^}]*)\}`)
	lightDarkRe      = regexp.MustCompile(`^light-dark\(([^,]+),\s*(.+)\)$`)
	codeBgRe         = regexp.MustCompile(`--code-bg: light-dark\(([^,]+),\s*([^)]+)\)`)
	cssDeclRe        = regexp.MustCompile(`(--[a-z-]+):\s*([^;}]+)`)
	cssVarRe         = regexp.MustCompile(`var\((--[a-z-]+)\)`)
	cssHexRe         = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	cssRGBARe        = regexp.MustCompile(`rgba\(\s*(\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\s*\)`)
	cssCommentRe     = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// embeddedAsset reads a file out of the bytes that ship in the binary, rather
// than a copy of them on disk.
func embeddedAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("read assets/%s: %v", name, err)
	}
	return string(b)
}

func cssDeclarations(block string) map[string]string {
	decls := map[string]string{}
	for _, d := range cssDeclRe.FindAllStringSubmatch(block, -1) {
		decls[d[1]] = strings.TrimSpace(d[2])
	}
	return decls
}

func paletteNames(p map[string]string) []string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// codeSurfaces is the --code-bg pair in app.css: the canvas .chroma paints, and
// so the ground every token colour in chroma.css is read against.
func codeSurfaces(t *testing.T, appCSS string) (light, dark string) {
	t.Helper()
	m := codeBgRe.FindStringSubmatch(appCSS)
	if m == nil {
		t.Fatal(`app.css declares no "--code-bg: light-dark(...)"; the code canvas has no dark theme to check against`)
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
}

// cssRGB is a colour literal in a stylesheet: a hex colour, or an rgba() tint
// composited over the surface it is painted on, which is how a diff token's
// background arrives.
func cssRGB(t *testing.T, value, surface string) [3]float64 {
	t.Helper()
	if hex := strings.TrimPrefix(value, "#"); hex != value {
		if len(hex) == 3 {
			hex = strings.Repeat(hex[0:1], 2) + strings.Repeat(hex[1:2], 2) + strings.Repeat(hex[2:], 2)
		}
		if len(hex) != 6 {
			t.Fatalf("%q is not a colour this test can read", value)
		}
		var c [3]float64
		for i := range c {
			n, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
			if err != nil {
				t.Fatalf("%q is not a colour this test can read: %v", value, err)
			}
			c[i] = float64(n)
		}
		return c
	}
	m := cssRGBARe.FindStringSubmatch(value)
	if m == nil {
		t.Fatalf("%q is neither a hex colour nor an rgba() tint", value)
	}
	bg := cssRGB(t, surface, surface)
	a, err := strconv.ParseFloat(m[4], 64)
	if err != nil {
		t.Fatalf("%q carries no alpha: %v", value, err)
	}
	var c [3]float64
	for i := range c {
		fg, err := strconv.ParseFloat(m[i+1], 64)
		if err != nil {
			t.Fatalf("%q is not a colour this test can read: %v", value, err)
		}
		c[i] = fg*a + bg[i]*(1-a)
	}
	return c
}

func contrastRatio(a, b [3]float64) float64 {
	lum := func(c [3]float64) float64 {
		channel := func(v float64) float64 {
			v /= 255
			if v <= 0.03928 {
				return v / 12.92
			}
			return math.Pow((v+0.055)/1.055, 2.4)
		}
		return 0.2126*channel(c[0]) + 0.7152*channel(c[1]) + 0.0722*channel(c[2])
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
