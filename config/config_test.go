package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadResolvesSecretsAndDefaults(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "s3cr3t")
	cfg, err := Load(write(t, `
oidc:
  issuer: http://localhost:5556/dex
  client_id: gist-client
  client_secret: TEST_CLIENT_SECRET
s3:
  bucket: gists
  endpoint: http://localhost:9090
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.ClientSecret != "s3cr3t" {
		t.Errorf("client secret not resolved from env: %q", cfg.OIDC.ClientSecret)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen default: %q", cfg.Listen)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("base_url default: %q", cfg.BaseURL)
	}
}

func TestLoadBaseURLFromExplicitListen(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "x")
	cfg, err := Load(write(t, `
listen: "127.0.0.1:9999"
oidc:
  issuer: http://localhost:5556/dex
  client_id: c
  client_secret: TEST_CLIENT_SECRET
s3:
  bucket: b
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:9999" {
		t.Errorf("base_url derived from listen: %q", cfg.BaseURL)
	}
}

func TestLoadWithoutClientSecretDisablesLogin(t *testing.T) {
	cfg, err := Load(write(t, `
oidc:
  issuer: http://localhost:5556/dex
  client_id: gist-client
s3:
  bucket: gists
`))
	if err != nil {
		t.Fatalf("config without client_secret rejected: %v", err)
	}
	if cfg.OIDC.ClientSecret != "" {
		t.Errorf("client secret = %q, want empty", cfg.OIDC.ClientSecret)
	}
}

func TestLoadReportsAllMissing(t *testing.T) {
	err := func() error {
		_, err := Load(write(t, "oidc:\n  client_secret: TEST_CLIENT_SECRET_NOT_SET\n"))
		return err
	}()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"oidc.issuer is required",
		"oidc.client_id is required",
		`environment variable "TEST_CLIENT_SECRET_NOT_SET" is not set`,
		"s3.bucket is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestLoadRejectsBadIssuer(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "x")
	_, err := Load(write(t, `
oidc:
  issuer: not-a-url
  client_id: c
  client_secret: TEST_CLIENT_SECRET
s3:
  bucket: b
`))
	if err == nil || !strings.Contains(err.Error(), "oidc.issuer must be an http(s) URL") {
		t.Fatalf("bad issuer accepted: %v", err)
	}
}

func TestLoadRejectsEmptySecretEnv(t *testing.T) {
	t.Setenv("TEST_EMPTY_SECRET", "")
	_, err := Load(write(t, `
oidc:
  issuer: http://localhost:5556/dex
  client_id: c
  client_secret: TEST_EMPTY_SECRET
s3:
  bucket: b
`))
	if err == nil || !strings.Contains(err.Error(), "is not set or empty") {
		t.Fatalf("set-but-empty secret accepted: %v", err)
	}
}

func TestCacheMaxBytesForms(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"", 128_000_000}, // DefaultCacheSize
		{"0", 0},          // caching off
		{"4096", 4096},    // bare number is bytes
		{"128MB", 128_000_000},
		{"512MiB", 512 * 1024 * 1024},
		{" 1 GiB ", 1 << 30},
		{"20mib", 20 * 1024 * 1024}, // unit case does not matter
	} {
		got, err := S3Config{CacheSize: tc.in}.CacheMaxBytes()
		if err != nil {
			t.Errorf("CacheMaxBytes(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CacheMaxBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestCacheMaxBytesRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"-1MB", "MB", "1e6", "12TBx", "1.5MB",
		"99999999999999999999",  // does not fit int64
		"9223372036854775807TB", // fits until the unit is applied
	} {
		if _, err := (S3Config{CacheSize: in}).CacheMaxBytes(); err == nil {
			t.Errorf("CacheMaxBytes(%q) accepted, want error", in)
		}
	}
}

func TestLoadValidatesCacheSize(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "x")
	const head = `
oidc:
  issuer: http://localhost:5556/dex
  client_id: c
  client_secret: TEST_CLIENT_SECRET
s3:
  bucket: b
`
	cfg, err := Load(write(t, head+"  cache_size: 256MiB\n"))
	if err != nil {
		t.Fatalf("valid cache_size rejected: %v", err)
	}
	if got, err := cfg.S3.CacheMaxBytes(); err != nil || got != 256*1024*1024 {
		t.Errorf("CacheMaxBytes = %d, %v, want 268435456", got, err)
	}

	if _, err := Load(write(t, head+"  cache_size: twelve\n")); err == nil ||
		!strings.Contains(err.Error(), "s3.cache_size") {
		t.Errorf("invalid cache_size accepted or misreported: %v", err)
	}
}

func TestRetentionDurationForms(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", 7 * 24 * time.Hour}, // DefaultDeletedRetention
		{"0", 0},                 // scrub purges immediately (e2e uses this)
		{"36h", 36 * time.Hour},
	} {
		got, err := Config{DeletedRetention: tc.in}.RetentionDuration()
		if err != nil {
			t.Errorf("RetentionDuration(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("RetentionDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"7 days", "-1h", "seven"} {
		if _, err := (Config{DeletedRetention: in}).RetentionDuration(); err == nil {
			t.Errorf("RetentionDuration(%q) accepted, want error", in)
		}
	}
}

func TestLoadValidatesDeletedRetention(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "x")
	const head = `
oidc:
  issuer: http://localhost:5556/dex
  client_id: c
  client_secret: TEST_CLIENT_SECRET
s3:
  bucket: b
`
	cfg, err := Load(write(t, head+"deleted_retention: 48h\n"))
	if err != nil {
		t.Fatalf("valid deleted_retention rejected: %v", err)
	}
	if got, err := cfg.RetentionDuration(); err != nil || got != 48*time.Hour {
		t.Errorf("RetentionDuration = %v, %v, want 48h", got, err)
	}

	if _, err := Load(write(t, head+"deleted_retention: last-week\n")); err == nil ||
		!strings.Contains(err.Error(), "deleted_retention") {
		t.Errorf("invalid deleted_retention accepted or misreported: %v", err)
	}
}
