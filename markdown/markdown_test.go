package markdown

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	htmlrenderer "github.com/yuin/goldmark/renderer/html"
)

func TestRenderMarkdownFences(t *testing.T) {
	src := "```go\npackage main\n```\n\n```mermaid\ngraph TD\n```\n\n```totallymadeup\nx < y\n```\n"
	body, flags := Convert(src)
	if !strings.Contains(body, `class="chroma"`) {
		t.Error("fenced go block not highlighted")
	}
	if !strings.Contains(body, `<pre class="mermaid">graph TD</pre>`) {
		t.Error("mermaid fence not emitted")
	}
	if !flags.Mermaid {
		t.Error("Mermaid flag not set")
	}
	if strings.Contains(body, `<div class="codeblock"><pre class="mermaid"`) {
		t.Error("mermaid fence must not get a copy wrapper")
	}
	if !strings.Contains(body, "x &lt; y") {
		t.Error("unknown-language fence not escaped")
	}
	if strings.Contains(body, `lnt`) {
		t.Error("markdown fences must not get line numbers")
	}
	// go fence + unknown fence get the copy wrapper, mermaid does not.
	if n := strings.Count(body, `<div class="codeblock">`); n != 2 {
		t.Errorf("codeblock wrappers = %d, want 2", n)
	}
}

func TestRenderMarkdownFenceInfoString(t *testing.T) {
	// "```go title=x" must still highlight: the lexer name is the first
	// word of the info string.
	body, _ := Convert("```go title=x\nvar a int\n```\n")
	if !strings.Contains(body, `class="chroma"`) {
		t.Error("fence with info attributes not highlighted")
	}
	if strings.Contains(body, `class="fallback"`) {
		t.Error("fence with info attributes fell back to plain")
	}
}

func TestRenderMarkdownSoftBreaksFlow(t *testing.T) {
	// Single newlines are soft breaks: the paragraph must flow as one line
	// so text fills the container width instead of breaking at source cols.
	body, _ := Convert("line one\nline two\n")
	if strings.Contains(body, "<br") {
		t.Errorf("soft break rendered as <br>: %s", body)
	}
	// One paragraph, newline kept as-is (renders as a space).
	if want := "<p>line one\nline two</p>"; !strings.Contains(body, want) {
		t.Errorf("soft break not flowed: %s", body)
	}
}

func TestRenderMarkdownMath(t *testing.T) {
	src := strings.Join([]string{
		`Inline $x_i + y_i$ end.`, // underscores must survive
		``,
		`$$`,
		`\frac{1}{2}`,
		`$$`,
		``,
		"Code `$not math$` stays literal.", // code spans are not math
		``,
		"```",
		"$fence$ not math",
		"```",
	}, "\n")
	body, flags := Convert(src)
	if !flags.Math {
		t.Fatal("Math flag not set")
	}
	if !strings.Contains(body, `\(x_i + y_i\)`) {
		t.Errorf("inline math not canonicalized: %s", body)
	}
	if strings.Contains(body, "<em>i + y</em>") {
		t.Error("emphasis leaked into protected math")
	}
	if !strings.Contains(body, `\[`) || !strings.Contains(body, `\frac{1}{2}`) {
		t.Error("display math mangled")
	}
	if !strings.Contains(body, "`$not math$`") && !strings.Contains(body, "$not math$") {
		t.Error("code span math-like text lost")
	}
	if !strings.Contains(body, "$fence$ not math") {
		t.Error("fence content altered")
	}
}

func TestRenderMarkdownMoneyNotMath(t *testing.T) {
	_, flags := Convert("Costs $5 and $6 today.\n")
	if flags.Math {
		t.Error("money amounts detected as math")
	}
}

func TestRenderMarkdownMathEscaped(t *testing.T) {
	body, _ := Convert("Relation $a < b$ holds.\n")
	if !strings.Contains(body, `\(a &lt; b\)`) {
		t.Error("restored math not HTML-escaped")
	}
}

