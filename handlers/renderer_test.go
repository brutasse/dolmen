package handlers

import (
	"strings"
	"testing"
)

func TestRenderCodeFileByExtension(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("main.go", "package main\n\nfunc main() {}\n", "L")
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
	res := fr.Render("notes.unknownext", "hello <b>x</b>", "L")
	if !strings.Contains(string(res.Body), `class="fallback"`) {
		t.Error("expected fallback <pre> for unknown extension")
	}
	if strings.Contains(string(res.Body), "<b>") {
		t.Error("raw HTML not escaped in fallback")
	}
}

func TestRenderScriptGating(t *testing.T) {
	fr := newFileRenderer()
	plain := fr.Render("a.txt", "just text", "L")
	if plain.NeedsMath || plain.NeedsMermaid {
		t.Error("plain text flagged needs")
	}
}

// The mermaid fence is what loads Mermaid: the flag comes from petroglyph's
// document walk, the markup is the container mermaid renders into. A code file
// must never carry either flag, or the view page ships vendor JS it does not
// need.
func TestRenderMermaidGating(t *testing.T) {
	fr := newFileRenderer()

	md := fr.Render("a.md", "# t\n\n```mermaid\ngraph TD\n```\n", "L")
	if !md.NeedsMermaid {
		t.Error("markdown mermaid fence did not set NeedsMermaid")
	}
	if !strings.Contains(string(md.Body), `<pre class="mermaid">`) {
		t.Error("no pre.mermaid container for the fence")
	}

	code := fr.Render("a.go", "package main\n", "L")
	if code.NeedsMermaid || code.NeedsMath {
		t.Error("code file flagged needs")
	}
}

// Line anchors: every line of a line-anchored file carries an id built from
// the prefix, and its gutter number is the link. The prefix namespaces the
// ids across a multi-file page ("L" for the first file, "file-{slug}-L" for
// the rest).
func TestRenderLineAnchors(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("main.go", "package main\nfunc main() {}\n", "L")
	for _, want := range []string{`id="L1"`, `id="L2"`, `href="#L2"`, `class="lnlinks"`} {
		if !strings.Contains(string(res.Body), want) {
			t.Errorf("bare prefix: no %q", want)
		}
	}
	res = fr.Render("main.go", "package main\nfunc main() {}\n", "file-main-go-L")
	if !strings.Contains(string(res.Body), `id="file-main-go-L2"`) {
		t.Error("no namespaced line id for a file prefix")
	}
}

// The fallback (no lexer claimed the content) must offer the same anchor
// shape as the chroma path, one line row per source line, escaped.
func TestRenderFallbackLineAnchors(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("notes.unknownext", "hello <b>x</b>\nworld\n", "L")
	if !strings.Contains(string(res.Body), `class="fallback"`) {
		t.Error("expected fallback <pre>")
	}
	for _, want := range []string{`id="L1"`, `id="L2"`, `href="#L2"`, `class="lnlinks"`, `&lt;b&gt;`} {
		if !strings.Contains(string(res.Body), want) {
			t.Errorf("fallback: no %q", want)
		}
	}
	// The trailing newline does not add an empty anchorable line.
	if strings.Contains(string(res.Body), `id="L3"`) {
		t.Error("trailing newline produced an extra line")
	}
}

// Markdown renders through petroglyph: its anchors are heading slugs, and a
// line prefix must not leak line anchors into it.
func TestRenderMarkdownHasNoLineAnchors(t *testing.T) {
	fr := newFileRenderer()
	res := fr.Render("a.md", "# heading\n\nprose line\n", "L")
	if strings.Contains(string(res.Body), `id="L`) || strings.Contains(string(res.Body), "lnlinks") {
		t.Error("markdown carries line anchors")
	}
}

func TestFileAnchorSlug(t *testing.T) {
	used := map[string]bool{}
	for _, c := range []struct{ name, want string }{
		{"Main.go", "main-go"},
		{"a b.txt", "a-b-txt"},
		{"///", "file"},
	} {
		if got := fileAnchorSlug(c.name, used); got != c.want {
			t.Errorf("fileAnchorSlug(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	used = map[string]bool{}
	// Same name is impossible in a gist; the collision comes from the slug
	// function itself.
	if got := fileAnchorSlug("foo.bar", used); got != "foo-bar" {
		t.Errorf("first slug = %q, want foo-bar", got)
	}
	if got := fileAnchorSlug("foo-bar", used); got != "foo-bar-2" {
		t.Errorf("duplicate slug = %q, want foo-bar-2", got)
	}
	if got := fileAnchorSlug("FOO.BAR", used); got != "foo-bar-3" {
		t.Errorf("second duplicate slug = %q, want foo-bar-3", got)
	}
}
