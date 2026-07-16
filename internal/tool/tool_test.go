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

func (m *mockTool) Name() string                     { return m.name }
func (m *mockTool) Description() string              { return m.description }
func (m *mockTool) Parameters() map[string]any       { return m.params }
func (m *mockTool) NeedsApproval() bool              { return m.needsApproval }
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

func TestGrepTool(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "test.txt")
	err := os.WriteFile(filename, []byte("hello world\nfoo bar\nhello again\n"), 0644)
	require.NoError(t, err)

	tool := NewGrepTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "hello",
		"path":    "test.txt",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1: hello world")
	assert.Contains(t, result.Content, "3: hello again")

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
