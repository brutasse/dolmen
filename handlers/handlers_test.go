package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"dolmen/config"
	"dolmen/models"
	"dolmen/web"
)

func TestParseForm(t *testing.T) {
	form := url.Values{}
	form.Set("description", "Test Gist")
	form.Set("file0_name", "main.go")
	form.Set("file0_content", "package main")
	form.Set("file1_name", "readme.md")
	form.Set("file1_content", "# Hello")

	gist, err := gistFromForm(form)
	if err != nil {
		t.Fatal(err)
	}

	if gist.Description != "Test Gist" {
		t.Errorf("Description mismatch")
	}
	if len(gist.Files) != 2 {
		t.Errorf("Files count mismatch")
	}
	if gist.Files[0].Name != "main.go" {
		t.Errorf("File name mismatch")
	}
}

func TestParseFormFileIndexGaps(t *testing.T) {
	// Removing file 0 in the UI leaves file1_* behind; the old parser
	// dropped all files in this case.
	form := url.Values{}
	form.Set("description", "Gap Gist")
	form.Set("file1_name", "b.go")
	form.Set("file1_content", "package b")
	form.Set("file3_name", "d.go")
	form.Set("file3_content", "package d")

	gist, err := gistFromForm(form)
	if err != nil {
		t.Fatal(err)
	}
	if len(gist.Files) != 2 {
		t.Fatalf("Files count mismatch: got %d, want 2", len(gist.Files))
	}
	if gist.Files[0].Name != "b.go" || gist.Files[1].Name != "d.go" {
		t.Errorf("File order/name mismatch: %+v", gist.Files)
	}
}

func TestReadFormSizeCap(t *testing.T) {
	// Just over the 16 MiB cap must be rejected before parsing.
	oversized := "title=" + strings.Repeat("x", maxBodyBytes)
	req := &http.Request{Body: io.NopCloser(strings.NewReader(oversized))}
	if _, err := readForm(req); !errors.Is(err, errBodyTooLarge) {
		t.Errorf("want errBodyTooLarge, got %v", err)
	}
	if got := formErrorStatus(errBodyTooLarge); got != http.StatusRequestEntityTooLarge {
		t.Errorf("formErrorStatus: got %d, want 413", got)
	}

	small := url.Values{"title": {"ok"}}
	req = &http.Request{Body: io.NopCloser(strings.NewReader(small.Encode()))}
	form, err := readForm(req)
	if err != nil || form.Get("title") != "ok" {
		t.Errorf("small form: %v %v", form, err)
	}
}

func TestSafeRedirectPath(t *testing.T) {
	ok := []string{"/", "/gist/abc", "/edit/abc?x=1"}
	bad := []string{"", "https://evil.com", "//evil.com", `/\evil.com`, "evil.com", "/ /evil.com"}
	for _, p := range ok {
		if got := safeRedirectPath(p); got != p {
			t.Errorf("safeRedirectPath(%q) = %q, want %q", p, got, p)
		}
	}
	for _, p := range bad {
		if got := safeRedirectPath(p); got != "" {
			t.Errorf("safeRedirectPath(%q) = %q, want \"\"", p, got)
		}
	}
}

func TestRenderList(t *testing.T) {
	var buf bytes.Buffer
	data := struct {
		Gists  []models.Gist
		UserID string
		Email  string
	}{Gists: []models.Gist{{ID: "abc", Description: "My Gist", UpdatedAt: time.Now()}}, UserID: "u", Email: "u@example.com"}
	if err := web.Render(&buf, "list", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `class="gist-row" href="/gist/abc"`) {
		t.Error("gist row is not a link")
	}

	buf.Reset()
	data.Gists = nil
	if err := web.Render(&buf, "list", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No gists yet") {
		t.Error("empty state missing")
	}
}

func TestRenderViewVersionBanner(t *testing.T) {
	versions := []models.Version{
		{ID: "abc", VersionID: "v1", LastModified: time.Now()},
		{ID: "abc", VersionID: "v2", LastModified: time.Now()},
	}

	data := viewData{
		Gist:       &models.Gist{ID: "abc", Description: "T"},
		Versions:   versions,
		UserID:     "u",
		Email:      "u@example.com",
		CurrentVid: "v2",
	}
	var buf bytes.Buffer
	if err := web.Render(&buf, "view", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Back to current version") {
		t.Error("back-to-current link missing when viewing a version")
	}
	if !strings.Contains(out, `<option value="/gist/abc"`) {
		t.Error("current-version option is not navigable")
	}
	if !strings.Contains(out, "selected") {
		t.Error("viewed version not marked selected")
	}
}

