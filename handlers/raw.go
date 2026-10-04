package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"dolmen/models"
	"github.com/go-chi/chi/v5"
)

// RawGist serves raw file text as text/plain, mirroring gist.github.com:
// /raw/{id} serves the first file, /raw/{id}/{filename} a named one (the
// wildcard keeps filenames containing slashes intact). Auth is whatever the
// OIDC middleware accepts — session cookie or Bearer token — and like the
// view page, any authenticated user may read any gist.
func (h *Handler) RawGist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
	var gist *models.Gist
	var err error
	// ?vid= reads the named stored version instead of the current one, so
	// Raw/Copy on a version page show that version's content — including
	// files later edits removed.
	if vid := r.URL.Query().Get("vid"); vid != "" {
		gist, err = h.s3Client.GetVersion(r.Context(), id, vid)
	} else {
		gist, err = h.s3Client.GetGist(r.Context(), id)
	}
	if err != nil {
		http.Error(w, "Gist not found", statusFor(err))
		return
	}
	name := chi.URLParam(r, "*")
	var target models.File
	if name == "" {
		if len(gist.Files) == 0 {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		target = gist.Files[0]
	} else {
		for _, f := range gist.Files {
			if f.Name == name {
				target = f
				break
			}
		}
		if target.Name == "" {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
	}
	if target.Key != "" {
		h.redirectToBlob(w, r, &target)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, target.Content)
}

// redirectToBlob sends the client straight to S3 with a short-lived
// presigned GET; bytes never transit this server and Range requests (video
// seeking) are S3's problem. Download-only blobs get a filename so the
// browser saves a sensible name instead of the uuid key.
func (h *Handler) redirectToBlob(w http.ResponseWriter, r *http.Request, f *models.File) {
	disposition := ""
	if f.EmbedKind() == "" {
		disposition = contentDisposition(f.Name)
	}
	url, err := h.s3Client.PresignGet(r.Context(), f.Key, disposition, 10*time.Minute)
	if err != nil {
		http.Error(w, "Blob unavailable", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// attrChars is the RFC 5987 attr-char set; every other byte is
// percent-encoded in the filename* value.
const attrChars = "!#$&+-.0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_`|~abcdefghijklmnopqrstuvwxyz"

// contentDisposition builds an RFC 6266 attachment header for a user-named
// file. filename* carries the RFC 5987 percent-encoded name, so control
// bytes, quotes, backslashes and stray percent-literals can never appear
// raw in the header the S3 provider emits from the presigned override;
// filename is a printable-ASCII allowlist fallback for old clients.
func contentDisposition(name string) string {
	var fallback strings.Builder
	for _, rr := range name {
		if rr >= 0x20 && rr <= 0x7e && rr != '"' && rr != '\\' {
			fallback.WriteRune(rr)
		} else {
			fallback.WriteByte('_')
		}
	}
	var extended strings.Builder
	for i := 0; i < len(name); i++ {
		if c := name[i]; strings.IndexByte(attrChars, c) >= 0 {
			extended.WriteByte(c)
		} else {
			fmt.Fprintf(&extended, "%%%02X", c)
		}
	}
	return `attachment; filename="` + fallback.String() + `"; filename*=UTF-8''` + extended.String()
}
