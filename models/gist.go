package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// File represents a single file in a gist. Text files carry Content inline;
// blobs (uploads too big or binary for JSON) store only a Key pointing at an
// S3 object plus its server-pinned Mime and Size.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	Key     string `json:"key,omitempty"`
	Mime    string `json:"mime,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

// IsBlob reports whether the file's bytes live in S3 under Key.
func (f *File) IsBlob() bool { return f.Key != "" }

// EmbedKind is how the view page presents a blob: image, audio, video, pdf,
// or "" for anything else (download-only).
func (f *File) EmbedKind() string {
	switch {
	case strings.HasPrefix(f.Mime, "image/"):
		return "image"
	case strings.HasPrefix(f.Mime, "audio/"):
		return "audio"
	case strings.HasPrefix(f.Mime, "video/"):
		return "video"
	case f.Mime == "application/pdf":
		return "pdf"
	}
	return ""
}

// DefaultFileName names a file whose entry was submitted without a name.
const DefaultFileName = "gistfile.txt"

// markdownExts: single source of truth for "this file renders as Markdown"
// — the view renderer, the edit page's Preview tabs, and the client-side
// tab toggling all key off it.
var markdownExts = map[string]bool{
	".md": true, ".markdown": true, ".mdown": true, ".mdwn": true,
}

// IsMarkdown reports whether a file name carries a Markdown extension.
func IsMarkdown(name string) bool {
	lower := strings.ToLower(name)
	if i := strings.LastIndex(lower, "."); i >= 0 {
		return markdownExts[lower[i:]]
	}
	return false
}

// HumanSize formats a byte count for UI display ("1.5 MB").
func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Gist represents a gist
type Gist struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Files       []File    `json:"files"`
	UpdatedAt   time.Time `json:"updated_at"`
	UserID      string    `json:"owner,omitempty"`       // owner user ID (creator)
	Owner       string    `json:"owner_email,omitempty"` // owner display email, captured on save
}

// DisplayName is how the gist is titled in the UI: the first file's name,
// GitHub-style. A file-less gist (possible when every entry was left blank)
// shows the default name.
func (g *Gist) DisplayName() string {
	if len(g.Files) > 0 {
		return g.Files[0].Name
	}
	return DefaultFileName
}

// ToJSON serializes the gist to JSON bytes
func (g *Gist) ToJSON() ([]byte, error) {
	return json.Marshal(g)
}

// FromJSON deserializes JSON bytes into a gist
func (g *Gist) FromJSON(data []byte) error {
	return json.Unmarshal(data, g)
}

// Version represents a gist version from S3
type Version struct {
	ID           string    `json:"id"`
	VersionID    string    `json:"version_id"`
	LastModified time.Time `json:"last_modified"`
	Size         int64     `json:"size"`
}
