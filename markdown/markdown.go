// Package markdown is the dolmen Markdown-to-HTML stack: a zero-config
// goldmark extension assembling GitHub-flavored Markdown (tables,
// strikethrough, task lists, autolinks, footnotes, emoji) plus heading ids
// and permalink anchors, GitHub alerts, `![alt](url =WxH)` image sizing,
// chroma-highlighted and mermaid fences, YAML front matter, and MathJax
// math in $...$, $$...$$, \(...\), \[...\] delimiters. Raw HTML passes
// through the renderer for DisplayPolicy to gate, GitHub-style.
//
// Convert is the intended entry point. The GFM extender alone composes
// into a custom goldmark pipeline, but then raw-HTML rendering and
// sanitizing are the caller's business.
package markdown

import (
	"bytes"
	stdhtml "html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	emoji "github.com/yuin/goldmark-emoji"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Flags tells the page which optional payloads the rendered HTML needs:
// the template loads only the vendored JS actually required.
type Flags struct {
	Math    bool // TeX math present: load MathJax
	Mermaid bool // mermaid fence present: load mermaid
}

// GFM extends a goldmark instance with the full dolmen feature set and no
// configuration.
var GFM goldmark.Extender = gfmExt{}

type gfmExt struct{}

func (gfmExt) Extend(m goldmark.Markdown) {
	// Assembled by hand rather than extension.GFM so the table extension
	// can be configured: goldmark aligns cells with style attributes,
	// which DisplayPolicy strips; the align attribute survives.
	extension.Linkify.Extend(m)
	extension.Strikethrough.Extend(m)
	extension.TaskList.Extend(m)
	extension.NewTable(
		extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute),
	).Extend(m)
	extension.Footnote.Extend(m)
	emoji.Emoji.Extend(m)

	m.Parser().AddOptions(
		parser.WithAutoHeadingID(),
		// "# Heading {#id .class}" suffixes, like GitHub: the heading
		// attribute filter drops unknown names, DisplayPolicy bounds
		// id and class downstream.
		parser.WithAttribute(),
		// 50: ahead of everything else on the parser's own trigger bytes
		// ($ and \); the inline loop consults parsers at \ before the
		// backslash-escape pass. codespans still win over math because
		// they trigger earlier in the line, at the backtick. sized
		// images before footnote (101) and link (1000).
		parser.WithInlineParsers(
			util.Prioritized(&mathInlineParser{}, 50),
			util.Prioritized(&sizedImageParser{}, 100),
		),
		parser.WithBlockParsers(
			util.Prioritized(&mathBlockParser{}, 250),
		),
		parser.WithASTTransformers(
			util.Prioritized(&alertTransformer{}, 900), // avoids footnote's 999
		),
	)
	m.Renderer().AddOptions(
		// Raw HTML reaches the sanitizer instead of being omitted;
		// DisplayPolicy is the gate, GitHub-style.
		htmlrenderer.WithUnsafe(),
		renderer.WithNodeRenderers(
			// lower priority number wins; ours override the defaults
			util.Prioritized(&fencedRenderer{}, 200),
			util.Prioritized(&headingRenderer{}, 200),
			util.Prioritized(&sizedImageRenderer{}, 200),
			util.Prioritized(&mathRenderer{}, 200),
		),
	)
}

// New returns a goldmark instance with the GFM extension applied.
func New() goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(GFM))
}

// Convert renders a whole Markdown document: the optional YAML front
// matter table, then the body with raw HTML gated by DisplayPolicy and
// every table wrapped for horizontal scrolling. On render failure it
// returns the source as escaped preformatted text.
func Convert(src string) (string, Flags) {
	fmTable, body := splitFrontMatter(src)
	md := New()
	source := []byte(body)
	doc := md.Parser().Parse(text.NewReader(source))
	var out bytes.Buffer
	if err := md.Renderer().Render(&out, source, doc); err != nil {
		return plainPre(src), Flags{}
	}
	safe := string(policy.SanitizeBytes(out.Bytes()))
	return fmTable + wrapTables(safe), Needs(doc, source)
}

// Needs reports whether the parsed document contains math or mermaid.
func Needs(doc ast.Node, source []byte) Flags {
	var f Flags
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case kindMath, kindMathBlock:
			f.Math = true
		case ast.KindFencedCodeBlock:
			if fencedInfo(n.(*ast.FencedCodeBlock), source) == "mermaid" {
				f.Mermaid = true
			}
		}
		return ast.WalkContinue, nil
	})
	return f
}

var policy = DisplayPolicy()

// DisplayPolicy renders gist content safely. Chroma emits class-named
// spans/tables and mermaid fences need a class; UGC strips class by
// default, so allow it back — no user-controlled CSS is ever served,
// classes only map to the fixed stylesheets the app ships.
func DisplayPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("table", "thead", "tbody", "tr", "td", "th", "div", "span")
	p.AllowAttrs("class").Globally()
	// Task-list checkboxes only: goldmark's TaskList emits disabled inputs,
	// which the UGC forms ban would otherwise strip.
	p.AllowAttrs("type").Matching(checkboxType).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	// ids are generated by goldmark (heading slugs, footnote fn:/fnref:
	// links), never taken verbatim from user text; the charset regex keeps
	// raw-HTML-injected ids out of DOM-clobbering shapes. Note: UGC's
	// AllowStandardAttributes already admits id/title globally with their
	// own charset-limited regexes and bluemonday offers no removal API,
	// so raw HTML can still set those two — accepted: the charset bounds
	// the effect to id collisions, not script or markup injection.
	p.AllowAttrs("id").Matching(safeID).OnElements("h1", "h2", "h3", "h4", "h5", "h6", "sup", "li", "div")
	// The one gap in UGC vs GitHub's raw-HTML subset.
	p.AllowElements("kbd")
	return p
}

var (
	checkboxType = regexp.MustCompile(`^checkbox$`)
	safeID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)
)

// wrapTables puts every table in a horizontally scrollable box, so wide
// tables (many columns, unbreakable paths) scroll instead of pushing the
// whole page wide. Runs after sanitize: the only literal "<table" left at
// this point is generated or sanitizer-approved markup — code-fence text
// is already escaped.
func wrapTables(s string) string {
	s = strings.ReplaceAll(s, "</table>", "</table></div>")
	return strings.ReplaceAll(s, "<table", `<div class="table-wrap"><table`)
}

func plainPre(s string) string {
	return `<pre class="fallback"><code>` + stdhtml.EscapeString(s) + `</code></pre>`
}
