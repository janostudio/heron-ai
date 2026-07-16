package types

import "context"

// Tool interface defines the contract for all tools
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, params map[string]any) (*ToolResult, error)
	NeedsApproval() bool
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// JSONSchema represents a JSON Schema for tool parameters
type JSONSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]JSONProperty `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

type JSONProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}
