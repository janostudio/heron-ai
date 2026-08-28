package types

import "context"

// ToolExecutionClass controls whether multiple calls returned by one
// ModelCall may run together.
type ToolExecutionClass string

const (
	ToolReadOnly ToolExecutionClass = "read_only"
	ToolSerial   ToolExecutionClass = "serial"
)

// ToolExecutionSpec is the runtime safety declaration for a Tool.
type ToolExecutionSpec struct {
	Class       ToolExecutionClass `yaml:"class" json:"class"`
	MaxParallel int                `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
	Async       bool               `yaml:"async,omitempty" json:"async,omitempty"`
	RestartSafe bool               `yaml:"restart_safe,omitempty" json:"restart_safe,omitempty"`
}

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
	Success         bool                  `json:"success"`
	Content         string                `json:"content"`
	Error           string                `json:"error,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	Next            *Route                `json:"next,omitempty"`
	PendingApproval *AgentPendingApproval `json:"pending_approval,omitempty"`
	WorkspaceOps    []WorkspaceOperation  `json:"workspace_ops,omitempty"`
	RecordRefs      []string              `json:"record_refs,omitempty"`
}

// JSONSchema represents a JSON Schema for tool parameters
type JSONSchema struct {
	Name        string                  `json:"name,omitempty"`
	Description string                  `json:"description,omitempty"`
	Type        string                  `json:"type"`
	Properties  map[string]JSONProperty `json:"properties,omitempty"`
	Required    []string                `json:"required,omitempty"`
}

type JSONProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}
