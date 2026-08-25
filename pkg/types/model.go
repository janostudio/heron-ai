package types

import "context"

// ModelProvider interface defines the LLM provider contract
type ModelProvider interface {
	Chat(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (*ChatResponse, error)
	ChatStream(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (<-chan ChatChunk, error)
}

// ModelProfile describes the capabilities and defaults advertised for one
// model by the global .agents/models.json registry. Optional generation
// values are pointers so "not declared" is different from an explicit zero.
type ModelProfile struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	ModelsURL string `json:"models_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`

	MaxAllowedSize  int `json:"maxAllowedSize,omitempty"`
	MaxInputTokens  int `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	MaxTokens       int `json:"max_tokens,omitempty"` // Deprecated registry alias.

	Temperature       *float64         `json:"temperature,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	TopK              *int             `json:"top_k,omitempty"`
	RepetitionPenalty *float64         `json:"repetition_penalty,omitempty"`
	OnlyReasoning     bool             `json:"onlyReasoning,omitempty"`
	Reasoning         *ReasoningConfig `json:"reasoning,omitempty"`

	SupportsImages            *bool `json:"supportsImages,omitempty"`
	SupportsReasoning         *bool `json:"supportsReasoning,omitempty"`
	SupportsToolCall          *bool `json:"supportsToolCall,omitempty"`
	SupportsTemperature       *bool `json:"supportsTemperature,omitempty"`
	SupportsTopP              *bool `json:"supportsTopP,omitempty"`
	SupportsTopK              *bool `json:"supportsTopK,omitempty"`
	SupportsRepetitionPenalty *bool `json:"supportsRepetitionPenalty,omitempty"`
	SupportsStructuredOutput  *bool `json:"supportsStructuredOutput,omitempty"`
}

// Message represents a chat message
type Message struct {
	ID         string     `json:"id,omitempty"`
	RoundNum   int        `json:"round_num,omitempty"`
	MemberID   string     `json:"member_id,omitempty"`
	TeamID     string     `json:"team_id,omitempty"`
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
