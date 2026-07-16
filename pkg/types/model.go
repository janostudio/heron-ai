package types

import "context"

// ModelProvider interface defines the LLM provider contract
type ModelProvider interface {
	Chat(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (*ChatResponse, error)
	ChatStream(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (<-chan ChatChunk, error)
}

// Message represents a chat message
type Message struct {
	ID         string     `json:"id,omitempty"`
	RoundNum   int        `json:"round_num,omitempty"`
	AgentName  string     `json:"agent_name,omitempty"`
	TeamName   string     `json:"team_name,omitempty"`
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	CreatedAt  string     `json:"created_at,omitempty"`
}

// ToolCall represents an LLM tool call request
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ChatResponse represents an LLM chat response
type ChatResponse struct {
	Text      string     `json:"text"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     TokenUsage `json:"usage"`
}

// ChatChunk represents a streaming chat response chunk
type ChatChunk struct {
	Text      string `json:"text"`
	Reasoning string `json:"reasoning,omitempty"`
	Finished  bool   `json:"finished"`
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`
}
