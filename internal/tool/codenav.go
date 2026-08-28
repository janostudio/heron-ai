package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// CodeNav uses an installed language-server client when available. It is
// deliberately optional: the Agent can use Read/Grep/Bash when no client is
// configured, while CodeNav gives typed definition/reference/diagnostic
// lookup to agents working in a language-aware workspace.
type CodeNavTool struct {
	workspace *workspace.Service
	command   string
}

func NewCodeNavTool(baseDir, command string) *CodeNavTool {
	ws, _ := workspace.New(baseDir)
	if strings.TrimSpace(command) == "" {
		command = "codels"
	}
	return &CodeNavTool{workspace: ws, command: command}
}

// NewCodeNavToolWithWorkspace uses the default codels helper command.
func NewCodeNavToolWithWorkspace(baseDir string) *CodeNavTool {
	return NewCodeNavTool(baseDir, "")
}

func (t *CodeNavTool) Name() string { return "CodeNav" }
func (t *CodeNavTool) Description() string {
	return "Navigate code definitions, references, symbols, hover information, and diagnostics"
}
func (t *CodeNavTool) NeedsApproval() bool { return false }
func (t *CodeNavTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolReadOnly}
}
func (t *CodeNavTool) Parameters() map[string]any {
	return map[string]any{
		"operation": map[string]any{"type": "string", "description": "definition, references, symbols, hover, or diagnostics", "required": true, "enum": []string{"definition", "references", "symbols", "hover", "diagnostics"}},
		"file":      map[string]any{"type": "string", "description": "Workspace-relative source file"},
		"line":      map[string]any{"type": "integer", "description": "1-based line"},
		"column":    map[string]any{"type": "integer", "description": "1-based column"},
		"symbol":    map[string]any{"type": "string", "description": "Optional symbol name"},
	}
}

func (t *CodeNavTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.workspace == nil {
		return &types.ToolResult{Success: false, Error: "workspace is not configured"}, nil
	}
	operation := stringParam(params, "operation")
	if operation == "" {
		return &types.ToolResult{Success: false, Error: "operation parameter is required"}, nil
	}
	file := stringParam(params, "file")
	if operation != "symbols" && file == "" {
		return &types.ToolResult{Success: false, Error: "file parameter is required for this operation"}, nil
	}
	if file != "" {
		if _, _, err := t.workspace.ResolvePathForTool(file); err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
	}
	args := []string{
		"--operation", operation,
		"--workspace", t.workspace.Root(),
	}
	if file != "" {
		args = append(args, "--file", file)
	}
	if line := intParam(params, "line"); line > 0 {
		args = append(args, "--line", strconv.Itoa(line))
	}
	if column := intParam(params, "column"); column > 0 {
		args = append(args, "--column", strconv.Itoa(column))
	}
	if symbol := stringParam(params, "symbol"); symbol != "" {
		args = append(args, "--symbol", symbol)
	}
	cmd := exec.CommandContext(ctx, t.command, args...)
	cmd.Dir = t.workspace.Root()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return &types.ToolResult{Success: false, Error: ctx.Err().Error()}, nil
		}
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("codenav failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	var value any
	content := strings.TrimSpace(string(output))
	if json.Unmarshal(output, &value) == nil {
		content = formatJSON(value)
	}
	return &types.ToolResult{
		Success: true,
		Content: content,
		Metadata: map[string]any{
			"operation": operation,
			"file":      file,
		},
		WorkspaceOps: []types.WorkspaceOperation{{
			OperationID: workspace.NewOperationID(),
			Kind:        "codenav",
			Path:        filepath.ToSlash(file),
			Command:     t.command,
			Summary:     fmt.Sprintf("code navigation %s", operation),
		}},
	}, nil
}

func formatJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
