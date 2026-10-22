package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fetchTimeout bounds the whole download, not each read: a server that trickles
// bytes would otherwise hold the command open forever.
const fetchTimeout = 120 * time.Second

// remoteSpec is a resolved download: the URL to GET and the ref it came from,
// which is part of the cache key so two branches of one repository do not
// overwrite each other.
type remoteSpec struct {
	URL string
	Ref string
}

// fetchMetadata is written beside the cached package so a cache hit can report
// where the files really came from and when.
type fetchMetadata struct {
	URL       string `json:"url"`
	Ref       string `json:"ref,omitempty"`
	FetchedAt string `json:"fetched_at"`
}

// isRemoteSource reports whether a teams add source is a URL rather than a
// local path. Only the scheme decides: a directory named "https:" is not a
// thing, and guessing from the shape of the string would turn a typo in a path
// into a network request.
func isRemoteSource(source string) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return true
	}
	return false
}

// resolveRemoteSource turns a user-supplied URL into the concrete archive URL.
//
// A GitHub repository page serves HTML, not an archive, so it is rewritten to
// codeload; every other https URL must already point at a tar.gz. Plain http and
// every non-https scheme are refused: a package is executable content, and
// fetching it over a channel anyone on the path can rewrite is not acceptable.
func resolveRemoteSource(source string) (remoteSpec, error) {
	u, err := url.Parse(source)
	if err != nil {
		return remoteSpec{}, fmt.Errorf("invalid source URL %q: %w", source, err)
	}
	if u.Scheme == "http" {
		return remoteSpec{}, fmt.Errorf("source must be https, not http: %s", source)
	}
	if u.Scheme != "https" {
		return remoteSpec{}, fmt.Errorf("source must be an https URL or a local path: %s", source)
	}
	if u.Host == "" {
		return remoteSpec{}, fmt.Errorf("source URL has no host: %s", source)
	}

	ref := u.Fragment
	if strings.EqualFold(u.Host, "github.com") {
		owner, repo, err := parseGitHubPath(u.Path)
		if err != nil {
			return remoteSpec{}, err
		}
		if ref == "" {
			ref = "HEAD"
		}
		return remoteSpec{
			URL: fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, repo, url.PathEscape(ref)),
			Ref: ref,
		}, nil
	}

	lower := strings.ToLower(u.Path)
	if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
		return remoteSpec{}, fmt.Errorf("source URL must point at a .tar.gz or .tgz archive: %s", source)
	}
	// The fragment is not sent to the server; dropping it keeps the request
	// identical to what the cache key was computed from.
	clean := *u
	clean.Fragment = ""
	return remoteSpec{URL: clean.String(), Ref: ref}, nil
}

// parseGitHubPath accepts /<owner>/<repo> with the optional .git suffix that
// copy-pasted clone URLs carry.
func parseGitHubPath(path string) (string, string, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("GitHub source must be https://github.com/<owner>/<repo>: %s", path)
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return "", "", fmt.Errorf("GitHub source is missing a repository name: %s", path)
	}
	return parts[0], repo, nil
}

// fetchCacheDir is the directory for one resolved spec. Hashing the URL and ref
// keeps the cache flat, so no ref (which may contain slashes) can escape it.
func fetchCacheDir(base string, spec remoteSpec) string {
	sum := sha256.Sum256([]byte(spec.URL + "@" + spec.Ref))
	return filepath.Join(base, hex.EncodeToString(sum[:]))
}

