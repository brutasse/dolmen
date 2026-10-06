//go:build e2e

// Package e2e runs the gist app against real infrastructure: adobe/s3mock
// (S3 with versioning) and a real Dex instance with two static users,
// exercising the full OIDC login flow.
//
// Run with: go test -tags e2e ./e2e/
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"dolmen/app"
	"dolmen/config"
	"dolmen/middleware"
	"dolmen/models"
	dolmens3 "dolmen/s3"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	tc "github.com/testcontainers/testcontainers-go"
	tcnet "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dexImage    = "dexidp/dex:v2.37.0"
	s3mockImage = "adobe/s3mock:5.2.3"

	bucketName = "e2e-gists"

	clientID     = "gist-client"
	clientSecret = "testsecret"

	alice = "alice@example.com"
	bob   = "bob@example.com"
	pass  = "password"
	// bcrypt of pass, cost 10 (Dex staticPasswords).
	aliceHash = "$2b$10$jam9gTBW6gtVKuy.4N9UxOc1eVqCLQtYvGUDwCdbLNcllwRBlNN9u"
	bobHash   = "$2b$10$TJyXNIQZevinNB49.NfgOeacUHgKrpX4TwCSLmlhQZnWIerlXFPBm"
)

var (
	appURL     string
	appCfg     config.Config
	setupErr   error
	awsC       *awss3.Client
	containers []tc.Container
	netw       *tc.DockerNetwork
	tmpDir     string
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if setupErr = startStack(ctx); setupErr != nil {
		fmt.Fprintf(os.Stderr, "e2e: stack setup failed: %v\n", setupErr)
	}
	code := m.Run()
	if setupErr == nil {
		stopStack(ctx)
	}
	os.Exit(code)
}

func requireStack(t *testing.T) {
	t.Helper()
	if setupErr != nil {
		t.Skipf("e2e stack unavailable (needs Docker): %v", setupErr)
	}
}

func reservePort() (int, net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	return ln.Addr().(*net.TCPAddr).Port, ln, nil
}

func startStack(ctx context.Context) error {
	var err error
	netw, err = tcnet.New(ctx)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	containers = nil

	tmpDir, err = os.MkdirTemp("", "gist-e2e")
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpDir, 0o755); err != nil {
		return err
	}

	// --- s3mock ---
	s3c, err := tc.Run(ctx, s3mockImage,
		tc.WithEnv(map[string]string{
			"COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS": bucketName,
		}),
		tc.WithExposedPorts("9090/tcp"),
		tc.WithWaitStrategy(wait.ForHTTP("/favicon.ico").WithPort("9090/tcp").WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return fmt.Errorf("s3mock: %w", err)
	}
	containers = append(containers, s3c)
	s3Host, err := s3c.Host(ctx)
	if err != nil {
		return err
	}
	s3Port, err := s3c.MappedPort(ctx, "9090/tcp")
	if err != nil {
		return err
	}
	s3Endpoint := fmt.Sprintf("http://%s:%s", s3Host, s3Port.Port())

	// Direct client for bucket setup and per-test resets.
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return err
	}
	awsC = awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(s3Endpoint)
	})
	if _, err := awsC.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucketName),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	}); err != nil {
		return fmt.Errorf("enable versioning: %w", err)
	}

	// --- dex ---
	// Reserve the app port first so the Dex static client can pin the exact
	// redirect URI; reserve Dex's host port too so the issuer is known
	// before Dex boots.
	appPort, appLn, err := reservePort()
	if err != nil {
		return err
	}
	dexPort, dexLn, err := reservePort()
	if err != nil {
		return err
	}
	dexLn.Close() // free the port for the container binding

	fixDexHostPort := func(hc *container.HostConfig) {
		hc.PortBindings = mobynetwork.PortMap{
			mobynetwork.MustParsePort("5556/tcp"): []mobynetwork.PortBinding{
				{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(dexPort)},
			},
		}
	}

	dexCfgPath := filepath.Join(tmpDir, "dex.yaml")
	dexCfg := fmt.Sprintf(`issuer: http://127.0.0.1:%d/dex
storage:
  type: memory
web:
  http: 0.0.0.0:5556
logger:
  level: error
staticClients:
  - id: %s
    name: Gist E2E
    secret: %s
    redirectURIs:
      - http://127.0.0.1:%d/callback
enablePasswordDB: true
staticPasswords:
  - email: %s
    hash: "%s"
    username: alice
    userID: e2e-alice
  - email: %s
    hash: "%s"
    username: bob
    userID: e2e-bob
`, dexPort, clientID, clientSecret, appPort, alice, aliceHash, bob, bobHash)
	if err := os.WriteFile(dexCfgPath, []byte(dexCfg), 0o644); err != nil {
		return err
	}

	dexCtr, err := tc.Run(ctx, dexImage,
		tc.WithCmd("dex", "serve", "/dex-config.yaml"),
		tcnet.WithNetwork([]string{"dex"}, netw),
		tc.WithExposedPorts("5556/tcp"),
		tc.WithHostConfigModifier(fixDexHostPort),
		tc.WithMounts(tc.BindMount(dexCfgPath, "/dex-config.yaml")),
		tc.WithWaitStrategy(wait.ForHTTP("/dex/keys").WithPort("5556/tcp").WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return fmt.Errorf("dex: %w", err)
	}
	containers = append(containers, dexCtr)

	// --- app ---
	// Only the S3 credentials ride the SDK env chain; everything else is
	// config, exactly as an operator would write it.
	os.Setenv("AWS_ACCESS_KEY_ID", "test")
	os.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	appCfg = config.Config{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", appPort),
		OIDC: config.OIDCConfig{
			Issuer:       fmt.Sprintf("http://127.0.0.1:%d/dex", dexPort),
			ClientID:     clientID,
			ClientSecret: clientSecret,
		},
		S3: config.S3Config{Endpoint: s3Endpoint, Bucket: bucketName},
	}

	s3Client, err := dolmens3.NewClient(ctx, appCfg.S3)
	if err != nil {
		return fmt.Errorf("app s3 client: %w", err)
	}
	provider, verifier, err := middleware.NewOIDCProviderAndVerifier(ctx, appCfg.OIDC)
	if err != nil {
		return fmt.Errorf("oidc: %w", err)
	}
	appURL = appCfg.BaseURL
	handler := app.New(s3Client, provider, verifier, &appCfg)

	go http.Serve(appLn, handler) //nolint:errcheck
	return waitForApp(appURL)
}

