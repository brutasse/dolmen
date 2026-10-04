package models

import (
	"testing"
	"time"
)

func TestGistJSON(t *testing.T) {
	gist := &Gist{
		ID:          "123",
		Description: "Test Gist",
		Files:       []File{{Name: "test.go", Content: "package main"}},
		UpdatedAt:   time.Now(),
		UserID:      "user1",
	}

	data, err := gist.ToJSON()
	if err != nil {
		t.Fatal(err)
	}

	var gist2 Gist
	err = gist2.FromJSON(data)
	if err != nil {
		t.Fatal(err)
	}

	if gist2.ID != gist.ID {
		t.Errorf("ID mismatch")
	}
	if gist2.Description != gist.Description {
		t.Errorf("Description mismatch")
	}
	if len(gist2.Files) != 1 {
		t.Errorf("Files count mismatch")
	}
}

func TestDisplayName(t *testing.T) {
	withFiles := &Gist{Files: []File{{Name: "a.go"}, {Name: "b.go"}}}
	if got := withFiles.DisplayName(); got != "a.go" {
		t.Errorf("DisplayName = %q, want a.go", got)
	}
	empty := &Gist{}
	if got := empty.DisplayName(); got != "gistfile.txt" {
		t.Errorf("DisplayName (no files) = %q, want gistfile.txt", got)
	}
}

func TestIsMarkdown(t *testing.T) {
	yes := []string{"a.md", "A.MD", "x.markdown", "y.mdown", "z.mdwn", "dir/sub/note.md"}
	no := []string{"a.go", "md", "notes.txt", "", "a.md.txt", ".mdx"}
	for _, n := range yes {
		if !IsMarkdown(n) {
			t.Errorf("IsMarkdown(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if IsMarkdown(n) {
			t.Errorf("IsMarkdown(%q) = true, want false", n)
		}
	}
}

func TestFileEmbed(t *testing.T) {
	cases := []struct {
		f    File
		blob bool
		kind string
	}{
		{File{Name: "a.go", Content: "x"}, false, ""},
		{File{Name: "a.png", Key: "k", Mime: "image/png"}, true, "image"},
		{File{Name: "s.m4a", Key: "k", Mime: "audio/mp4"}, true, "audio"},
		{File{Name: "v.mp4", Key: "k", Mime: "video/mp4"}, true, "video"},
		{File{Name: "d.pdf", Key: "k", Mime: "application/pdf"}, true, "pdf"},
		{File{Name: "x.bin", Key: "k", Mime: "application/octet-stream"}, true, ""},
	}
	for _, c := range cases {
		if got := c.f.IsBlob(); got != c.blob {
			t.Errorf("%s: IsBlob = %v, want %v", c.f.Name, got, c.blob)
		}
		if got := c.f.EmbedKind(); got != c.kind {
			t.Errorf("%s: EmbedKind = %q, want %q", c.f.Name, got, c.kind)
		}
	}
}
