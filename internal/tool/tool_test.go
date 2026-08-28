package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// mockTool implements types.Tool for testing
type mockTool struct {
	name          string
	description   string
	params        map[string]any
	needsApproval bool
	executeFn     func(ctx context.Context, params map[string]any) (*types.ToolResult, error)
}

func (m *mockTool) Name() string               { return m.name }
func (m *mockTool) Description() string        { return m.description }
func (m *mockTool) Parameters() map[string]any { return m.params }
func (m *mockTool) NeedsApproval() bool        { return m.needsApproval }
func (m *mockTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	return m.executeFn(ctx, params)
}

func TestToolRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewToolRegistry()
	tool := &mockTool{name: "test", description: "a test tool"}

	reg.Register(tool)

	found, err := reg.Lookup("test")
	require.NoError(t, err)
	assert.Equal(t, "test", found.Name())

	_, err = reg.Lookup("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestToolRegistry_List(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "tool1"})
	reg.Register(&mockTool{name: "tool2"})

	tools := reg.List()
	assert.Len(t, tools, 2)
}

func TestToolRegistry_ListNames(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "tool1"})
	reg.Register(&mockTool{name: "tool2"})

	names := reg.ListNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "tool1")
	assert.Contains(t, names, "tool2")
}

func TestToolExecutor_Execute_Success(t *testing.T) {
	reg := NewToolRegistry()
	tool := &mockTool{
		name: "echo",
		executeFn: func(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
			msg, _ := params["message"].(string)
			return &types.ToolResult{Success: true, Content: msg}, nil
		},
	}
	reg.Register(tool)

	exec := NewToolExecutor(reg)
	result, err := exec.Execute(context.Background(), "echo", map[string]any{"message": "hello"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "hello", result.Content)
}

func TestToolExecutor_Execute_NotFound(t *testing.T) {
	reg := NewToolRegistry()
	exec := NewToolExecutor(reg)

	result, err := exec.Execute(context.Background(), "nonexistent", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestToolExecutor_ExecuteValidatesParameters(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockTool{
		name: "typed",
		params: map[string]any{
			"message": map[string]any{
				"type":     "string",
				"required": true,
			},
		},
		executeFn: func(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
			return &types.ToolResult{Success: true}, nil
		},
	})
	exec := NewToolExecutor(reg)

	result, err := exec.Execute(context.Background(), "typed", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, `parameter "message" is required`)

	result, err = exec.Execute(context.Background(), "typed", map[string]any{"message": 1})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, `parameter "message" must be a string`)

	result, err = exec.Execute(context.Background(), "typed", map[string]any{"message": "ok", "extra": true})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, `unexpected parameter "extra"`)
}

func TestToolExecutor_ExecuteWithApproval_Approved(t *testing.T) {
	reg := NewToolRegistry()
	tool := &mockTool{
		name:          "safe",
		needsApproval: false,
		executeFn: func(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
			return &types.ToolResult{Success: true, Content: "ok"}, nil
		},
	}
	reg.Register(tool)

	exec := NewToolExecutor(reg)
	result, err := exec.ExecuteWithApproval(context.Background(), "safe", nil)
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestToolExecutor_ExecuteWithApproval_NeedsApproval(t *testing.T) {
	reg := NewToolRegistry()
	tool := &mockTool{
		name:          "dangerous",
		needsApproval: true,
	}
	reg.Register(tool)

	exec := NewToolExecutor(reg)
	result, err := exec.ExecuteWithApproval(context.Background(), "dangerous", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires approval")
}

func TestGenerateSchema(t *testing.T) {
	tool := &mockTool{
		name: "test",
		params: map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Path to file",
			},
		},
	}

	schema := GenerateSchema(tool)
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Properties, "file")
	assert.Equal(t, "string", schema.Properties["file"].Type)
	assert.Equal(t, "Path to file", schema.Properties["file"].Description)
}

func TestGenerateSchemas(t *testing.T) {
	tools := []types.Tool{
		&mockTool{name: "t1", params: map[string]any{}},
		&mockTool{name: "t2", params: map[string]any{}},
	}

	schemas := GenerateSchemas(tools)
	assert.Len(t, schemas, 2)
}

