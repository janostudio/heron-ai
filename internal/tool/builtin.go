package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
			"required":    true,
		},
		"line_start": map[string]any{
			"type":        "integer",
			"description": "1-based first line to read",
		},
		"line_end": map[string]any{
			"type":        "integer",
			"description": "1-based last line to read",
		},
		"max_bytes": map[string]any{
			"type":        "integer",
			"description": "Maximum number of bytes to return",
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
	data, err := t.workspace.Read(ctx, workspace.ReadRequest{
		Path:      file,
		LineStart: intParam(params, "line_start"),
		LineEnd:   intParam(params, "line_end"),
		MaxBytes:  intParam(params, "max_bytes"),
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: data.Content,
		Metadata: map[string]any{
			"file":        file,
			"revision":    data.Revision,
			"truncated":   data.Truncated,
			"line_start":  data.LineStart,
			"line_end":    data.LineEnd,
			"total_lines": data.TotalLines,
			"total_bytes": data.TotalBytes,
		},
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
func (t *WriteTool) NeedsApproval() bool { return false }
func (t *WriteTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
func (t *WriteTool) Parameters() map[string]any {
	return map[string]any{
		"file":          map[string]any{"type": "string", "description": "Path to the file to write", "required": true},
		"content":       map[string]any{"type": "string", "description": "Full content for replace mode"},
		"mode":          map[string]any{"type": "string", "description": "create, replace, or edit", "enum": []string{"create", "replace", "edit"}},
		"base_revision": map[string]any{"type": "string", "description": "Revision returned by Read"},
		"old_text":      map[string]any{"type": "string", "description": "Exact text to replace in edit mode"},
		"new_text":      map[string]any{"type": "string", "description": "Replacement text in edit mode"},
	}
}
func (t *WriteTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	file, _ := params["file"].(string)
	content, _ := params["content"].(string)
	mode, _ := params["mode"].(string)
	baseRevision, _ := params["base_revision"].(string)
	oldText, _ := params["old_text"].(string)
	newText, _ := params["new_text"].(string)
	if file == "" {
		return &types.ToolResult{Success: false, Error: "file parameter is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	result, err := t.workspace.Write(ctx, workspace.WriteRequest{
		Path:         file,
		Content:      content,
		Mode:         mode,
		BaseRevision: baseRevision,
		OldText:      oldText,
		NewText:      newText,
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("File %s successfully", result.Mode),
		Metadata: map[string]any{
			"file":          file,
			"mode":          result.Mode,
			"revision":      result.Revision,
			"matched_count": result.MatchedCount,
			"changed_bytes": result.ChangedBytes,
		},
		WorkspaceOps: []types.WorkspaceOperation{result.Operation},
	}, nil
}

// BashTool executes a command inside the configured workspace. It is
// intentionally synchronous in V1; long-running execution can later be
// lifted into the Agent Task runtime without changing the Agent-facing name.
type BashTool struct {
	workspace *workspace.Service
}

func NewBashTool(baseDir string) *BashTool {
	ws, _ := workspace.New(baseDir)
	return &BashTool{workspace: ws}
}

func (t *BashTool) Name() string        { return "Bash" }
func (t *BashTool) Description() string { return "Execute a shell command in the workspace" }
func (t *BashTool) NeedsApproval() bool { return false }
func (t *BashTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial, Async: true}
}
func (t *BashTool) Parameters() map[string]any {
	return map[string]any{
		"command": map[string]any{"type": "string", "description": "Shell command to execute", "required": true},
		"stdin":   map[string]any{"type": "string", "description": "Optional standard input"},
		"timeout_ms": map[string]any{
			"type":        "integer",
			"description": "Optional timeout in milliseconds",
		},
		"max_output_bytes": map[string]any{
			"type":        "integer",
			"description": "Maximum stdout/stderr bytes returned",
		},
	}
}
func (t *BashTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" {
		return &types.ToolResult{Success: false, Error: "command parameter is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	execCtx, cancel := commandContext(ctx, intParam(params, "timeout_ms"))
	defer cancel()
	result, err := t.workspace.Run(execCtx, workspace.CommandRequest{
		Command:        command,
		Stdin:          stringParam(params, "stdin"),
		Shell:          "/bin/bash",
		Kind:           "bash",
		MaxOutputBytes: intParam(params, "max_output_bytes"),
	})
	content := result.Stdout
	if result.Stderr != "" {
		if content != "" {
			content += "\n"
		}
		content += result.Stderr
	}
	success := err == nil && result.ExitCode == 0 && !result.TimedOut && !result.Canceled
	message := map[string]any{
		"exit_code": result.ExitCode,
		"timed_out": result.TimedOut,
		"canceled":  result.Canceled,
		"truncated": result.Truncated,
		"stdout":    result.Stdout,
		"stderr":    result.Stderr,
	}
	if err != nil && content == "" {
		content = err.Error()
	}
	return &types.ToolResult{
		Success:      success,
		Content:      content,
		Error:        toolError(err, result.ExitCode, result.TimedOut, result.Canceled),
		Metadata:     message,
		WorkspaceOps: []types.WorkspaceOperation{result.Operation},
	}, nil
}

func toolError(err error, exitCode int, timedOut, canceled bool) string {
	if timedOut {
		return "command timed out"
	}
	if canceled {
		return "command canceled"
	}
	if err != nil && exitCode == 0 {
		return err.Error()
	}
	if exitCode != 0 {
		return fmt.Sprintf("command exited with code %d", exitCode)
	}
	return ""
}

func commandContext(ctx context.Context, timeoutMS int) (context.Context, context.CancelFunc) {
	if timeoutMS <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, durationFromMilliseconds(timeoutMS))
}

func durationFromMilliseconds(value int) time.Duration {
	return time.Duration(value) * time.Millisecond
}

func intParam(params map[string]any, key string) int {
	value, ok := params[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

func boolParam(params map[string]any, key string) bool {
	value, _ := params[key].(bool)
	return value
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
		"pattern":     map[string]any{"type": "string", "description": "Text or regular expression to search for", "required": true},
		"path":        map[string]any{"type": "string", "description": "File or directory to search in"},
		"include":     map[string]any{"type": "string", "description": "Optional filename pattern such as *.go"},
		"regex":       map[string]any{"type": "boolean", "description": "Interpret pattern as a regular expression"},
		"ignore_case": map[string]any{"type": "boolean", "description": "Ignore case when matching"},
		"max_results": map[string]any{"type": "integer", "description": "Maximum number of matching lines"},
		"max_chars":   map[string]any{"type": "integer", "description": "Maximum result characters"},
	}
}
func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	pattern, _ := params["pattern"].(string)
	searchPath, _ := params["path"].(string)
	if pattern == "" {
		return &types.ToolResult{Success: false, Error: "pattern is required"}, nil
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	search, err := t.workspace.Search(ctx, workspace.SearchRequest{
		Pattern:    pattern,
		Path:       searchPath,
		Include:    stringParam(params, "include"),
		Regex:      boolParam(params, "regex"),
		IgnoreCase: boolParam(params, "ignore_case"),
		MaxResults: intParam(params, "max_results"),
		MaxChars:   intParam(params, "max_chars"),
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	lines := make([]string, 0, len(search.Matches))
	for _, match := range search.Matches {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", match.Path, match.Line, match.Content))
	}
	return &types.ToolResult{
		Success: true,
		Content: joinLines(lines),
		Metadata: map[string]any{
			"matches":   len(search.Matches),
			"truncated": search.Truncated,
		},
		WorkspaceOps: []types.WorkspaceOperation{search.Operation},
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
		"pattern":      map[string]any{"type": "string", "description": "Glob pattern (e.g., **/*.go)", "required": true},
		"max_results":  map[string]any{"type": "integer", "description": "Maximum number of paths"},
		"include_dirs": map[string]any{"type": "boolean", "description": "Include matching directories"},
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
	matches, err := t.workspace.GlobWithOptions(ctx, workspace.GlobRequest{
		Pattern:     pattern,
		MaxResults:  intParam(params, "max_results"),
		IncludeDirs: boolParam(params, "include_dirs"),
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{
		Success:  true,
		Content:  joinLines(matches),
		Metadata: map[string]any{"matches": len(matches)},
	}, nil
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
func joinLines(lines []string) string {
	result := ""
	for _, line := range lines {
		result += line + "\n"
	}
	return result
}
