package tool

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// MCPToolDispatcher is the slice of the MCP adapter the executor needs.
//
// The executor names the behaviour rather than importing the mcp package so
// the tool layer keeps no opinion about transport, handshake or discovery —
// it only needs to know that some tool names are served elsewhere.
type MCPToolDispatcher interface {
	HasTool(name string) bool
	CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
}

type ToolExecutor struct {
	registry *ToolRegistry
	mcp      MCPToolDispatcher
}

func NewToolExecutor(registry *ToolRegistry) *ToolExecutor {
	return &ToolExecutor{registry: registry}
}

// SetMCPDispatcher routes tool names served by an MCP server to that server
// instead of the builtin registry.
//
// MCP tools are not registered in the registry because they do not exist until
// a server is connected: their names come from tools/list at startup, and a
// name in the registry would have to be kept in sync with a live connection
// that can come and go. Dispatching by lookup keeps one owner per tool name.
func (e *ToolExecutor) SetMCPDispatcher(dispatcher MCPToolDispatcher) {
	e.mcp = dispatcher
}

// MCPExecutor exposes the dispatcher so callers that need to execute an MCP
// tool directly (outside a TurnLoop) can do so without guessing whether one is
// configured.
func (e *ToolExecutor) MCPExecutor() MCPToolDispatcher {
	if e == nil {
		return nil
	}
	return e.mcp
}

func (e *ToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if e == nil {
		return &types.ToolResult{Success: false, Error: "tool registry is not configured"}, nil
	}
	if e.mcp != nil && e.mcp.HasTool(name) {
		// Deliberately not validated against a Parameters() schema: the
		// engine's schema type is a lossy projection of the server's JSON
		// Schema, and rejecting a correct call because a nested object was
		// flattened to "object" would break tools that work fine. The server
		// validates its own arguments.
		result, err := e.mcp.CallTool(ctx, name, args)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return result, nil
	}
	if e.registry == nil {
		return &types.ToolResult{Success: false, Error: "tool registry is not configured"}, nil
	}
	t, err := e.registry.Lookup(name)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}
	if err := ValidateParameters(t.Parameters(), args); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	result, err := t.Execute(ctx, args)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return result, nil
}

// ExecutionSpec exposes the optional safety classification used by the V1
// parallel-safe Tool scheduler. Unknown tools default to serial execution in
// the registry.
func (e *ToolExecutor) ExecutionSpec(name string) types.ToolExecutionSpec {
	// An MCP tool's side effects are unknowable here — it may be a read on one
	// server and a deploy on another — so it runs serially rather than being
	// batched with read-only builtins.
	if e.mcp != nil && e.mcp.HasTool(name) {
		return types.ToolExecutionSpec{Class: types.ToolSerial}
	}
	return e.registry.ExecutionSpec(name)
}

func (e *ToolExecutor) NeedsApproval(name string, _ map[string]any) (bool, error) {
	if e == nil {
		return false, fmt.Errorf("tool registry is not configured")
	}
	// MCP servers gate their own side effects; the engine has no visibility
	// into which call is dangerous, so it does not add a second gate that
	// would ask the human to approve something they cannot see.
	if e.mcp != nil && e.mcp.HasTool(name) {
		return false, nil
	}
	if e.registry == nil {
		return false, fmt.Errorf("tool registry is not configured")
	}
	t, err := e.registry.Lookup(name)
	if err != nil {
		return false, err
	}
	return t.NeedsApproval(), nil
}

func (e *ToolExecutor) ExecuteWithApproval(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	t, err := e.registry.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("tool %q not found: %w", name, err)
	}

	if t.NeedsApproval() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool %q requires approval", name),
		}, nil
	}

	return e.Execute(ctx, name, args)
}
