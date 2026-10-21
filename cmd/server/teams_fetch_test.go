package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveRemoteSourceGitHub(t *testing.T) {
	cases := []struct{ in, url, ref string }{
		{"https://github.com/acme/review-team", "https://codeload.github.com/acme/review-team/tar.gz/HEAD", "HEAD"},
		{"https://github.com/acme/review-team#v1.2.0", "https://codeload.github.com/acme/review-team/tar.gz/v1.2.0", "v1.2.0"},
		{"https://github.com/acme/review-team.git", "https://codeload.github.com/acme/review-team/tar.gz/HEAD", "HEAD"},
		{"https://github.com/acme/review-team/#main", "https://codeload.github.com/acme/review-team/tar.gz/main", "main"},
	}
	for _, c := range cases {
		got, err := resolveRemoteSource(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.URL != c.url || got.Ref != c.ref {
			t.Fatalf("%s: got %+v, want url=%s ref=%s", c.in, got, c.url, c.ref)
		}
	}
}

func TestResolveRemoteSourceDirectArchive(t *testing.T) {
	got, err := resolveRemoteSource("https://files.example.com/teams/review.tar.gz")
	if err != nil {
		t.Fatalf("tar.gz: %v", err)
	}
	if got.URL != "https://files.example.com/teams/review.tar.gz" || got.Ref != "" {
		t.Fatalf("unexpected spec: %+v", got)
	}

	got, err = resolveRemoteSource("https://files.example.com/teams/review.tgz")
	if err != nil {
		t.Fatalf("tgz: %v", err)
	}
	if got.URL != "https://files.example.com/teams/review.tgz" {
		t.Fatalf("unexpected spec: %+v", got)
	}
}

func TestResolveRemoteSourceRejectsPlainHTTPAndOtherSchemes(t *testing.T) {
	for _, in := range []string{
		"http://github.com/acme/review-team",
		"http://files.example.com/review.tar.gz",
		"git@github.com:acme/review-team.git",
		"ssh://github.com/acme/review-team",
		"git://github.com/acme/review-team",
		"ftp://files.example.com/review.tar.gz",
	} {
		if _, err := resolveRemoteSource(in); err == nil {
			t.Fatalf("%s: expected an error", in)
		}
	}
	if _, err := resolveRemoteSource("https://github.com/acme"); err == nil || !strings.Contains(err.Error(), "<owner>/<repo>") {
		t.Fatalf("incomplete GitHub path: %v", err)
	}
	if _, err := resolveRemoteSource("https://files.example.com/review.zip"); err == nil || !strings.Contains(err.Error(), ".tar.gz") {
		t.Fatalf("non-archive URL: %v", err)
	}
}

func TestIsRemoteSource(t *testing.T) {
	for _, in := range []string{"https://x/y", "http://x/y"} {
		if !isRemoteSource(in) {
			t.Fatalf("%s should be remote", in)
		}
	}
	for _, in := range []string{"./pkg", "/abs/pkg", "pkg", "pkg.tar.gz", "git@github.com:o/r"} {
		if isRemoteSource(in) {
			t.Fatalf("%s should not be remote", in)
		}
	}
}

func TestFetchCacheDirIsStableAndRefSensitive(t *testing.T) {
	base := t.TempDir()
	a := fetchCacheDir(base, remoteSpec{URL: "https://x/a.tar.gz", Ref: "main"})
	b := fetchCacheDir(base, remoteSpec{URL: "https://x/a.tar.gz", Ref: "main"})
	c := fetchCacheDir(base, remoteSpec{URL: "https://x/a.tar.gz", Ref: "dev"})
	if a != b {
		t.Fatal("cache dir must be a pure function of the spec")
	}
	if a == c {
		t.Fatal("refs must not share a cache entry")
	}
	if filepath.Dir(a) != base {
		t.Fatalf("cache dir escaped its base: %s", a)
	}
}

// trustedServer is an httptest TLS server plus a client that accepts its
// certificate. The code under test keeps its https-only rule while the test
// talks to 127.0.0.1.
func trustedServer(t *testing.T, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	client := srv.Client()
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	t.Cleanup(srv.Close)
	return srv, client
}

// archiveServer serves one tarball, counts requests, and returns the https URL
// an installer would be given.
func archiveServer(t *testing.T, pkgDir string) (string, *http.Client, *int32) {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "pkg.tar.gz")
	tarDir(t, pkgDir, archive, "review-team")
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var hits int32
	srv, client := trustedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(data)
	}))
	return srv.URL + "/review.tar.gz", client, &hits
}

// cacheDirFor computes the cache entry the code will use for source, so a test
// can assert on the exact directory without re-deriving the hash.
func cacheDirFor(t *testing.T, cache, source string) string {
	t.Helper()
	spec, err := resolveRemoteSource(source)
	if err != nil {
		t.Fatalf("resolve %s: %v", source, err)
	}
	return fetchCacheDir(cache, spec)
}

