package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"crypto/rand"
	"dolmen/config"
	"dolmen/middleware"
	"dolmen/models"
	"dolmen/s3"
	"dolmen/web"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// Handler holds dependencies
type Handler struct {
	s3Client *s3.Client
	files    *fileRenderer
	provider *oidc.Provider
	baseURL  string
	oidcCfg  config.OIDCConfig
}

// NewHandler creates a new handler. cfg.OIDC must carry the resolved
// client secret, not an env var name.
func NewHandler(s3Client *s3.Client, provider *oidc.Provider, cfg *config.Config) *Handler {
	return &Handler{
		s3Client: s3Client,
		files:    newFileRenderer(),
		provider: provider,
		baseURL:  cfg.BaseURL,
		oidcCfg:  cfg.OIDC,
	}
}

// ListGists lists user's gists
func (h *Handler) ListGists(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r.Context())
	gists, err := h.s3Client.ListGists(r.Context(), userID)
	if err != nil {
		respondStorage(w, r, err)
		return
	}
	web.RenderHTTP(w, "list", struct {
		Gists  []models.Gist
		UserID string
		Email  string
	}{Gists: gists, UserID: userID, Email: middleware.GetUserEmail(r.Context())})
}

// ViewGist views a gist
func (h *Handler) ViewGist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
	userID := middleware.GetUserID(r.Context())
	gist, err := h.s3Client.GetGist(r.Context(), id)
	if err != nil {
		http.Error(w, "Gist not found", statusFor(err))
		return
	}
	versions, _ := h.s3Client.ListVersions(r.Context(), id)
	h.renderView(w, gist, versions, userID, middleware.GetUserEmail(r.Context()), "", middleware.CSRFToken(r.Context()))
}

type viewData struct {
	Gist          *models.Gist
	RenderedFiles []FileRender
	Versions      []models.Version
	UserID        string
	Email         string
	CSRF          string
	CurrentVid    string // version ID being displayed; empty for the current version
	NeedsMath     bool
	NeedsMermaid  bool
}

// renderView renders the view template
func (h *Handler) renderView(w http.ResponseWriter, gist *models.Gist, versions []models.Version, userID, email, currentVid, csrf string) {
	// ListObjectVersions is newest-first, and its first entry is the content
	// the dropdown's static "Current Version" option already represents.
	if len(versions) > 0 {
		versions = versions[1:]
	}
	data := viewData{Gist: gist, Versions: versions, UserID: userID, Email: email, CurrentVid: currentVid, CSRF: csrf}
	rawSuffix := ""
	if currentVid != "" {
		rawSuffix = "?vid=" + url.QueryEscape(currentVid)
	}
	for _, file := range gist.Files {
		raw := template.URL(rawURL(gist.ID, file.Name) + rawSuffix)
		if file.IsBlob() {
			kind := file.EmbedKind()
			if kind == "" {
				kind = "file"
			}
			data.RenderedFiles = append(data.RenderedFiles, FileRender{
				Name:     file.Name,
				Kind:     kind,
				RawURL:   raw,
				SizeText: models.HumanSize(file.Size),
			})
			continue
		}
		fr := h.files.Render(file.Name, file.Content)
		fr.Kind = "text"
		fr.RawURL = raw
		data.NeedsMath = data.NeedsMath || fr.NeedsMath
		data.NeedsMermaid = data.NeedsMermaid || fr.NeedsMermaid
		data.RenderedFiles = append(data.RenderedFiles, fr)
	}
	web.RenderHTTP(w, "view", data)
}

