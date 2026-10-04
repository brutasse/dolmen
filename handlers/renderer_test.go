package handlers

import (
	"strings"
	"testing"
)

func TestRenderCodeFileByExtension(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("main.go", "package main\n\nfunc main() {}\n")
	if !strings.Contains(string(res.Body), `class="chroma"`) {
		t.Error("no chroma markup for .go file")
	}
	if !strings.Contains(string(res.Body), `class="ln"`) {
		t.Error("no line-number gutter for standalone code file")
	}
	// "package" should be a keyword-colored token, not bare text.
	if strings.Contains(string(res.Body), ">package main<") {
		t.Error("keyword not tokenized")
	}
}

func TestRenderUnknownExtensionPlain(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("notes.unknownext", "hello <b>x</b>")
	if !strings.Contains(string(res.Body), `class="fallback"`) {
		t.Error("expected fallback <pre> for unknown extension")
	}
	if strings.Contains(string(res.Body), "<b>") {
		t.Error("raw HTML not escaped in fallback")
	}
}

func TestRenderMarkdownFences(t *testing.T) {
	fr := newFileRenderer()
	src := "```go\npackage main\n```\n\n```mermaid\ngraph TD\n```\n\n```totallymadeup\nx < y\n```\n"
	res := fr.Render("doc.md", src)
	body := string(res.Body)
	if !strings.Contains(body, `class="chroma"`) {
		t.Error("fenced go block not highlighted")
	}
	if !strings.Contains(body, `<pre class="mermaid">graph TD</pre>`) {
		t.Error("mermaid fence not emitted")
	}
	if !res.NeedsMermaid {
		t.Error("NeedsMermaid not set")
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
	fr := newFileRenderer()
	res := fr.Render("doc.md", "```go title=x\nvar a int\n```\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("doc.md", "line one\nline two\n")
	body := string(res.Body)
	if strings.Contains(body, "<br") {
		t.Errorf("soft break rendered as <br>: %s", body)
	}
	// One paragraph, newline kept as-is (renders as a space).
	if want := "<p>line one\nline two</p>"; !strings.Contains(body, want) {
		t.Errorf("soft break not flowed: %s", body)
	}
}

func TestRenderMarkdownMath(t *testing.T) {
	fr := newFileRenderer()
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
	res := fr.Render("doc.md", src)
	body := string(res.Body)
	if !res.NeedsMath {
		t.Fatal("NeedsMath not set")
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
	fr := newFileRenderer()
	res := fr.Render("doc.md", "Costs $5 and $6 today.\n")
	if res.NeedsMath {
		t.Error("money amounts detected as math")
	}
}

func TestRenderMarkdownMathEscaped(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("doc.md", "Relation $a < b$ holds.\n")
	if !strings.Contains(string(res.Body), `\(a &lt; b\)`) {
		t.Error("restored math not HTML-escaped")
	}
}

func TestRenderMarkdownMoneyWithMath(t *testing.T) {
	// Money on a page that loads MathJax for real math must stay plain
	// text: the renderer restores math in TeX delimiters only, so MathJax
	// never sees a "$...$" pair it could re-detect with laxer rules.
	fr := newFileRenderer()
	res := fr.Render("doc.md", "Money \"$5 and $6\" stays text.\n\nFormula: $x_i$ end.\n")
	body := string(res.Body)
	if !res.NeedsMath {
		t.Fatal("NeedsMath not set")
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

func TestRenderMarkdownSanitized(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("doc.md", "# hi\n\n<img src=x onerror=alert(1)>\n")
	body := string(res.Body)
	if strings.Contains(body, "onerror") {
		t.Error("event handler survived sanitize")
	}
	if !strings.Contains(body, "<h1") {
		t.Error("markdown not rendered")
	}
}

func TestRenderMarkdownRawHTML(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("doc.md", "<details>\n<summary>More</summary>\n\n**shown** when opened\n\n</details>\n\n<kbd>Ctrl</kbd>+<kbd>K</kbd>\n\n<details open onclick=\"steal()\">x</details>\n\n<iframe src=\"https://evil.example\"></iframe>\n\n<svg onload=\"steal()\"></svg>\n\n<script>steal()</script>\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", "|a|b|c|\n|:--|:-:|--:|\n|1|2|3|\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", "---\ntitle: X\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\n<table>\n</table>\n```\n")
	body := string(res.Body)
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

func TestRenderScriptGating(t *testing.T) {
	fr := newFileRenderer()
	plain := fr.Render("a.txt", "just text")
	if plain.NeedsMath || plain.NeedsMermaid {
		t.Error("plain text flagged needs")
	}
}

func TestRenderMarkdownTaskList(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("t.md", "- [ ] todo\n- [x] done\n")
	body := string(res.Body)
	if !strings.Contains(body, `type="checkbox"`) {
		t.Error("checkbox stripped by sanitizer")
	}
	if !strings.Contains(body, "checked") {
		t.Error("checked state lost")
	}
}

func TestRenderMarkdownHeadingAnchors(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("d.md", "# Hello World\n\nBody.\n\n# Hello World\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", "A claim.[^1]\n\n[^1]: The note.\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", ":tada: party :definitely_not_an_emoji:\n")
	body := string(res.Body)
	if !strings.Contains(body, "🎉") {
		t.Error("shortcode not replaced")
	}
	if !strings.Contains(body, ":definitely_not_an_emoji:") {
		t.Error("unknown shortcode should stay literal")
	}
}

func TestRenderMarkdownAlerts(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("d.md", "> [!WARNING]\n> Danger ahead\n\n> plain quote\n\n> [!TIP]\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", "![shot](https://example.com/a.png =200x100)\n\n```madeuplang\n![no](u =1x2)\n```\n")
	body := string(res.Body)
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
	fr := newFileRenderer()
	res := fr.Render("d.md", "---\ntitle: Notes\nauthor: <b>brute</b>\n---\n\n# Body\n")
	body := string(res.Body)
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
	res = fr.Render("d.md", "---\n- a\n- b\n---\n")
	if strings.Contains(string(res.Body), `class="frontmatter"`) {
		t.Error("non-mapping front matter rendered as table")
	}
	// Nested collection values marshal inline instead of rejecting the
	// whole block (the shape that used to fall through as a setext h2).
	res = fr.Render("d.md", "---\ntitle: X\ndirectories:\n  - /srv/a\n  - /srv/b\nmeta:\n  k: v\n---\n\n# B\n")
	body = string(res.Body)
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
