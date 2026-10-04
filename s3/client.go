package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	dolmencfg "dolmen/config"
	"dolmen/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// listObjectsAPI is the single S3 call ListGists makes; unit tests inject a
// multi-page fake. *s3.Client satisfies it.
type listObjectsAPI interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Client wraps the S3 client
type Client struct {
	s3Client *s3.Client
	list     listObjectsAPI
	bucket   string
}

var (
	// ErrNotFound is returned when a gist (or a version of it) does not exist.
	ErrNotFound = errors.New("gist not found")
	// ErrForbidden is returned when the acting user is not the gist's owner.
	ErrForbidden = errors.New("not the gist owner")
)

// NewClient creates a new S3 client. Credentials come from the standard
// AWS SDK chain, not from sc.
func NewClient(ctx context.Context, sc dolmencfg.S3Config) (*Client, error) {
	if sc.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		// S3 clones (s3mock and others) don't implement the SDK's default
		// rolling-checksum trailers; only compute/validate when required.
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		return nil, err
	}

	if sc.Endpoint != "" {
		cfg.EndpointResolverWithOptions = aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{URL: sc.Endpoint}, nil
		})
	}

	// Path style is required when pointing at endpoint-based S3 clones.
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
	return &Client{s3Client: client, list: client, bucket: sc.Bucket}, nil
}

// EnsureConfigured adapts the bucket to what the app needs: it enables
// object versioning (gist history *is* S3 object versions) and ensures a
// bucket CORS rule that lets the browser PUT direct uploads from the app's
// origin. It is idempotent and runs on boot. Versioning failures are fatal;
// for CORS, a backend that does not implement the CORS API at all (some S3
// clones) only warns, while real errors like AccessDenied stay fatal.
func (c *Client) EnsureConfigured(ctx context.Context, baseURL string) error {
	origin, err := originOf(baseURL)
	if err != nil {
		return err
	}
	if err := c.ensureVersioning(ctx); err != nil {
		return err
	}
	return c.ensureCORS(ctx, origin)
}

func originOf(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url %q is not an absolute URL", baseURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

func (c *Client) ensureVersioning(ctx context.Context) error {
	v, err := c.s3Client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &c.bucket})
	if err != nil {
		return fmt.Errorf("get bucket versioning: %w", err)
	}
	if v.Status == s3types.BucketVersioningStatusEnabled {
		return nil
	}
	if _, err := c.s3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  &c.bucket,
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	}); err != nil {
		return fmt.Errorf("enable bucket versioning: %w", err)
	}
	log.Printf("s3: enabled object versioning on bucket %s", c.bucket)
	return nil
}

// corsRule is what direct browser uploads need: PUT from the app origin
// with the server-pinned Content-Type header.
func corsRule(origin string) s3types.CORSRule {
	return s3types.CORSRule{
		AllowedHeaders: []string{"Content-Type"},
		AllowedMethods: []string{"PUT"},
		AllowedOrigins: []string{origin},
		MaxAgeSeconds:  aws.Int32(300),
	}
}

// corsNeedsRule reports whether rules already cover origin for our PUT.
func corsNeedsRule(rules []s3types.CORSRule, origin string) bool {
	for _, r := range rules {
		ok := (containsFold(r.AllowedOrigins, "*") || containsFold(r.AllowedOrigins, origin)) &&
			containsFold(r.AllowedMethods, "PUT") &&
			(containsFold(r.AllowedHeaders, "*") || containsFold(r.AllowedHeaders, "content-type"))
		if ok {
			return false
		}
	}
	return true
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// isUnsupported reports an error signature of a backend that does not
// implement the API rather than a real problem. BucketAlreadyOwnedByYou is
// how s3mock answers PUT /?cors: it misroutes it to CreateBucket.
func isUnsupported(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NotImplemented", "MethodNotAllowed", "BucketAlreadyOwnedByYou":
			return true
		}
	}
	return false
}

