//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	dolmens3 "dolmen/s3"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestEnsureConfigured boots the s3 client against s3mock and provisions
// the bucket. s3mock has no CORS API, so EnsureConfigured must survive that
// leg with only a warning; versioning must end up enabled and the whole
// call must be idempotent.
func TestEnsureConfigured(t *testing.T) {
	requireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := dolmens3.NewClient(ctx, appCfg.S3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := cl.EnsureConfigured(ctx, "http://localhost:8080"); err != nil {
			t.Fatalf("EnsureConfigured call %d: %v", i+1, err)
		}
	}
	v, err := awsC.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(bucketName)})
	if err != nil || v.Status != s3types.BucketVersioningStatusEnabled {
		t.Errorf("versioning after EnsureConfigured: status=%q err=%v", v.Status, err)
	}
}
