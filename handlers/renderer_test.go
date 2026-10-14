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
