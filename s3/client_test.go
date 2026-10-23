package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k), ETag: aws.String(`"` + k + `-etag"`)})
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

	objs, err := c.listKeys(context.Background(), "gists/u/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gists/u/a.json", "gists/u/b.json", "gists/u/c.json", "gists/u/d.json"}
	if len(objs) != len(want) {
		t.Fatalf("objs = %v, want %v", objs, want)
	}
	for i, key := range want {
		if objs[i].key != key {
			t.Fatalf("objs = %v, want %v", objs, want)
		}
		// Listing ETags arrive quoted and must reach the cache unquoted, or
		// they would never match the ETag a GET or HEAD reports.
		if objs[i].etag != key+"-etag" {
			t.Errorf("etag for %s = %q, want %q", key, objs[i].etag, key+"-etag")
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
	objs, err := c.listKeys(context.Background(), "gists/u/")
	if err != nil || len(objs) != 1 || objs[0].key != "gists/u/only.json" {
		t.Fatalf("objs = %v, err = %v", objs, err)
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
// operation and serves canned index/ and gists/ objects with real ETags, so
// tests can pin the per-call S3 request fan-out of read paths through the real
// SDK client. Counts are keyed by operation: "get-index", "head-index",
// "get-gist", "head-gist", "list", "versions", "put", "delete", "other".
type countingStore struct {
	mu      sync.Mutex
	counts  map[string]int    // operation -> requests
	objects map[string]string // key -> body
	// delay holds every response, so a test can keep one fetch in flight while
	// concurrent callers arrive and check they were coalesced into it.
	delay time.Duration
}

// etagOf is S3's own rule for a single-PUT object: the quoted MD5 of its
// bytes, which is why a rewritten object gets a new ETag here.
func etagOf(body string) string {
	return fmt.Sprintf("%q", hex.EncodeToString(md5sum([]byte(body))))
}

func md5sum(b []byte) []byte {
	sum := md5.Sum(b)
	return sum[:]
}

func (s *countingStore) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[op]
}

func (s *countingStore) snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		out[k] = v
	}
	return out
}

func (s *countingStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.TrimPrefix(r.URL.Path, "/b/")
	body, exists := s.objects[key]
	op := "other"
	switch {
	case r.URL.Query().Has("versions"):
		op = "versions"
	case r.URL.Query().Has("list-type"):
		op = "list"
	case r.Method == http.MethodPut:
		op = "put"
	case r.Method == http.MethodDelete:
		op = "delete"
	case strings.HasPrefix(key, "index/"):
		op = strings.ToLower(r.Method) + "-index"
	case strings.HasPrefix(key, "gists/"):
		op = strings.ToLower(r.Method) + "-gist"
	}
	s.counts[op]++

	switch op {
	case "list":
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, s.listXML(r.URL.Query().Get("prefix")))
	case "versions":
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><IsTruncated>false</IsTruncated><Version><Key>%s</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest><LastModified>2026-01-01T00:00:00.000Z</LastModified><Size>%d</Size></Version></ListVersionsResult>`, key, len(body))
	case "put":
		put, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.objects[key] = string(put)
		w.Header().Set("ETag", etagOf(string(put)))
		w.WriteHeader(http.StatusOK)
	case "delete":
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case "head-index", "head-gist":
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", etagOf(body))
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	default:
		if !exists {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", etagOf(body))
		io.WriteString(w, body)
	}
}

func (s *countingStore) listXML(prefix string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><IsTruncated>false</IsTruncated>`)
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		body := s.objects[k]
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2026-01-01T00:00:00.000Z</LastModified><ETag>%s</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>",
			html.EscapeString(k), html.EscapeString(etagOf(body)), len(body))
	}
	b.WriteString("</ListBucketResult>")
	return b.String()
}

// cacheBytes bounds the test client's read cache; 0 would disable it and hide
// the behaviour most of these tests are about.
const cacheBytes = 4 << 20

func newCountingClient(t *testing.T, objects map[string]string) (*Client, *countingStore) {
	return newCountingClientCached(t, cacheBytes, objects)
}