// UpdateGist updates a gist
func (h *Handler) UpdateGist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
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
	gist, err := gistFromForm(form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.resolveBlobs(r.Context(), gist, userID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	gist.ID = id
	gist.UpdatedAt = time.Now()
	// updates are owner-only (enforced by the s3 layer), so the session
	// email is the owner's: capture it, self-healing gists predating it
	gist.Owner = middleware.GetUserEmail(r.Context())
	if err := h.s3Client.UpdateGist(r.Context(), userID, gist); err != nil {
		respondStorage(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/gist/%s", id), http.StatusFound)
}

// EditGist shows edit form for a gist
func (h *Handler) EditGist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
	userID := middleware.GetUserID(r.Context())
	gist, err := h.s3Client.GetGist(r.Context(), id)
	if err != nil {
		http.Error(w, "Gist not found", statusFor(err))
		return
	}
	if gist.UserID != userID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	web.RenderHTTP(w, "edit", struct {
		Gist   *models.Gist
		UserID string
		Email  string
		CSRF   string
	}{Gist: gist, UserID: userID, Email: middleware.GetUserEmail(r.Context()), CSRF: middleware.CSRFToken(r.Context())})
}

// CreateGist shows create form
func (h *Handler) CreateGist(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r.Context())
	web.RenderHTTP(w, "edit", struct {
		Gist   *models.Gist
		UserID string
		Email  string
		CSRF   string
	}{Gist: &models.Gist{}, UserID: userID, Email: middleware.GetUserEmail(r.Context()), CSRF: middleware.CSRFToken(r.Context())})
}

// StoreGist creates a new gist
func (h *Handler) StoreGist(w http.ResponseWriter, r *http.Request) {
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
	gist, err := gistFromForm(form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.resolveBlobs(r.Context(), gist, userID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	gist.ID = generateID()
	gist.UpdatedAt = time.Now()
	gist.Owner = middleware.GetUserEmail(r.Context())
	if err := h.s3Client.CreateGist(r.Context(), userID, gist); err != nil {
		respondStorage(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/gist/%s", gist.ID), http.StatusFound)
}

// DeleteGist deletes a gist
func (h *Handler) DeleteGist(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
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
	if err := h.s3Client.DeleteGist(r.Context(), userID, id); err != nil {
		respondStorage(w, r, err)
		return
	}
	http.Redirect(w, r, "/gists", http.StatusFound)
}

// ListVersions lists versions
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
	versions, err := h.s3Client.ListVersions(r.Context(), id)
	if err != nil {
		respondStorage(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(versions)
}

// ViewVersion views a version
func (h *Handler) ViewVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validGistID(id) {
		http.Error(w, "Gist not found", http.StatusNotFound)
		return
	}
	vid := chi.URLParam(r, "vid")
	userID := middleware.GetUserID(r.Context())
	gist, err := h.s3Client.GetVersion(r.Context(), id, vid)
	if err != nil {
		http.Error(w, "Version not found", statusFor(err))
		return
	}
	versions, _ := h.s3Client.ListVersions(r.Context(), id)
	h.renderView(w, gist, versions, userID, middleware.GetUserEmail(r.Context()), vid, middleware.CSRFToken(r.Context()))
}

// Login redirects to OIDC auth
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if h.oidcCfg.ClientSecret == "" {
		http.Error(w, "The built-in login flow is disabled (no oidc.client_secret)", http.StatusServiceUnavailable)
		return
	}

	state, err := generateState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	codeVerifier := oauth2.GenerateVerifier()

	oauth2Config := &oauth2.Config{
		ClientID:     h.oidcCfg.ClientID,
		ClientSecret: h.oidcCfg.ClientSecret,
		Endpoint:     h.provider.Endpoint(),
		Scopes:       []string{"openid", "email"},
		RedirectURL:  h.baseURL + "/callback",
	}

	// The flow cookies must survive the cross-site top-level redirect back
	// from the IdP, so they need SameSite=None — which browsers only accept
	// together with Secure. On plain http (local dev) the attribute is left
	// off and the browser default applies, matching prior behavior.
	flowSite := http.SameSiteDefaultMode
	if middleware.CookieSecure(h.baseURL) {
		flowSite = http.SameSiteNoneMode
	}
	h.setCookie(w, "oauth_state", state, 600, flowSite)
	h.setCookie(w, "oauth_code_verifier", codeVerifier, 600, flowSite)
	h.setCookie(w, "redirect_url", safeRedirectPath(r.URL.Query().Get("redirect")), 600, flowSite)

	authURL := oauth2Config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(codeVerifier))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// Callback handles OIDC callback
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	if h.oidcCfg.ClientSecret == "" {
		http.Error(w, "The built-in login flow is disabled (no oidc.client_secret)", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	cookieState, _ := r.Cookie("oauth_state")
	cookieVerifier, _ := r.Cookie("oauth_code_verifier")
	cookieRedirect, _ := r.Cookie("redirect_url")

	// The flow is over either way: expire the single-use cookies before any
	// decision so a replanted or stale flow cookie cannot outlive this
	// round trip (the values above were already read from the request).
	h.expireCookie(w, "oauth_state")
	h.expireCookie(w, "oauth_code_verifier")
	h.expireCookie(w, "redirect_url")

	// An empty state must not pass: an attacker-planted empty cookie would
	// equal an empty query parameter.
	if state == "" || cookieState == nil || cookieState.Value != state {
		http.Error(w, "Invalid state", http.StatusBadRequest)
		return
	}

	if cookieVerifier == nil {
		http.Error(w, "Missing code verifier", http.StatusBadRequest)
		return
	}

	oauth2Config := &oauth2.Config{
		ClientID:     h.oidcCfg.ClientID,
		ClientSecret: h.oidcCfg.ClientSecret,
		Endpoint:     h.provider.Endpoint(),
		Scopes:       []string{"openid", "email"},
		RedirectURL:  h.baseURL + "/callback",
	}

	token, err := oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(cookieVerifier.Value))
	if err != nil {
		log.Printf("Token exchange failed: %v", err)
		http.Error(w, "Token exchange failed", http.StatusInternalServerError)
		return
	}

	// Verify ID token
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No ID token", http.StatusInternalServerError)
		return
	}

	verifier := h.provider.Verifier(&oidc.Config{ClientID: h.oidcCfg.ClientID})
	_, err = verifier.Verify(ctx, rawIDToken)
	if err != nil {
		log.Printf("Token verification failed: %v", err)
		http.Error(w, "Token verification failed", http.StatusInternalServerError)
		return
	}

	// Set ID token in cookie. The session cookie is only ever sent on
	// same-site navigations, so Lax is safe (and blocks cross-site POSTs
	// from carrying the session).
	h.setCookie(w, "id_token", rawIDToken, 3600, http.SameSiteLaxMode)

	// Redirect to original URL
	redirectURL := "/"
	if cookieRedirect != nil {
		if p := safeRedirectPath(cookieRedirect.Value); p != "" {
			redirectURL = p
		}
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// statusFor maps storage errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, s3.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, s3.ErrForbidden):
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// respondStorage answers with the status statusFor maps to. On 500 the real
// error is logged and only a generic message is sent: S3 error strings can
// surface bucket and endpoint internals.
func respondStorage(w http.ResponseWriter, r *http.Request, err error) {
	if statusFor(err) == http.StatusInternalServerError {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	http.Error(w, http.StatusText(statusFor(err)), statusFor(err))
}

// validGistID reports whether id is the canonical lowercase UUID spelling
// the app mints (generateID). Rejecting anything else at the route keeps
// attacker-chosen strings from ever reaching storage lookups.
func validGistID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// maxBodyBytes matches the storage-layer 16 MiB gist limit. Going through
// r.ParseForm is not an option: it caps urlencoded bodies at 10 MiB, which
// would make the storage check unreachable.
const maxBodyBytes = 16 << 20

var errBodyTooLarge = errors.New("request body exceeds 16 MiB")

func formErrorStatus(err error) int {
	if errors.Is(err, errBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// readForm reads the whole (size-capped) urlencoded body. Handlers own the
// body, so CSRF verification reads the token from the returned values.
func readForm(r *http.Request) (url.Values, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, errBodyTooLarge
	}
	return url.ParseQuery(string(body))
}

// gistFromForm builds a gist from submitted form values. Files are
// submitted as fileN_name/fileN_content. N can have gaps when entries are
// removed in the UI, so collect every index instead of stopping at the
// first missing one.
// errDuplicateName rejects a submission that names two files the same.
// User-typed names are never silently mutated; only unnamed entries get
// numbered auto-names.
var errDuplicateName = errors.New("duplicate file name")

// maxFileNameLen bounds stored file names: they flow into Content-Disposition
// headers and rendered pages, so an unbounded name is an unbounded header.
const maxFileNameLen = 255

var errNameTooLong = errors.New("file name too long")

func gistFromForm(form url.Values) (*models.Gist, error) {
	gist := &models.Gist{
		Description: form.Get("description"),
	}
	var indexes []int
	seen := map[string]bool{}
	for key := range form {
		base := strings.TrimSuffix(key, "_name")
		if base == key {
			base = strings.TrimSuffix(key, "_key")
		}
		if base == key {
			continue
		}
		var i int
		if _, err := fmt.Sscanf(base, "file%d", &i); err == nil && !seen[base] {
			seen[base] = true
			indexes = append(indexes, i)
		}
	}
	sort.Ints(indexes)

	// Pass 1: user-typed names win and collide loudly. Pass 2: unnamed
	// entries get numbered auto-names that dodge every used name, so the
	// outcome never depends on entry order.
	used := map[string]bool{}
	entries := make([]models.File, 0, len(indexes))
	var unnamed []int
	for _, i := range indexes {
		name := strings.TrimSpace(form.Get(fmt.Sprintf("file%d_name", i)))
		// Browsers submit textarea values with CRLF; store canonical LF so
		// line-based pre-passes (front matter, math, fences) see one shape.
		content := strings.ReplaceAll(form.Get(fmt.Sprintf("file%d_content", i)), "\r\n", "\n")
		key := strings.TrimSpace(form.Get(fmt.Sprintf("file%d_key", i)))
		if name == "" && content == "" && key == "" {
			continue // blank entry: dropped, not auto-named
		}
		if len(name) > maxFileNameLen {
			return nil, fmt.Errorf("%w: max %d bytes", errNameTooLong, maxFileNameLen)
		}
		if key != "" {
			content = "" // a dropped file is the entry's truth, not the textarea
		}
		if name != "" {
			if used[name] {
				return nil, fmt.Errorf("%w: %s", errDuplicateName, name)
			}
			used[name] = true
		} else {
			unnamed = append(unnamed, len(entries))
		}
		entries = append(entries, models.File{Name: name, Content: content, Key: key})
	}
	for _, pos := range unnamed {
		name := autoFileName(used)
		used[name] = true
		entries[pos].Name = name
	}
	gist.Files = entries
	return gist, nil
}

// autoFileName picks the first free gistfile name: gistfile.txt, then
// gistfile.1.txt, gistfile.2.txt, ... skipping whatever the submission
// already used.
func autoFileName(used map[string]bool) string {
	name := models.DefaultFileName
	for n := 1; used[name]; n++ {
		name = fmt.Sprintf("gistfile.%d.txt", n)
	}
	return name
}

// generateID generates a UUID
func generateID() string {
	return uuid.New().String()
}

// generateState generates a random state string
func generateState() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// safeRedirectPath returns p if it is a same-site absolute path, "" otherwise.
// Rejects absolute URLs and scheme-relative forms (//host, /\host) that
// browsers normalize to cross-origin redirects.
func safeRedirectPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return ""
	}
	// Browsers strip tabs/newlines from Location values, which could turn
	// "/\t/evil.com" into the scheme-relative "//evil.com".
	for _, c := range p {
		if c <= ' ' {
			return ""
		}
	}
	return p
}

// setCookie sets a cookie, Secure only when the deployment origin is https
// (browsers drop Secure cookies over plain http).
func (h *Handler) setCookie(w http.ResponseWriter, name, value string, maxAge int, sameSite http.SameSite) {
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   middleware.CookieSecure(h.baseURL),
		SameSite: sameSite,
	}
	http.SetCookie(w, cookie)
}

// expireCookie removes a cookie client-side (Max-Age -1).
func (h *Handler) expireCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   middleware.CookieSecure(h.baseURL),
	})
}
