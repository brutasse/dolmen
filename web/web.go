// Package web embeds the UI resources (templates, stylesheet, fonts,
// vendored JS) so the deployed binary is self-contained: no CDN, no files
// on disk.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"dolmen/models"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/*.css assets/*.js assets/*.svg assets/vendor/*.js assets/fonts/*.woff2
var assetFS embed.FS

// Content-based asset addressing: every asset is served under two keys —
// its content-hashed name (immutable forever; stale hashes 404 once the
// bytes change, so pages always reference current content) and its plain
// name (revalidating, for hand-typed URLs and pages cached before a
// deploy).
const assetHashLen = 10

type assetEntry struct {
	body      []byte
	immutable bool
}

var (
	served    = map[string]assetEntry{}
	assetURLs = map[string]string{}
)

var cssURLRE = regexp.MustCompile(`url\("/static/([^"]+)"\)`)

func init() { buildAssets() }

func buildAssets() {
	bodies := map[string][]byte{}
	err := fs.WalkDir(assetFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assetFS.ReadFile(p)
		if err != nil {
			return err
		}
		bodies[strings.TrimPrefix(p, "assets/")] = b
		return nil
	})
	if err != nil {
		panic("web: " + err.Error())
	}
	hash := func(b []byte) string {
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])[:assetHashLen]
	}
	hashes := make(map[string]string, len(bodies))
	for name, b := range bodies {
		hashes[name] = hash(b)
	}
	// Rewrite url() references inside stylesheets (the fonts) to hashed
	// names, then recompute the stylesheet hashes over the rewritten
	// bytes. Fonts reference nothing, so this single pass is stable.
	for name, b := range bodies {
		if !strings.HasSuffix(name, ".css") {
			continue
		}
		rw := cssURLRE.ReplaceAllFunc(b, func(m []byte) []byte {
			target := string(cssURLRE.FindSubmatch(m)[1])
			return []byte(`url("/static/` + hashedName(target, hashes[target]) + `")`)
		})
		bodies[name] = rw
		hashes[name] = hash(rw)
	}
	for name, b := range bodies {
		hn := hashedName(name, hashes[name])
		served[hn] = assetEntry{body: b, immutable: true}
		served[name] = assetEntry{body: b}
		assetURLs[name] = "/static/" + hn
	}
}

// hashedName inserts the hash before the extension: app.css -> app.<h>.css.
// An empty hash (unknown reference) leaves the name untouched.
func hashedName(name, h string) string {
	if h == "" {
		return name
	}
	i := strings.LastIndexByte(name, '.')
	return name[:i] + "." + h + name[i:]
}

// assetFunc is the "asset" template function: the content-addressed URL,
// falling back to the plain (revalidating) path for unknown names.
func assetFunc(name string) template.URL {
	if u, ok := assetURLs[name]; ok {
		return template.URL(u)
	}
	return template.URL("/static/" + name)
}

// pages are parsed per page: layout + partials + one page file, so each
// page can own its {{define "content"}} without name collisions.
var pages = parsePages()

func parsePages() map[string]*template.Template {
	funcs := template.FuncMap{
		"ismd":      models.IsMarkdown,
		"humansize": models.HumanSize,
		"dict": func(args ...any) (map[string]any, error) {
			if len(args)%2 != 0 {
				return nil, fmt.Errorf("dict: want even args, got %d", len(args))
			}
			m := make(map[string]any, len(args)/2)
			for i := 0; i < len(args); i += 2 {
				k, ok := args[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is not a string", i)
				}
				m[k] = args[i+1]
			}
			return m, nil
		},
		"asset": assetFunc,
		"initial": func(s string) string {
			for _, r := range s {
				return strings.ToUpper(string(r))
			}
			return ""
		},
		"localpart": func(s string) string {
			if i := strings.IndexByte(s, '@'); i >= 0 {
				return s[:i]
			}
			return s
		},
	}
	shared := []string{"templates/layout.html", "templates/partials.html"}
	out := map[string]*template.Template{}
	for _, page := range []string{"list", "view", "edit"} {
		files := append(append([]string{}, shared...), "templates/"+page+".html")
		out[page] = template.Must(template.New(page).Funcs(funcs).ParseFS(templateFS, files...))
	}
	return out
}

// Render executes the named page ("list", "view", "edit") inside the base
// layout.
func Render(w io.Writer, page string, data any) error {
	t, ok := pages[page]
	if !ok {
		return fmt.Errorf("web: unknown page %q", page)
	}
	return t.ExecuteTemplate(w, "base", data)
}

// RenderHTTP is Render for handlers: it sets the content type and logs
// mid-stream failures (the status line is already out, so nothing better
// can be done).
func RenderHTTP(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Render(w, page, data); err != nil {
		log.Printf("web: render %s: %v", page, err)
	}
}

var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
}

// StaticHandler serves embedded assets under /static/ by exact lookup:
// hashed names are cache-immutable, plain names revalidate, anything
// unknown (including hashes from previous builds) is a 404.
func StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		e, ok := served[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if e.immutable {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if ct := contentTypes[path.Ext(name)]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(e.body))
	})
}
