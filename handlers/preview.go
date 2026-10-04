package handlers

import (
	"encoding/json"
	"net/http"

	"dolmen/middleware"
)

// Preview renders one draft file server-side so the edit page's Preview tab
// shows exactly what saving would produce. Stateless: the draft comes from
// the POST body, the answer is a sanitized HTML fragment plus the script
// gating flags, so the client can load MathJax/Mermaid on demand.
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	form, err := readForm(r)
	if err != nil {
		http.Error(w, err.Error(), formErrorStatus(err))
		return
	}
	if !middleware.CheckCSRF(r, form.Get("csrf_token")) {
		http.Error(w, "CSRF token mismatch", http.StatusForbidden)
		return
	}
	fr := h.files.Render(form.Get("name"), form.Get("content"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		HTML         string `json:"html"`
		NeedsMath    bool   `json:"needsMath"`
		NeedsMermaid bool   `json:"needsMermaid"`
	}{string(fr.Body), fr.NeedsMath, fr.NeedsMermaid})
}
