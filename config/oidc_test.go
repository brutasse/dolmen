package config

import (
	"strings"
	"testing"
)

func TestLoadPublicClientEnablesLogin(t *testing.T) {
	cfg, err := Load(write(t, `
oidc:
  issuer: https://idp.example.com
  client_id: dolmen
  public_client: true
s3:
  bucket: gists
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OIDC.LoginEnabled() {
		t.Error("public client did not enable login")
	}
	if cfg.OIDC.ClientSecret != "" {
		t.Errorf("public client has secret %q", cfg.OIDC.ClientSecret)
	}
}

func TestLoadRejectsPublicClientWithSecret(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "secret")
	_, err := Load(write(t, `
oidc:
  issuer: https://idp.example.com
  client_id: dolmen
  client_secret: TEST_CLIENT_SECRET
  public_client: true
s3:
  bucket: gists
`))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("public client with secret accepted: %v", err)
	}
}
