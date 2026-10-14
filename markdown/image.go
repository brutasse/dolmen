package markdown

import (
	"fmt"
	stdhtml "html"
	"regexp"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// GitHub's explicit image sizing is not CommonMark; parse the syntax into a
// custom node. The emitted tag goes through the same sanitizer as regular
// images. (Raw <img width=height=> markup also survives the sanitizer now,
// but the markdown shorthand is what GitHub's own editor produces.)

var sizedImageMatch = regexp.MustCompile(`^!\[([^\]]*)\]\(([^\s()]+) =([0-9]*)x([0-9]*)\)`)

type sizedImageNode struct {
	ast.BaseInline
	alt, url, width, height []byte
}

var kindSizedImage = ast.NewNodeKind("SizedImage")

func (n *sizedImageNode) Kind() ast.NodeKind { return kindSizedImage }

func (n *sizedImageNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"URL": string(n.url), "Width": string(n.width), "Height": string(n.height),
	}, nil)
}

type sizedImageParser struct{}

func (sizedImageParser) Trigger() []byte { return []byte{'!'} }

func (sizedImageParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	m := sizedImageMatch.FindSubmatch(line)
	if m == nil || (len(m[3]) == 0 && len(m[4]) == 0) {
		return nil
	}
	block.Advance(len(m[0]))
	return &sizedImageNode{alt: m[1], url: m[2], width: m[3], height: m[4]}
}

type sizedImageRenderer struct{}

func (s *sizedImageRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindSizedImage, s.render)
}

func (s *sizedImageRenderer) render(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	ni := n.(*sizedImageNode)
	fmt.Fprintf(w, `<img src="%s" alt="%s"`,
		stdhtml.EscapeString(string(ni.url)), stdhtml.EscapeString(string(ni.alt)))
	if len(ni.width) > 0 {
		fmt.Fprintf(w, ` width="%s"`, ni.width)
	}
	if len(ni.height) > 0 {
		fmt.Fprintf(w, ` height="%s"`, ni.height)
	}
	w.WriteString(`>`)
	return ast.WalkSkipChildren, nil
}