func newCountingClientCached(t *testing.T, cacheBytes int64, objects map[string]string) (*Client, *countingStore) {
	t.Helper()
	store := &countingStore{counts: map[string]int{}, objects: objects}
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)
	c := &Client{
		bucket: "b",
		cache:  newReadCache(cacheBytes),
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

// storedGist is a gist object body for the keys the read paths use.
func storedGist(t *testing.T, id, description string) string {
	t.Helper()
	data, err := (&models.Gist{ID: id, Description: description}).ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	got := store.snapshot()
	if got["get-index"] != want["get-index"] || got["list"] != 0 ||
		got["get-gist"] != 0 || got["put"] != 0 || got["other"] != 0 {
		t.Fatalf("counts = %v, want exactly one index GET per miss and nothing else", got)
	}
}

// TestGetGistIndexedCostsTwoRequests pins that the normal resolve path is
// one index GET plus one gist GET, with zero list requests.
func TestGetGistIndexedCostsTwoRequests(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "hello"),
	})
	got, err := c.GetGist(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
	if store.count("get-index") != 1 || store.count("get-gist") != 1 ||
		store.count("list") != 0 || store.count("put") != 0 || store.count("other") != 0 {
		t.Fatalf("counts = %v, want 1 index GET + 1 gist GET", store.snapshot())
	}
}

// TestCachedGistIsValidatedByMetadata pins the cost of a warm read: the cached
// body is reused after the backend confirms its ETag, so nothing is refetched
// and no object bytes move.
func TestCachedGistIsValidatedByMetadata(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "hello"),
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := c.GetGist(ctx, "abc"); err != nil {
			t.Fatalf("GetGist %d: %v", i, err)
		}
	}
	if store.count("get-index") != 1 || store.count("get-gist") != 1 {
		t.Fatalf("counts = %v, want each object fetched once", store.snapshot())
	}
	if store.count("head-index") != 2 || store.count("head-gist") != 2 {
		t.Fatalf("counts = %v, want both objects validated on reads 2 and 3", store.snapshot())
	}
}

func TestRewrittenObjectIsRefetched(t *testing.T) {
	objects := map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "first"),
	}
	c, store := newCountingClient(t, objects)
	ctx := context.Background()
	if _, err := c.GetGist(ctx, "abc"); err != nil {
		t.Fatal(err)
	}

	// A write that did not go through this process — another replica, say.
	objects["gists/u1/abc.json"] = storedGist(t, "abc", "second")

	got, err := c.GetGist(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "second" {
		t.Errorf("Description = %q, want second: a rewritten object was served from cache", got.Description)
	}
	if store.count("get-gist") != 2 {
		t.Fatalf("get-gist = %d, want 2 (initial fetch plus the changed one)", store.count("get-gist"))
	}
}

// TestDeletedObjectFailsValidation covers the shape the cache must not leak:
// an object deleted behind this process's back must 404, not answer from the
// body it cached earlier.
func TestDeletedObjectFailsValidation(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"index pointer gone", "index/abc"},
		{"gist object gone", "gists/u1/abc.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store := newCountingClient(t, map[string]string{
				"index/abc":         "u1",
				"gists/u1/abc.json": storedGist(t, "abc", "hello"),
			})
			ctx := context.Background()
			if _, err := c.GetGist(ctx, "abc"); err != nil {
				t.Fatal(err)
			}
			delete(store.objects, tc.key)

			if _, err := c.GetGist(ctx, "abc"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetGist: err = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestConcurrentGetGistFetchesOnce is the thundering herd: every concurrent
// reader of one gist must be served by a single fetch per object.
func TestConcurrentGetGistFetchesOnce(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "hello"),
	})
	store.delay = 120 * time.Millisecond

	const readers = 24
	var wg sync.WaitGroup
	errs := make([]error, readers)
	descs := make([]string, readers)
	for i := range readers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			gist, err := c.GetGist(context.Background(), "abc")
			if err != nil {
				errs[i] = err
				return
			}
			errs[i], descs[i] = nil, gist.Description
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
		if descs[i] != "hello" {
			t.Errorf("reader %d saw %q, want hello", i, descs[i])
		}
	}
	if got := store.count("get-index"); got != 1 {
		t.Errorf("get-index = %d, want exactly 1 for %d concurrent readers", got, readers)
	}
	if got := store.count("get-gist"); got != 1 {
		t.Errorf("get-gist = %d, want exactly 1 for %d concurrent readers", got, readers)
	}
}

// TestCancelledLeaderDoesNotFailWaiters pins why a coalesced fetch runs under a
// context detached from the caller: the request that happens to lead the fetch
// may hang up, and the callers waiting on its result must not be failed for it.
func TestCancelledLeaderDoesNotFailWaiters(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "hello"),
	})
	store.delay = 150 * time.Millisecond

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.GetGist(leaderCtx, "abc")
	}()

	time.Sleep(30 * time.Millisecond) // the leader is inside its fetch now
	cancelLeader()

	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = c.GetGist(context.Background(), "abc")
		}(i)
	}
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("waiter %d: err = %v, want the leader's fetch to succeed anyway", i, err)
		}
	}
}