func waitForApp(url string) error {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("app never became ready at %s", url)
}

func stopStack(ctx context.Context) {
	for _, c := range containers {
		_ = c.Terminate(ctx)
	}
	if netw != nil {
		_ = netw.Remove(ctx)
	}
	if tmpDir != "" {
		os.RemoveAll(tmpDir)
	}
}

// --- browser helper: walks the OIDC flow like a human with a browser ---

type browser struct {
	t   *testing.T
	c   *http.Client // follows redirects, keeps cookies
	jar *cookiejar.Jar
}

func newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{
		t: t,
		c: &http.Client{
			Jar: jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		jar: jar,
	}
}

// raw issues a request without following redirects (assert redirect targets).
func (b *browser) raw(method, path string, body io.Reader) *http.Response {
	b.t.Helper()
	req, err := http.NewRequest(method, appURL+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	noFollow := &http.Client{Jar: b.jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// get follows redirects and returns status + body. App HTML pages are also
// checked for third-party CDN references: the app must be fully self-hosted.
func (b *browser) get(u string) (int, string) {
	b.t.Helper()
	appPage := !strings.HasPrefix(u, "http")
	if appPage {
		u = appURL + u
	}
	resp, err := b.c.Get(u)
	if err != nil {
		b.t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if appPage && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		for _, banned := range []string{"cdn.", "jsdelivr", "tailwindcss.com", "unpkg.com"} {
			if strings.Contains(string(body), banned) {
				b.t.Errorf("app page %s references third-party %q", u, banned)
			}
		}
	}
	return resp.StatusCode, string(body)
}

var (
	rePasswordInput = regexp.MustCompile(`name="password"`)
	reLoginForm     = regexp.MustCompile(`(?s)<form[^>]*action="([^"]*)"`)
	reApproval      = regexp.MustCompile(`name="approval" value="approve"`)
	reReqHidden     = regexp.MustCompile(`name="req" value="([^"]*)"`)
)

// login walks: app -> dex login form -> consent page -> back to app.
func (b *browser) login(username, password string) error {
	resp, err := b.c.Get(appURL + "/")
	if err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		final := resp.Request.URL
		if final.Host == strings.TrimPrefix(appURL, "http://") && b.idToken() != "" {
			return nil
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case rePasswordInput.Match(body):
			if resp.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("login rejected for %s", username)
			}
			action := final.String()
			if m := reLoginForm.FindSubmatch(body); m != nil {
				rel := html.UnescapeString(string(m[1]))
				if u, err := url.Parse(rel); err == nil {
					action = final.ResolveReference(u).String()
				}
			}
			form := url.Values{"login": {username}, "password": {password}}
			resp, err = b.c.PostForm(action, form)
		case reApproval.Match(body):
			reqVal := ""
			if m := reReqHidden.FindSubmatch(body); m != nil {
				reqVal = string(m[1])
			}
			form := url.Values{"req": {reqVal}, "approval": {"approve"}}
			resp, err = b.c.PostForm(final.String(), form)
		default:
			return fmt.Errorf("unexpected page at %s (%d): %.200s", final, resp.StatusCode, body)
		}
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("login did not settle")
}

func (b *browser) mustLogin(username string) {
	b.t.Helper()
	if err := b.login(username, pass); err != nil {
		b.t.Fatalf("login as %s: %v", username, err)
	}
}

func (b *browser) idToken() string {
	u, _ := url.Parse(appURL)
	for _, ck := range b.jar.Cookies(u) {
		if ck.Name == "id_token" {
			return ck.Value
		}
	}
	return ""
}

// --- app-level helpers ---

func loginAs(t *testing.T, email string) *browser {
	t.Helper()
	requireStack(t)
	b := newBrowser(t)
	b.mustLogin(email)
	return b
}

// resetStore empties the bucket so tests don't leak state into each other.
func resetStore(t *testing.T) {
	t.Helper()
	resetCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := awss3.NewListObjectsV2Paginator(awsC, &awss3.ListObjectsV2Input{Bucket: aws.String(bucketName)})
	for p.HasMorePages() {
		page, err := p.NextPage(resetCtx)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, obj := range page.Contents {
			if _, err := awsC.DeleteObject(resetCtx, &awss3.DeleteObjectInput{Bucket: aws.String(bucketName), Key: obj.Key}); err != nil {
				t.Fatalf("delete object: %v", err)
			}
		}
	}
}

// plainText strips tags/entities so content assertions work even when the
// renderer has split text into highlighted spans.
var reTag = regexp.MustCompile(`(?s)<[^>]*>`)

func plainText(s string) string {
	return html.UnescapeString(reTag.ReplaceAllString(s, ""))
}

// csrfToken returns the double-submit token from the jar; any prior app GET
// (login, list, forms) sets it.
func (b *browser) csrfToken(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(appURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.jar.Cookies(u) {
		if c.Name == "csrf_token" {
			return c.Value
		}
	}
	t.Fatal("no csrf_token cookie in jar")
	return ""
}

func createGist(t *testing.T, b *browser, title string, files [][2]string) string {
	t.Helper()
	v := url.Values{"description": {title}, "csrf_token": {b.csrfToken(t)}}
	for i, f := range files {
		v.Set(fmt.Sprintf("file%d_name", i), f[0])
		v.Set(fmt.Sprintf("file%d_content", i), f[1])
	}
	resp := b.raw("POST", "/create", strings.NewReader(v.Encode()))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("create: expected 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/gist/") {
		t.Fatalf("create: unexpected redirect %q", loc)
	}
	return strings.TrimPrefix(loc, "/gist/")
}

// --- tests ---

func TestAnonymousRedirectsToLogin(t *testing.T) {
	requireStack(t)
	resetStore(t)
	resp := newBrowser(t).raw("GET", "/", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login?redirect=") {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestLoginSetsSession(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	code, body := b.get("/")
	if code != 200 {
		t.Fatalf("GET /: got %d", code)
	}
	if !strings.Contains(body, alice) {
		t.Errorf("session page should show user %s", alice)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	requireStack(t)
	b := newBrowser(t)
	if err := b.login(alice, "wrong-password"); err == nil {
		t.Fatal("expected login with wrong password to fail")
	}
	if b.idToken() != "" {
		t.Error("no id_token cookie should be set on failed login")
	}
}

func TestCreateAndView(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "My gist", [][2]string{
		{"main.go", "package main"},
		{"readme.md", "# Hello World"},
	})
	code, body := b.get("/gist/" + id)
	if code != 200 {
		t.Fatalf("view: got %d", code)
	}
	for _, want := range []string{"main.go", `class="chroma"`, "readme.md", `<h1 id="hello-world">Hello World`, `class="file-head"`, `data-copy-url="/raw/`} {
		if !strings.Contains(body, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// Header row: Edit (and versions) sit right of the title; there is no
	// gist-level Raw button (every file has its own) and no back link.
	for _, want := range []string{
		`class="gist-actions"`,
		`class="btn btn-secondary btn-sm" href="/edit/` + id + `"`,
		`class="gist-date rel-time"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("view header missing %q", want)
		}
	}
	if strings.Contains(body, `btn-secondary btn-sm" href="/raw/`) {
		t.Error("view header must not offer a gist-level Raw button")
	}
	if strings.Contains(body, "Back to gists") {
		t.Error("view page still carries a Back to gists link")
	}
	if !strings.Contains(body, "<title>alice / My gist</title>") {
		t.Error("view page title does not carry owner prefix and gist name")
	}
	if _, editPage := b.get("/edit/" + id); !strings.Contains(editPage, "<title>Edit: My gist</title>") {
		t.Error("edit page title does not carry the gist name")
	}

	// Tombstone the owner pointer: with the pre-index fallback scan removed,
	// a gist without its index pointer must 404 (no prefix scan, no backfill).
	delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := awsC.DeleteObject(delCtx, &awss3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("index/" + id),
	}); err != nil {
		t.Fatalf("delete index: %v", err)
	}
	if code, _ := b.get("/gist/" + id); code != 404 {
		t.Errorf("view without index pointer: got %d, want 404", code)
	}
}

func TestEmptyState(t *testing.T) {
	resetStore(t)
	code, body := loginAs(t, bob).get("/gists")
	if code != 200 {
		t.Fatalf("GET /gists: got %d", code)
	}
	if !strings.Contains(body, "No gists yet") {
		t.Error("empty-state message missing")
	}
}

func TestEditFileIndexGap(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Gap", [][2]string{
		{"drop.go", "DROP-ME"},
		{"keep.go", "KEEP-ME"},
	})
	// Simulate removing the first file in the UI: only file1_* is submitted.
	v := url.Values{"description": {"Gap"}, "csrf_token": {b.csrfToken(t)}}
	v.Set("file1_name", "keep.go")
	v.Set("file1_content", "KEEP-ME v2")
	resp := b.raw("POST", "/edit/"+id, strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("edit: expected 302, got %d", resp.StatusCode)
	}

	_, body := b.get("/gist/" + id)
	text := plainText(body)
	if !strings.Contains(text, "KEEP-ME v2") || strings.Contains(text, "DROP-ME") {
		t.Errorf("after gap-edit, gist should contain only keep.go: %s", body)
	}
}

func TestEditUpdates(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Before", [][2]string{{"a.md", "one"}})
	v := url.Values{"description": {"After"}, "csrf_token": {b.csrfToken(t)}}
	v.Set("file0_name", "a.md")
	v.Set("file0_content", "two")
	resp := b.raw("POST", "/edit/"+id, strings.NewReader(v.Encode()))
	resp.Body.Close()

	_, body := b.get("/gist/" + id)
	if !strings.Contains(body, "After") || !strings.Contains(body, "two") {
		t.Errorf("gist not updated: %s", body)
	}
}

func TestVersions(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Versioned", [][2]string{{"a.md", "ALPHA"}})
	v := url.Values{"description": {"Versioned"}, "csrf_token": {b.csrfToken(t)}}
	v.Set("file0_name", "a.md")
	v.Set("file0_content", "BETA")
	resp := b.raw("POST", "/edit/"+id, strings.NewReader(v.Encode()))
	resp.Body.Close()

	code, body := b.get("/gist/" + id + "/versions")
	if code != 200 {
		t.Fatalf("versions: got %d", code)
	}
	var versions []models.Version
	if err := json.Unmarshal([]byte(body), &versions); err != nil {
		t.Fatalf("versions json: %v (%s)", err, body)
	}
	if len(versions) < 2 {
		t.Fatalf("expected >=2 versions, got %d", len(versions))
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].LastModified.Before(versions[j].LastModified) })
	oldest := versions[0]

	code, body = b.get(fmt.Sprintf("/gist/%s/version/%s", id, oldest.VersionID))
	if code != 200 {
		t.Fatalf("view version: got %d", code)
	}
	if !strings.Contains(body, "ALPHA") {
		t.Error("old version should contain ALPHA")
	}
	if !strings.Contains(body, "Back to current version") {
		t.Error("version view missing back-to-current link")
	}

	// Raw follows versions: current serves current content, ?vid= reaches
	// the stored version (files removed by later edits included).
	if code, body = b.get("/raw/" + id); code != 200 || body != "BETA" {
		t.Errorf("current raw = %d %q, want 200 BETA", code, body)
	}
	if code, body = b.get("/raw/" + id + "/a.md?vid=" + oldest.VersionID); code != 200 || body != "ALPHA" {
		t.Errorf("versioned raw = %d %q, want 200 ALPHA", code, body)
	}
	// The dropdown lists Current Version and each *past* version once:
	// two stored versions means two options, not three.
	if code, body = b.get("/gist/" + id); code != 200 {
		t.Fatalf("view: got %d", code)
	}
	if n := strings.Count(body, "<option"); n != 2 {
		t.Errorf("dropdown options = %d, want 2 (Current + 1 past)", n)
	}
}

func TestDelete(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Doomed", [][2]string{{"a.md", "bye"}})

	// The owner's detail page carries the delete button (it moved off the
	// list page).
	_, page := b.get("/gist/" + id)
	if !strings.Contains(page, `action="/delete/`+id+`"`) {
		t.Error("view page missing delete form")
	}
	if !strings.Contains(page, `onsubmit="return confirm(`) {
		t.Error("delete form has no confirm guard")
	}

	resp := b.raw("POST", "/delete/"+id, strings.NewReader(url.Values{"csrf_token": {b.csrfToken(t)}}.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("delete: expected 302, got %d", resp.StatusCode)
	}

	_, body := b.get("/gists")
	if strings.Contains(body, "Doomed") {
		t.Error("deleted gist still listed")
	}
	if code, _ := b.get("/gist/" + id); code != http.StatusNotFound {
		t.Errorf("deleted gist view: expected 404, got %d", code)
	}
}

func TestUserIsolation(t *testing.T) {
	resetStore(t)
	a := loginAs(t, alice)
	id := createGist(t, a, "Secrets", [][2]string{{"a.md", "alice-data"}})

	b := loginAs(t, bob)
	_, body := b.get("/gists")
	if strings.Contains(body, "Secrets") {
		t.Error("bob's list should not contain alice's gist")
	}
	// Bob's delete attempt must not destroy alice's gist. (The CSRF token is
	// sent so this exercises the ownership check, not the CSRF one.)
	resp := b.raw("POST", "/delete/"+id, strings.NewReader(url.Values{"csrf_token": {b.csrfToken(t)}}.Encode()))
	resp.Body.Close()
	_, body = a.get("/gist/" + id)
	if !strings.Contains(body, "alice-data") {
		t.Error("alice's gist should survive bob's delete attempt")
	}
}

func TestRedirectCookieSanitized(t *testing.T) {
	requireStack(t)
	b := newBrowser(t)
	resp := b.raw("GET", "/login?redirect=https://evil.com", nil)
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == "redirect_url" && ck.Value != "" {
			t.Fatalf("redirect_url cookie should be empty for absolute URLs, got %q", ck.Value)
		}
	}
}

func TestBearerAuth(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	token := b.idToken()
	if token == "" {
		t.Fatal("no id_token after login")
	}
	req, _ := http.NewRequest("GET", appURL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("bearer-auth GET /: expected 200, got %d", resp.StatusCode)
	}
}

// TestLoginDisabledMode boots a second app instance without a client
// secret — the behind-a-proxy topology — and pins its contract: a bearer
// token from a real login passes, anonymous requests get 401 (not a
// /login redirect, giving the proxy something to re-authenticate on), and
// the disabled built-in flow answers 503.
func TestLoginDisabledMode(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	token := b.idToken()
	if token == "" {
		t.Fatal("no id_token after login")
	}

	port, ln, err := reservePort()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cl, err := dolmens3.NewClient(ctx, appCfg.S3)
	if err != nil {
		t.Fatal(err)
	}
	prov, ver, err := middleware.NewOIDCProviderAndVerifier(ctx, appCfg.OIDC)
	if err != nil {
		t.Fatal(err)
	}
	disabled := appCfg
	disabled.OIDC.ClientSecret = ""
	disabled.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	go http.Serve(ln, app.New(cl, prov, ver, &disabled)) //nolint:errcheck

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := retryGet(client, disabled.BaseURL+"/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous GET /: %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", disabled.BaseURL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	tokResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusOK {
		t.Errorf("bearer GET /: %d, want 200", tokResp.StatusCode)
	}

	lr, err := retryGet(client, disabled.BaseURL+"/login")
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Body.Close()
	if lr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /login: %d, want 503", lr.StatusCode)
	}
}

// retryGet waits for a freshly served listener to answer.
func retryGet(client *http.Client, url string) (*http.Response, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			return resp, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("waiting for %s: %w", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestCrossUserView verifies cross-user read access and write isolation.
func TestCrossUserView(t *testing.T) {
	resetStore(t)
	a := loginAs(t, alice)
	id := createGist(t, a, "Shareable", [][2]string{{"a.md", "shared-content"}})

	b := loginAs(t, bob)
	code, body := b.get("/gist/" + id)
	if code != 200 {
		t.Fatalf("bob should be able to view alice's gist by URL, got %d", code)
	}
	if !strings.Contains(body, "shared-content") {
		t.Error("alice's gist content missing for bob")
	}
	if strings.Contains(body, `/edit/`+id) {
		t.Error("edit link should be hidden for non-owners")
	}
	if strings.Contains(body, `action="/delete/`+id+`"`) {
		t.Error("delete form should be hidden for non-owners")
	}

	// Bob may view but must not edit or delete alice's gist.
	if code, _ := b.get("/edit/" + id); code != http.StatusForbidden {
		t.Errorf("bob GET /edit: expected 403, got %d", code)
	}
	v := url.Values{"description": {"hijacked"}}
	v.Set("file0_name", "a.md")
	v.Set("file0_content", "hijacked")
	resp := b.raw("POST", "/edit/"+id, strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob POST /edit: expected 403, got %d", resp.StatusCode)
	}
	resp = b.raw("POST", "/delete/"+id, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob POST /delete: expected 403, got %d", resp.StatusCode)
	}

	// Cross-user versions are readable too (versions dropdown needs them).
	if code, _ = b.get("/gist/" + id + "/versions"); code != 200 {
		t.Errorf("bob GET versions of alice's gist: expected 200, got %d", code)
	}
}

func TestContentRendering(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)

	codeSrc := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	mdSrc := "# Title\n\n" +
		"Inline $x_i + y_i$ math.\n\n" +
		"$$\n\\frac{1}{2}\n$$\n\n" +
		"Not math in code: `$notmath$`.\n\n" +
		"```mermaid\ngraph TD\n```\n\n" +
		"```go\nvar x = 1\n```\n"
	plainSrc := "just some plain text <b>not bold</b>\n"

	id := createGist(t, b, "Render", [][2]string{
		{"code.go", codeSrc},
		{"doc.md", mdSrc},
		{"notes.unknownext", plainSrc},
	})
	code, body := b.get("/gist/" + id)
	if code != 200 {
		t.Fatalf("view: got %d", code)
	}

	// Detail page titles the gist with its owner's localpart.
	if !strings.Contains(body, `<span class="title-owner">alice / </span>`) {
		t.Error("owner prefix missing from gist title")
	}
	// Code file: highlighted by extension, with line numbers.
	if !strings.Contains(body, `class="chroma"`) {
		t.Error("code.go not syntax-highlighted")
	}
	// Markdown: math survived (underscores not eaten by emphasis),
	// restored with canonical TeX delimiters.
	if !strings.Contains(body, `\(x_i + y_i\)`) {
		t.Error("inline math mangled")
	}
	if !strings.Contains(body, `\frac{1}{2}`) {
		t.Error("display math mangled")
	}
	// Markdown fenced go block highlighted.
	if !strings.Contains(body, "var") || !strings.Contains(body, "chroma") {
		t.Error("markdown go fence not highlighted")
	}
	// Mermaid fence emitted as a mermaid block.
	if !strings.Contains(body, `<pre class="mermaid">`) || !strings.Contains(body, "graph TD") {
		t.Error("mermaid fence not emitted")
	}
	// MathJax + Mermaid scripts loaded on this page. Asset URLs are
	// content-addressed (web.go hashedName): tex-svg.<hash>.js.
	if !texSVGURL.MatchString(body) {
		t.Error("MathJax script not loaded on math page")
	}
	if !mermaidURL.MatchString(body) {
		t.Error("Mermaid script not loaded on diagram page")
	}

	// Plain-text file with an unknown extension: escaped, no highlighting.
	if !strings.Contains(body, "just some plain text") {
		t.Error("plain text missing")
	}
	if strings.Contains(body, "<b>not bold</b>") {
		t.Error("unknown-ext file not escaped")
	}
}

func TestMathAndMermaidScriptsConditional(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	// A gist with no math or diagrams must not ship the heavy vendor JS.
	id := createGist(t, b, "Plain", [][2]string{
		{"a.md", "# no math here\n\njust prose\n"},
		{"b.go", "package x\n"},
	})
	_, body := b.get("/gist/" + id)
	if texSVGURL.MatchString(body) {
		t.Error("MathJax loaded on page without math")
	}
	if mermaidURL.MatchString(body) {
		t.Error("Mermaid loaded on page without diagrams")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	for _, path := range []string{"/static/app.css", "/static/app.js", "/static/chroma.css", "/static/vendor/tex-svg.js", "/static/vendor/mermaid.min.js"} {
		code, _ := b.get(path)
		if code != 200 {
			t.Errorf("GET %s: got %d, want 200", path, code)
		}
	}
	for _, path := range []string{"/static/fonts/inter-var.woff2", "/static/fonts/jetbrainsmono-var.woff2"} {
		resp, err := b.c.Get(appURL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: got %d, want 200", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("GET %s: Content-Type = %q, want font/woff2", path, ct)
		}
	}
	// Pages reference content-hashed assets, served cache-immutable.
	code, body := b.get("/gists")
	if code != 200 {
		t.Fatalf("GET / = %d, want 200", code)
	}
	hashed := regexp.MustCompile(`/static/app\.[0-9a-f]{10}\.css`).FindString(body)
	if hashed == "" {
		t.Fatal("list page references no content-hashed app.css")
	}
	resp, err := b.c.Get(appURL + hashed)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET %s: got %d, want 200", hashed, resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("GET %s: Cache-Control = %q, want immutable", hashed, cc)
	}
}

func TestRawEndpoints(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Raw", [][2]string{
		{"first.go", "FIRST-RAW"},
		{"two words.md", "SECOND-RAW"},
	})

	// Gist-level raw serves the first file.
	code, body := b.get("/raw/" + id)
	if code != 200 || body != "FIRST-RAW" {
		t.Errorf("raw first file: %d %q", code, body)
	}
	// Named file (URL-encoded space).
	code, body = b.get("/raw/" + id + "/two%20words.md")
	if code != 200 || body != "SECOND-RAW" {
		t.Errorf("raw named file: %d %q", code, body)
	}
	// Unknown file or gist: 404.
	if code, _ := b.get("/raw/" + id + "/nope.txt"); code != http.StatusNotFound {
		t.Errorf("raw unknown file: got %d, want 404", code)
	}
	if code, _ := b.get("/raw/does-not-exist"); code != http.StatusNotFound {
		t.Errorf("raw unknown gist: got %d, want 404", code)
	}

	// Bearer auth without any cookies: curl-style access with the id_token.
	req, err := http.NewRequest("GET", appURL+"/raw/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.idToken())
	resp, err := (&http.Client{}).Do(req) // no jar: cookie-less on purpose
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(raw) != "FIRST-RAW" {
		t.Errorf("raw with Bearer auth: %d %q", resp.StatusCode, raw)
	}

	// The view page offers per-file Raw and Copy; there is no gist-level
	// Raw link (it silently meant "first file").
	_, page := b.get("/gist/" + id)
	if !strings.Contains(page, `href="/raw/`+id+`/first.go"`) {
		t.Error("view missing per-file Raw link")
	}
	if strings.Contains(page, `href="/raw/`+id+`"`) {
		t.Error("view must not link the gist-level raw URL")
	}
	if !strings.Contains(page, "copy-file") {
		t.Error("view missing copy buttons")
	}
	if !strings.Contains(page, `data-copy-url="/raw/`+id+"/") {
		t.Error("file copy buttons missing data-copy-url")
	}
}

func TestMarkdownFencesHighlighted(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "Fences", [][2]string{{"doc.md",
		"```bash title=shell\necho hello\n```\n\n```totallymadeup\nplain\n```\n"}})
	_, body := b.get("/gist/" + id)
	// The bash fence highlights even with info-string attributes; the
	// unknown language keeps its plain fallback.
	if !strings.Contains(body, `class="chroma"`) {
		t.Error("bash fence in markdown not highlighted")
	}
	if !strings.Contains(body, `class="fallback"`) {
		t.Error("unknown-language fence lost its plain fallback")
	}
	// Both fences carry the wrapper the copy buttons attach to.
	if n := strings.Count(body, `<div class="codeblock">`); n != 2 {
		t.Errorf("codeblock wrappers = %d, want 2", n)
	}
	if !strings.Contains(plainText(body), "echo hello") {
		t.Error("fence content missing from rendered page")
	}
	// The shared copy script carries the button behavior; it is served
	// under a content hash for cache immutability.
	if !regexp.MustCompile(`src="/static/app\.[0-9a-f]{10}\.js"`).MatchString(body) {
		t.Error("shared copy script not loaded on view page")
	}
}

func TestCSRFEnforcedOnPOSTs(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)

	v := url.Values{"description": {"NoCSRF"}}
	v.Set("file0_name", "a.md")
	v.Set("file0_content", "x")

	// Missing token.
	resp := b.raw("POST", "/create", strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("create without csrf token: got %d, want 403", resp.StatusCode)
	}

	// Wrong token.
	v.Set("csrf_token", "not-the-token")
	resp = b.raw("POST", "/create", strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("create with wrong csrf token: got %d, want 403", resp.StatusCode)
	}

	// Delete without token must not delete.
	id := createGist(t, b, "Keeper", [][2]string{{"a.md", "stay"}})
	resp = b.raw("POST", "/delete/"+id, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("delete without csrf token: got %d, want 403", resp.StatusCode)
	}
	if code, body := b.get("/gist/" + id); code != 200 || !strings.Contains(body, "stay") {
		t.Error("gist was deleted despite missing csrf token")
	}

	// The rendered forms carry the hidden field matching the cookie.
	_, page := b.get("/")
	if !strings.Contains(page, `name="csrf_token" value="`+b.csrfToken(t)+`"`) {
		t.Error("create form missing csrf hidden field")
	}
	if code, _ := b.get("/edit/" + id); code != 200 {
		t.Errorf("GET edit form for owner: %d", code)
	}
}

func TestOversizedRequestRejected(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)

	// Just over the 16 MiB body cap: rejected with 413 before storage.
	v := url.Values{
		"title":         {"TooBig"},
		"csrf_token":    {b.csrfToken(t)},
		"file0_name":    {"big.txt"},
		"file0_content": {strings.Repeat("x", 16<<20)},
	}
	resp := b.raw("POST", "/create", strings.NewReader(v.Encode()))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized create: got %d, want 413 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "16 MiB") {
		t.Errorf("oversized create error not friendly: %q", body)
	}

	// A normal create right after still works.
	createGist(t, b, "AfterBig", [][2]string{{"a.md", "fine"}})
}

func TestListPageParity(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	first := createGist(t, b, "Beta One", [][2]string{{"a.go", "x"}, {"b.go", "y"}})
	second := createGist(t, b, "alpha two", [][2]string{{"c.go", "z"}})

	_, body := b.get("/gists")

	// Newest gist first (server-side sort by UpdatedAt); rows are links now.
	newIdx := strings.Index(body, `href="/gist/`+second+`"`)
	oldIdx := strings.Index(body, `href="/gist/`+first+`"`)
	if newIdx < 0 || oldIdx < 0 {
		t.Fatal("gist rows missing")
	}
	if newIdx > oldIdx {
		t.Error("list is not sorted newest-first")
	}
	// Search/sort keys ride on the row anchors; no file count anymore.
	if !strings.Contains(body, `class="gist-row" href="/gist/`+first+`" data-name="a.go" data-updated="`) {
		t.Error("gist row missing data-name/data-updated")
	}
	if strings.Contains(body, "data-files=") {
		t.Error("list still renders file counts")
	}
	if strings.Contains(body, `/delete/`) {
		t.Error("list must not carry delete forms anymore")
	}
	if !strings.Contains(body, `class="gist-desc">Beta One<`) {
		t.Error("description not rendered under gist name")
	}
	// Relative-time element, search box, no-match row.
	for _, want := range []string{`class="gist-date rel-time"`, `id="gist-search"`, `id="no-match"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list page missing %s", want)
		}
	}
	// Shared layout: header nav present on the list page.
	if !strings.Contains(body, `class="site-head"`) || !strings.Contains(body, `href="/gists"`) {
		t.Error("shared header/nav missing on list page")
	}
}

// Asset URLs are content-addressed by web.go (hash inserted before the
// extension), so script gating must be matched, not substringed.
var (
	texSVGURL  = regexp.MustCompile(`tex-svg\.[0-9a-f]{10}\.js`)
	mermaidURL = regexp.MustCompile(`mermaid\.min\.[0-9a-f]{10}\.js`)
)

// filesRegion returns the server-rendered file entries on the edit page:
// everything inside #files, up to the toolbar. The inline addFile script
// holds a template string with the same markup, so markup counts must be
// scoped to this region.
func filesRegion(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `id="files">`)
	if start < 0 {
		t.Fatal("edit page #files region not found")
	}
	rest := body[start:]
	end := strings.Index(rest, `<p class="toolbar">`)
	if end < 0 {
		t.Fatal("edit page toolbar not found")
	}
	return rest[:end]
}

func TestEditPageTabVisibility(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "tabs", [][2]string{{"a.md", "x"}, {"b.go", "y"}})
	_, body := b.get("/edit/" + id)
	region := filesRegion(t, body)
	if n := strings.Count(region, `class="tabs">`); n != 1 {
		t.Errorf("visible tab pairs = %d, want 1 (the .md entry)", n)
	}
	if n := strings.Count(region, `class="tabs hidden">`); n != 1 {
		t.Errorf("hidden tab pairs = %d, want 1 (the .go entry)", n)
	}
	// Create page: the unnamed starter entry shows no tabs.
	if _, body = b.get("/"); strings.Count(filesRegion(t, body), `class="tabs">`) != 0 {
		t.Error("create page shows Preview tabs for an unnamed file")
	}
}

func TestEditFormButtonTabOrder(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	id := createGist(t, b, "taborder", [][2]string{{"a.md", "x"}, {"b.go", "y"}})
	// Inside a file row Tab must walk name input -> textarea: every button
	// (Edit/Preview/Remove) is click-only, and no element in the row
	// carries any other tabindex.
	for _, page := range []string{"/edit/" + id, "/"} {
		_, body := b.get(page)
		region := filesRegion(t, body)
		if strings.Count(region, "<button") != strings.Count(region, `tabindex="-1"`) {
			t.Errorf("%s: some file-row button stays in the tab order", page)
		}
		if strings.Count(region, "tabindex=") != strings.Count(region, `tabindex="-1"`) {
			t.Errorf("%s: unexpected tabindex inside file entries", page)
		}
		// One reorder grip per entry (JS-gated: server renders them hidden).
		if strings.Count(region, `class="grip`) != strings.Count(region, `<div class="file-entry">`) {
			t.Errorf("%s: grip count does not match entry count", page)
		}
	}
}

// TestPresignSpike validates the direct-upload primitives against s3mock:
// presigned PUT (with a server-pinned Content-Type), HEAD verification, and
// presigned GET. If this fails, the browser-upload architecture does not.
func TestPresignSpike(t *testing.T) {
	resetStore(t)
	ctx := context.Background()
	cl, err := dolmens3.NewClient(ctx, appCfg.S3)
	if err != nil {
		t.Fatal(err)
	}
	body := "presign-spike-bytes"
	key := fmt.Sprintf("uploads/spike/%d.png", time.Now().UnixNano())

	putURL, err := cl.PresignPut(ctx, key, "image/png", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	req, err := http.NewRequest("PUT", putURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT presigned: %v", err)
	}
	putBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT presigned: %d %s", resp.StatusCode, putBody)
	}

	size, mime, err := cl.HeadObject(ctx, key)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if size != int64(len(body)) || mime != "image/png" {
		t.Errorf("HEAD = size %d mime %q, want %d image/png", size, mime, len(body))
	}

	getURL, err := cl.PresignGet(ctx, key, "", time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	gresp, err := http.Get(getURL)
	if err != nil {
		t.Fatalf("GET presigned: %v", err)
	}
	defer gresp.Body.Close()
	got, _ := io.ReadAll(gresp.Body)
	if gresp.StatusCode != http.StatusOK || string(got) != body {
		t.Errorf("presigned GET: %d %q", gresp.StatusCode, got)
	}
	if ct := gresp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("presigned GET Content-Type = %q, want image/png", ct)
	}

	// Informational CORS report: browser uploads need bucket CORS configured;
	// Go's http client does not enforce it, so log what s3mock says.
	objURL := putURL
	if i := strings.IndexByte(objURL, '?'); i >= 0 {
		objURL = objURL[:i]
	}
	probe, err := http.NewRequest("OPTIONS", objURL, nil)
	if err == nil {
		probe.Header.Set("Origin", appURL)
		probe.Header.Set("Access-Control-Request-Method", "PUT")
		if p, err := http.DefaultClient.Do(probe); err == nil {
			t.Logf("CORS preflight: %d ACAO=%q ACAM=%q ACAH=%q", p.StatusCode,
				p.Header.Get("Access-Control-Allow-Origin"),
				p.Header.Get("Access-Control-Allow-Methods"),
				p.Header.Get("Access-Control-Allow-Headers"))
			p.Body.Close()
		}
	}
}

func TestUploadEndpoint(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)
	post := func(name, csrf string) (int, struct {
		Key         string `json:"key"`
		URL         string `json:"url"`
		ContentType string `json:"contentType"`
	}) {
		v := url.Values{"name": {name}, "csrf_token": {csrf}}
		resp := b.raw("POST", "/upload", strings.NewReader(v.Encode()))
		defer resp.Body.Close()
		var out struct {
			Key         string `json:"key"`
			URL         string `json:"url"`
			ContentType string `json:"contentType"`
		}
		if resp.StatusCode == http.StatusOK {
			json.NewDecoder(resp.Body).Decode(&out)
		}
		return resp.StatusCode, out
	}

	code, up := post("pic.png", b.csrfToken(t))
	if code != http.StatusOK || up.ContentType != "image/png" ||
		!regexp.MustCompile(`^uploads/[0-9a-f]{16}/`).MatchString(up.Key) {
		t.Fatalf("upload: %d %+v", code, up)
	}
	// The presigned PUT must work with exactly the pinned content type.
	req, err := http.NewRequest("PUT", up.URL, strings.NewReader("img-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT presigned: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("PUT presigned = %d, want 200", resp.StatusCode)
	}

	if code, up := post("mystery.bin", b.csrfToken(t)); code != http.StatusOK || up.ContentType != "application/octet-stream" {
		t.Errorf("non-renderable upload: %d %q, want octet-stream", code, up.ContentType)
	}
	if code, _ := post("", b.csrfToken(t)); code != http.StatusBadRequest {
		t.Errorf("empty name: %d, want 400", code)
	}
	resp = b.raw("POST", "/upload", strings.NewReader(url.Values{"name": {"x.png"}, "csrf_token": {"nope"}}.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bad csrf: %d, want 403", resp.StatusCode)
	}
}

// uploadBlob asks /upload for a slot and PUTs the bytes to S3 with the
// pinned content type, like the browser would. Returns the key.
func uploadBlob(t *testing.T, b *browser, name, content string) string {
	t.Helper()
	v := url.Values{"name": {name}, "csrf_token": {b.csrfToken(t)}}
	resp := b.raw("POST", "/upload", strings.NewReader(v.Encode()))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload %s: %d", name, resp.StatusCode)
	}
	var out struct {
		Key         string `json:"key"`
		URL         string `json:"url"`
		ContentType string `json:"contentType"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	req, err := http.NewRequest("PUT", out.URL, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", out.ContentType)
	presp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", name, err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s: %d", name, presp.StatusCode)
	}
	return out.Key
}

func createWithBlob(t *testing.T, b *browser, name, key string) string {
	t.Helper()
	v := url.Values{"csrf_token": {b.csrfToken(t)}, "file0_name": {name}, "file0_key": {key}}
	resp := b.raw("POST", "/create", strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("create blob gist: %d", resp.StatusCode)
	}
	return strings.TrimPrefix(resp.Header.Get("Location"), "/gist/")
}

func TestBlobRoundTrip(t *testing.T) {
	resetStore(t)
	b := loginAs(t, alice)

	key := uploadBlob(t, b, "pic.png", "PNGBYTES")
	id := createWithBlob(t, b, "pic.png", key)
	_, body := b.get("/gist/" + id)
	if !strings.Contains(body, `<img class="blob blob-image" src="/raw/`+id+`/pic.png"`) {
		t.Error("image embed missing")
	}

	resp := b.raw("GET", "/raw/"+id+"/pic.png", nil)
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.Contains(loc, "X-Amz-Signature") {
		t.Fatalf("raw blob redirect: %d %q", resp.StatusCode, loc)
	}
	gresp, err := http.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	defer gresp.Body.Close()
	got, _ := io.ReadAll(gresp.Body)
	if gresp.StatusCode != http.StatusOK || string(got) != "PNGBYTES" {
		t.Errorf("presigned GET: %d %q", gresp.StatusCode, got)
	}
	if ct := gresp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("presigned GET Content-Type = %q", ct)
	}

	// Editing a blob gist without touching the file must preserve it:
	// the edit form round-trips the key in a hidden field.
	code, body := b.get("/edit/" + id)
	if code != 200 || !strings.Contains(body, `name="file0_key" value="`+key[:20]) ||
		!strings.Contains(body, `class="attach-badge"`) ||
		!strings.Contains(body, `placeholder="File content" class="hidden"`) {
		t.Errorf("edit page missing blob state: %d", code)
	}
	resp = b.raw("POST", "/edit/"+id, strings.NewReader(url.Values{
		"csrf_token": {b.csrfToken(t)}, "file0_name": {"pic.png"}, "file0_key": {key},
	}.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("edit blob: %d", resp.StatusCode)
	}
	if _, body = b.get("/gist/" + id); !strings.Contains(body, `<img class="blob blob-image"`) {
		t.Error("blob lost through edit round-trip")
	}

	// Download-only blob: filename rides in the presigned redirect.
	dkey := uploadBlob(t, b, "mystery.bin", "BINARY")
	did := createWithBlob(t, b, "mystery.bin", dkey)
	_, body = b.get("/gist/" + did)
	if !strings.Contains(body, `class="blob-file"`) || !strings.Contains(body, "(6 B)") {
		t.Error("download-only blob row missing")
	}
	resp = b.raw("GET", "/raw/"+did+"/mystery.bin", nil)
	loc = resp.Header.Get("Location")
	resp.Body.Close()
	if !strings.Contains(loc, "response-content-disposition") {
		t.Errorf("octet-stream redirect lacks content disposition: %q", loc)
	}

	// Foreign and never-uploaded keys are rejected at save time.
	bob := loginAs(t, bob)
	v := url.Values{"csrf_token": {bob.csrfToken(t)}, "file0_name": {"steal.png"}, "file0_key": {key}}
	resp = bob.raw("POST", "/create", strings.NewReader(v.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("cross-user key: %d, want 400", resp.StatusCode)
	}
	fresh := struct {
		Key string `json:"key"`
	}{}
	uresp := b.raw("POST", "/upload", strings.NewReader(url.Values{"name": {"gone.png"}, "csrf_token": {b.csrfToken(t)}}.Encode()))
	json.NewDecoder(uresp.Body).Decode(&fresh)
	uresp.Body.Close()
	v2 := url.Values{"csrf_token": {b.csrfToken(t)}, "file0_name": {"gone.png"}, "file0_key": {fresh.Key}}
	resp = b.raw("POST", "/create", strings.NewReader(v2.Encode()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("never-uploaded key: %d, want 400", resp.StatusCode)
	}
}
