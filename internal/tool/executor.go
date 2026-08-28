package tool

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ToolExecutor struct {
	registry *ToolRegistry
}

func NewToolExecutor(registry *ToolRegistry) *ToolExecutor {
	return &ToolExecutor{registry: registry}
}

func (e *ToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if e == nil || e.registry == nil {
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
	return e.registry.ExecutionSpec(name)
}

func (e *ToolExecutor) NeedsApproval(name string, _ map[string]any) (bool, error) {
	if e == nil || e.registry == nil {
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
