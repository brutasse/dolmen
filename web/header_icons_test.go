package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestHeaderNavigationIcons(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, "list", map[string]any{
		"Email": "alice@example.com",
		"Gists": []any{},
	}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`<symbol id="i-list"`,
		`<use href="#i-list"/></svg>My gists`,
		`<use href="#i-trash"/></svg>Deleted`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("header missing %q", want)
		}
	}
}