func TestRenderMarkdownMoneyWithMath(t *testing.T) {
	// Money on a page that loads MathJax for real math must stay plain
	// text: the renderer restores math in TeX delimiters only, so MathJax
	// never sees a "$...$" pair it could re-detect with laxer rules.
	body, flags := Convert("Money \"$5 and $6\" stays text.\n\nFormula: $x_i$ end.\n")
	if !flags.Math {
		t.Fatal("Math flag not set")
	}
	if !strings.Contains(body, `\(x_i\)`) {
		t.Errorf("math not canonicalized: %s", body)
	}
	if !strings.Contains(body, "$5 and $6") {
		t.Errorf("money mangled: %s", body)
	}
	if strings.Contains(body, `\(5 and`) {
		t.Error("money canonicalized as math")
	}
}

func TestRenderMarkdownMathUnterminatedBlock(t *testing.T) {
	// A "$$" that never closes must not swallow the rest of the document
	// as math: the source text stays visible.
	body, _ := Convert("$$\nnot really math\n")
	if !strings.Contains(body, "not really math") {
		t.Errorf("unterminated block lost content: %s", body)
	}
	if !strings.Contains(body, "$$") {
		t.Errorf("unterminated block hid its delimiter: %s", body)
	}
}

func TestRenderMarkdownMathPlaceholderText(t *testing.T) {
	// The old masking implementation reserved MJPH<n>XJM placeholders and
	// text shaped like one could collide with it. Node-based math has no
	// placeholder space, so such text must round-trip untouched.
	body, flags := Convert("Protocol token MJPH0XJM is literal.\n")
	if !strings.Contains(body, "MJPH0XJM") {
		t.Errorf("placeholder-shaped text altered: %s", body)
	}
	if flags.Math {
		t.Error("placeholder-shaped text detected as math")
	}
}

func TestRenderMarkdownSanitized(t *testing.T) {
	body, _ := Convert("# hi\n\n<img src=x onerror=alert(1)>\n")
	if strings.Contains(body, "onerror") {
		t.Error("event handler survived sanitize")
	}
	if !strings.Contains(body, "<h1") {
		t.Error("markdown not rendered")
	}
}

