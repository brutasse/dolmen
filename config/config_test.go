package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
