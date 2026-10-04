// Package app wires the HTTP router and its dependencies.
package app

import (
	"net/http"

	"dolmen/config"
	"dolmen/handlers"
	"dolmen/middleware"
	"dolmen/s3"
	"dolmen/web"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// New builds the application's HTTP handler. cfg supplies the externally
// reachable origin (used to build OAuth redirect URIs) and the optional
// name of a proxy cookie holding a signed ID token. Templates and static
// assets come from the embedded web package, so the binary runs from
// anywhere with no CDN.
func New(s3Client *s3.Client, provider *oidc.Provider, verifier *oidc.IDTokenVerifier, cfg *config.Config) http.Handler {
	h := handlers.NewHandler(s3Client, provider, cfg)

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(middleware.OIDCMiddleware(verifier, cfg.AuthCookieName))
	r.Use(middleware.CSRF(middleware.CookieSecure(cfg.BaseURL)))

	r.Handle("/static/*", web.StaticHandler())
	r.Get("/", h.CreateGist)
	r.Get("/gists", h.ListGists)
	r.Get("/raw/{id}", h.RawGist)
	r.Get("/raw/{id}/*", h.RawGist)
	r.Get("/gist/{id}", h.ViewGist)
	r.Get("/gist/{id}/versions", h.ListVersions)
	r.Get("/gist/{id}/version/{vid}", h.ViewVersion)
	r.Get("/edit/{id}", h.EditGist)
	r.Post("/edit/{id}", h.UpdateGist)
	r.Post("/create", h.StoreGist)
	r.Post("/upload", h.Upload)
	r.Post("/preview", h.Preview)
	r.Post("/delete/{id}", h.DeleteGist)
	r.Get("/login", h.Login)
	r.Get("/callback", h.Callback)

	return r
}
