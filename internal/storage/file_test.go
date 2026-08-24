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
