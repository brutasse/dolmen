package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dolmen/app"
	"dolmen/config"
	"dolmen/middleware"
	"dolmen/s3"
)

// version is stamped at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.SetFlags(0)
		log.Println(version)
		return
	}

	// SIGINT/SIGTERM trigger the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	s3Client, err := s3.NewClient(ctx, cfg.S3)
	if err != nil {
		log.Fatal(err)
	}

	provider, verifier, err := middleware.NewOIDCProviderAndVerifier(ctx, cfg.OIDC)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.OIDC.ClientSecret == "" {
		log.Printf("oidc.client_secret unset: built-in login flow disabled, /login and /callback answer 503, unauthenticated requests 401")
	}

	if err := s3Client.EnsureConfigured(ctx, cfg.BaseURL); err != nil {
		log.Fatal(err)
	}

	h := app.New(s3Client, provider, verifier, cfg)

	log.Printf("Server starting on %s (base URL %s)", cfg.Listen, cfg.BaseURL)
	// Explicit timeouts: the zero http.Server sets none, so stalled clients
	// would retain handler goroutines and connections indefinitely.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatal(err)
	case <-ctx.Done():
	}

	log.Print("shutdown signal received, draining (10s grace)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
}