func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "test.txt")
	err := os.WriteFile(filename, []byte("hello world"), 0644)
	require.NoError(t, err)

	tool := NewReadTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"file": "test.txt"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "hello world", result.Content)

	// Test missing file
	result, err = tool.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "required")

	// Test file not found
	result, err = tool.Execute(context.Background(), map[string]any{"file": "nonexistent.txt"})
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestReadToolSupportsLineRangeByteLimitAndRevision(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("one\ntwo\nthree\nfour\n"), 0644))
	tool := NewReadTool(dir)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":       "fixture.txt",
		"line_start": 2,
		"line_end":   3,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, "two\nthree\n", result.Content)
	assert.Equal(t, 2, result.Metadata["line_start"])
	assert.Equal(t, 3, result.Metadata["line_end"])
	assert.NotEmpty(t, result.Metadata["revision"])
	assert.Equal(t, false, result.Metadata["truncated"])

	result, err = tool.Execute(context.Background(), map[string]any{
		"file":      "fixture.txt",
		"max_bytes": 5,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.True(t, result.Metadata["truncated"].(bool))
	assert.LessOrEqual(t, len(result.Content), 5)
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":    "subdir/output.txt",
		"content": "test content",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data, err := os.ReadFile(filepath.Join(dir, "subdir/output.txt"))
	require.NoError(t, err)
	assert.Equal(t, "test content", string(data))

	// Test missing file
	result, err = tool.Execute(context.Background(), map[string]any{"content": "x"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "required")
}

func TestReadWriteQueryWorkflowOnProjectFixture(t *testing.T) {
	dir := copyDirectoryForTest(t, filepath.Join("..", "..", "examples", "simple-qa", "project"))
	readTool := NewReadTool(dir)
	writeTool := NewWriteTool(dir)
	grepTool := NewGrepTool(dir)
	globTool := NewGlobTool(dir)

	config, err := readTool.Execute(context.Background(), map[string]any{"file": "src/config.js"})
	require.NoError(t, err)
	require.True(t, config.Success, config.Error)
	assert.Contains(t, config.Content, "port: 3000")
	revision := config.Metadata["revision"].(string)

	search, err := grepTool.Execute(context.Background(), map[string]any{
		"pattern": "greeting",
		"path":    "src",
		"include": "*.js",
	})
	require.NoError(t, err)
	require.True(t, search.Success, search.Error)
	assert.Contains(t, search.Content, "src/config.js")
	assert.Contains(t, search.Content, "src/greeting.js")

	files, err := globTool.Execute(context.Background(), map[string]any{
		"pattern": "**/*.js",
	})
	require.NoError(t, err)
	require.True(t, files.Success, files.Error)
	assert.Contains(t, files.Content, "src/config.js")
	assert.Contains(t, files.Content, "src/greeting.test.js")

	updated, err := writeTool.Execute(context.Background(), map[string]any{
		"file":          "src/config.js",
		"mode":          "edit",
		"old_text":      "port: 3000",
		"new_text":      "port: 4173",
		"base_revision": revision,
	})
	require.NoError(t, err)
	require.True(t, updated.Success, updated.Error)
	assert.Equal(t, "edit", updated.Metadata["mode"])
	assert.Equal(t, 1, updated.Metadata["matched_count"])

	verified, err := readTool.Execute(context.Background(), map[string]any{"file": "src/config.js"})
	require.NoError(t, err)
	require.True(t, verified.Success)
	assert.Contains(t, verified.Content, "port: 4173")
	assert.NotEqual(t, revision, verified.Metadata["revision"])

	// Restore the fixture so the test never modifies a checked-in example.
	_, err = writeTool.Execute(context.Background(), map[string]any{
		"file":          "src/config.js",
		"mode":          "edit",
		"old_text":      "port: 4173",
		"new_text":      "port: 3000",
		"base_revision": verified.Metadata["revision"],
	})
	require.NoError(t, err)
}

func copyDirectoryForTest(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	require.NoError(t, err)
	return destination
}

func TestWriteToolEditWithRevisionAndMetadata(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("const port = 3000;\n"), 0644))
	tool := NewWriteTool(dir)

	readTool := NewReadTool(dir)
	read, err := readTool.Execute(context.Background(), map[string]any{"file": "app.js"})
	require.NoError(t, err)
	require.True(t, read.Success)
	revision, ok := read.Metadata["revision"].(string)
	require.True(t, ok)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file":          "app.js",
		"mode":          "edit",
		"old_text":      "const port = 3000;",
		"new_text":      "const port = 5173;",
		"base_revision": revision,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "edit", result.Metadata["mode"])
	require.Equal(t, 1, result.Metadata["matched_count"])

	data, err := os.ReadFile(filepath.Join(dir, "app.js"))
	require.NoError(t, err)
	require.Equal(t, "const port = 5173;\n", string(data))
}

