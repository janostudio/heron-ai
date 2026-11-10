package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default mode is replace. A model that truncates its output or misspells
// the key used to blank the file and be told it succeeded, so a missing content
// is now an error naming the mode it would have applied.
func TestWriteToolRejectsMissingContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(newTestWorkspace(t, dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("keep me"), 0o644))

	for _, mode := range []string{"", "create", "replace"} {
		params := map[string]any{"file": "target.txt"}
		if mode != "" {
			params["mode"] = mode
		}
		result, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		require.False(t, result.Success, "mode %q without content must fail", mode)
		assert.Contains(t, result.Error, "content is required")

		// The file must be untouched: refusing is only useful if it happens
		// before the write.
		data, readErr := os.ReadFile(filepath.Join(dir, "target.txt"))
		require.NoError(t, readErr)
		assert.Equal(t, "keep me", string(data))
	}
}

// An explicit empty string is a legitimate way to truncate a file. Only the
// key's absence is refused.
func TestWriteToolAllowsExplicitEmptyContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(newTestWorkspace(t, dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("keep me"), 0o644))

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":    "target.txt",
		"content": "",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	data, readErr := os.ReadFile(filepath.Join(dir, "target.txt"))
	require.NoError(t, readErr)
	assert.Empty(t, string(data))
}

func TestWriteToolRejectsNonStringContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(newTestWorkspace(t, dir))

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":    "target.txt",
		"content": 42,
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "content must be a string")
}

// Edit mode never reads content, so requiring it would break the one mode that
// exists to avoid rewriting a whole file.
func TestWriteToolEditModeNeedsNoContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(newTestWorkspace(t, dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("alpha beta"), 0o644))

	readTool := NewReadTool(newTestWorkspace(t, dir))
	read, err := readTool.Execute(context.Background(), map[string]any{"file": "target.txt"})
	require.NoError(t, err)
	require.True(t, read.Success)
	revision, _ := read.Metadata["revision"].(string)
	require.NotEmpty(t, revision)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":          "target.txt",
		"mode":          "edit",
		"base_revision": revision,
		"old_text":      "beta",
		"new_text":      "gamma",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	data, readErr := os.ReadFile(filepath.Join(dir, "target.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "alpha gamma", string(data))
}