func TestRenderMarkdownRawHTML(t *testing.T) {
	body, _ := Convert("<details>\n<summary>More</summary>\n\n**shown** when opened\n\n</details>\n\n<kbd>Ctrl</kbd>+<kbd>K</kbd>\n\n<details open onclick=\"steal()\">x</details>\n\n<iframe src=\"https://evil.example\"></iframe>\n\n<svg onload=\"steal()\"></svg>\n\n<script>steal()</script>\n")
	for _, want := range []string{
		"<details>", "<summary>More</summary>", "<strong>shown</strong>",
		"<kbd>", `<details open="">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	for _, bad := range []string{"onclick", "onload", "<iframe", "<svg", "steal"} {
		if strings.Contains(body, bad) {
			t.Errorf("raw HTML leak %q", bad)
		}
	}
}

func TestRenderMarkdownTableAlignment(t *testing.T) {
	body, _ := Convert("|a|b|c|\n|:--|:-:|--:|\n|1|2|3|\n")
	if !strings.Contains(body, `align="left"`) ||
		!strings.Contains(body, `align="center"`) ||
		!strings.Contains(body, `align="right"`) {
		t.Error("cell alignment attributes stripped")
	}
	if strings.Contains(body, "style=") {
		t.Error("style attributes should not be emitted")
	}
}

func TestRenderMarkdownTableWrap(t *testing.T) {
	body, _ := Convert("---\ntitle: X\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\n<table>\n</table>\n```\n")
	for _, want := range []string{
		`<div class="table-wrap"><table class="frontmatter">`,
		`<div class="table-wrap"><table>`,
		`</table></div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	// Exactly two wraps: the code-fence "<table>" is escaped text.
	if n := strings.Count(body, `<div class="table-wrap">`); n != 2 {
		t.Errorf("wrap count = %d, want 2: %s", n, body)
	}
}

func TestRenderMarkdownTaskList(t *testing.T) {
	body, _ := Convert("- [ ] todo\n- [x] done\n")
	if !strings.Contains(body, `type="checkbox"`) {
		t.Error("checkbox stripped by sanitizer")
	}
	if !strings.Contains(body, "checked") {
		t.Error("checked state lost")
	}
}

func TestRenderMarkdownHeadingAnchors(t *testing.T) {
	body, _ := Convert("# Hello World\n\nBody.\n\n# Hello World\n")
	if !strings.Contains(body, `id="hello-world"`) {
		t.Error("heading slug id missing")
	}
	if !strings.Contains(body, `<a class="heading-anchor" href="#hello-world"`) {
		t.Error("permalink anchor missing")
	}
	if !strings.Contains(body, `id="hello-world-1"`) {
		t.Error("duplicate heading not deduped")
	}
}

func TestRenderMarkdownFootnotes(t *testing.T) {
	body, _ := Convert("A claim.[^1]\n\n[^1]: The note.\n")
	if !strings.Contains(body, `id="fnref:1"`) {
		t.Error("footnote reference stripped")
	}
	if !strings.Contains(body, `href="#fn:1"`) || !strings.Contains(body, `id="fn:1"`) {
		t.Error("footnote definition or link stripped")
	}
	if !strings.Contains(body, "The note.") {
		t.Error("footnote body missing")
	}
}

func TestRenderMarkdownEmoji(t *testing.T) {
	body, _ := Convert(":tada: party :definitely_not_an_emoji:\n")
	if !strings.Contains(body, "🎉") {
		t.Error("shortcode not replaced")
	}
	if !strings.Contains(body, ":definitely_not_an_emoji:") {
		t.Error("unknown shortcode should stay literal")
	}
}

func TestRenderMarkdownAlerts(t *testing.T) {
	body, _ := Convert("> [!WARNING]\n> Danger ahead\n\n> plain quote\n\n> [!TIP]\n")
	if !strings.Contains(body, `class="markdown-alert markdown-alert-warning"`) {
		t.Error("alert class not set")
	}
	if strings.Contains(body, "[!WARNING]") {
		t.Error("alert marker left in output")
	}
	if !strings.Contains(body, "Danger ahead") {
		t.Error("alert body lost")
	}
	if !strings.Contains(body, `class="markdown-alert markdown-alert-tip"`) || strings.Contains(body, "[!TIP]") {
		t.Error("marker-only alert not recognized")
	}
	if !strings.Contains(body, "<blockquote>\n<p>plain quote") {
		t.Error("plain blockquote altered")
	}
}

func TestRenderMarkdownImageSize(t *testing.T) {
	body, _ := Convert("![shot](https://example.com/a.png =200x100)\n\n```madeuplang\n![no](u =1x2)\n```\n")
	if !strings.Contains(body, `width="200"`) || !strings.Contains(body, `height="100"`) {
		t.Errorf("sized image not rewritten: %s", body)
	}
	if strings.Contains(body, "=200x100") {
		t.Error("size marker leaked into output")
	}
	if !strings.Contains(body, "![no](u =1x2)") {
		t.Error("sized-image syntax inside fence was rewritten")
	}
}

func TestRenderMarkdownFrontMatter(t *testing.T) {
	body, _ := Convert("---\ntitle: Notes\nauthor: <b>brute</b>\n---\n\n# Body\n")
	if !strings.Contains(body, `<table class="frontmatter">`) {
		t.Error("front matter table missing")
	}
	if !strings.Contains(body, "<th>title</th><td>Notes</td>") {
		t.Error("front matter pair missing")
	}
	if strings.Contains(body, "<b>brute</b>") {
		t.Error("front matter value not escaped")
	}
	if strings.Contains(body, "<hr>") {
		t.Error("front matter delimiters leaked into body")
	}
	if !strings.Contains(body, `id="body"`) {
		t.Error("markdown body after front matter not rendered")
	}
	// A leading list (not a mapping) must fall through as regular content.
	body, _ = Convert("---\n- a\n- b\n---\n")
	if strings.Contains(body, `class="frontmatter"`) {
		t.Error("non-mapping front matter rendered as table")
	}
	// Nested collection values marshal inline instead of rejecting the
	// whole block (the shape that used to fall through as a setext h2).
	body, _ = Convert("---\ntitle: X\ndirectories:\n  - /srv/a\n  - /srv/b\nmeta:\n  k: v\n---\n\n# B\n")
	if !strings.Contains(body, `<table class="frontmatter">`) {
		t.Error("front matter with nested values did not render as table")
	}
	if !strings.Contains(body, "/srv/a") || !strings.Contains(body, "/srv/b") ||
		!strings.Contains(body, "k: v") {
		t.Errorf("nested front matter values missing: %s", body)
	}
	if strings.Contains(body, "<hr>") || strings.Contains(body, "<h2") {
		t.Error("front matter delimiters leaked into body")
	}
}

func TestRenderMarkdownHeadingAttributes(t *testing.T) {
	// parser.WithAttribute: "# Heading {#custom-id}" overrides the slug,
	// like GitHub. Unknown attribute names die in the heading attribute
	// filter and the sanitizer.
	body, _ := Convert("# Section {#custom-id}\n\n# Unsafe {onclick=steal}\n")
	if !strings.Contains(body, `id="custom-id"`) {
		t.Errorf("explicit heading id lost: %s", body)
	}
	if !strings.Contains(body, `<a class="heading-anchor" href="#custom-id"`) {
		t.Error("permalink missing for explicit id")
	}
	if strings.Contains(body, "onclick") || strings.Contains(body, "steal") {
		t.Errorf("heading attribute injection survived: %s", body)
	}
	if !strings.Contains(body, ">Unsafe<a") {
		t.Errorf("heading with attribute mangled: %s", body)
	}
}

func TestRenderMarkdownAutolinks(t *testing.T) {
	body, _ := Convert("Ping https://example.com/x, www.example.com, or bob@example.com.\n")
	if !strings.Contains(body, `href="https://example.com/x"`) {
		t.Error("bare URL not autolinked")
	}
	if !strings.Contains(body, `href="http://www.example.com"`) {
		t.Error("www address not autolinked")
	}
	if !strings.Contains(body, `href="mailto:bob@example.com"`) {
		t.Error("mail address not autolinked (goldmark Linkify default)")
	}
}

func TestRenderMarkdownDataURLImage(t *testing.T) {
	// SVG data-URI images are the canonical img-XSS. The renderer runs
	// goldmark in unsafe mode (raw HTML for the sanitizer to gate), so
	// goldmark passes such URLs through and DisplayPolicy is what blocks
	// the data: scheme. Pin that end to end.
	body, _ := Convert("![svg](data:image/svg+xml;base64,PHN2Zy8+PGVtYmVkLz48L3N2Zz4=)\n\n" +
		"<img src=\"data:image/svg+xml,<svg onload=steal()/>\">\n")
	if strings.Contains(strings.ToLower(body), "data:image") {
		t.Errorf("data: image URL survived: %s", body)
	}
	if strings.Contains(body, "steal") {
		t.Error("script text leaked")
	}
}

func TestGoldmarkSVGDataURLIsDangerous(t *testing.T) {
	// Pinned to goldmark >= v1.8.6: that release stopped classifying
	// data:image/svg+xml as safe. DisplayPolicy blocks data: anyway; this
	// keeps the goldmark-side check honest should the unsafe flag or the
	// policy ever change. Fails on goldmark < v1.8.6.
	if !htmlrenderer.IsDangerousURL([]byte("data:image/svg+xml;base64,PHN2Zy8+")) {
		t.Error("goldmark treats SVG data URIs as safe; need >= v1.8.6")
	}
}

func TestHeadingSlugs(t *testing.T) {
	for _, tc := range []struct {
		name, src, want, not string
	}{
		{"underscore", "# role_id", `id="role_id"`, ""},
		{"accented", "# Comité exécutif", `id="comité-exécutif"`, ""},
		{"cjk", "# 東京 Tokyo", `id="東京-tokyo"`, ""},
		{"pure-cjk", "# 東京", `id="東京"`, ""},
		{"digit-dot", "# 5. Kafka", `id="5-kafka"`, ""},
		{"collapse-comma", "# Hello , World", `id="hello-world"`, ""},
		{"collapse-dashes", "# C -- D", `id="c-d"`, ""},
		{"trim-edge-dashes", "# - Dash", `id="dash"`, ""},
		{"emoji", "# :tada: party", `id="party"`, ""},
		{"setext", "Title\n=====\n", `id="title"`, ""},
		{"math-skipped", `# $x_i$ y`, `id="y"`, ""},
		{
			// the URL must not bake itself into the id, and the
			// self-link must resolve against the text slug.
			"link-in-heading", "#### [Release Notes](#release-notes)",
			`id="release-notes"`, "release-notesrelease-notes",
		},
		{"collision", "# Hello World\n\n# Hello World\n", `id="hello-world-1"`, ""},
		{"empty-fallback", "# !!!\n\n# !!!\n", `id="heading-1"`, ""},
		{
			// {#custom} wins and reserves the name: the plain
			// "foo" heading behind it must dedup.
			"custom-reserves", "# A {#foo}\n\n# foo\n", `id="foo-1"`, "",
		},
	} {
		body, _ := Convert(tc.src)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: %q missing %s", tc.name, tc.src, tc.want)
		}
		if tc.not != "" && strings.Contains(body, tc.not) {
			t.Errorf("%s: %q must not contain %q: %s", tc.name, tc.src, tc.not, body)
		}
	}
}

