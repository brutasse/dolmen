package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

// errHelp says the command line asked for help; the text is already printed.
var errHelp = errors.New("help requested")

// command is what the command line asked dolmen to do.
type command struct {
	mode        string // "serve" when no command is given, or "scrub"
	cfgPath     string
	showVersion bool
}

func main() {
	cmd, err := parseArgs(os.Args[1:], os.Stdout, os.Stderr)
	if errors.Is(err, errHelp) {
		return
	}
	if err != nil {
		os.Exit(2)
	}

	if cmd.showVersion {
		fmt.Println(version)
		return
	}

	// SIGINT/SIGTERM trigger the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(cmd.cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	s3Client, err := s3.NewClient(ctx, cfg.S3)
	if err != nil {
		log.Fatal(err)
	}

	// Scrub runs one job and exits, no server.
	if cmd.mode == "scrub" {
		runScrub(ctx, s3Client, cfg)
		return
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

// parseArgs parses dolmen's command line. Flags are accepted before or after
// the command, so "-config x scrub" and "scrub -config x" are the same. Help
// goes to stdout, everything the command line got wrong to stderr; main turns
// a non-nil error into exit 2.
func parseArgs(args []string, stdout, stderr io.Writer) (command, error) {
	var cmd command
	fs := flag.NewFlagSet("dolmen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The flag package prints usage on -h and after an error; dolmen prints its
	// own help once it knows which stream and exit code apply.
	fs.Usage = func() {}
	cfgPath := fs.String("config", "config.yaml", "path to YAML config file")
	help := fs.Bool("help", false, "show this help and exit")
	showVersion := fs.Bool("version", false, "print version and exit")

	// report renders the outcome of a flag.Parse: help on stdout, or the usage
	// after the message the flag package already wrote to stderr.
	report := func(err error) error {
		switch {
		case err != nil && !errors.Is(err, flag.ErrHelp):
			writeUsage(stderr, fs)
			return err
		case *help || errors.Is(err, flag.ErrHelp):
			writeUsage(stdout, fs)
			return errHelp
		}
		return nil
	}
	// fail reports an error dolmen found itself, which the flag package has not
	// already printed.
	fail := func(format string, a ...any) error {
		err := fmt.Errorf(format, a...)
		fmt.Fprintf(stderr, "dolmen: %v\n", err)
		writeUsage(stderr, fs)
		return err
	}

	if err := report(fs.Parse(args)); err != nil {
		return cmd, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		cmd = command{mode: "serve", cfgPath: *cfgPath, showVersion: *showVersion}
		return cmd, nil
	}

	// The first non-flag argument is the command; everything after it is parsed
	// again so its flags may follow.
	cmd.mode = rest[0]
	if cmd.mode != "scrub" {
		return cmd, fail("unknown command %q (want \"scrub\", or no command to run the server)", cmd.mode)
	}
	if err := report(fs.Parse(rest[1:])); err != nil {
		return cmd, err
	}
	if fs.NArg() > 0 {
		return cmd, fail("unexpected argument %q", fs.Arg(0))
	}
	cmd.cfgPath, cmd.showVersion = *cfgPath, *showVersion
	return cmd, nil
}

// usageBody is the part of the help that flag.PrintDefaults cannot generate:
// what the commands do and how the binary gets invoked.
const usageBody = `Usage:
  dolmen [flags]          run the HTTP server
  dolmen [flags] scrub    erase gists deleted over deleted_retention ago and
                          sweep orphan uploads, then exit

Examples:
  dolmen -config /etc/dolmen/config.yaml
  dolmen -config /etc/dolmen/config.yaml scrub

Flags:
`

// writeUsage prints dolmen's help. flag.PrintDefaults writes to the flag
// set's output, so the set points at w while the flags are rendered.
func writeUsage(w io.Writer, fs *flag.FlagSet) {
	fs.SetOutput(w)
	defer fs.SetOutput(io.Discard)
	fmt.Fprintf(w, "dolmen %s stores gists in S3 and serves them over HTTP.\n\n", version)
	fmt.Fprint(w, usageBody)
	fs.PrintDefaults()
}

// runScrub purges expired deleted gists and orphan uploads once, then exits
// non-zero if anything still needs doing (a re-run finishes it). Meant to be
// run daily by cron; it does not start the server.
func runScrub(ctx context.Context, s3Client *s3.Client, cfg *config.Config) {
	retention, err := cfg.RetentionDuration()
	if err != nil {
		log.Fatal(err)
	}
	stats, err := s3Client.Scrub(ctx, time.Now().UTC(), retention)
	if err != nil {
		log.Printf("scrub: %v", err)
	}
	log.Printf("scrub: purged %d gists, %d orphan uploads", stats.GistsPurged, stats.UploadsPurged)
	if err != nil {
		os.Exit(1)
	}
}
