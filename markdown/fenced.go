package markdown

import (
	stdhtml "html"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

var (
	chromaFormatter = chromahtml.New(chromahtml.WithClasses(true))
	chromaStyle     = styles.Get("github")
)

// fencedRenderer replaces goldmark's default fenced-code rendering:
// mermaid fences become <pre class="mermaid">, other languages get chroma,
// unknown ones an escaped <pre>.
type fencedRenderer struct{}

func (f *fencedRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, f.render)
}

func (f *fencedRenderer) render(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	cb := n.(*ast.FencedCodeBlock)
	info := fencedInfo(cb, source)
	src := fencedText(cb, source)

	if info == "mermaid" {
		w.WriteString(`<pre class="mermaid">`)
		w.WriteString(stdhtml.EscapeString(src))
		w.WriteString("</pre>")
		return ast.WalkSkipChildren, nil
	}
	// Language hint is the first word of the info string (```bash matches,
	// ```bash title=x matches too); unknown hints fall back to escaped
	// preformatted text. The wrapper div lets the shared app.js attach copy
	// buttons — sanitized content carries no scripts or buttons of its own.
	lang := info
	if i := strings.IndexAny(lang, " ,"); i >= 0 {
		lang = lang[:i]
	}
	w.WriteString(`<div class="codeblock">`)
	if lexer := lexers.Get(lang); lexer != nil {
		it, err := chroma.Coalesce(lexer).Tokenise(nil, src)
		if err == nil {
			err = chromaFormatter.Format(w, chromaStyle, it)
		}
		if err != nil {
			w.WriteString(plainPre(src))
		}
	} else {
		w.WriteString(plainPre(src))
	}
	w.WriteString("</div>")
	return ast.WalkSkipChildren, nil
}

// fencedInfo is the lowercased, trimmed info string of a fence.
func fencedInfo(cb *ast.FencedCodeBlock, source []byte) string {
	if cb.Info == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(cb.Info.Segment.Value(source))))
}

func fencedText(cb *ast.FencedCodeBlock, source []byte) string {
	var code strings.Builder
	lines := cb.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		code.Write(line.Value(source))
	}
	return strings.TrimRight(code.String(), "\n")
}
