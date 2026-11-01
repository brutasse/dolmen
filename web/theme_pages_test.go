package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeletedPageLoadsThemeToggleScript(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, "trash", map[string]any{
		"Email":         "alice@example.com",
		"RetentionDays": 7,
		"Rows":          []any{},
	}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), `<script src="`+assetURLs["app.js"]+`" defer></script>`) {
		t.Error("deleted page does not load app.js, so its theme toggle stays hidden")
	}
}
