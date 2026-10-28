package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// credentialFileProvider reloads credential_process JSON before each S3
// request so rotated credentials take effect without restarting Dolmen.
type credentialFileProvider struct {
	path string
}

func (p credentialFileProvider) Retrieve(context.Context) (aws.Credentials, error) {
	b, err := os.ReadFile(p.path)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("read S3 credentials file: %w", err)
	}
	var v struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return aws.Credentials{}, fmt.Errorf("parse S3 credentials file: %w", err)
	}
	if v.AccessKeyID == "" || v.SecretAccessKey == "" {
		return aws.Credentials{}, fmt.Errorf("s3 credentials file must contain AccessKeyId and SecretAccessKey")
	}
	return aws.Credentials{
		AccessKeyID: v.AccessKeyID, SecretAccessKey: v.SecretAccessKey,
		SessionToken: v.SessionToken, Source: "dolmen credentials file",
	}, nil
}
