package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStore_ReadWrite(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	data := []byte("hello world")
	path := "test.txt"

	err := fs.Write(path, data)
	require.NoError(t, err)

	readData, err := fs.Read(path)
	require.NoError(t, err)
	assert.Equal(t, data, readData)
}

func TestFileStore_ReadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	_, err := fs.Read("nonexistent.txt")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileStore_Append(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	path := "append.txt"

	err := fs.Append(path, []byte("line1\n"))
	require.NoError(t, err)

	err = fs.Append(path, []byte("line2\n"))
	require.NoError(t, err)

	data, err := fs.Read(path)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\n", string(data))
}

func TestFileStore_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	assert.False(t, fs.Exists("test.txt"))

	err := fs.Write("test.txt", []byte("data"))
	require.NoError(t, err)

	assert.True(t, fs.Exists("test.txt"))
}

func TestFileStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	err := fs.Write("a.txt", []byte("a"))
	require.NoError(t, err)
	err = fs.Write("b.txt", []byte("b"))
	require.NoError(t, err)
	err = fs.Write(filepath.Join("subdir", "c.txt"), []byte("c"))
	require.NoError(t, err)

	names, err := fs.List(".")
	require.NoError(t, err)
	assert.Len(t, names, 3)
	assert.Contains(t, names, "a.txt")
	assert.Contains(t, names, "b.txt")
	assert.Contains(t, names, "subdir")
}

func TestFileStore_ListNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	names, err := fs.List("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, names)
}

func TestFileStore_WriteCreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	err := fs.Write(filepath.Join("deep", "nested", "dir", "file.txt"), []byte("data"))
	require.NoError(t, err)

	data, err := fs.Read(filepath.Join("deep", "nested", "dir", "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(data))
}

func TestFileStore_ReadAbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)

	absPath := filepath.Join(tmpDir, "abs.txt")
	err := os.WriteFile(absPath, []byte("absolute"), 0644)
	require.NoError(t, err)

	data, err := fs.Read(absPath)
	require.NoError(t, err)
	assert.Equal(t, "absolute", string(data))
}

type testState struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func TestRunStateStore_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	store := NewRunStateStore(fs)
	ctx := context.Background()

	state := testState{Name: "test", Value: 42}
	err := store.Save(ctx, "run-001", state)
	require.NoError(t, err)

	var loaded testState
	err = store.Load(ctx, "run-001", &loaded)
	require.NoError(t, err)
	assert.Equal(t, state, loaded)
}

func TestRunStateStore_LoadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	store := NewRunStateStore(fs)
	ctx := context.Background()

	var target testState
	err := store.Load(ctx, "nonexistent", &target)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRunLog_AppendRead(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	msg1 := map[string]string{"role": "user", "content": "hello"}
	msg2 := map[string]string{"role": "assistant", "content": "hi there"}

	err := log.Append(ctx, "run-001", msg1)
	require.NoError(t, err)
	err = log.Append(ctx, "run-001", msg2)
	require.NoError(t, err)

	// Read as raw JSONL string
	data, err := fs.Read(filepath.Join(".agents", "data", "run-001", "run.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
	assert.Contains(t, string(data), "hi there")
}

func TestRunLog_ReadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	var target interface{}
	err := log.Read(ctx, "nonexistent", &target)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCheckpointManager_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	cp := NewCheckpointManager(fs)
	ctx := context.Background()

	checkpoint := map[string]interface{}{
		"run_id":    "run-001",
		"round_num": 3,
	}

	err := cp.Save(ctx, checkpoint)
	require.NoError(t, err)

	var loaded map[string]interface{}
	err = cp.Load(ctx, &loaded)
	require.NoError(t, err)
	assert.Equal(t, "run-001", loaded["run_id"])
	assert.Equal(t, float64(3), loaded["round_num"])
}

func TestCheckpointManager_LoadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileStore(tmpDir)
	cp := NewCheckpointManager(fs)
	ctx := context.Background()

	// Remove the checkpoints dir to ensure it's clean
	var target map[string]interface{}
	err := cp.Load(ctx, &target)
	assert.ErrorIs(t, err, ErrNotFound)
}
