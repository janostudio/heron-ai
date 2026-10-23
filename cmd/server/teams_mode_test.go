package main

import (
	"archive/tar"
	"compress/gzip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtractArchiveKeepsExecutableBit covers an install dropping the mode: a
// packaged skill's run.sh arrived as 0644 and could not be executed, while an
// archive shipping 0777 must not be able to place world-writable files either.
func TestExtractArchiveKeepsExecutableBit(t *testing.T) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	entries := []struct {
		name    string
		mode    int64
		content string
	}{
		{name: "skills/demo/run.sh", mode: 0o755, content: "#!/bin/sh\necho hi\n"},
		{name: "skills/demo/SKILL.md", mode: 0o644, content: "# demo\n"},
		{name: "skills/demo/world-writable.sh", mode: 0o777, content: "#!/bin/sh\n"},
	}
	for _, entry := range entries {
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(entry.content)), Typeflag: tar.TypeReg,
		}))
		_, err := writer.Write([]byte(entry.content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, gzipWriter.Close())

	archive := filepath.Join(t.TempDir(), "package.tar.gz")
	require.NoError(t, os.WriteFile(archive, buffer.Bytes(), 0o644))

	target := t.TempDir()
	require.NoError(t, extractTarGz(archive, target))

	script, err := os.Stat(filepath.Join(target, "skills", "demo", "run.sh"))
	require.NoError(t, err)
	require.NotZero(t, script.Mode().Perm()&0o100, "an executable script must stay executable")

	doc, err := os.Stat(filepath.Join(target, "skills", "demo", "SKILL.md"))
	require.NoError(t, err)
	require.Zero(t, doc.Mode().Perm()&0o111, "a document must not become executable")

	writable, err := os.Stat(filepath.Join(target, "skills", "demo", "world-writable.sh"))
	require.NoError(t, err)
	require.Zero(t, writable.Mode().Perm()&0o022, "an archive must not place group/world-writable files")
}