func TestFragmentLinksResolve(t *testing.T) {
	// Every in-page link in the corpus fixture must land on a real id,
	// including headings whose slugs are non-ASCII (the sanitizer keeps
	// them) or taken from a self-link's text.
	src, err := os.ReadFile("testdata/fragment-links.md")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := Convert(string(src))
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(body, -1) {
		// goldmark percent-escapes link destinations; browsers percent-
		// decode fragments before matching them against ids. Decode here
		// to compare the way a browser resolves the link.
		href, err := url.PathUnescape(m[1])
		if err != nil {
			t.Errorf("fragment link %q is not decodable", m[1])
			continue
		}
		if !ids[href] {
			t.Errorf("fragment link %q (%s) has no target", m[1], href)
		}
	}
}

func TestFrontMatterNoPhantomHeadings(t *testing.T) {
	// The shape that made a site builder see 672 phantom H1s: front
	// matter whose closing "---" underlines "orphan: true" as a setext
	// heading when the split is skipped. SplitFrontMatter consumes it.
	fmTable, body := SplitFrontMatter("---\norphan: true\n---\n\n# Real heading\n")
	if !strings.Contains(fmTable, `<th>orphan</th><td>true</td>`) {
		t.Errorf("front matter not split: %q", fmTable)
	}
	if strings.Contains(body, "---") {
		t.Errorf("delimiters left in body: %q", body)
	}
	rendered, _ := Convert("---\norphan: true\n---\n\n# Real heading\n")
	if strings.Contains(rendered, `id="orphan`) {
		t.Errorf("front matter parsed as setext heading: %s", rendered)
	}
	if !strings.Contains(rendered, `id="real-heading"`) {
		t.Errorf("body after front matter not rendered: %s", rendered)
	}
}
