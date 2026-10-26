package s3

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
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
)

// storedVersion is one object version; body "" with deleted true is a
// delete marker.
type storedVersion struct {
	id      string
	body    string
	deleted bool
}

// versionedStore is an in-memory stand-in for a *versioned* bucket: a PUT
// appends a version, a DELETE appends a delete marker, and a DELETE with a
// version id removes that version (silently succeeding when it is gone, as
// real S3 does). ListObjectsV2 answers with current keys only;
// ListObjectVersions answers for one key's stored history. Every call is
// recorded as an op line so tests can pin scrub's step order and inject
// mid-purge failures.
type versionedStore struct {
	mu       sync.Mutex
	versions map[string][]storedVersion
	mtimes   map[string]time.Time // per-key LastModified reported by listings
	nextVid  int
	ops      []string
	// failDelete, when set, is consulted for every version-targeted delete
	// with "key@versionId"; a true answer fails that call once.
	failDelete func(op string) bool
}

func newVersionedStore(t *testing.T) (*Client, *versionedStore) {
	t.Helper()
	s := &versionedStore{versions: map[string][]storedVersion{}, mtimes: map[string]time.Time{}}
	srv := httptest.NewServer(s)
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
	return c, s
}

// record renders the op lines tests assert on.
func (s *versionedStore) record(format string, args ...any) {
	s.ops = append(s.ops, fmt.Sprintf(format, args...))
}

func (s *versionedStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := r.URL.Query()
	key := strings.TrimPrefix(r.URL.Path, "/b/")

	switch {
	case r.Method == http.MethodGet && q.Has("versions"):
		prefix := q.Get("prefix")
		s.record("versions %s", prefix)
		keys := make([]string, 0, len(s.versions))
		for k := range s.versions {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><IsTruncated>false</IsTruncated>`)
		for _, k := range keys {
			vers := s.versions[k]
			for i, v := range vers {
				latest := "false"
				if i == len(vers)-1 {
					latest = "true"
				}
				tag := "Version"
				if v.deleted {
					tag = "DeleteMarker"
					fmt.Fprintf(&b, "<%s><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%s</IsLatest></%s>", tag, html.EscapeString(k), v.id, latest, tag)
					continue
				}
				mod := s.modtimeLocked(k)
				fmt.Fprintf(&b, "<%s><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%s</IsLatest><LastModified>%s</LastModified><Size>%d</Size></%s>",
					tag, html.EscapeString(k), v.id, latest, mod.UTC().Format(time.RFC3339), len(v.body), tag)
			}
		}
		b.WriteString("</ListVersionsResult>")
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, b.String())

	case r.Method == http.MethodGet && q.Has("list-type"):
		s.record("list %s", q.Get("prefix"))
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><IsTruncated>false</IsTruncated>`)
		keys := make([]string, 0, len(s.versions))
		for k := range s.versions {
			if strings.HasPrefix(k, q.Get("prefix")) && s.currentLocked(k) != nil {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := s.currentLocked(k)
			fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>%q</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>",
				html.EscapeString(k), s.modtimeLocked(k).UTC().Format(time.RFC3339), etagOf(v.body), len(v.body))
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, b.String())

	case r.Method == http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.nextVid++
		vid := fmt.Sprintf("v%d", s.nextVid)
		s.versions[key] = append(s.versions[key], storedVersion{id: vid, body: string(body)})
		s.record("put %s@%s", key, vid)
		w.Header().Set("ETag", etagOf(string(body)))
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodDelete:
		if vid := q.Get("versionId"); vid != "" {
			if s.failDelete != nil && s.failDelete(key+"@"+vid) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			s.record("delete-version %s@%s", key, vid)
			vers := s.versions[key]
			for i, v := range vers {
				if v.id == vid {
					s.versions[key] = append(vers[:i], vers[i+1:]...)
					break
				}
			}
			if len(s.versions[key]) == 0 {
				delete(s.versions, key)
			}
			w.WriteHeader(http.StatusNoContent) // S3: succeeds even if the version is gone
			return
		}
		s.record("delete %s", key)
		s.nextVid++
		vid := fmt.Sprintf("v%d", s.nextVid)
		s.versions[key] = append(s.versions[key], storedVersion{id: vid, deleted: true})
		w.Header().Set("x-amz-delete-marker", "true")
		w.WriteHeader(http.StatusNoContent)

	default: // GET / HEAD of an object
		v := s.currentLocked(key)
		if v == nil {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("ETag", etagOf(v.body))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(v.body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		io.WriteString(w, v.body)
	}
}

// currentLocked returns a key's current (latest, non-marker) version.
func (s *versionedStore) currentLocked(key string) *storedVersion {
	vers := s.versions[key]
	if len(vers) == 0 {
		return nil
	}
	v := vers[len(vers)-1]
	if v.deleted {
		return nil
	}
	return &v
}

// modtimeLocked ages a key from its set mtime, defaulting to long ago so
// sweep age checks pass unless a test pins a recent time.
func (s *versionedStore) modtimeLocked(key string) time.Time {
	if t, ok := s.mtimes[key]; ok {
		return t
	}
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// --- test-side store surgery ---

func (s *versionedStore) put(key, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextVid++
	s.versions[key] = append(s.versions[key], storedVersion{id: fmt.Sprintf("v%d", s.nextVid), body: body})
}

func (s *versionedStore) versionCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.versions[key])
}

func (s *versionedStore) current(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.currentLocked(key)
	if v == nil {
		return "", false
	}
	return v.body, true
}

func (s *versionedStore) opLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.ops...)
}

