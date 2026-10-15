package markdown

import (
	"bytes"
	"regexp"
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

// alertTitles carries the GitHub-parity title markup (octicon + label) for
// each alert type; octicons are from primer/octicons (MIT).
var alertTitles = map[string]string{
	"note":      `<svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="M11.013 1.427a1.75 1.75 0 0 1 2.474 0l1.086 1.086a1.75 1.75 0 0 1 0 2.474l-8.61 8.61c-.21.21-.47.364-.756.445l-3.251.93a.75.75 0 0 1-.927-.928l.929-3.25c.081-.286.235-.547.445-.758l8.61-8.61Zm.176 4.823L9.75 4.81l-6.286 6.287a.253.253 0 0 0-.064.108l-.558 1.953 1.953-.558a.253.253 0 0 0 .108-.064Zm1.238-3.763a.25.25 0 0 0-.354 0L10.811 3.75l1.439 1.44 1.263-1.263a.25.25 0 0 0 0-.354Z"/></svg>Note`,
	"tip":       `<svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="m8 14.25.345.666a.75.75 0 0 1-.69 0l-.008-.004-.018-.01a7.152 7.152 0 0 1-.31-.17 22.055 22.055 0 0 1-3.434-2.414C2.045 10.731 0 8.35 0 5.5 0 2.836 2.086 1 4.25 1 5.797 1 7.153 1.802 8 3.02 8.847 1.802 10.203 1 11.75 1 13.914 1 16 2.836 16 5.5c0 2.85-2.045 5.231-3.885 6.818a22.066 22.066 0 0 1-3.744 2.584l-.018.01-.006.003h-.002ZM4.25 2.5c-1.336 0-2.75 1.164-2.75 3 0 2.15 1.58 4.144 3.365 5.682A20.58 20.58 0 0 0 8 13.393a20.58 20.58 0 0 0 3.135-2.211C12.92 9.644 14.5 7.65 14.5 5.5c0-1.836-1.414-3-2.75-3-1.373 0-2.609.986-3.029 2.456a.749.749 0 0 1-1.442 0C6.859 3.486 5.623 2.5 4.25 2.5Z"/></svg>Tip`,
	"important": `<svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="M3.25 9a.75.75 0 0 1 .75.75c0 2.142.456 3.828.733 4.653a.122.122 0 0 0 .05.064.212.212 0 0 0 .117.033h1.31c.085 0 .18-.042.258-.152a.45.45 0 0 0 .075-.366A16.743 16.743 0 0 1 6 9.75a.75.75 0 0 1 1.5 0c0 1.588.25 2.926.494 3.85.293 1.113-.504 2.4-1.783 2.4H4.9c-.686 0-1.35-.41-1.589-1.12A16.4 16.4 0 0 1 2.5 9.75.75.75 0 0 1 3.25 9Z"/><path d="M0 6a4 4 0 0 1 4-4h2.75a.75.75 0 0 1 .75.75v6.5a.75.75 0 0 1-.75.75H4a4 4 0 0 1-4-4Zm4-2.5a2.5 2.5 0 1 0 0 5h2v-5Z"/><path d="M15.59.082A.75.75 0 0 1 16 .75v10.5a.75.75 0 0 1-1.189.608l-.002-.001h.001l-.014-.01a5.775 5.775 0 0 0-.422-.25 10.63 10.63 0 0 0-1.469-.64C11.576 10.484 9.536 10 6.75 10a.75.75 0 0 1 0-1.5c2.964 0 5.174.516 6.658 1.043.423.151.787.302 1.092.443V2.014c-.305.14-.669.292-1.092.443C11.924 2.984 9.713 3.5 6.75 3.5a.75.75 0 0 1 0-1.5c2.786 0 4.826-.484 6.155-.957.665-.236 1.154-.47 1.47-.64.144-.077.284-.161.421-.25l.014-.01a.75.75 0 0 1 .78-.061Z"/></svg>Important`,
	"warning":   `<svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="M6.457 1.047c.659-1.234 2.427-1.234 3.086 0l6.082 11.378A1.75 1.75 0 0 1 14.082 15H1.918a1.75 1.75 0 0 1-1.543-2.575Zm1.763.707a.25.25 0 0 0-.44 0L1.698 13.132a.25.25 0 0 0 .22.368h12.164a.25.25 0 0 0 .22-.368Zm.53 3.996v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 11a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z"/></svg>Warning`,
	"caution":   `<svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="M4.47.22A.749.749 0 0 1 5 0h6c.199 0 .389.079.53.22l4.25 4.25c.141.14.22.331.22.53v6a.749.749 0 0 1-.22.53l-4.25 4.25A.749.749 0 0 1 11 16H5a.749.749 0 0 1-.53-.22L.22 11.53A.749.749 0 0 1 0 11V5c0-.199.079-.389.22-.53Zm.84 1.28L1.5 5.31v5.38l3.81 3.81h5.38l3.81-3.81V5.31L10.69 1.5ZM8 4a.75.75 0 0 1 .75.75v3.5a.75.75 0 0 1-1.5 0v-3.5A.75.75 0 0 1 8 4Zm0 8a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z"/></svg>Caution`,
}

var alertBlockquoteRe = regexp.MustCompile(`<blockquote class="markdown-alert markdown-alert-(note|tip|important|warning|caution)">`)

// AddAlertTitles injects the GitHub title paragraph (octicon + label) as the
// first child of every alert blockquote. Callers apply it to sanitized
// output: the markup is this file's, never content's, so the sanitizer's
// GitHub raw-HTML subset — which rejects svg — stays intact.
func AddAlertTitles(html string) string {
	return alertBlockquoteRe.ReplaceAllStringFunc(html, func(m string) string {
		const prefix = "markdown-alert-"
		body := strings.TrimSuffix(m, `">`)
		i := strings.LastIndex(body, prefix)
		return m + `<p class="markdown-alert-title">` + alertTitles[body[i+len(prefix):]] + `</p>`
	})
}
