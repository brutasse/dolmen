package handlers

import (
	"bytes"
	"html/template"
	"io"
	"net/url"
	"strings"

	"dolmen/models"
	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/brutasse/petroglyph"
)

// fileRenderer turns one gist file into display HTML: markdown rendering
// via the petroglyph package, syntax highlighting by file name for
// anything chroma recognizes, and an escaped <pre> otherwise. Markdown
// sanitizing and math/mermaid detection live in petroglyph.Convert; the
// flags it returns let the template load only the vendored JS the page
// actually needs.
type fileRenderer struct {
	numbered *chromahtml.Formatter // line numbers (standalone code files)
	style    *chroma.Style
}

// FileRender is one file rendered for display. Body is self-contained block
// markup: rendered markdown or a code chroma/`<pre>` block.
type FileRender struct {
	Name         string
	Body         template.HTML
	NeedsMath    bool
	NeedsMermaid bool
	// Kind drives presentation: "text" renders Body; image/audio/video/pdf
	// embed RawURL; "file" is a download-only blob.
	Kind     string
	RawURL   template.URL
	SizeText string
}

// rawURL builds the app URL for a file; blobs answer with a redirect to S3.
func rawURL(gistID, name string) string {
	return "/raw/" + gistID + "/" + url.PathEscape(name)
}

func newFileRenderer() *fileRenderer {
	return &fileRenderer{
		numbered: chromahtml.New(chromahtml.WithClasses(true), chromahtml.WithLineNumbers(true)),
		style:    styles.Get("github"),
	}
}

func (fr *fileRenderer) Render(name, content string) FileRender {
	if models.IsMarkdown(name) {
		body, flags := petroglyph.Convert(content)
		return FileRender{
			Name:         name,
			Body:         template.HTML(body),
			NeedsMath:    flags.Math,
			NeedsMermaid: flags.Mermaid,
		}
	}
	return fr.renderCode(name, content)
}

// --- code files ---

func (fr *fileRenderer) renderCode(name, content string) FileRender {
	lexer := lexers.Match(name)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	var b bytes.Buffer
	if lexer == nil {
		return FileRender{Name: name, Body: template.HTML(plainPre(content))}
	}
	fr.highlightCode(&b, lexer, strings.TrimRight(content, "\n"), fr.numbered)
	return FileRender{Name: name, Body: template.HTML(b.String())}
}

// highlightCode tokenizes src and writes chroma markup to w. The formatter
// output is generated from escaped source, so it needs no sanitizing.
func (fr *fileRenderer) highlightCode(w io.StringWriter, lexer chroma.Lexer, src string, fm *chromahtml.Formatter) {
	var b bytes.Buffer
	it, err := chroma.Coalesce(lexer).Tokenise(nil, src)
	if err == nil {
		err = fm.Format(&b, fr.style, it)
	}
	if err != nil {
		w.WriteString(plainPre(src))
		return
	}
	w.WriteString(b.String())
}

// --- fallback ---

func plainPre(s string) string {
	return `<pre class="fallback"><code>` + template.HTMLEscapeString(s) + `</code></pre>`
}