// fetchRemotePackage downloads and unpacks a remote package into the cache and
// returns the package directory.
//
// The cache is content-addressed by URL and ref, so a hit means the exact same
// bytes were already verified by an earlier install; --refresh exists for the
// case where a tag was moved, which is the one thing the key cannot see.
func fetchRemotePackage(base, source string, refresh bool, client *http.Client, stdout io.Writer) (string, error) {
	spec, err := resolveRemoteSource(source)
	if err != nil {
		return "", err
	}
	cacheDir := fetchCacheDir(base, spec)

	if !refresh {
		// unpackPackageDir is what decides the package root, and it is a
		// function of the unpacked layout, so a hit is only valid when that
		// root still carries a manifest.
		if pkgDir, err := unpackPackageDir(cacheDir); err == nil {
			fmt.Fprintf(stdout, "Using cached download: %s\n", cacheDir)
			return pkgDir, nil
		}
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	// Everything lands in a staging directory first: a failed download or a
	// corrupt archive must leave the previous cache entry untouched.
	staging, err := os.MkdirTemp(cacheDir, ".fetch-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	archive := filepath.Join(staging, "package.tar.gz")
	if err := downloadArchive(client, spec.URL, archive); err != nil {
		return "", err
	}
	unpacked := filepath.Join(staging, "pkg")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return "", err
	}
	if err := extractTarGz(archive, unpacked); err != nil {
		return "", err
	}
	if err := writeFetchMetadata(staging, fetchMetadata{
		URL:       spec.URL,
		Ref:       spec.Ref,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return "", err
	}

	// The archive itself is not kept: only the unpacked package is ever read,
	// and keeping both would double the cache for no benefit.
	metaPath := filepath.Join(cacheDir, "manifest.json")
	for _, victim := range []string{filepath.Join(cacheDir, "pkg"), metaPath} {
		if err := os.RemoveAll(victim); err != nil {
			return "", err
		}
	}
	if err := os.Rename(unpacked, filepath.Join(cacheDir, "pkg")); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(staging, "manifest.json"), metaPath); err != nil {
		return "", err
	}
	return unpackPackageDir(cacheDir)
}

// unpackPackageDir resolves the package root inside a cache entry.
//
// The tarball GitHub serves wraps everything in <repo>-<ref>/, so the entry
// directory is one level above the real package; a hand-made archive may not
// wrap at all. The manifest is the discriminator either way.
func unpackPackageDir(cacheDir string) (string, error) {
	dir := filepath.Join(cacheDir, "pkg")
	if _, err := os.Stat(filepath.Join(dir, teamManifestFile)); err == nil {
		return dir, nil
	}
	if pkgDir, err := findPackageRoot(dir); err == nil {
		return pkgDir, nil
	}
	return "", fmt.Errorf("cache entry %s has no %s", cacheDir, teamManifestFile)
}

// findPackageRoot steps into a single wrapping directory, the shape GitHub's
// tarballs use. It is the read-only half of unwrapPackageRoot and never
// deletes anything.
func findPackageRoot(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	visible := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name() == "__MACOSX" || e.Name() == ".DS_Store" {
			continue
		}
		visible = append(visible, e)
	}
	if len(visible) == 1 && visible[0].IsDir() {
		return filepath.Join(dir, visible[0].Name()), nil
	}
	return "", fmt.Errorf("no package root in %s", dir)
}

func writeFetchMetadata(dir string, meta fetchMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o644)
}

// downloadArchive fetches url into path. The token is only ever attached to
// GitHub hosts, so a redirect or a third-party archive URL can never be handed
// the user's credentials for another service.
func downloadArchive(client *http.Client, rawURL, path string) error {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && isGitHubHost(rawURL) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download %s: server returned %s", rawURL, resp.Status)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	// Copy with the same ceiling the extractor enforces: a compressed archive
	// can be smaller than what it inflates to, but this still stops a server
	// from filling the disk before that check runs.
	written, err := io.Copy(out, io.LimitReader(resp.Body, maxPackageTotalSize+1))
	closeErr := out.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxPackageTotalSize {
		return fmt.Errorf("download %s: archive is over the %d byte limit", rawURL, maxPackageTotalSize)
	}
	return nil
}

func isGitHubHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "github.com", "codeload.github.com":
		return true
	}
	return false
}

// defaultFetchCacheBase is ~/.heron/cache. It sits outside the project so a
// download is reused across checkouts, and outside ~/.cache so Windows users
// get the same path.
func defaultFetchCacheBase() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory for download cache: %w", err)
	}
	return filepath.Join(home, ".heron", "cache"), nil
}
