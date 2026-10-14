package markdown

import (
	"fmt"
	stdhtml "html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Math parses as regular AST nodes: a span inside a line becomes an inline
// node, and a $$...$$ or \[...\] block with the delimiters on their own
// lines becomes a block node. Content renders as escaped text in canonical
// TeX delimiters ($$..$$ and \[..\] render as \[..\], $..$ and \(..\) as
// \(..\)). MathJax is configured for TeX delimiters only, so dollar-form
// text the parser deliberately left alone (money) can never be re-detected
// by MathJax's own, laxer scanner. Code spans and fences never reach the
// inline parser: they consume their trigger bytes (backticks) earlier in
// the line, so math-looking text inside them is untouched.

var (
	// All four are anchored at position 0: the parser triggers at the
	// opening delimiter and must match from there.
	mathDisplayTex = regexp.MustCompile(`^\\\[(.+?)\\\]`)
	mathDisplay    = regexp.MustCompile(`^\$\$(.+?)\$\$`)
	mathInlineTex  = regexp.MustCompile(`^\\\((.+?)\\\)`)
	// Inline $...$: same line, non-empty, no space right after the opening
	// or before the closing delimiter ("$5 and $6" is money, not math).
	mathDollar = regexp.MustCompile(`^\$(\S(?:[^$\n]*\S)?)\$`)
)

var mathSpanRules = []struct {
	re      *regexp.Regexp
	display bool
}{
	{mathDisplayTex, true},
	{mathDisplay, true},
	{mathInlineTex, false},
	{mathDollar, false},
}

var (
	kindMath      = ast.NewNodeKind("Math")
	kindMathBlock = ast.NewNodeKind("MathBlock")
)

// mathNode is a single-line TeX span; content is the source segment
// between the delimiters.
type mathNode struct {
	ast.BaseInline
	content text.Segment
	display bool
}

func (n *mathNode) Kind() ast.NodeKind { return kindMath }

func (n *mathNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Display": fmt.Sprintf("%t", n.display),
	}, nil)
}

type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$', '\\'} }

func (mathInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	for _, rule := range mathSpanRules {
		m := rule.re.FindSubmatchIndex(line)
		if m == nil {
			continue
		}
		block.Advance(m[1])
		return &mathNode{
			content: text.NewSegment(seg.Start+m[2], seg.Start+m[3]),
			display: rule.display,
		}
	}
	return nil
}

// mathBlockNode is a display block delimited by standalone "$$" or "\["
// lines. When the document runs out before the closing delimiter, the
// source renders as literal text instead of math.
type mathBlockNode struct {
	ast.BaseBlock
	opener string
	closer string
	closed bool
}

func (n *mathBlockNode) Kind() ast.NodeKind { return kindMathBlock }

func (n *mathBlockNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Opener": n.opener, "Closed": fmt.Sprintf("%t", n.closed),
	}, nil)
}

var mathBlockDelims = map[string]string{"$$": "$$", `\[`: `\]`}

type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$', '\\'} }

func (mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	opener := strings.TrimSpace(string(line))
	closer, ok := mathBlockDelims[opener]
	if !ok {
		return nil, parser.NoChildren
	}
	// No reader advance here: the block loop advances the opener line.
	return &mathBlockNode{opener: opener, closer: closer}, parser.NoChildren
}

func (mathBlockParser) Continue(n ast.Node, reader text.Reader, pc parser.Context) parser.State {
	mb := n.(*mathBlockNode)
	line, seg := reader.PeekLine()
	if strings.TrimSpace(string(line)) == mb.closer {
		mb.closed = true
		reader.AdvanceToEOL()
		return parser.Close
	}
	mb.Lines().Append(seg)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

// Math never interrupts a paragraph: mid-paragraph delimiters are inline
// parser business, and a "$$" that merely sits among text lines is
// ordinary content.
func (mathBlockParser) CanInterruptParagraph() bool { return false }

func (mathBlockParser) CanAcceptIndentedLine() bool { return false }

func (mathBlockParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

type mathRenderer struct{}

func (m *mathRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMath, m.renderSpan)
	reg.Register(kindMathBlock, m.renderBlock)
}

func (m *mathRenderer) renderSpan(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	mn := n.(*mathNode)
	open, closing := `\(`, `\)`
	if mn.display {
		open, closing = `\[`, `\]`
	}
	w.WriteString(open)
	w.WriteString(stdhtml.EscapeString(string(mn.content.Value(source))))
	w.WriteString(closing)
	return ast.WalkSkipChildren, nil
}

func (m *mathRenderer) renderBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	mb := n.(*mathBlockNode)
	var body strings.Builder
	lines := mb.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		body.Write(line.Value(source))
	}
	content := strings.TrimRight(body.String(), "\n")
	if !mb.closed {
		w.WriteString(stdhtml.EscapeString(mb.opener + "\n" + content))
		w.WriteByte('\n')
		return ast.WalkSkipChildren, nil
	}
	w.WriteString(stdhtml.EscapeString(`\[` + content + `\]`))
	w.WriteByte('\n')
	return ast.WalkSkipChildren, nil
}
