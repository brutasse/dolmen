package s3

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dolmen/config"
)

func TestCredentialFileProviderReloadsRotatedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"Version":1,"AccessKeyId":"first","SecretAccessKey":"secret-1"}`)

	client, err := NewClient(context.Background(), config.S3Config{
		Bucket: "gists", CacheSize: "0", CredentialsFile: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := client.s3Client.Options().Credentials
	first, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	write(`{"Version":1,"AccessKeyId":"second","SecretAccessKey":"secret-2"}`)
	second, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.AccessKeyID != "first" || second.AccessKeyID != "second" {
		t.Fatalf("credentials were not reloaded: first=%q second=%q", first.AccessKeyID, second.AccessKeyID)
	}
}

func TestCredentialFileProviderRejectsMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"Version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (credentialFileProvider{path: path}).Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AccessKeyId and SecretAccessKey") {
		t.Fatalf("missing keys accepted: %v", err)
	}
}
