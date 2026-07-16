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
	t, err := e.registry.Lookup(name)
	if err != nil {
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
