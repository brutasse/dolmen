package web

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestEditorSettingsTemplate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gist  map[string]any
		files int
	}{
		{"create", map[string]any{"Files": []any{}}, 1},
		{"edit", map[string]any{"ID": "abc", "Files": []any{
			map[string]any{"Name": "a.txt", "Content": "long line\n"},
			map[string]any{"Name": "b.md", "Content": "# Heading"},
			map[string]any{"Name": "image.png", "Key": "blob/image"},
		}}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, "edit", map[string]any{"Email": "alice@example.com", "Gist": tc.gist}); err != nil {
				t.Fatal(err)
			}
			html := buf.String()
			// One slot for each rendered file, plus one in the add-entry script.
			if got := strings.Count(html, `class="editor-settings-slot"`); got != tc.files+1 {
				t.Fatalf("settings slots = %d, want %d", got, tc.files+1)
			}
			settings := regexp.MustCompile(`(?s)<template id="editorSettingsTemplate">(.*?)</template>`).FindStringSubmatch(html)
			if len(settings) != 2 {
				t.Fatal("shared settings template missing")
			}
			for _, want := range []string{
				`type="button"`, `aria-label="Editor settings"`, `aria-expanded="false"`,
				`class="editor-settings hidden"`, `<use href="#i-settings"`,
				`<option value="off" selected>No wrap</option>`,
				`<option value="spaces" selected>Spaces</option>`,
				`<option value="2" selected>2</option>`,
				`<option value="4">4</option>`, `<option value="8">8</option>`,
			} {
				if !strings.Contains(settings[1], want) {
					t.Errorf("settings missing %s", want)
				}
			}
			if strings.Contains(settings[1], "name=") {
				t.Error("settings controls must not be submitted or tracked as content")
			}
			for _, tag := range regexp.MustCompile(`<textarea[^>]*>`).FindAllString(html, -1) {
				if !strings.Contains(tag, `wrap="off"`) {
					t.Errorf("textarea does not default to no wrap: %s", tag)
				}
			}
		})
	}
}
