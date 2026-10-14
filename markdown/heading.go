package markdown

import (
	"fmt"
	stdhtml "html"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// headingRenderer replaces the default heading rendering: keeps the
// parser-generated slug id (or a "# Heading {#id}" override) and appends a
// hover permalink anchor.
type headingRenderer struct{}

func (h *headingRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, h.render)
}

func (h *headingRenderer) render(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	hn := n.(*ast.Heading)
	if entering {
		fmt.Fprintf(w, "<h%d", hn.Level)
		htmlrenderer.RenderAttributes(w, n, htmlrenderer.HeadingAttributeFilter)
		w.WriteByte('>')
		return ast.WalkContinue, nil
	}
	if v, ok := n.Attribute([]byte("id")); ok {
		var idStr string
		switch id := v.(type) {
		case []byte:
			idStr = string(id)
		case string:
			idStr = id
		}
		if idStr != "" {
			fmt.Fprintf(w, `<a class="heading-anchor" href="#%s" aria-label="Link to this section">#</a>`,
				stdhtml.EscapeString(idStr))
		}
	}
	fmt.Fprintf(w, "</h%d>\n", hn.Level)
	return ast.WalkContinue, nil
}
