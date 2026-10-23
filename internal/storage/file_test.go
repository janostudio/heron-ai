package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_ReadWrite(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	data := []byte("hello world")
	require.NoError(t, fs.Write("test.txt", data))
	readData, err := fs.Read("test.txt")
	require.NoError(t, err)
	assert.Equal(t, data, readData)
}

func TestFileStore_ReadNotFound(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	_, err := fs.Read("nonexistent.txt")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileStore_Append(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	require.NoError(t, fs.Append("append.txt", []byte("line1\n")))
	require.NoError(t, fs.Append("append.txt", []byte("line2\n")))
	data, err := fs.Read("append.txt")
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\n", string(data))
}

func TestFileStore_Exists(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	assert.False(t, fs.Exists("test.txt"))
	require.NoError(t, fs.Write("test.txt", []byte("data")))
	assert.True(t, fs.Exists("test.txt"))
}

func TestFileStore_List(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	require.NoError(t, fs.Write("a.txt", []byte("a")))
	require.NoError(t, fs.Write("b.txt", []byte("b")))
	require.NoError(t, fs.Write(filepath.Join("subdir", "c.txt"), []byte("c")))
	names, err := fs.List(".")
	require.NoError(t, err)
	assert.Len(t, names, 3)
	assert.Contains(t, names, "a.txt")
	assert.Contains(t, names, "b.txt")
	assert.Contains(t, names, "subdir")
}

func TestFileStore_ListNonExistent(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	names, err := fs.List("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, names)
}

func TestFileStore_WriteCreatesDirectories(t *testing.T) {
	fs := NewFileStore(t.TempDir())
	path := filepath.Join("deep", "nested", "dir", "file.txt")
	require.NoError(t, fs.Write(path, []byte("data")))
	data, err := fs.Read(path)
	require.NoError(t, err)
	assert.Equal(t, "data", string(data))
}

func TestFileStore_ReadAbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	absPath := filepath.Join(tmpDir, "abs.txt")
	require.NoError(t, os.WriteFile(absPath, []byte("absolute"), 0644))
	data, err := fs.Read(absPath)
	require.NoError(t, err)
	assert.Equal(t, "absolute", string(data))
}

// TestWriteIsAtomicAndLeavesNoTempFiles covers the in-place document writes the
// engine relies on (state.md, settings, indexes): os.WriteFile truncated first,
// so a crash left a file that no longer parsed. The atomic swap leaves either
// the old content or the new one, and no temp files behind.
func TestWriteIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	path := filepath.Join(".agents", "data", "agents", "a", "state", "state.md")

	require.NoError(t, store.Write(path, []byte("first")))
	require.NoError(t, store.Write(path, []byte("second version, longer than the first")))

	data, err := store.Read(path)
	require.NoError(t, err)
	require.Equal(t, "second version, longer than the first", string(data))

	entries, err := os.ReadDir(filepath.Join(dir, ".agents", "data", "agents", "a", "state"))
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".heron-write-",
			"the atomic write must not leave temp files behind")
	}
}
