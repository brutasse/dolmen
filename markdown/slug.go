package markdown

import (
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
)

// headingText returns the heading's visible text as rendered: the text
// nodes of its inline subtree concatenated (links, autolinks, code spans
// and image alts reduce to their child text), line breaks as single
// spaces. Nodes without text children contribute nothing: math spans and
// raw HTML hold source segments, emoji and footnote references hold none.
func headingText(h *ast.Heading, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(h, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if t, ok := n.(*ast.Text); ok {
				b.Write(t.Segment.Value(source))
				if t.SoftLineBreak() || t.HardLineBreak() {
					b.WriteByte(' ')
				}
			}
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// slug lowercases text, collapses every run of characters outside
// letters/digits/underscore into a single hyphen, and trims hyphens at
// the ends. Letters of any script survive verbatim ("comité-exécutif",
// "東京"); underscores are kept, like GitHub.
func slug(s string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		} else {
			pendingDash = true
		}
	}
	return b.String()
}
