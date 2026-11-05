package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestExampleTheme(t *testing.T) {
	css := embeddedAsset(t, "app.css")
	for _, want := range []string{
		"--accent: #6f42c1",
		"--accent: light-dark(#6f42c1, #b794f4)",
		"--accent-btn: light-dark(#6f42c1, #6f42c1)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}

	var buf bytes.Buffer
	if err := Render(&buf, "list", map[string]any{
		"Email": "alice@example.com",
		"Gists": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `class="brand-marks"`) ||
		!strings.Contains(html, `fill="var(--accent-btn)"`) ||
		strings.Count(html, `class="brand-mark"`) != 2 {
		t.Error("header does not contain the example mark and Dolmen mark")
	}

	for _, pair := range [][2]string{
		{"#6f42c1", "#ffffff"},
		{"#b794f4", "#151b23"},
	} {
		if ratio := contrastRatio(cssRGB(t, pair[0], pair[1]), cssRGB(t, pair[1], pair[1])); ratio < 4.5 {
			t.Errorf("%s on %s has %.2f:1 contrast, want at least 4.5:1", pair[0], pair[1], ratio)
		}
	}
}
