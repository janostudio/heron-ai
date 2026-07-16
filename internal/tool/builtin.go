package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ReadTool reads file contents
type ReadTool struct {
	baseDir string
}

func NewReadTool(baseDir string) *ReadTool { return &ReadTool{baseDir: baseDir} }
func (t *ReadTool) Name() string           { return "Read" }
func (t *ReadTool) Description() string    { return "Read file contents" }
func (t *ReadTool) NeedsApproval() bool    { return false }
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
	data, err := os.ReadFile(filepath.Join(t.baseDir, file))
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

// WriteTool writes file contents
type WriteTool struct {
	baseDir string
}

func NewWriteTool(baseDir string) *WriteTool { return &WriteTool{baseDir: baseDir} }
func (t *WriteTool) Name() string            { return "Write" }
func (t *WriteTool) Description() string     { return "Write file contents" }
func (t *WriteTool) NeedsApproval() bool     { return true }
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
	fullPath := filepath.Join(t.baseDir, file)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{Success: true, Content: "File written successfully"}, nil
}

// GrepTool searches file contents
type GrepTool struct {
	baseDir string
}

func NewGrepTool(baseDir string) *GrepTool { return &GrepTool{baseDir: baseDir} }
func (t *GrepTool) Name() string           { return "Grep" }
func (t *GrepTool) Description() string    { return "Search for a pattern in files" }
func (t *GrepTool) NeedsApproval() bool    { return false }
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
	fullPath := filepath.Join(t.baseDir, searchPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	content := string(data)
	var matches []string
	for i, line := range splitLines(content) {
		if contains(line, pattern) {
			matches = append(matches, fmt.Sprintf("%d: %s", i+1, line))
		}
	}
	return &types.ToolResult{Success: true, Content: joinLines(matches)}, nil
}

// GlobTool matches file patterns
type GlobTool struct {
	baseDir string
}

func NewGlobTool(baseDir string) *GlobTool { return &GlobTool{baseDir: baseDir} }
func (t *GlobTool) Name() string           { return "Glob" }
func (t *GlobTool) Description() string    { return "Find files matching a pattern" }
func (t *GlobTool) NeedsApproval() bool    { return false }
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
	matches, err := filepath.Glob(filepath.Join(t.baseDir, pattern))
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{Success: true, Content: joinLines(matches)}, nil
}

// TodoWriteTool writes todo list
type TodoWriteTool struct{}

func NewTodoWriteTool() *TodoWriteTool  { return &TodoWriteTool{} }
func (t *TodoWriteTool) Name() string   { return "TodoWrite" }
func (t *TodoWriteTool) Description() string  { return "Write todo items" }
func (t *TodoWriteTool) NeedsApproval() bool { return false }
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

func NewTodoReadTool() *TodoReadTool  { return &TodoReadTool{} }
func (t *TodoReadTool) Name() string  { return "TodoRead" }
func (t *TodoReadTool) Description() string { return "Read todo items" }
func (t *TodoReadTool) NeedsApproval() bool { return false }
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
