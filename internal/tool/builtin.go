package tool

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ReadTool reads file contents
type ReadTool struct {
	workspace *workspace.Service
}

func NewReadTool(baseDir string) *ReadTool {
	ws, _ := workspace.New(baseDir)
	return &ReadTool{workspace: ws}
}
func (t *ReadTool) Name() string        { return "Read" }
func (t *ReadTool) Description() string { return "Read file contents" }
func (t *ReadTool) NeedsApproval() bool { return false }
func (t *ReadTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *ReadTool) Parameters() map[string]any {
	return map[string]any{
		"file": map[string]any{
			"type":        "string",
			"description": "Path to the file to read",
		},
	}
}
func (t *ReadTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	file, _ := params["file"].(string)
	if file == "" {
		return &types.ToolResult{Success: false, Error: "file parameter is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	data, err := t.workspace.Read(ctx, workspace.ReadRequest{Path: file})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{
		Success:      true,
		Content:      data.Content,
		WorkspaceOps: []types.WorkspaceOperation{data.Operation},
	}, nil
}

// WriteTool writes file contents
type WriteTool struct {
	workspace *workspace.Service
}

func NewWriteTool(baseDir string) *WriteTool {
	ws, _ := workspace.New(baseDir)
	return &WriteTool{workspace: ws}
}
func (t *WriteTool) Name() string        { return "Write" }
func (t *WriteTool) Description() string { return "Write file contents" }
func (t *WriteTool) NeedsApproval() bool { return true }
func (t *WriteTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
func (t *WriteTool) Parameters() map[string]any {
	return map[string]any{
		"file":    map[string]any{"type": "string", "description": "Path to the file to write"},
		"content": map[string]any{"type": "string", "description": "Content to write"},
	}
}
func (t *WriteTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	file, _ := params["file"].(string)
	content, _ := params["content"].(string)
	if file == "" {
		return &types.ToolResult{Success: false, Error: "file parameter is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	result, err := t.workspace.Write(ctx, workspace.WriteRequest{
		Path:    file,
		Content: content,
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{
		Success:      true,
		Content:      "File written successfully",
		WorkspaceOps: []types.WorkspaceOperation{result.Operation},
	}, nil
}

// GrepTool searches file contents
type GrepTool struct {
	workspace *workspace.Service
}

func NewGrepTool(baseDir string) *GrepTool {
	ws, _ := workspace.New(baseDir)
	return &GrepTool{workspace: ws}
}
func (t *GrepTool) Name() string        { return "Grep" }
func (t *GrepTool) Description() string { return "Search for a pattern in files" }
func (t *GrepTool) NeedsApproval() bool { return false }
func (t *GrepTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *GrepTool) Parameters() map[string]any {
	return map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Pattern to search for"},
		"path":    map[string]any{"type": "string", "description": "File or directory to search in"},
	}
}
func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	pattern, _ := params["pattern"].(string)
	searchPath, _ := params["path"].(string)
	if pattern == "" || searchPath == "" {
		return &types.ToolResult{Success: false, Error: "pattern and path are required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	data, err := t.workspace.Read(ctx, workspace.ReadRequest{Path: searchPath})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	content := data.Content
	var matches []string
	for i, line := range splitLines(content) {
		if contains(line, pattern) {
			matches = append(matches, fmt.Sprintf("%d: %s", i+1, line))
		}
	}
	return &types.ToolResult{
		Success:      true,
		Content:      joinLines(matches),
		WorkspaceOps: []types.WorkspaceOperation{data.Operation},
	}, nil
}

// GlobTool matches file patterns
type GlobTool struct {
	workspace *workspace.Service
}

func NewGlobTool(baseDir string) *GlobTool {
	ws, _ := workspace.New(baseDir)
	return &GlobTool{workspace: ws}
}
func (t *GlobTool) Name() string        { return "Glob" }
func (t *GlobTool) Description() string { return "Find files matching a pattern" }
func (t *GlobTool) NeedsApproval() bool { return false }
func (t *GlobTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *GlobTool) Parameters() map[string]any {
	return map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern (e.g., *.go)"},
	}
}
func (t *GlobTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return &types.ToolResult{Success: false, Error: "pattern is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	matches, err := t.workspace.Glob(pattern)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{Success: true, Content: joinLines(matches)}, nil
}

// TodoWriteTool writes todo list
type TodoWriteTool struct{}

func NewTodoWriteTool() *TodoWriteTool       { return &TodoWriteTool{} }
func (t *TodoWriteTool) Name() string        { return "TodoWrite" }
func (t *TodoWriteTool) Description() string { return "Write todo items" }
func (t *TodoWriteTool) NeedsApproval() bool { return false }
func (t *TodoWriteTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
func (t *TodoWriteTool) Parameters() map[string]any {
	return map[string]any{
		"items": map[string]any{"type": "array", "description": "List of todo items"},
	}
}
func (t *TodoWriteTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "Todos updated"}, nil
}

// TodoReadTool reads todo list
type TodoReadTool struct{}

func NewTodoReadTool() *TodoReadTool        { return &TodoReadTool{} }
func (t *TodoReadTool) Name() string        { return "TodoRead" }
func (t *TodoReadTool) Description() string { return "Read todo items" }
func (t *TodoReadTool) NeedsApproval() bool { return false }
func (t *TodoReadTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *TodoReadTool) Parameters() map[string]any {
	return map[string]any{}
}
func (t *TodoReadTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "No todos"}, nil
}

// Helper functions
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func joinLines(lines []string) string {
	result := ""
	for _, line := range lines {
		result += line + "\n"
	}
	return result
}