// TestVersionContentNeedsNoValidation: a stored version's bytes are immutable,
// so they are served from cache without a metadata check even after the
// current object is rewritten.
func TestVersionContentNeedsNoValidation(t *testing.T) {
	objects := map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "first"),
	}
	c, store := newCountingClient(t, objects)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		got, err := c.GetVersion(ctx, "abc", "v1")
		if err != nil {
			t.Fatalf("GetVersion %d: %v", i, err)
		}
		if got.Description != "first" {
			t.Fatalf("Description = %q, want first", got.Description)
		}
	}
	objects["gists/u1/abc.json"] = storedGist(t, "abc", "second")
	got, err := c.GetVersion(ctx, "abc", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "first" {
		t.Errorf("Description = %q, want first: version v1 content does not change", got.Description)
	}
	if store.count("get-gist") != 1 {
		t.Fatalf("get-gist = %d, want 1 for three reads of one version", store.count("get-gist"))
	}
}

// TestListGistsCostsOneListWhenObjectsUnchanged is the widest fan-out in the
// app: the listing already carries every key's ETag, so a warm list page reads
// nothing but the listing itself.
func TestListGistsCostsOneListWhenObjectsUnchanged(t *testing.T) {
	objects := map[string]string{
		"index/one":           "u1",
		"index/two":           "u1",
		"gists/u1/one.json":   storedGist(t, "one", "first"),
		"gists/u1/two.json":   storedGist(t, "two", "second"),
		"gists/u2/other.json": storedGist(t, "other", "not mine"),
	}
	c, store := newCountingClient(t, objects)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		gists, err := c.ListGists(ctx, "u1")
		if err != nil {
			t.Fatalf("ListGists %d: %v", i, err)
		}
		if len(gists) != 2 {
			t.Fatalf("ListGists %d returned %d gists, want 2", i, len(gists))
		}
	}
	if store.count("list") != 2 {
		t.Fatalf("list = %d, want 2", store.count("list"))
	}
	if got := store.count("get-gist"); got != 2 {
		t.Errorf("get-gist = %d, want 2: only the first ListGists may fetch objects", got)
	}
	if got := store.count("head-gist"); got != 0 {
		t.Errorf("head-gist = %d, want 0: the listing's ETags make per-object HEADs unnecessary", got)
	}

	objects["gists/u1/two.json"] = storedGist(t, "two", "changed")
	gists, err := c.ListGists(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	var changed string
	for _, g := range gists {
		if g.ID == "two" {
			changed = g.Description
		}
	}
	if changed != "changed" {
		t.Errorf("changed gist read as %q, want changed", changed)
	}
	if got := store.count("get-gist"); got != 3 {
		t.Errorf("get-gist = %d, want 3: only the rewritten object is refetched", got)
	}
}

// TestUpdateNeedsNoRefetch: the process that saved a gist holds the bytes and
// the ETag the backend just returned, so the next read of its own save is a hit.
func TestUpdateNeedsNoRefetch(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "first"),
	})
	ctx := context.Background()
	if _, err := c.GetGist(ctx, "abc"); err != nil {
		t.Fatal(err)
	}

	if err := c.UpdateGist(ctx, "u1", &models.Gist{ID: "abc", Description: "second"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetGist(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "second" {
		t.Errorf("Description = %q, want second", got.Description)
	}
	if store.count("get-gist") != 1 {
		t.Fatalf("get-gist = %d, want 1: the writer must not refetch what it just wrote", store.count("get-gist"))
	}
}

func TestDeleteDropsCachedEntries(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{})
	ctx := context.Background()
	gist := &models.Gist{ID: "abc", Description: "hello"}
	if err := c.CreateGist(ctx, "u1", gist); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetGist(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteGist(ctx, "u1", "abc"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.GetGist(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetGist after delete: err = %v, want ErrNotFound", err)
	}
	// Creating wrote both objects through, so the only index GET is the lookup
	// after the delete: the cached entries did not survive it.
	want := map[string]int{"put": 2, "delete": 2, "head-index": 2, "head-gist": 1, "get-index": 1}
	if got := store.snapshot(); !maps.Equal(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}

func TestConcurrentListVersionsFetchesOnce(t *testing.T) {
	c, store := newCountingClient(t, map[string]string{
		"index/abc":         "u1",
		"gists/u1/abc.json": storedGist(t, "abc", "hello"),
	})
	store.delay = 120 * time.Millisecond

	const readers = 12
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListVersions(context.Background(), "abc"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := store.count("versions"); got != 1 {
		t.Errorf("versions = %d, want exactly 1 for %d concurrent readers", got, readers)
	}
}
