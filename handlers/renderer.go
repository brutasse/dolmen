package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strconv"
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
	style *chroma.Style
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
	// FileSlug is the file's line-anchor slug, empty when the file is not
	// line-anchored (markdown, blobs). The template keys the section id and
	// the #file-{slug} fragment off it.
	FileSlug string
}

// rawURL builds the app URL for a file; blobs answer with a redirect to S3.
func rawURL(gistID, name string) string {
	return "/raw/" + gistID + "/" + url.PathEscape(name)
}

func newFileRenderer() *fileRenderer {
	return &fileRenderer{
		style: styles.Get("github"),
	}
}

// numberedFormatter builds the line-numbered formatter for one file. The id
// prefix is per-file, so the formatter is built per render: the first
// line-anchored file on a page uses "L", the rest "file-{slug}-L" (renderView
// assigns these), making every line a page-unique anchor target and turning
// its gutter number into the link.
func numberedFormatter(linePrefix string) *chromahtml.Formatter {
	return chromahtml.New(
		chromahtml.WithClasses(true),
		chromahtml.WithLineNumbers(true),
		chromahtml.WithLinkableLineNumbers(true, linePrefix),
	)
}

// Render renders one file for display. linePrefix namespaces the file's line
// ids; it is ignored for markdown (rendered by petroglyph, whose anchors are
// heading slugs) and passed through for everything else.
func (fr *fileRenderer) Render(name, content, linePrefix string) FileRender {
	if models.IsMarkdown(name) {
		body, flags := petroglyph.Convert(content)
		return FileRender{
			Name:         name,
			Body:         template.HTML(body),
			NeedsMath:    flags.Math,
			NeedsMermaid: flags.Mermaid,
		}
	}
	return fr.renderCode(name, content, linePrefix)
}

// --- code files ---

func (fr *fileRenderer) renderCode(name, content, linePrefix string) FileRender {
	lexer := lexers.Match(name)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	var b bytes.Buffer
	if lexer == nil {
		return FileRender{Name: name, Body: template.HTML(plainPre(content, linePrefix))}
	}
	fr.highlightCode(&b, lexer, strings.TrimRight(content, "\n"), numberedFormatter(linePrefix))
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
		w.WriteString(plainPre(src, ""))
		return
	}
	w.WriteString(b.String())
}

// --- fallback ---

// plainPre is the no-lexer fallback: the content, escaped, one line row per
// source line in the same shape as the chroma path (line / number / code),
// so line anchors, the gutter and the highlight behave identically in both.
// linePrefix is "" only when the chroma pass fails mid-render and the rows
// were already emitted without it.
func plainPre(src, linePrefix string) string {
	src = strings.TrimRight(src, "\n")
	var b strings.Builder
	b.WriteString(`<pre class="fallback"><code>`)
	for i, line := range strings.Split(src, "\n") {
		n := i + 1
		num := strconv.Itoa(n)
		if linePrefix != "" {
			id := linePrefix + num
			fmt.Fprintf(&b, `<span class="line"><span class="ln" id="%s"><a class="lnlinks" href="#%s">%s</a></span><span class="cl">%s</span></span>`,
				id, id, num, template.HTMLEscapeString(line))
		} else {
			fmt.Fprintf(&b, `<span class="line"><span class="ln">%s</span><span class="cl">%s</span></span>`,
				num, template.HTMLEscapeString(line))
		}
	}
	b.WriteString(`</code></pre>`)
	return b.String()
}

// fileAnchorSlug is the slug a file's line anchors use: the lowercased name
// with runs of non-alphanumerics collapsed to dashes, the way GitHub slugs
// gist files. used carries the slugs already taken earlier on the page; a
// duplicate takes -2, -3, … (file names are unique per gist, so only the
// slug function can collide).
func fileAnchorSlug(name string, used map[string]bool) string {
	var b strings.Builder
	dash := true // leading punctuation collapses away
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimSuffix(b.String(), "-")
	if slug == "" {
		slug = "file"
	}
	base := slug
	for i := 2; used[slug]; i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	used[slug] = true
	return slug
}