func TestWriteToolEditRejectsStaleOrAmbiguousTarget(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("x\nx\n"), 0644))
	tool := NewWriteTool(dir)

	stale, err := tool.Execute(context.Background(), map[string]any{
		"file":          "app.js",
		"mode":          "edit",
		"old_text":      "x",
		"new_text":      "y",
		"base_revision": "sha256:stale",
	})
	require.NoError(t, err)
	require.False(t, stale.Success)
	require.Contains(t, stale.Error, "revision")

	readTool := NewReadTool(dir)
	read, err := readTool.Execute(context.Background(), map[string]any{"file": "app.js"})
	require.NoError(t, err)
	revision := read.Metadata["revision"].(string)
	ambiguous, err := tool.Execute(context.Background(), map[string]any{
		"file":          "app.js",
		"mode":          "edit",
		"old_text":      "x",
		"new_text":      "y",
		"base_revision": revision,
	})
	require.NoError(t, err)
	require.False(t, ambiguous.Success)
	require.Contains(t, ambiguous.Error, "ambiguous")
}

func TestGrepTool(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0755))
	err := os.WriteFile(filepath.Join(dir, "src", "test.go"), []byte("hello world\nfoo bar\nhello again\n"), 0644)
	require.NoError(t, err)

	tool := NewGrepTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "hello",
		"path":    ".",
		"include": "*.go",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "src/test.go:1: hello world")
	assert.Contains(t, result.Content, "src/test.go:3: hello again")

	// Test missing params
	result, err = tool.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestGlobTool(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("text"), 0644)
	require.NoError(t, err)

	tool := NewGlobTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "a.go")
	assert.Contains(t, result.Content, "b.go")
	assert.NotContains(t, result.Content, "c.txt")

	// Test missing pattern
	result, err = tool.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestBashToolSuccessFailureTimeoutAndOutputLimit(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool(dir)

	success, err := tool.Execute(context.Background(), map[string]any{"command": "printf 'hello'"})
	require.NoError(t, err)
	require.True(t, success.Success)
	require.Equal(t, "hello", success.Content)
	require.Equal(t, 0, success.Metadata["exit_code"])
	require.Equal(t, "bash", success.WorkspaceOps[0].Kind)

	failure, err := tool.Execute(context.Background(), map[string]any{"command": "printf 'bad' >&2; exit 3"})
	require.NoError(t, err)
	require.False(t, failure.Success)
	require.Contains(t, failure.Error, "3")
	require.Contains(t, failure.Content, "bad")

	timeout, err := tool.Execute(context.Background(), map[string]any{
		"command":    "sleep 1",
		"timeout_ms": 20,
	})
	require.NoError(t, err)
	require.False(t, timeout.Success)
	require.Contains(t, timeout.Error, "timed out")

	limited, err := tool.Execute(context.Background(), map[string]any{
		"command":          "printf '1234567890'",
		"max_output_bytes": 4,
	})
	require.NoError(t, err)
	require.True(t, limited.Success)
	require.Equal(t, "1234", limited.Content)
	require.Equal(t, true, limited.Metadata["truncated"])
}

func TestBashToolUsesWorkspaceAndStdin(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool(dir)

	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "pwd",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Contains(t, result.Content, dir)

	result, err = tool.Execute(context.Background(), map[string]any{
		"command": "cat",
		"stdin":   "input from stdin",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, "input from stdin", result.Content)
}

func TestBashToolContextCancellation(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]any{"command": "sleep 1"})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, "canceled")
}

func TestTodoWriteTool(t *testing.T) {
	tool := NewTodoWriteTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"items": []string{"task1", "task2"},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestTodoReadTool(t *testing.T) {
	tool := NewTodoReadTool()
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "No todos", result.Content)
}
