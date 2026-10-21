package main

import (
	"archive/tar"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// newTestFileStore mirrors what production uses for read-only package access.
func newTestFileStore() storage.FileStore { return storage.NewFileStore("") }

// tarDir packs dir under prefix (empty keeps paths relative to dir), matching
// what `teams export` produces.
func tarDir(t *testing.T, dir, archive, prefix string) {
	t.Helper()
	tarEntries(t, dir, archive, prefix, nil)
}

// tarWithSymlink adds a link entry pointing outside the package. The installer
// must refuse it before following anything.
func tarWithSymlink(t *testing.T, dir, archive string) {
	t.Helper()
	tarEntries(t, dir, archive, "review-team", []tar.Header{{
		Typeflag: tar.TypeSymlink,
		Name:     "review-team/agents/security-reviewer/escape",
		Linkname: "/etc/passwd",
		Mode:     0o777,
	}})
}

func tarEntries(t *testing.T, dir, archive, prefix string, extra []tar.Header) {
	t.Helper()
	out, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}
		if d.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	}
	if err := filepath.WalkDir(dir, walk); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	for i := range extra {
		if err := tw.WriteHeader(&extra[i]); err != nil {
			t.Fatalf("write header: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
}
