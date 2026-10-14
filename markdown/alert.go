package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

var alertMarkers = map[string]string{
	"[!note]":      "note",
	"[!tip]":       "tip",
	"[!important]": "important",
	"[!warning]":   "warning",
	"[!caution]":   "caution",
}

// alertTransformer turns blockquotes whose first paragraph line is exactly an
// alert marker into classed "markdown-alert" blockquotes. The marker source
// "[!TYPE]" splits into several bracket-aware text nodes, so the first-line
// nodes are collected and removed together.
type alertTransformer struct{}

func (alertTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		bq, ok := n.(*ast.Blockquote)
		if !ok {
			return ast.WalkContinue, nil
		}
		p, ok := bq.FirstChild().(*ast.Paragraph)
		if !ok {
			return ast.WalkContinue, nil
		}
		// first physical line of the paragraph (marker may be the whole
		// paragraph, e.g. an empty alert body)
		var line []byte
		for c := p.FirstChild(); c != nil; c = c.NextSibling() {
			t, ok := c.(*ast.Text)
			if !ok {
				return ast.WalkContinue, nil
			}
			line = append(line, t.Segment.Value(source)...)
			if t.SoftLineBreak() {
				break
			}
		}
		typ, ok := alertMarkers[strings.ToLower(string(bytes.TrimSpace(line)))]
		if !ok {
			return ast.WalkContinue, nil
		}
		for c := p.FirstChild(); c != nil; {
			next := c.NextSibling()
			soft := false
			if t, ok := c.(*ast.Text); ok {
				soft = t.SoftLineBreak()
			}
			p.RemoveChild(p, c)
			if soft {
				break
			}
			c = next
		}
		if t, ok := p.FirstChild().(*ast.Text); ok { // trim " > " leftovers
			for t.Segment.Start < t.Segment.Stop && source[t.Segment.Start] == ' ' {
				t.Segment.Start++
			}
		}
		if !p.HasChildren() {
			bq.RemoveChild(bq, p)
		}
		bq.SetAttribute([]byte("class"), []byte("markdown-alert markdown-alert-"+typ))
		return ast.WalkContinue, nil
	})
}