func TestFetchRemotePackageCachesAndRefreshes(t *testing.T) {
	source, client, hits := archiveServer(t, writeTestPackage(t))
	cache := t.TempDir()

	var out bytes.Buffer
	first, err := fetchRemotePackage(cache, source, false, client, &out)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first, teamManifestFile)); err != nil {
		t.Fatalf("manifest not unpacked: %v", err)
	}
	entryDir := cacheDirFor(t, cache, source)
	// The archive wraps the package in review-team/, so the package dir is
	// nested inside the cache entry rather than being the entry itself.
	if want := filepath.Join(entryDir, "pkg", "review-team"); first != want {
		t.Fatalf("package dir = %s, want %s", first, want)
	}
	meta := readFetchMetadataForTest(t, entryDir)
	if meta.URL != source || meta.FetchedAt == "" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	// Second call must be served from the cache: same directory, no request.
	out.Reset()
	second, err := fetchRemotePackage(cache, source, false, client, &out)
	if err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if second != first {
		t.Fatalf("cache hit returned %s, want %s", second, first)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("cache hit made a network request (hits=%d)", got)
	}
	if !strings.Contains(out.String(), "Using cached download") {
		t.Fatalf("cache hit was not reported: %q", out.String())
	}

	// --refresh must go back to the server even though the entry exists.
	out.Reset()
	if _, err := fetchRemotePackage(cache, source, true, client, &out); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("refresh did not re-download (hits=%d)", got)
	}
}

func TestFetchRemotePackageRejectsPlainHTTP(t *testing.T) {
	cache := t.TempDir()
	_, err := fetchRemotePackage(cache, "http://example.com/review.tar.gz", false, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https error, got %v", err)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("rejected source wrote to the cache: %v", entries)
	}
}

func TestFetchRemotePackageReportsHTTPStatus(t *testing.T) {
	srv, client := trustedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	source := srv.URL + "/review.tar.gz"

	cache := t.TempDir()
	_, err := fetchRemotePackage(cache, source, false, client, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected status in error, got %v", err)
	}
	// A failed download must not leave an entry that later reads as a hit.
	if _, statErr := os.Stat(filepath.Join(cacheDirFor(t, cache, source), "pkg")); !os.IsNotExist(statErr) {
		t.Fatalf("failed fetch left a package dir: %v", statErr)
	}
}

func TestFetchRemotePackageRejectsCorruptArchive(t *testing.T) {
	srv, client := trustedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a tarball"))
	}))
	// The bytes are not a gzip stream, so extraction is what fails; the
	// resolver is satisfied because the URL still ends in .tar.gz.
	source := srv.URL + "/review.tar.gz"

	cache := t.TempDir()
	if _, err := fetchRemotePackage(cache, source, false, client, io.Discard); err == nil {
		t.Fatal("expected an extraction error")
	}
	entryDir := cacheDirFor(t, cache, source)
	for _, leftover := range []string{"pkg", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(entryDir, leftover)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind after a failed fetch", leftover)
		}
	}
}

func TestDownloadArchiveSendsGitHubTokenOnlyToGitHub(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token")
	var auth []string
	srv, client := trustedServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Write([]byte("ok"))
	}))

	dest := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := downloadArchive(client, srv.URL+"/a.tar.gz", dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(auth) != 1 || auth[0] != "" {
		t.Fatalf("token leaked to a non-GitHub host: %v", auth)
	}
	for _, host := range []string{"https://codeload.github.com/a/b/tar.gz/HEAD", "https://github.com/a/b"} {
		if !isGitHubHost(host) {
			t.Fatalf("%s must be recognized as GitHub", host)
		}
	}
	for _, host := range []string{"https://evil.example.com/github.com", "https://github.com.evil.test/a"} {
		if isGitHubHost(host) {
			t.Fatalf("%s must not be treated as GitHub", host)
		}
	}
}

// TestInstallFromRemoteURL runs the whole path a user hits: a URL source is
// fetched, installed under its manifest scope, and recorded with the address.
func TestInstallFromRemoteURL(t *testing.T) {
	source, client, hits := archiveServer(t, writeTestPackage(t))
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	// The installer builds its own client, so the test server's certificate is
	// trusted process-wide for the duration of the test.
	prevDefault := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prevDefault })
	tr := client.Transport.(*http.Transport).Clone()
	http.DefaultTransport = tr

	var out, errOut bytes.Buffer
	if err := runTeamsAddFromInputs(root, teamsAddInputs{
		Source: source,
		Yes:    true,
		Stdout: &out,
		Stderr: &errOut,
	}); err != nil {
		t.Fatalf("install from URL: %v\n%s", err, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile)); err != nil {
		t.Fatalf("package not installed: %v", err)
	}
	entry, ok := readIndexForTest(t, root).Installed["review-team"]
	if !ok {
		t.Fatal("index entry missing")
	}
	if entry.Source != source || entry.Remote != source {
		t.Fatalf("index did not record the remote address: %+v", entry)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("expected one download, got %d", got)
	}
	if !strings.Contains(out.String(), "Installed team package: review-team") {
		t.Fatalf("unexpected summary: %q", out.String())
	}
}

func readFetchMetadataForTest(t *testing.T, dir string) fetchMetadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta fetchMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	return meta
}

var _ = url.Parse
