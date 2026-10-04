package handlers

import "testing"

func TestBlobContentType(t *testing.T) {
	cases := map[string]string{
		"doc.pdf":                 "application/pdf",
		"pic.PNG":                 "image/png",
		"photo.jpeg":              "image/jpeg",
		"song.m4a":                "audio/mp4",
		"clip.webm":               "video/webm",
		"mystery.bin":             "application/octet-stream",
		"noext":                   "application/octet-stream",
		"script.html":             "application/octet-stream",
		"..tricksy.weird.pdf":     "application/pdf",
		"/absolute/unix/path.mp3": "audio/mpeg",
	}
	for name, want := range cases {
		if got := blobContentType(name); got != want {
			t.Errorf("blobContentType(%q) = %q, want %q", name, got, want)
		}
	}
}
