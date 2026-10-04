package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"dolmen/middleware"
	"dolmen/models"
	"github.com/google/uuid"
)

// blobTypes maps extensions to the Content-Type a direct browser upload is
// pinned to. The presigned PUT does not sign the header (the SDK signs only
// host), so the pin is enforced at save time: resolveBlobs rejects any
// object whose stored type differs from the pinned one. Renderable types get
// their real type; anything else becomes application/octet-stream:
// downloadable, never rendered, never sniffable as something else.
var blobTypes = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".ogv":  "video/ogg",
}

func blobContentType(name string) string {
	if t, ok := blobTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// uploadKeyPrefix scopes all browser-uploaded blobs; they are only ever
// referenced by gist JSON after a HEAD check at save time.
const uploadKeyPrefix = "uploads/"

// validUploadKey reports whether key is an upload issued for this user:
// uploads/{user}/{uuid}. Nothing else may ever be referenced by a gist.
func validUploadKey(key, userID string) bool {
	prefix := uploadKeyPrefix + userID + "/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	rest := strings.TrimPrefix(key, prefix)
	u, err := uuid.Parse(rest)
	// Canonical spelling only: uuid.Parse also accepts braces, urn prefixes
	// and hyphenless forms, which would make one key spell two valid ones.
	return err == nil && u.String() == rest
}

// resolveBlobs turns uploaded-file entries into stored facts: the key must
// be the submitting user's own upload, the object must actually exist, and
// the authoritative mime/size are taken from S3 — never from the form. The
// stored mime must equal the type /upload pinned for the file's extension:
// the presigned PUT does not sign Content-Type, so this check is what keeps
// the browser from substituting an embed-renderable type.
func (h *Handler) resolveBlobs(ctx context.Context, gist *models.Gist, userID string) error {
	for i := range gist.Files {
		f := &gist.Files[i]
		if f.Key == "" {
			continue
		}
		if !validUploadKey(f.Key, userID) {
			return fmt.Errorf("invalid upload for file %q", f.Name)
		}
		size, mime, err := h.s3Client.HeadObject(ctx, f.Key)
		if err != nil {
			return fmt.Errorf("uploaded file %q not found, upload it again", f.Name)
		}
		if want := blobContentType(f.Name); !strings.EqualFold(strings.TrimSpace(mime), want) {
			return fmt.Errorf("uploaded file %q has type %q, expected %q: re-upload it", f.Name, mime, want)
		}
		f.Mime, f.Size = mime, size
	}
	return nil
}

// Upload mints a presigned PUT URL so the browser sends a file straight to
// S3 without its bytes transiting this server. The key is an unguessable
// uuid under the session user's prefix, valid for minutes. Files uploaded
// but never saved are orphans the bucket tolerates by design.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r.Context())
	form, err := readForm(r)
	if err != nil {
		http.Error(w, err.Error(), formErrorStatus(err))
		return
	}
	if !middleware.CheckCSRF(r, form.Get("csrf_token")) {
		http.Error(w, "CSRF token mismatch", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(form.Get("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	contentType := blobContentType(name)
	key := uploadKeyPrefix + userID + "/" + uuid.NewString()
	putURL, err := h.s3Client.PresignPut(r.Context(), key, contentType, 5*time.Minute)
	if err != nil {
		log.Printf("upload: presign for %s: %v", userID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Key         string `json:"key"`
		URL         string `json:"url"`
		ContentType string `json:"contentType"`
	}{key, putURL, contentType})
}
