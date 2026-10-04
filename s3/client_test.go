package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dolmen/models"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeLister serves canned ListObjectsV2 pages and records the tokens it
// was asked for.
type fakeLister struct {
	pages   []*s3.ListObjectsV2Output
	calls   []*s3.ListObjectsV2Input
	callIdx int
}

func (f *fakeLister) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.calls = append(f.calls, in)
	page := f.pages[f.callIdx]
	f.callIdx++
	return page, nil
}

func page(keys []string, truncated bool, next string) *s3.ListObjectsV2Output {
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(truncated)}
	for _, k := range keys {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	if next != "" {
		out.NextContinuationToken = aws.String(next)
	}
	return out
}

func TestListKeysPaginates(t *testing.T) {
	fake := &fakeLister{pages: []*s3.ListObjectsV2Output{
		page([]string{"gists/u/a.json", "gists/u/b.json"}, true, "tok1"),
		page([]string{"gists/u/c.json"}, true, "tok2"),
		page([]string{"gists/u/d.json"}, false, ""),
	}}
	c := &Client{list: fake, bucket: "b"}

	keys, err := c.listKeys(context.Background(), "gists/u/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gists/u/a.json", "gists/u/b.json", "gists/u/c.json", "gists/u/d.json"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
	// Follow-up requests must carry the returned continuation tokens.
	if len(fake.calls) != 3 {
		t.Fatalf("expected 3 list calls, got %d", len(fake.calls))
	}
	if fake.calls[0].ContinuationToken != nil {
		t.Error("first call should have no token")
	}
	if aws.ToString(fake.calls[1].ContinuationToken) != "tok1" || aws.ToString(fake.calls[2].ContinuationToken) != "tok2" {
		t.Error("continuation tokens not threaded through")
	}
}

func TestListKeysSinglePage(t *testing.T) {
	fake := &fakeLister{pages: []*s3.ListObjectsV2Output{
		page([]string{"gists/u/only.json"}, false, ""),
	}}
	c := &Client{list: fake, bucket: "b"}
	keys, err := c.listKeys(context.Background(), "gists/u/")
	if err != nil || len(keys) != 1 || keys[0] != "gists/u/only.json" {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("expected 1 list call, got %d", len(fake.calls))
	}
}

func TestOriginOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:8080", "http://localhost:8080"},
		{"https://gist.example.com", "https://gist.example.com"},
		{"https://gist.example.com/prefix", "https://gist.example.com"}, // path stripped
		{"https://gist.example.com/", "https://gist.example.com"},
	} {
		got, err := originOf(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("originOf(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "not a url", "localhost:8080"} {
		if _, err := originOf(bad); err == nil {
			t.Errorf("originOf(%q) should fail", bad)
		}
	}
}

func TestCorsNeedsRule(t *testing.T) {
	ours := []s3types.CORSRule{corsRule("https://gist.example.com")}
	if corsNeedsRule(ours, "https://gist.example.com") {
		t.Error("our own rule must count as covering the origin")
	}
	if !corsNeedsRule(nil, "https://gist.example.com") {
		t.Error("empty rules need one")
	}
	if !corsNeedsRule(ours, "https://other.example.com") {
		t.Error("another origin is not covered")
	}
	wide := []s3types.CORSRule{{
		AllowedOrigins: []string{"*"}, AllowedMethods: []string{"put"}, AllowedHeaders: []string{"*"},
	}}
	if corsNeedsRule(wide, "https://gist.example.com") {
		t.Error("wildcard rule with PUT covers us (method case-insensitive)")
	}
	getOnly := []s3types.CORSRule{{
		AllowedOrigins: []string{"https://gist.example.com"}, AllowedMethods: []string{"GET"}, AllowedHeaders: []string{"*"},
	}}
	if !corsNeedsRule(getOnly, "https://gist.example.com") {
		t.Error("GET-only rule does not cover our PUT")
	}
	noHeaders := []s3types.CORSRule{{
		AllowedOrigins: []string{"https://gist.example.com"}, AllowedMethods: []string{"PUT"},
	}}
	if !corsNeedsRule(noHeaders, "https://gist.example.com") {
		t.Error("rule without Content-Type allowance does not cover us")
	}
}

// countingStore is an httptest loopback S3 stand-in that counts requests by
// operation and serves canned index/ and gists/ objects, so tests can pin
// the per-call S3 request fan-out of read paths through the real SDK client.
type countingStore struct {
	mu      sync.Mutex
	counts  map[string]int    // "get-index" | "get-gist" | "list" | "put" | "other"
	objects map[string]string // key -> body
}

func (s *countingStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/b/")
	op := "other"
	switch {
	case r.URL.Query().Has("list-type"):
		op = "list"
	case r.Method == http.MethodPut:
		op = "put"
	case strings.HasPrefix(key, "index/"):
		op = "get-index"
	case strings.HasPrefix(key, "gists/"):
		op = "get-gist"
	}
	s.counts[op]++
	if op == "list" {
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
		return
	}
	body, ok := s.objects[key]
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.WriteString(w, body)
}

func newCountingClient(t *testing.T, objects map[string]string) (*Client, *countingStore) {
	t.Helper()
	store := &countingStore{counts: map[string]int{}, objects: objects}
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)
	c := &Client{
		bucket: "b",
		s3Client: s3.New(s3.Options{
			Region:       "us-east-1",
			Credentials:  aws.AnonymousCredentials{},
			BaseEndpoint: aws.String(srv.URL),
			UsePathStyle: true,
		}),
	}
	c.list = c.s3Client
	return c, store
}

// TestGetGistUnindexedIDNoScanFallback pins the security regression for the
// removed pre-index fallback scan: every miss of an un-indexed id must cost
// exactly one index GET — no ListObjectsV2, no per-prefix GetObject storm,
// regardless of how often the same id is requested.
func TestGetGistUnindexedIDNoScanFallback(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{})
	for i := 0; i < 3; i++ {
		if _, err := c.GetGist(context.Background(), "no-such-id"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetGist: err = %v, want ErrNotFound", err)
		}
	}
	want := map[string]int{"get-index": 3}
	if store.counts["get-index"] != want["get-index"] || store.counts["list"] != 0 ||
		store.counts["get-gist"] != 0 || store.counts["put"] != 0 || store.counts["other"] != 0 {
		t.Fatalf("counts = %v, want exactly one index GET per miss and nothing else", store.counts)
	}
}

// TestGetGistIndexedCostsTwoRequests pins that the normal resolve path is
// one index GET plus one gist GET, with zero list requests.
func TestGetGistIndexedCostsTwoRequests(t *testing.T) {
	g := models.Gist{ID: "abc", Description: "hello"}
	data, err := g.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": string(data),
	})
	got, err := c.GetGist(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
	if store.counts["get-index"] != 1 || store.counts["get-gist"] != 1 ||
		store.counts["list"] != 0 || store.counts["put"] != 0 || store.counts["other"] != 0 {
		t.Fatalf("counts = %v, want 1 index GET + 1 gist GET", store.counts)
	}
}
