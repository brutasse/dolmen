package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"dolmen/app"
	"dolmen/config"
	"dolmen/middleware"
	"dolmen/s3"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	s3Client, err := s3.NewClient(ctx, cfg.S3)
	if err != nil {
		log.Fatal(err)
	}

	provider, verifier, err := middleware.NewOIDCProviderAndVerifier(ctx, cfg.OIDC)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.OIDC.ClientSecret == "" {
		log.Printf("oidc.client_secret unset: built-in login flow disabled, /login and /callback answer 503")
	}

	if err := s3Client.EnsureConfigured(ctx, cfg.BaseURL); err != nil {
		log.Fatal(err)
	}

	h := app.New(s3Client, provider, verifier, cfg)

	log.Printf("Server starting on %s (base URL %s)", cfg.Listen, cfg.BaseURL)
	// Explicit timeouts: http.ListenAndServe sets none, so stalled clients
	// would retain handler goroutines and connections indefinitely.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