func (s *versionedStore) setMtime(key string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mtimes[key] = t
}

func gistWithBlob(t *testing.T, id, blobKey string) string {
	t.Helper()
	g := &models.Gist{ID: id, Files: []models.File{{Name: "blob.png", Key: blobKey}}}
	data, err := g.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func gistText(t *testing.T, id, content string) string {
	t.Helper()
	g := &models.Gist{ID: id, Files: []models.File{{Name: "a.txt", Content: content}}}
	data, err := g.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestScrubPurgesExpiredAndSweepsOrphans is the happy path: an expired
// deletion loses index, gist object, and tombstone entirely; its blob —
// unreferenced once the gist is gone — is swept, as is a never-saved upload,
// while blobs a surviving gist references and young orphans are kept.
func TestScrubPurgesExpiredAndSweepsOrphans(t *testing.T) {
	c, s := newVersionedStore(t)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)

	s.put("index/abc", "u1")
	s.put("gists/u1/abc.json", gistWithBlob(t, "abc", "uploads/u1/blob"))
	s.put("deleted/u1/abc", now.Add(-8*24*time.Hour).Format(time.RFC3339)) // past 7d retention

	s.put("index/keep", "u1")
	s.put("gists/u1/keep.json", gistWithBlob(t, "keep", "uploads/u1/keep"))

	s.put("uploads/u1/blob", "png-bytes")
	s.put("uploads/u1/keep", "png-bytes")
	s.put("uploads/u1/orphan", "never-saved")
	s.put("uploads/u1/young", "in-flight") // referenced by nobody, but too young to purge
	for _, k := range []string{"uploads/u1/blob", "uploads/u1/keep", "uploads/u1/orphan"} {
		s.setMtime(k, old)
	}
	s.setMtime("uploads/u1/young", now.Add(-time.Hour))

	stats, err := c.Scrub(context.Background(), now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats.GistsPurged != 1 || stats.UploadsPurged != 2 {
		t.Fatalf("stats = %+v, want 1 gist and 2 uploads (dead gist's blob + orphan) purged", stats)
	}

	for _, key := range []string{"index/abc", "gists/u1/abc.json", "deleted/u1/abc", "uploads/u1/blob", "uploads/u1/orphan"} {
		if n := s.versionCount(key); n != 0 {
			t.Errorf("%s has %d stored versions after purge, want 0", key, n)
		}
	}
	for _, key := range []string{"index/keep", "gists/u1/keep.json", "uploads/u1/keep", "uploads/u1/young"} {
		if _, ok := s.current(key); !ok {
			t.Errorf("%s was purged but must survive", key)
		}
	}
}

// TestScrubKeepsYoungDeletions: inside the retention window nothing is
// touched — the recovery promise in one test.
func TestScrubKeepsYoungDeletions(t *testing.T) {
	c, s := newVersionedStore(t)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	s.put("index/abc", "u1")
	s.put("gists/u1/abc.json", gistText(t, "abc", "hello"))
	s.put("deleted/u1/abc", now.Add(-time.Hour).Format(time.RFC3339))
	s.put("uploads/u1/orphan", "x")
	s.setMtime("uploads/u1/orphan", now.Add(-time.Hour))

	stats, err := c.Scrub(context.Background(), now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats.GistsPurged != 0 || stats.UploadsPurged != 0 {
		t.Fatalf("stats = %+v, want untouched", stats)
	}
	for _, key := range []string{"index/abc", "gists/u1/abc.json", "deleted/u1/abc", "uploads/u1/orphan"} {
		if _, ok := s.current(key); !ok {
			t.Errorf("%s was purged inside the retention window", key)
		}
	}
}

// TestScrubOrderAndResume pins the crash-safety contract: a purge runs
// index → gist → tombstone; a mid-purge failure leaves the tombstone (so the
// work stays visible and re-runnable) and a re-run finishes it.
func TestScrubOrderAndResume(t *testing.T) {
	c, s := newVersionedStore(t)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	s.put("index/abc", "u1")
	s.put("gists/u1/abc.json", gistText(t, "abc", "hello"))
	s.put("deleted/u1/abc", now.Add(-8*24*time.Hour).Format(time.RFC3339))

	// Fail the first delete that targets the gist object's history.
	s.failDelete = func(op string) bool { return strings.HasPrefix(op, "gists/u1/abc.json@") }
	stats, err := c.Scrub(context.Background(), now, 7*24*time.Hour)
	if err == nil {
		t.Fatal("failed purge reported success")
	}
	if stats.GistsPurged != 0 {
		t.Fatalf("stats = %+v, want the incomplete gist not counted", stats)
	}
	// Index is already gone (an index must never outlive its gist); the
	// tombstone survived so the purge is still in this run's scope.
	if n := s.versionCount("index/abc"); n != 0 {
		t.Errorf("index has %d versions, want 0 purged first", n)
	}
	if _, ok := s.current("deleted/u1/abc"); !ok {
		t.Fatal("tombstone purged before the gist was done")
	}

	// The op order before the failure: the index's versions were listed and
	// purged before the gist's history was even listed.
	var sawIndexPurge, sawGistList bool
	for _, op := range s.opLines() {
		if !sawGistList && op == "versions gists/u1/abc.json" {
			sawGistList = true
			if !sawIndexPurge {
				t.Fatalf("ops %v: gist listed before the index was purged", s.opLines())
			}
		}
		if strings.HasPrefix(op, "delete-version index/abc") {
			sawIndexPurge = true
		}
	}
	if !sawIndexPurge || !sawGistList {
		t.Fatalf("ops %v: expected the index purged then the gist attempted", s.opLines())
	}
	// The tombstone's history was never touched: the purge stopped at the gist.
	for _, op := range s.opLines() {
		if strings.Contains(op, "deleted/u1/abc") {
			t.Fatalf("tombstone touched mid-purge: %v", s.opLines())
		}
	}

	// Re-run without the injected failure finishes the job.
	s.failDelete = nil
	stats, err = c.Scrub(context.Background(), now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if stats.GistsPurged != 1 {
		t.Fatalf("stats = %+v, want 1", stats)
	}
	for _, key := range []string{"index/abc", "gists/u1/abc.json", "deleted/u1/abc"} {
		if n := s.versionCount(key); n != 0 {
			t.Errorf("%s has %d versions after resume, want 0", key, n)
		}
	}
}

// TestScrubAbortsUploadSweepOnUnreadableGist: an unreadable survivor must
// stop the sweep rather than guess its references.
func TestScrubAbortsUploadSweepOnUnreadableGist(t *testing.T) {
	c, s := newVersionedStore(t)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	s.put("gists/u1/broken.json", "this is not gist json")
	s.put("uploads/u1/orphan", "x") // old and unreferenced, but...

	stats, err := c.Scrub(context.Background(), now, 7*24*time.Hour)
	if err == nil {
		t.Fatal("corrupt survivor did not abort the sweep")
	}
	if stats.UploadsPurged != 0 {
		t.Fatalf("stats = %+v, want no uploads purged", stats)
	}
	if _, ok := s.current("uploads/u1/orphan"); !ok {
		t.Error("orphan purged while the reference set was incomplete")
	}
}

// TestDeleteRestoreRoundtrip walks the whole recovery promise against a
// versioned bucket: delete hides the gist everywhere but the trash, restore
// brings it back exactly, and only the right user may restore.
func TestDeleteRestoreRoundtrip(t *testing.T) {
	c, s := newVersionedStore(t)
	ctx := context.Background()
	g := &models.Gist{ID: "abc", Description: "mine"}
	if err := c.CreateGist(ctx, "u1", g); err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteGist(ctx, "u1", "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetGist(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetGist after delete: %v, want ErrNotFound", err)
	}
	live, err := c.ListGists(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("ListGists: %d gists, want 0: the tombstone filter must hide it", len(live))
	}
	trash, err := c.ListDeleted(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].Gist.ID != "abc" || time.Since(trash[0].DeletedAt) > time.Minute {
		t.Fatalf("trash = %+v, want one fresh entry for abc", trash)
	}
	// The gist object survived deletion untouched — that is what recovery
	// reads, so it must still be the current version.
	if _, ok := s.current("gists/u1/abc.json"); !ok {
		t.Fatal("delete touched the gist object")
	}

	// Another user has no tombstone under their prefix and cannot restore.
	if err := c.RestoreGist(ctx, "u2", "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user restore: %v, want ErrNotFound", err)
	}
	// A never-deleted id cannot be "restored" either.
	if err := c.RestoreGist(ctx, "u1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore of an undeleted id: %v, want ErrNotFound", err)
	}

	if err := c.RestoreGist(ctx, "u1", "abc"); err != nil {
		t.Fatal(err)
	}
	// The tombstone is gone from the bucket, not just hidden behind a delete
	// marker: a marker no listing would ever return, so it would outlive every
	// scrub — and a backend that still lists delete-marked keys would keep the
	// restored gist off the list page.
	if n := s.versionCount("deleted/u1/abc"); n != 0 {
		t.Errorf("tombstone versions after restore = %d, want 0", n)
	}
	if _, err := c.GetGist(ctx, "abc"); err != nil {
		t.Fatalf("GetGist after restore: %v", err)
	}
	live, err = c.ListGists(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != "abc" {
		t.Fatalf("ListGists after restore: %+v, want abc back", live)
	}
	trash, err = c.ListDeleted(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 0 {
		t.Fatalf("trash after restore: %+v, want empty", trash)
	}

	// Delete again: the tombstone becomes current again with a fresh
	// timestamp, on top of the restored copy's delete marker.
	if err := c.DeleteGist(ctx, "u1", "abc"); err != nil {
		t.Fatal(err)
	}
	trash, err = c.ListDeleted(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 {
		t.Fatalf("trash after re-delete: %+v, want abc back in it", trash)
	}
}
