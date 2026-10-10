package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/osenv"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// CodeNav uses an installed language-server client when available. It is
// deliberately optional: the Agent can use Read/Grep/Bash when no client is
// configured, while CodeNav gives typed definition/reference/diagnostic
// lookup to agents working in a language-aware workspace.
type CodeNavTool struct {
	workspace workspace.Workspace
	command   string
	// env 是注入 codels 子进程的环境变量（含引擎级全局 env）。nil 表示继承进程环境。
	env []string
}

// NewCodeNavTool 构造 CodeNav 工具。injectEnv 可选（可变参数）：其键值注入
// codels 子进程（覆盖同名进程环境变量）。
func NewCodeNavTool(ws workspace.Workspace, command string, injectEnv ...map[string]string) *CodeNavTool {
	if strings.TrimSpace(command) == "" {
		command = "codels"
	}
	var env []string
	if len(injectEnv) > 0 && len(injectEnv[0]) > 0 {
		env = osenv.Merge(nil, injectEnv[0])
	}
	return &CodeNavTool{workspace: ws, command: command, env: env}
}

// NewCodeNavToolWithWorkspace uses the default codels helper command.
func NewCodeNavToolWithWorkspace(ws workspace.Workspace) *CodeNavTool {
	return NewCodeNavTool(ws, "")
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
	if t.workspace.IsRemote() {
		return &types.ToolResult{Success: false, Error: "CodeNav is not supported on remote workspaces; use Grep/Glob instead"}, nil
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
		// Restricted resolve, not the plain one: the path is about to be
		// handed to an external helper that reads the filesystem on its own,
		// so checking only that it is inside the workspace would let a
		// caller point the helper at another agent's private knowledge.
		if _, _, err := t.workspace.ResolvePathForToolRestricted(file, ToolPathRestriction(ctx)); err != nil {
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
	// 注入全局 env（如有）。nil 时保持 exec 的默认继承行为。
	if len(t.env) > 0 {
		cmd.Env = t.env
	}
	// Own process group + a group kill on cancel: the helper may fork, and a
	// forked child holding the captured stdout would keep CombinedOutput
	// blocked past the deadline (the same trap fixed for Bash in
	// ISSUE-bash-timeout-grandchild-hang).
	workspace.SetProcessGroup(cmd)
	cmd.Cancel = func() error { return workspace.KillProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
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
