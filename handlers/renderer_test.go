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

func TestRenderScriptGating(t *testing.T) {
	fr := newFileRenderer()
	plain := fr.Render("a.txt", "just text")
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

	md := fr.Render("a.md", "# t\n\n```mermaid\ngraph TD\n```\n")
	if !md.NeedsMermaid {
		t.Error("markdown mermaid fence did not set NeedsMermaid")
	}
	if !strings.Contains(string(md.Body), `<pre class="mermaid">`) {
		t.Error("no pre.mermaid container for the fence")
	}

	code := fr.Render("a.go", "package main\n")
	if code.NeedsMermaid || code.NeedsMath {
		t.Error("code file flagged needs")
	}
}