func TestGistFromFormNaming(t *testing.T) {
	v := url.Values{
		"file0_name": {""}, "file0_content": {"one"},
		// User reserves the plain fallback name; auto-names must dodge it.
		"file1_name": {"gistfile.txt"}, "file1_content": {"named"},
		"file2_name": {"  "}, "file2_content": {"two"},
		// Fully blank entry: dropped.
		"file3_name": {""}, "file3_content": {""},
		"file4_name": {""}, "file4_content": {"three"},
	}
	gist, err := gistFromForm(v)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gistfile.1.txt", "gistfile.txt", "gistfile.2.txt", "gistfile.3.txt"}
	if len(gist.Files) != len(want) {
		t.Fatalf("files = %d, want %d", len(gist.Files), len(want))
	}
	for i, name := range want {
		if gist.Files[i].Name != name {
			t.Errorf("file %d name = %q, want %q", i, gist.Files[i].Name, name)
		}
	}
}

func TestGistFromFormDuplicateName(t *testing.T) {
	v := url.Values{
		"file0_name": {"dup.txt"}, "file0_content": {"a"},
		"file1_name": {"dup.txt"}, "file1_content": {"b"},
	}
	if _, err := gistFromForm(v); !errors.Is(err, errDuplicateName) {
		t.Fatalf("err = %v, want errDuplicateName", err)
	}
}

func TestGistFromFormCRLF(t *testing.T) {
	// Browsers submit textarea values with CRLF; storage and the renderer
	// must see canonical LF.
	v := url.Values{
		"file0_name": {"a.md"}, "file0_content": {"---\r\nfoo: bar\r\n---\r\n\r\n# H\r\n"},
	}
	gist, err := gistFromForm(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gist.Files[0].Content, "\r") {
		t.Error("CRLF survived into stored content")
	}
	res := newFileRenderer().Render("a.md", gist.Files[0].Content)
	if body := string(res.Body); !strings.Contains(body, `<table class="frontmatter">`) ||
		strings.Contains(body, "<h2") {
		t.Errorf("front matter did not survive browser line endings: %s", body)
	}
}

func TestPreviewEndpoint(t *testing.T) {
	h := &Handler{files: newFileRenderer()}

	post := func(body url.Values) (int, string, bool) {
		req := httptest.NewRequest("POST", "/preview", strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
		rec := httptest.NewRecorder()
		h.Preview(rec, req)
		if rec.Code != http.StatusOK {
			return rec.Code, "", false
		}
		var res struct {
			HTML      string `json:"html"`
			NeedsMath bool   `json:"needsMath"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("bad json: %v (%s)", err, rec.Body)
		}
		return rec.Code, res.HTML, res.NeedsMath
	}

	code, html, needsMath := post(url.Values{"csrf_token": {"tok"}, "name": {"x.md"}, "content": {"# Hi"}})
	if code != 200 || !strings.Contains(html, `<h1 id="hi">Hi`) || needsMath {
		t.Errorf("markdown preview: %d %q math=%v", code, html, needsMath)
	}
	_, html, needsMath = post(url.Values{"csrf_token": {"tok"}, "name": {"y.md"}, "content": {"$x_i$"}})
	if !needsMath || !strings.Contains(html, `\(x_i\)`) {
		t.Errorf("math preview: %q math=%v", html, needsMath)
	}
	if code, _, _ := post(url.Values{"csrf_token": {"wrong"}, "name": {"x.md"}}); code != http.StatusForbidden {
		t.Errorf("csrf mismatch: got %d, want 403", code)
	}
}

func TestValidUploadKey(t *testing.T) {
	user := "alice@example.com"
	cases := map[string]bool{
		uploadKeyPrefix + user + "/" + generateID():   true,
		uploadKeyPrefix + "bob@x.com/" + generateID(): false,
		"gists/" + user + "/" + generateID():          false,
		uploadKeyPrefix + user + "/../../etc/passwd":  false,
		uploadKeyPrefix + user + "/deadbeef":          false,
		uploadKeyPrefix + user:                        false,
	}
	for key, want := range cases {
		if got := validUploadKey(key, user); got != want {
			t.Errorf("validUploadKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestGistFromFormKeyEntry(t *testing.T) {
	v := url.Values{
		"file0_name":    {"pic.png"},
		"file0_key":     {uploadKeyPrefix + "alice@example.com/k1"},
		"file0_content": {"ignored"},
		// Key with no name: kept (auto-named), unlike a fully blank entry.
		"file1_key": {uploadKeyPrefix + "alice@example.com/k2"},
	}
	gist, err := gistFromForm(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(gist.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(gist.Files))
	}
	if gist.Files[0].Content != "" {
		t.Errorf("key entry must drop textarea content, got %q", gist.Files[0].Content)
	}
	if gist.Files[1].Name != "gistfile.txt" || gist.Files[1].Key == "" {
		t.Errorf("unnamed key entry = %+v, want auto-name + key", gist.Files[1])
	}
}

func TestLoginDisabledWithoutClientSecret(t *testing.T) {
	h := &Handler{oidcCfg: config.OIDCConfig{Issuer: "http://issuer.example.com/dex", ClientID: "c"}}
	for name, fn := range map[string]http.HandlerFunc{"Login": h.Login, "Callback": h.Callback} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodGet, "/"+strings.ToLower(name), nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with empty client secret: code %d, want 503", name, w.Code)
		}
	}
}