func isNoSuch(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

func (c *Client) ensureCORS(ctx context.Context, origin string) error {
	var rules []s3types.CORSRule
	cur, err := c.s3Client.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: &c.bucket})
	switch {
	case err == nil:
		rules = cur.CORSRules
	case isNoSuch(err, "NoSuchCORSConfiguration"): // configured bucket, no rules yet
	case isUnsupported(err):
		log.Printf("s3: this backend does not support the CORS API; for attachments, allow PUT from %s with the Content-Type header on bucket %s yourself", origin, c.bucket)
		return nil
	default:
		return fmt.Errorf("get bucket cors: %w", err)
	}
	if !corsNeedsRule(rules, origin) {
		return nil
	}
	rules = append(rules, corsRule(origin)) // append: keep rules others manage
	if _, err := c.s3Client.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket:            &c.bucket,
		CORSConfiguration: &s3types.CORSConfiguration{CORSRules: rules},
	}); err != nil {
		if isUnsupported(err) {
			log.Printf("s3: PutBucketCors unsupported by this backend (%v); for attachments, allow PUT from %s with the Content-Type header on bucket %s yourself", err, origin, c.bucket)
			return nil
		}
		return fmt.Errorf("put bucket cors: %w", err)
	}
	log.Printf("s3: added CORS rule for PUT from %s on bucket %s", origin, c.bucket)
	return nil
}

func gistKey(user, id string) string { return fmt.Sprintf("gists/%s/%s.json", user, id) }

// PresignPut mints a presigned PUT URL for direct browser uploads. The
// contentType expresses the type we want stored, but the SDK's presigner
// signs only the host header — it does not sign Content-Type — so the pin
// is enforced at save time by handlers.resolveBlobs, not by the signature.
func (c *Client) PresignPut(ctx context.Context, key, contentType string, expiry time.Duration) (string, error) {
	req, err := s3.NewPresignClient(c.s3Client).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		ContentType: &contentType,
	}, func(o *s3.PresignOptions) { o.Expires = expiry })
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// PresignGet mints a presigned GET URL, used to hand browsers a direct S3
// download for stored blobs. A non-empty contentDisposition is attached to
// the signature (e.g. forcing a download filename for octet-stream blobs).
func (c *Client) PresignGet(ctx context.Context, key, contentDisposition string, expiry time.Duration) (string, error) {
	input := &s3.GetObjectInput{Bucket: &c.bucket, Key: &key}
	if contentDisposition != "" {
		input.ResponseContentDisposition = &contentDisposition
	}
	req, err := s3.NewPresignClient(c.s3Client).PresignGetObject(ctx, input,
		func(o *s3.PresignOptions) { o.Expires = expiry })
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// HeadObject reports an object's size and stored content type; used to
// verify a browser upload actually landed before referencing it in a gist.
func (c *Client) HeadObject(ctx context.Context, key string) (int64, string, error) {
	out, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		if isNotFound(err) {
			return 0, "", ErrNotFound
		}
		return 0, "", err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	var mime string
	if out.ContentType != nil {
		mime = *out.ContentType
	}
	return size, mime, nil
}

// indexKey holds a pointer object whose content is the gist's owner user ID,
// so any authenticated user can resolve id -> owner in one read.
func indexKey(id string) string { return "index/" + id }

func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() == 404 {
		return true
	}
	return false
}

func (c *Client) getRaw(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) putRaw(ctx context.Context, key, body string) error {
	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
		Body:   strings.NewReader(body),
	})
	return err
}

// resolveOwner returns the owner of a gist, resolved solely via the index
// pointer object written by CreateGist. There is deliberately no fallback
// prefix scan: scanning every gists/ user prefix per unresolved id lets one
// authenticated request fan out into ~1000 S3 reads (repeatable), and no
// pre-index gists exist to recover.
func (c *Client) resolveOwner(ctx context.Context, id string) (string, error) {
	owner, err := c.getRaw(ctx, indexKey(id))
	if err == nil {
		return strings.TrimSpace(string(owner)), nil
	}
	if isNotFound(err) {
		return "", ErrNotFound
	}
	return "", err
}

// ListGists lists gists owned by userID
// listKeys pages through ListObjectsV2 (continuation token) and returns
// every key under prefix. One ListObjectsV2 response is capped at 1000 keys.
func (c *Client) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	var token *string
	for {
		resp, err := c.list.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            &c.bucket,
			Prefix:            &prefix,
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range resp.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			return keys, nil
		}
		token = resp.NextContinuationToken
		if token == nil {
			return keys, nil // defensive: truncated without a token
		}
	}
}

