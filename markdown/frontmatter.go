package markdown

import (
	"fmt"
	stdhtml "html"
	"strings"

	"gopkg.in/yaml.v3"
)

// splitFrontMatter consumes a leading YAML front-matter block ("---" line to
// "---" or "..." line). It returns the table markup plus remaining source;
// when the block is absent or the top level is not a scalar-keyed mapping,
// the source passes through untouched with an empty table. Values that are
// nested collections render as compact inline values instead of a rejection.
//
// This runs as a source pass before goldmark, not as an extension: an
// invalid front-matter block must fall through to normal Markdown parsing,
// and by the time any transformer or block parser could inspect it, the
// "---" delimiters are already thematic breaks or setext heading
// underlines. Convert applies it.
func splitFrontMatter(src string) (string, string) {
	if !strings.HasPrefix(src, "---\n") {
		return "", src
	}
	lines := strings.Split(src, "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimRight(lines[i], " \t\r"); t == "---" || t == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return "", src
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &doc); err != nil ||
		doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return "", src
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content) == 0 {
		return "", src
	}
	var b strings.Builder
	b.WriteString(`<div class="table-wrap"><table class="frontmatter"><tbody>`)
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return "", src
		}
		val := v.Value
		if v.Kind != yaml.ScalarNode {
			flowize(v)
			out, err := yaml.Marshal(v)
			if err != nil {
				return "", src
			}
			val = strings.TrimSpace(string(out))
		}
		fmt.Fprintf(&b, "<tr><th>%s</th><td>%s</td></tr>",
			stdhtml.EscapeString(k.Value), stdhtml.EscapeString(val))
	}
	b.WriteString("</tbody></table></div>\n")
	return b.String(), strings.Join(lines[end+1:], "\n")
}

// flowize restyles a node subtree as flow, so nested collections marshal to
// one inline line instead of block-form multiline.
func flowize(n *yaml.Node) {
	n.Style = yaml.FlowStyle
	for _, c := range n.Content {
		flowize(c)
	}
}