func (c *Client) ListGists(ctx context.Context, userID string) ([]models.Gist, error) {
	prefix := fmt.Sprintf("gists/%s/", userID)
	keys, err := c.listKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// N+1 by design: gist JSON is the source of truth (title, files), so
	// listing fetches each object. Fine for personal scale.
	var gists []models.Gist
	for _, key := range keys {
		gist, err := c.getGistByKey(ctx, key)
		if err != nil {
			continue // skip unreadable objects
		}
		gist.UserID = userID
		gists = append(gists, *gist)
	}
	sort.Slice(gists, func(i, j int) bool { return gists[i].UpdatedAt.After(gists[j].UpdatedAt) })
	return gists, nil
}

func (c *Client) getGistByKey(ctx context.Context, key string) (*models.Gist, error) {
	data, err := c.getRaw(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var gist models.Gist
	if err := gist.FromJSON(data); err != nil {
		return nil, err
	}
	return &gist, nil
}

// GetGist loads a gist by ID, whatever user owns it. Gist.UserID is set to
// the owner.
func (c *Client) GetGist(ctx context.Context, id string) (*models.Gist, error) {
	owner, err := c.resolveOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	gist, err := c.getGistByKey(ctx, gistKey(owner, id))
	if err != nil {
		return nil, err
	}
	gist.UserID = owner
	return gist, nil
}

func (c *Client) putGist(ctx context.Context, owner string, gist *models.Gist) error {
	gist.UserID = owner // ensure
	data, err := gist.ToJSON()
	if err != nil {
		return err
	}

	// Size check
	if len(data) > 16*1024*1024 {
		return errors.New("gist size exceeds 16 MiB")
	}

	key := gistKey(owner, gist.ID)
	_, err = c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

// CreateGist stores a new gist and its owner pointer. If the pointer write
// fails, the gist object is deleted so creation is all-or-nothing.
func (c *Client) CreateGist(ctx context.Context, userID string, gist *models.Gist) error {
	if err := c.putGist(ctx, userID, gist); err != nil {
		return err
	}
	if err := c.putRaw(ctx, indexKey(gist.ID), userID); err != nil {
		_, _ = c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &c.bucket,
			Key:    aws.String(gistKey(userID, gist.ID)),
		})
		return err
	}
	return nil
}

// UpdateGist overwrites an existing gist; only the owner may do so.
func (c *Client) UpdateGist(ctx context.Context, userID string, gist *models.Gist) error {
	owner, err := c.resolveOwner(ctx, gist.ID)
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrForbidden
	}
	return c.putGist(ctx, owner, gist)
}

// DeleteGist tombstones the owner pointer first (so views 404 immediately,
// versions hidden) then the gist object; only the owner may delete.
func (c *Client) DeleteGist(ctx context.Context, userID, id string) error {
	owner, err := c.resolveOwner(ctx, id)
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrForbidden
	}
	_, _ = c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &c.bucket,
		Key:    aws.String(indexKey(id)),
	})
	_, err = c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &c.bucket,
		Key:    aws.String(gistKey(owner, id)),
	})
	return err
}

// ListVersions lists versions of a gist by ID
func (c *Client) ListVersions(ctx context.Context, id string) ([]models.Version, error) {
	owner, err := c.resolveOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	key := gistKey(owner, id)
	resp, err := c.s3Client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: &c.bucket,
		Prefix: &key,
	})
	if err != nil {
		return nil, err
	}

	var versions []models.Version
	for _, v := range resp.Versions {
		if v.Key == nil || v.VersionId == nil {
			continue
		}
		version := models.Version{
			ID:           id,
			VersionID:    *v.VersionId,
			LastModified: *v.LastModified,
			Size:         *v.Size,
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// GetVersion gets a specific version of a gist by ID
func (c *Client) GetVersion(ctx context.Context, id, versionID string) (*models.Gist, error) {
	owner, err := c.resolveOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	key := gistKey(owner, id)
	resp, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:    &c.bucket,
		Key:       &key,
		VersionId: &versionID,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var gist models.Gist
	if err := gist.FromJSON(data); err != nil {
		return nil, err
	}
	gist.UserID = owner
	return &gist, nil
}
